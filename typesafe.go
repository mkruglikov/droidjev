// TypeSafe (System One / jev) client. Security rules:
//   - the API key is read from TYPESAFE_API_KEY at runtime and never appears
//     in any message, trace, or error (errors carry status + redacted body
//     only)
//   - responses are strictly validated before any answer is trusted; on any
//     validation failure nothing downstream executes
//
// Ported from src/typesafe.js: wire shapes, retry policy, byte caps, and every
// error message are kept word for word.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultModel is pinned, not jev-latest: the box primitive and image assets
// are 1.14-only, and 1.14's multilingual gains feed the cross-locale matching
// the prompts rely on. --model is an explicit operator escape hatch; vision
// refuses non-default models because only 1.14+ answers the box primitive.
const DefaultModel = "jev-1.14.0"

// Injectable seams: tests point apiURL at an httptest server, stub keyRunner
// for the login-shell fallback, and (Task 13) reuse httpClient for the uia2
// HTTP calls.
var (
	apiURL     = "https://api.typesafe.ai/v1/systemone"
	keyRunner  = Run
	httpClient = &http.Client{}
)

const (
	maxAttempts         = 3
	maxBodyBytes        = 2 * 1024 * 1024
	apiDefaultTimeoutMs = int64(20000)
	keyProbeTimeout     = int64(5000)
)

// retryStatus: 451 joins the rate-limit set because the region-block flap
// lasts minutes, and the retried final error ("typesafe api failed after N
// attempts: typesafe api 451") is what the benchmark adapter keys on.
var retryStatus = map[int]bool{429: true, 529: true, 503: true, 451: true}

var cachedKey string

// looksLikeToken reports whether s is a usable bearer token: single line,
// printable ASCII, sane length. Validating BEFORE any HTTP client sees the
// value matters: an invalid header value is the one realistic key-leak path.
func looksLikeToken(s string) bool {
	if len(s) < 8 || len(s) > 4096 {
		return false
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] < 0x20 || trimmed[i] > 0x7e {
			return false
		}
	}
	return true
}

// RequireApiKey resolves the TypeSafe key. Real env first; if absent
// (non-interactive shells, IDE launches), read it from the user's login shell
// environment. The value is kept in memory only — never printed, logged, or
// written to disk.
//
// ponytail: shells other than zsh/bash aren't probed; upgrade path is a
// keychain integration.
func RequireApiKey() (string, error) {
	if cachedKey != "" {
		return cachedKey, nil
	}
	if fromEnv := os.Getenv("TYPESAFE_API_KEY"); looksLikeToken(fromEnv) {
		cachedKey = strings.TrimSpace(fromEnv)
		return cachedKey, nil
	}
	for _, shell := range [][2]string{{"zsh", "-lc"}, {"bash", "-lc"}} {
		r, err := keyRunner(shell[0], []string{shell[1], "printenv TYPESAFE_API_KEY"}, RunOpts{TimeoutMs: keyProbeTimeout})
		if err != nil {
			continue // shell missing; try the next one
		}
		// A login shell can echo extra lines (profile MOTD, tool init); the
		// key is a single token, so take the last line that looks like one.
		var candidates []string
		for _, line := range strings.Split(strings.TrimSpace(string(r.Stdout)), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if looksLikeToken(line) {
				candidates = append(candidates, line)
			}
		}
		if r.Code == 0 && len(candidates) > 0 {
			cachedKey = candidates[len(candidates)-1]
			return cachedKey, nil
		}
	}
	return "", NewDroidJevError("TYPESAFE_API_KEY is not set (or not a usable single-line token); droidjev needs it to ask jev where to click")
}

// Asset is one image asset carried by a request. JSON field names match the
// wire shape built in src/check.js / src/agent.js.
type Asset struct {
	Type       string `json:"type"`
	Format     string `json:"format"`
	DataBase64 string `json:"data_base64"`
}

// AskRequest is one jev request. State and Questions arrive as `any` (callers
// pass typed structs / ordered maps), Assets is nil unless the request carries
// images, and zero TimeoutMs/Model take the defaults.
type AskRequest struct {
	State     any
	Questions any
	Assets    map[string]Asset
	TimeoutMs int64
	Model     string
}

// Usage is the token accounting. A nil *Usage means the response carried none
// (the JS `usage: json.usage ?? null`).
type Usage struct {
	InputTokens  float64 `json:"input_tokens"`
	OutputTokens float64 `json:"output_tokens"`
}

// AskResult is a parsed, structural-checked response. Answers are raw because
// each caller passes its own strict validator over them.
type AskResult struct {
	Answers   map[string]json.RawMessage
	Usage     *Usage
	Model     string
	LatencyMs int64
}

// askBody controls key order and omits assets entirely when absent, matching
// JSON.stringify({ model, state, questions, ...(assets && { assets }) }).
type askBody struct {
	Model     string           `json:"model"`
	State     any              `json:"state"`
	Questions any              `json:"questions"`
	Assets    map[string]Asset `json:"assets,omitempty"`
}

// Ask sends one request with N parallel questions. Any assets request must
// send the danger-zone images header (400 otherwise). Retries rate-limit /
// overload statuses and network-level failures up to maxAttempts.
func Ask(req AskRequest) (*AskResult, error) {
	key, err := RequireApiKey()
	if err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = DefaultModel
	}
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = apiDefaultTimeoutMs
	}
	payload, err := json.Marshal(askBody{Model: model, State: req.State, Questions: req.Questions, Assets: req.Assets})
	if err != nil {
		// No JS counterpart: JSON.stringify would throw a TypeError. A payload
		// that cannot be encoded is a caller bug, not a transport failure.
		return nil, NewDroidJevError(fmt.Sprintf("typesafe api request could not be encoded: %v", err))
	}
	hasAssets := len(req.Assets) > 0

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			Sleep(500 << (attempt - 1))
		}
		start := time.Now()
		text, status, retriable, err := askOnce(key, payload, hasAssets, timeoutMs)
		if retriable {
			lastErr = NewDroidJevError(fmt.Sprintf("typesafe api %d", status))
			continue
		}
		if err != nil {
			// Only network-level failures retry; a cap violation, a decoded
			// HTTP error, or a malformed body is final.
			if !isNetworkError(err) {
				return nil, err
			}
			lastErr = err
			continue
		}

		var parsedAny any
		if err := json.Unmarshal([]byte(text), &parsedAny); err != nil {
			return nil, NewDroidJevError("typesafe api returned non-JSON body")
		}
		var parsed map[string]json.RawMessage
		_ = json.Unmarshal([]byte(text), &parsed) // nil for non-object JSON
		answersRaw, ok := parsed["answers"]
		if !ok || !isJSONObject(answersRaw) {
			return nil, NewDroidJevError("typesafe api response missing answers")
		}
		var answers map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimSpace(answersRaw), &answers); err != nil || answers == nil {
			return nil, NewDroidJevError("typesafe api response missing answers")
		}

		result := &AskResult{Answers: answers, Model: model, LatencyMs: time.Since(start).Milliseconds()}
		if raw, ok := parsed["usage"]; ok && !isJSONNull(raw) {
			var u Usage
			if err := json.Unmarshal(raw, &u); err == nil {
				result.Usage = &u
			}
		}
		if raw, ok := parsed["model"]; ok {
			var m string
			if err := json.Unmarshal(raw, &m); err == nil {
				result.Model = m
			}
		}
		return result, nil
	}

	reason := "unknown"
	if lastErr != nil && lastErr.Error() != "" {
		reason = lastErr.Error()
	}
	return nil, NewDroidJevError(fmt.Sprintf("typesafe api failed after %d attempts: %s", maxAttempts, reason))
}

// askOnce performs one attempt. retriable reports a retryable HTTP status (the
// body is deliberately not read); a non-2xx status is a final DroidJevError
// carrying the redacted, 300-char-capped body.
func askOnce(key string, payload []byte, hasAssets bool, timeoutMs int64) (text string, status int, retriable bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return "", 0, false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	if hasAssets {
		httpReq.Header.Set("TypeSafe-Danger-Zone-Images", "true")
	}

	res, err := httpClient.Do(httpReq)
	if err != nil {
		return "", 0, false, err
	}
	defer res.Body.Close()

	if retryStatus[res.StatusCode] {
		return "", res.StatusCode, true, nil
	}
	// The body read stays under the same context: a server that trickles or
	// floods must neither hang us nor grow memory unbounded.
	text, err = readCapped(res.Body)
	if err != nil {
		return "", res.StatusCode, false, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", res.StatusCode, false, NewDroidJevError(fmt.Sprintf(
			"typesafe api %d: %s", res.StatusCode, truncateBytes(Redact(text), 300)))
	}
	return text, res.StatusCode, false, nil
}

// readCapped reads at most maxBodyBytes, failing closed one byte past it.
func readCapped(body io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxBodyBytes {
		return "", NewDroidJevError(fmt.Sprintf("typesafe api response exceeded %d bytes", maxBodyBytes))
	}
	return string(b), nil
}

// truncateBytes caps s at n bytes without splitting a UTF-8 sequence.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xc0 == 0x80 { // back off a continuation byte
		cut--
	}
	return s[:cut]
}

// isNetworkError is the Go spelling of the JS `e instanceof TypeError ||
// e?.name === 'AbortError'`: connection failures, DNS errors, and timeouts.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

// Prewarm issues a key-less HEAD so TLS + connection setup is paid while the
// initial layout dump runs. The response is ignored entirely.
func Prewarm() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, apiURL, nil)
	if err != nil {
		return
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return
	}
	res.Body.Close()
}

// --- strict answer validation (model output never becomes coordinates/commands) ---

// invalidAnswer builds the JS validator's `bad(why)` error. Messages are
// concatenated (not %q) so quoting is byte-identical to the template literal.
func invalidAnswer(qid, why string, noAction bool) *DroidJevError {
	msg := `invalid typesafe answer for "` + qid + `" (` + why + `)`
	if noAction {
		msg += "; no action executed"
	}
	return NewDroidJevError(msg)
}

// IsValidationError reports a malformed model answer — routine, not fatal: a
// caller may retry the same request once. Every validator consumer shares this
// one predicate.
func IsValidationError(err error) bool {
	var de *DroidJevError
	return errors.As(err, &de) && strings.HasPrefix(de.Msg, "invalid typesafe answer")
}

// Choice is a validated choice answer.
type Choice struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
}

// ValidatedChoice validates a choice answer against exactly the offered ids:
// the choice must be one of them, probabilities must cover that set exactly
// and sum to 1 (±0.02), and the choice must be the argmax. Confidence falls
// back to p(choice) when it is not a usable number, so a non-numeric value can
// never satisfy (or dodge) a threshold.
func ValidatedChoice(raw json.RawMessage, validIDs []string, qid string) (Choice, error) {
	bad := func(why string) (Choice, error) { return Choice{}, invalidAnswer(qid, why, true) }

	answer, ok := answerObject(raw)
	if !ok {
		return bad("missing answer")
	}
	choice, isString := jsonString(answer["choice"])
	if !isString || !slices.Contains(validIDs, choice) {
		return bad(`choice "` + displayRaw(answer["choice"]) + `" not in offered options`)
	}
	probsRaw, hasProbs := answer["probabilities"]
	probs, ok := answerObject(probsRaw)
	if !hasProbs || !ok {
		return bad("missing probabilities")
	}
	offered := make(map[string]bool, len(validIDs))
	for _, id := range validIDs {
		offered[id] = true
	}
	for k := range probs {
		if !offered[k] {
			return bad("probabilities contain options that were not offered")
		}
	}
	if len(probs) != len(offered) {
		return bad("probabilities do not cover exactly the offered options")
	}

	probabilities := make(map[string]float64, len(validIDs))
	var sum float64
	for _, id := range validIDs {
		p, ok := jsonNumber(probs[id])
		if !ok || p < 0 || p > 1 {
			return bad(`probability ` + displayRaw(probs[id]) + ` for "` + id + `" out of range`)
		}
		probabilities[id] = p
		sum += p
	}
	if math.Abs(sum-1) > 0.02 {
		return bad("probabilities sum to " + strconv.FormatFloat(sum, 'f', 3, 64))
	}
	maxP := math.Inf(-1)
	for _, id := range validIDs {
		if p := probabilities[id]; p > maxP {
			maxP = p
		}
	}
	if probabilities[choice] < maxP-1e-6 {
		return bad("chosen option is not the probability argmax")
	}

	confidence := probabilities[choice]
	if conf := jsNumber(answer["confidence"]); conf >= 0 && conf <= 1 {
		confidence = conf
	}
	return Choice{Choice: choice, Probabilities: probabilities, Confidence: confidence}, nil
}

// Noul is a validated `noul` (no-op likelihood) answer.
type Noul struct {
	Value float64
}

// ValidatedNoul bounds-checks a noul answer to [0,1].
func ValidatedNoul(raw json.RawMessage, qid string) (Noul, error) {
	answer, ok := answerObject(raw)
	if !ok {
		return Noul{}, invalidAnswer(qid, "missing answer", false)
	}
	n, ok := jsonNumber(answer["noul"])
	if !ok || n < 0 || n > 1 {
		return Noul{}, invalidAnswer(qid, "noul "+displayRaw(answer["noul"])+" out of range", false)
	}
	return Noul{Value: n}, nil
}

// Box is a validated rectangle in image fractions. HasConfidence reports
// whether the model supplied a confidence (the schema allows null).
type Box struct {
	Top, Bottom, Left, Right float64
	Confidence               float64
	HasConfidence            bool
}

// ValidatedBox is the one place the model may name a position: its edges are
// fractions of the image, validated here to a proper in-bounds rectangle
// before code converts them to a tap point. A box answer is returned even when
// the target is absent — the caller's noul gate decides that, not this.
func ValidatedBox(raw json.RawMessage, qid string) (Box, error) {
	bad := func(why string) (Box, error) { return Box{}, invalidAnswer(qid, why, true) }

	answer, ok := answerObject(raw)
	if !ok {
		return bad("missing answer")
	}
	boxObj, ok := answerObject(answer["box"])
	if !ok {
		return bad("missing box")
	}
	edges := [4]struct {
		name string
		raw  json.RawMessage
	}{
		{"top", boxObj["top"]},
		{"bottom", boxObj["bottom"]},
		{"left", boxObj["left"]},
		{"right", boxObj["right"]},
	}
	vals := make(map[string]float64, 4)
	for _, e := range edges {
		v, ok := jsonNumber(e.raw)
		if !ok || v < 0 || v > 1 {
			return bad("edge " + e.name + "=" + displayRaw(e.raw) + " out of range")
		}
		vals[e.name] = v
	}
	if !(vals["left"] < vals["right"]) || !(vals["top"] < vals["bottom"]) {
		return bad("edges do not form a rectangle")
	}
	box := Box{Top: vals["top"], Bottom: vals["bottom"], Left: vals["left"], Right: vals["right"]}
	if confRaw, present := answer["confidence"]; present && !isJSONNull(confRaw) {
		conf, ok := jsonNumber(confRaw) // a non-numeric confidence never passes
		if !ok || conf < 0 || conf > 1 {
			return bad("confidence " + displayRaw(confRaw) + " out of range")
		}
		box.Confidence = conf
		box.HasConfidence = true
	}
	return box, nil
}

// answerObject decodes a JSON object, rejecting null, arrays, and scalars.
func answerObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// jsonNumber decodes a JSON number strictly (no null, no strings, no bools).
func jsonNumber(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, false
	}
	if c := trimmed[0]; c != '-' && (c < '0' || c > '9') {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return 0, false
	}
	return f, true
}

func jsonString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", false
	}
	return s, true
}

// isJSONObject reports a JSON object (the only shape `answers` may take).
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// jsNumber is JS `Number(v)`: absent → NaN, null → 0, booleans → 0/1, numeric
// strings coerced, anything else → NaN. Only Confidence uses the coercing
// form; probabilities and box edges stay strict JSON numbers.
func jsNumber(raw json.RawMessage) float64 {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0:
		return math.NaN()
	case trimmed[0] == '"':
		s, ok := jsonString(trimmed)
		if !ok {
			return math.NaN()
		}
		t := strings.TrimSpace(s)
		if t == "" {
			return 0
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	case isJSONNull(trimmed), bytes.Equal(trimmed, []byte("false")):
		return 0
	case bytes.Equal(trimmed, []byte("true")):
		return 1
	default:
		if f, ok := jsonNumber(trimmed); ok {
			return f
		}
		return math.NaN()
	}
}

// displayRaw renders a raw JSON value the way `${String(v)}` does in the JS
// validators (undefined for an absent key).
func displayRaw(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "undefined"
	}
	if s, ok := jsonString(trimmed); ok {
		return s
	}
	if f, ok := jsonNumber(trimmed); ok {
		return formatJSNumber(f)
	}
	return string(trimmed)
}

func formatJSNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
