package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ports the typesafe section of the Node tests — the security tests the
// design mandates: model answers are only ever an index into our own options,
// and every malformed answer is rejected before anything executes.

var choiceIDs = []string{"click", "scroll_down", "done"}

const soundChoice = `{"choice":"click","confidence":0.9,"probabilities":{"click":0.8,"scroll_down":0.15,"done":0.05}}`

// tsErr requires an error and returns its message, asserting DroidJevError.
func tsErr(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	var de *DroidJevError
	if !errors.As(err, &de) {
		t.Fatalf("error %v has type %T, want *DroidJevError", err, err)
	}
	return err.Error()
}

func tsWantErr(t *testing.T, err error, wantSub string) {
	t.Helper()
	if msg := tsErr(t, err); !strings.Contains(msg, wantSub) {
		t.Fatalf("error = %q, want substring %q", msg, wantSub)
	}
}

func tsWantExact(t *testing.T, err error, want string) {
	t.Helper()
	if msg := tsErr(t, err); msg != want {
		t.Fatalf("error = %q, want %q", msg, want)
	}
}

func TestValidatedChoiceAcceptsSoundAnswer(t *testing.T) {
	got, err := ValidatedChoice(json.RawMessage(soundChoice), choiceIDs, "operation")
	if err != nil {
		t.Fatalf("ValidatedChoice(sound) error: %v", err)
	}
	if got.Choice != "click" {
		t.Errorf("choice = %q, want click", got.Choice)
	}
	if got.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", got.Confidence)
	}
	wantProbs := map[string]float64{"click": 0.8, "scroll_down": 0.15, "done": 0.05}
	if !reflect.DeepEqual(got.Probabilities, wantProbs) {
		t.Errorf("probabilities = %v, want %v", got.Probabilities, wantProbs)
	}
}

// Sum tolerance: a sum inside ±0.02 of 1 is still accepted (model outputs are
// rounded), and the chosen option must be the argmax.
func TestValidatedChoiceAcceptsSumWithinTolerance(t *testing.T) {
	raw := `{"choice":"click","probabilities":{"click":0.80,"scroll_down":0.15,"done":0.06}}`
	if _, err := ValidatedChoice(json.RawMessage(raw), choiceIDs, "operation"); err != nil {
		t.Fatalf("ValidatedChoice(sum 1.01) error: %v", err)
	}
}

func TestValidatedChoiceRejectsMalformedAnswers(t *testing.T) {
	const prefix = `invalid typesafe answer for "operation" `
	cases := []struct{ name, raw, want string }{
		{
			"choice not offered",
			`{"choice":"rm -rf /","probabilities":{}}`,
			prefix + `(choice "rm -rf /" not in offered options); no action executed`,
		},
		{
			"missing probabilities",
			`{"choice":"click"}`,
			prefix + `(missing probabilities); no action executed`,
		},
		{
			"null probabilities",
			`{"choice":"click","probabilities":null}`,
			prefix + `(missing probabilities); no action executed`,
		},
		{
			"extra option in probabilities",
			`{"choice":"click","probabilities":{"click":0.8,"scroll_down":0.15,"done":0.05,"rm -rf /":5}}`,
			prefix + `(probabilities contain options that were not offered); no action executed`,
		},
		{
			"missing option in probabilities",
			`{"choice":"click","probabilities":{"click":0.8,"scroll_down":0.2}}`,
			prefix + `(probabilities do not cover exactly the offered options); no action executed`,
		},
		{
			"probability out of range",
			`{"choice":"click","probabilities":{"click":1.5,"scroll_down":-0.3,"done":-0.2}}`,
			prefix + `(probability 1.5 for "click" out of range); no action executed`,
		},
		{
			"non-numeric probability",
			`{"choice":"click","probabilities":{"click":"0.8","scroll_down":0.15,"done":0.05}}`,
			prefix + `(probability 0.8 for "click" out of range); no action executed`,
		},
		{
			"sum too far from 1",
			`{"choice":"click","probabilities":{"click":0.8,"scroll_down":0.15,"done":0.1}}`,
			prefix + `(probabilities sum to 1.050); no action executed`,
		},
		{
			"chosen option is not argmax",
			`{"choice":"scroll_down","probabilities":{"click":0.9,"scroll_down":0.05,"done":0.05}}`,
			prefix + `(chosen option is not the probability argmax); no action executed`,
		},
		{
			"null answer",
			`null`,
			prefix + `(missing answer); no action executed`,
		},
		{
			"non-object answer",
			`"x"`,
			prefix + `(missing answer); no action executed`,
		},
		{
			"numeric choice cannot match a string id",
			`{"choice":5,"probabilities":{"click":0.8,"scroll_down":0.15,"done":0.05}}`,
			prefix + `(choice "5" not in offered options); no action executed`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatedChoice(json.RawMessage(tc.raw), choiceIDs, "operation")
			tsWantExact(t, err, tc.want)
		})
	}

	if _, err := ValidatedChoice(nil, choiceIDs, "operation"); err == nil {
		t.Fatal("ValidatedChoice(nil) = nil error, want missing answer")
	}
}

// Confidence falls back to the validated p(choice) whenever Number(confidence)
// is not a usable number, so a non-numeric value can never satisfy (or dodge)
// a threshold. Ported quirk: Number(null) is 0, which is usable.
func TestValidatedChoiceConfidenceFallback(t *testing.T) {
	const probs = `"probabilities":{"click":0.8,"scroll_down":0.15,"done":0.05}`
	cases := []struct {
		name string
		raw  string
		want float64
	}{
		{"absent", `{"choice":"click",` + probs + `}`, 0.8},
		{"non-numeric string", `{"choice":"click","confidence":"garbage",` + probs + `}`, 0.8},
		{"out of range", `{"choice":"click","confidence":1.5,` + probs + `}`, 0.8},
		{"numeric string coerces", `{"choice":"click","confidence":"0.42",` + probs + `}`, 0.42},
		{"valid", `{"choice":"click","confidence":0.7,` + probs + `}`, 0.7},
		{"null is zero", `{"choice":"click","confidence":null,` + probs + `}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidatedChoice(json.RawMessage(tc.raw), choiceIDs, "operation")
			if err != nil {
				t.Fatalf("ValidatedChoice: %v", err)
			}
			if got.Confidence != tc.want {
				t.Fatalf("confidence = %v, want %v", got.Confidence, tc.want)
			}
			if math.IsNaN(got.Confidence) {
				t.Fatal("confidence must never be NaN")
			}
		})
	}
}

func TestValidatedNoulBoundsChecks(t *testing.T) {
	got, err := ValidatedNoul(json.RawMessage(`{"noul":0.42}`), "goal_met")
	if err != nil {
		t.Fatalf("ValidatedNoul(0.42) error: %v", err)
	}
	if got.Value != 0.42 {
		t.Fatalf("noul = %v, want 0.42", got.Value)
	}

	cases := []struct{ name, raw, want string }{
		{"above one", `{"noul":1.2}`, `invalid typesafe answer for "goal_met" (noul 1.2 out of range)`},
		{"negative", `{"noul":-0.1}`, `invalid typesafe answer for "goal_met" (noul -0.1 out of range)`},
		{"string", `{"noul":"yes"}`, `invalid typesafe answer for "goal_met" (noul yes out of range)`},
		{"null", `{"noul":null}`, `invalid typesafe answer for "goal_met" (noul null out of range)`},
		{"absent", `{}`, `invalid typesafe answer for "goal_met" (noul undefined out of range)`},
		{"missing answer", `null`, `invalid typesafe answer for "goal_met" (missing answer)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatedNoul(json.RawMessage(tc.raw), "goal_met")
			tsWantExact(t, err, tc.want)
		})
	}
}

func TestValidatedBoxAcceptsBoundedRectangles(t *testing.T) {
	good := `{"type":"box","box":{"top":0.25,"bottom":0.82,"left":0.44,"right":0.84},"confidence":0.92}`
	got, err := ValidatedBox(json.RawMessage(good), "tap_target")
	if err != nil {
		t.Fatalf("ValidatedBox(good) error: %v", err)
	}
	want := Box{Top: 0.25, Bottom: 0.82, Left: 0.44, Right: 0.84, Confidence: 0.92, HasConfidence: true}
	if got != want {
		t.Fatalf("box = %+v, want %+v", got, want)
	}

	// confidence: null means the model offered none; the box is still valid.
	got, err = ValidatedBox(json.RawMessage(`{"box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":null}`), "t")
	if err != nil {
		t.Fatalf("ValidatedBox(null confidence) error: %v", err)
	}
	if got.HasConfidence {
		t.Fatalf("null confidence reported present: %+v", got)
	}

	// Absent confidence behaves the same.
	got, err = ValidatedBox(json.RawMessage(`{"box":{"top":0,"bottom":1,"left":0,"right":1}}`), "t")
	if err != nil {
		t.Fatalf("ValidatedBox(absent confidence) error: %v", err)
	}
	if got.HasConfidence {
		t.Fatalf("absent confidence reported present: %+v", got)
	}
}

func TestValidatedBoxRejectsMalformedAnswers(t *testing.T) {
	const prefix = `invalid typesafe answer for "t" `
	cases := []struct{ name, raw, want string }{
		{
			"not a rectangle",
			`{"box":{"top":0.5,"bottom":0.4,"left":0,"right":1}}`,
			prefix + `(edges do not form a rectangle); no action executed`,
		},
		{
			"top out of range",
			`{"box":{"top":-0.1,"bottom":1,"left":0,"right":1}}`,
			prefix + `(edge top=-0.1 out of range); no action executed`,
		},
		{
			"bottom out of range",
			`{"box":{"top":0,"bottom":2,"left":0,"right":1}}`,
			prefix + `(edge bottom=2 out of range); no action executed`,
		},
		{
			"left out of range",
			`{"box":{"top":0,"bottom":1,"left":1.2,"right":1}}`,
			prefix + `(edge left=1.2 out of range); no action executed`,
		},
		{
			"edge not a number",
			`{"box":{"top":"0.5","bottom":1,"left":0,"right":1}}`,
			prefix + `(edge top=0.5 out of range); no action executed`,
		},
		{
			"missing box",
			`{"confidence":0.5}`,
			prefix + `(missing box); no action executed`,
		},
		{
			"bad confidence",
			`{"box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":1.5}`,
			prefix + `(confidence 1.5 out of range); no action executed`,
		},
		{
			"string confidence",
			`{"box":{"top":0,"bottom":1,"left":0,"right":1},"confidence":"0.5"}`,
			prefix + `(confidence 0.5 out of range); no action executed`,
		},
		{
			"missing answer",
			`"x"`,
			prefix + `(missing answer); no action executed`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatedBox(json.RawMessage(tc.raw), "t")
			tsWantExact(t, err, tc.want)
		})
	}
}

func TestIsValidationError(t *testing.T) {
	_, err := ValidatedChoice(json.RawMessage(`{"choice":"nope"}`), choiceIDs, "operation")
	if !IsValidationError(err) {
		t.Fatalf("IsValidationError(%v) = false, want true", err)
	}
	_, err = ValidatedBox(json.RawMessage(`{}`), "t")
	if !IsValidationError(err) {
		t.Fatalf("IsValidationError(box %v) = false, want true", err)
	}
	if IsValidationError(nil) {
		t.Fatal("IsValidationError(nil) = true")
	}
	if IsValidationError(NewDroidJevError("typesafe api 500: boom")) {
		t.Fatal("transport errors are not validation errors")
	}
	if IsValidationError(errors.New("invalid typesafe answer for something")) {
		t.Fatal("a plain error is not a validation error")
	}
}

// --- key resolution ---

// tsResetTypeSafe isolates the package seams: no cached key, no ambient env
// key, and the real seams restored afterwards.
func tsResetTypeSafe(t *testing.T) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	cachedKey = ""
	oldURL, oldRunner, oldClient := apiURL, keyRunner, httpClient
	t.Cleanup(func() {
		apiURL, keyRunner, httpClient = oldURL, oldRunner, oldClient
		cachedKey = ""
	})
}

func TestRequireApiKeyFromEnv(t *testing.T) {
	tsResetTypeSafe(t)
	t.Setenv("TYPESAFE_API_KEY", "sk-env-key-123\n")
	keyRunner = func(file string, args []string, opts RunOpts) (RunResult, error) {
		t.Fatalf("keyRunner(%q) called for a valid env key", file)
		return RunResult{}, nil
	}
	key, err := RequireApiKey()
	if err != nil {
		t.Fatalf("RequireApiKey: %v", err)
	}
	if key != "sk-env-key-123" {
		t.Fatalf("key = %q, want the trimmed env value", key)
	}

	// The value is cached: a later env change is ignored, as in the JS client.
	t.Setenv("TYPESAFE_API_KEY", "sk-other-key-456")
	if again, err := RequireApiKey(); err != nil || again != key {
		t.Fatalf("RequireApiKey(cached) = %q, %v; want %q", again, err, key)
	}
}

func TestRequireApiKeyFallback(t *testing.T) {
	tsResetTypeSafe(t)
	var calls []string
	keyRunner = func(file string, args []string, opts RunOpts) (RunResult, error) {
		calls = append(calls, file+" "+strings.Join(args, " "))
		return RunResult{Code: 0, Stdout: []byte("MOTD line\nsk-test-12345\n")}, nil
	}
	key, err := RequireApiKey()
	if err != nil {
		t.Fatalf("RequireApiKey: %v", err)
	}
	if key != "sk-test-12345" {
		t.Fatalf("key = %q, want the last token-looking line", key)
	}
	if want := []string{"zsh -lc printenv TYPESAFE_API_KEY"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("keyRunner calls = %v, want %v", calls, want)
	}
}

func TestRequireApiKeyFallbackTriesBashAfterZsh(t *testing.T) {
	tsResetTypeSafe(t)
	var shells []string
	keyRunner = func(file string, args []string, opts RunOpts) (RunResult, error) {
		shells = append(shells, file)
		if file == "zsh" {
			return RunResult{}, errors.New("zsh: no such file")
		}
		return RunResult{Code: 0, Stdout: []byte("sk-bash-key-1\n")}, nil
	}
	key, err := RequireApiKey()
	if err != nil {
		t.Fatalf("RequireApiKey: %v", err)
	}
	if key != "sk-bash-key-1" {
		t.Fatalf("key = %q, want sk-bash-key-1", key)
	}
	if want := []string{"zsh", "bash"}; !reflect.DeepEqual(shells, want) {
		t.Fatalf("shells probed = %v, want %v", shells, want)
	}
}

func TestRequireApiKeyRejectsUnusableValues(t *testing.T) {
	const want = "TYPESAFE_API_KEY is not set (or not a usable single-line token); " +
		"droidjev needs it to ask jev where to click"

	cases := []struct {
		name string
		env  string
		out  string
		code int
	}{
		{"too short everywhere", "", "nope\n", 0},
		{"multi-line env value", "sk-ok\nnot-a-header", "", 0},
		{"non-ASCII env value", "ключ-ключ-ключ", "", 0},
		{"shell exits non-zero", "", "sk-never-used-1\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tsResetTypeSafe(t)
			t.Setenv("TYPESAFE_API_KEY", tc.env)
			keyRunner = func(file string, args []string, opts RunOpts) (RunResult, error) {
				return RunResult{Code: tc.code, Stdout: []byte(tc.out)}, nil
			}
			_, err := RequireApiKey()
			tsWantExact(t, err, want)
		})
	}
}

// A key that could not be a valid header value must fail before any HTTP
// client sees it — an invalid header value is the one realistic key-leak path.
func TestAskInvalidKeyNeverReachesServer(t *testing.T) {
	tsResetTypeSafe(t)
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer server.Close()
	apiURL = server.URL

	keyRunner = func(file string, args []string, opts RunOpts) (RunResult, error) {
		return RunResult{Code: 0, Stdout: []byte("")}, nil
	}
	_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}})
	tsWantErr(t, err, "TYPESAFE_API_KEY is not set")
	if hits != 0 {
		t.Fatalf("server saw %d requests, want 0", hits)
	}
}

// --- ask: retries, caps, headers, response validation ---

// tsRecorder records each request the test server sees without racing the
// handler goroutines.
type tsRecorder struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
}

func (r *tsRecorder) add(req *http.Request) int {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers = append(r.headers, req.Header.Clone())
	r.bodies = append(r.bodies, string(body))
	return len(r.headers)
}

func (r *tsRecorder) snapshot() ([]http.Header, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.headers...), append([]string(nil), r.bodies...)
}

func TestAskRetriesAndCaps(t *testing.T) {
	const okBody = `{"answers":{"operation":{"choice":"click"}},` +
		`"usage":{"input_tokens":5,"output_tokens":6},"model":"jev-1.14.0"}`

	t.Run("451 then 200 retries with auth and danger-zone headers", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		var rec tsRecorder
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if n := rec.add(r); n == 1 {
				w.WriteHeader(451)
				io.WriteString(w, `{"error":"region blocked"}`)
				return
			}
			io.WriteString(w, okBody)
		}))
		defer server.Close()
		apiURL = server.URL

		res, err := Ask(AskRequest{
			State:     map[string]any{"app": "com.example/.Main"},
			Questions: map[string]any{"operation": map[string]any{"type": "choice"}},
			Assets: map[string]Asset{
				"ref": {Type: "image", Format: "png", DataBase64: "aGk="},
			},
			TimeoutMs: 5000,
		})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if string(res.Answers["operation"]) != `{"choice":"click"}` {
			t.Fatalf("answers = %v", res.Answers)
		}
		if res.Usage == nil || res.Usage.InputTokens != 5 || res.Usage.OutputTokens != 6 {
			t.Fatalf("usage = %+v, want 5/6", res.Usage)
		}
		if res.Model != "jev-1.14.0" {
			t.Fatalf("model = %q", res.Model)
		}

		headers, bodies := rec.snapshot()
		if len(headers) != 2 {
			t.Fatalf("requests = %d, want 2 (451 then retry)", len(headers))
		}
		for i, h := range headers {
			if got := h.Get("Authorization"); got != "Bearer sk-test-12345" {
				t.Errorf("request %d Authorization = %q", i, got)
			}
			if got := h.Get("Content-Type"); got != "application/json" {
				t.Errorf("request %d Content-Type = %q", i, got)
			}
			if got := h.Get("TypeSafe-Danger-Zone-Images"); got != "true" {
				t.Errorf("request %d danger-zone header = %q, want true", i, got)
			}
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if body["model"] != DefaultModel {
			t.Errorf("body model = %v, want %s", body["model"], DefaultModel)
		}
		assets, ok := body["assets"].(map[string]any)
		if !ok || assets["ref"] == nil {
			t.Errorf("body assets = %v", body["assets"])
		}
	})

	t.Run("no assets means no danger-zone header and no assets key", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		var rec tsRecorder
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(r)
			io.WriteString(w, okBody)
		}))
		defer server.Close()
		apiURL = server.URL

		if _, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}}); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		headers, bodies := rec.snapshot()
		if len(headers) != 1 {
			t.Fatalf("requests = %d, want 1", len(headers))
		}
		if got := headers[0].Get("TypeSafe-Danger-Zone-Images"); got != "" {
			t.Fatalf("danger-zone header = %q, want absent", got)
		}
		if strings.Contains(bodies[0], `"assets"`) {
			t.Fatalf("body has an assets key: %s", bodies[0])
		}
	})

	t.Run("three 429s fail after three attempts", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		var hits int
		var mu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits++
			mu.Unlock()
			w.WriteHeader(429)
		}))
		defer server.Close()
		apiURL = server.URL

		_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}, TimeoutMs: 5000})
		tsWantExact(t, err, "typesafe api failed after 3 attempts: typesafe api 429")
		mu.Lock()
		defer mu.Unlock()
		if hits != 3 {
			t.Fatalf("requests = %d, want 3", hits)
		}
	})

	t.Run("oversized body is capped", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.Copy(w, bytes.NewReader(bytes.Repeat([]byte("a"), 2*1024*1024+1)))
		}))
		defer server.Close()
		apiURL = server.URL

		_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}, TimeoutMs: 5000})
		tsWantExact(t, err, "typesafe api response exceeded 2097152 bytes")
	})

	t.Run("non-JSON body", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html>not json</html>")
		}))
		defer server.Close()
		apiURL = server.URL

		_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}})
		tsWantExact(t, err, "typesafe api returned non-JSON body")
	})

	t.Run("missing answers", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"usage":{"input_tokens":1}}`)
		}))
		defer server.Close()
		apiURL = server.URL

		_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}})
		tsWantExact(t, err, "typesafe api response missing answers")
	})

	t.Run("http error carries status and redacted body", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			io.WriteString(w, "bad Bearer sk-test-12345 oops")
		}))
		defer server.Close()
		apiURL = server.URL

		_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}})
		msg := tsErr(t, err)
		if msg != "typesafe api 400: bad Bearer [REDACTED]" {
			t.Fatalf("error = %q", msg)
		}
		if strings.Contains(msg, "sk-test-12345") {
			t.Fatalf("error leaks the api key: %q", msg)
		}
	})

	t.Run("network timeout is retried", func(t *testing.T) {
		tsResetTypeSafe(t)
		t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
		var hits int
		var mu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits++
			n := hits
			mu.Unlock()
			if n == 1 {
				time.Sleep(250 * time.Millisecond)
			}
			io.WriteString(w, okBody)
		}))
		defer server.Close()
		apiURL = server.URL

		res, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}, TimeoutMs: 60})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if res.Answers["operation"] == nil {
			t.Fatalf("answers = %v", res.Answers)
		}
		mu.Lock()
		defer mu.Unlock()
		if hits != 2 {
			t.Fatalf("requests = %d, want 2", hits)
		}
	})
}

// A truncated response body is a transport failure, not a malformed answer:
// it retries like the JS TypeError did. io.EOF joins for the same reason (a
// reader that ends mid-body).
func TestIsNetworkErrorTreatsTruncatedBodiesAsRetryable(t *testing.T) {
	if !isNetworkError(io.ErrUnexpectedEOF) {
		t.Error("isNetworkError(io.ErrUnexpectedEOF) = false, want true")
	}
	if !isNetworkError(io.EOF) {
		t.Error("isNetworkError(io.EOF) = false, want true")
	}
	if !isNetworkError(fmt.Errorf("read body: %w", io.ErrUnexpectedEOF)) {
		t.Error("isNetworkError(wrapped io.ErrUnexpectedEOF) = false, want true")
	}
}

func TestAskRetriesATruncatedBody(t *testing.T) {
	tsResetTypeSafe(t)
	t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
	var hits int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		// Promise 100 bytes, deliver 11: the client's body read ends early.
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, `{"answers":`)
	}))
	defer server.Close()
	apiURL = server.URL

	_, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}, TimeoutMs: 5000})
	tsWantExact(t, err, "typesafe api failed after 3 attempts: unexpected EOF")
	mu.Lock()
	defer mu.Unlock()
	if hits != 3 {
		t.Fatalf("requests = %d, want 3 (a truncated body retries)", hits)
	}
}

// `"model": null` in a response means "not reported": the request's model
// stands. It must never overwrite it with the empty string.
func TestAskNullModelKeepsTheRequestModel(t *testing.T) {
	tsResetTypeSafe(t)
	t.Setenv("TYPESAFE_API_KEY", "sk-test-12345")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"answers":{"operation":{"choice":"click"}},"model":null}`)
	}))
	defer server.Close()
	apiURL = server.URL

	res, err := Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if res.Model != DefaultModel {
		t.Fatalf("model = %q, want %q (null falls back to the request model)", res.Model, DefaultModel)
	}

	res, err = Ask(AskRequest{State: map[string]any{}, Questions: map[string]any{}, Model: "jev-custom-1"})
	if err != nil {
		t.Fatalf("Ask(custom): %v", err)
	}
	if res.Model != "jev-custom-1" {
		t.Fatalf("model = %q, want the requested jev-custom-1", res.Model)
	}
}

func TestPrewarmSendsHead(t *testing.T) {
	tsResetTypeSafe(t)
	var method string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method = r.Method
		mu.Unlock()
	}))
	defer server.Close()
	apiURL = server.URL

	Prewarm()
	mu.Lock()
	defer mu.Unlock()
	if method != http.MethodHead {
		t.Fatalf("prewarm method = %q, want HEAD", method)
	}
}
