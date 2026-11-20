// check.go tests, plus the prompt parity gate for the check questions.

package main

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// checkKindAnswer is the well-formed choice answer for one kind: probabilities
// cover exactly CheckKinds and the choice is the argmax.
func checkKindAnswer(choice string) json.RawMessage {
	probs := make(map[string]float64, len(CheckKinds))
	for _, k := range CheckKinds {
		probs[k] = 0
	}
	probs[choice] = 1
	b, _ := json.Marshal(map[string]any{"choice": choice, "probabilities": probs})
	return b
}

func checkAnswers(impossible float64, kind string, evidence string) map[string]json.RawMessage {
	answers := map[string]json.RawMessage{
		"impossible": json.RawMessage(`{"noul":` + strconv.FormatFloat(impossible, 'f', -1, 64) + `}`),
		"kind":       checkKindAnswer(kind),
	}
	if evidence != "" {
		answers["evidence"] = json.RawMessage(evidence)
	}
	return answers
}

// --- pure verdicts ----------------------------------------------------------

func TestBuildCheckQuestions(t *testing.T) {
	q := BuildCheckQuestions("screenshot", "g", nil)
	if q.Impossible.Type != "noul" {
		t.Errorf("impossible type = %q, want noul", q.Impossible.Type)
	}
	if q.Kind.Type != "choice" {
		t.Errorf("kind type = %q, want choice", q.Kind.Type)
	}
	if !reflect.DeepEqual(q.Kind.Criteria.Keys(), CheckKinds) {
		t.Errorf("kind criteria = %v, want %v", q.Kind.Criteria.Keys(), CheckKinds)
	}
	if q.Evidence.Type != "box" {
		t.Errorf("evidence type = %q, want box", q.Evidence.Type)
	}
	if !strings.Contains(q.Evidence.Instructions, "`screenshot`") {
		t.Error("the evidence box grounds on the asset reference")
	}
	named := BuildCheckQuestions("r", "g", []string{"Reykjavik", "Akureyri"})
	if !strings.Contains(named.Impossible.Instructions, " The goal names these values: Reykjavik, Akureyri.") {
		t.Errorf("named values ride the noul instructions: %q", named.Impossible.Instructions)
	}
}

func TestCheckVerdict(t *testing.T) {
	evidence := &Box{Top: 0.1, Bottom: 0.2, Left: 0.1, Right: 0.9, Confidence: 0.8, HasConfidence: true}
	v := CheckVerdict(Noul{Value: 0.9}, Choice{Choice: "no_route"}, evidence)
	if !v.Impossible {
		t.Error("a terminal kind at 0.9 is impossible")
	}
	if v.Confidence != 0.9 || v.Kind != "no_route" {
		t.Errorf("verdict = %#v", v)
	}
	if v.Evidence == nil {
		t.Fatal("evidence carried through")
	}
	wantBox := CheckEvidenceBox{Top: 0.1, Bottom: 0.2, Left: 0.1, Right: 0.9}
	if v.Evidence.Box != wantBox {
		t.Errorf("evidence box = %#v, want %#v", v.Evidence.Box, wantBox)
	}
	if v.Evidence.Confidence == nil || *v.Evidence.Confidence != 0.8 {
		t.Errorf("evidence confidence = %v, want 0.8", v.Evidence.Confidence)
	}

	if CheckVerdict(Noul{Value: 0.9}, Choice{Choice: "no_route"}, nil).Evidence != nil {
		t.Error("no box answer = null evidence")
	}
	noConf := CheckVerdict(Noul{Value: 0.9}, Choice{Choice: "no_route"}, &Box{Top: 0, Bottom: 1, Left: 0, Right: 1})
	if noConf.Evidence == nil || noConf.Evidence.Confidence != nil {
		t.Errorf("an absent confidence stays null: %#v", noConf.Evidence)
	}

	cases := []struct {
		name       string
		confidence float64
		kind       string
		want       bool
	}{
		{"empty_state is never impossible", 0.9, "empty_state", false},
		{"other is never impossible", 0.9, "other", false},
		{"below the threshold", 0.5, "unavailable", false},
		{"exactly at the threshold", CheckConfidence, "unavailable", true},
		{"no_route above it", 0.9, "no_route", true},
	}
	for _, c := range cases {
		if got := CheckVerdict(Noul{Value: c.confidence}, Choice{Choice: c.kind}, nil).Impossible; got != c.want {
			t.Errorf("%s: impossible = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFormatCheck(t *testing.T) {
	got := FormatCheck(CheckVerdictResult{Impossible: true, Confidence: 0.9, Kind: "no_route"})
	if want := "impossible=yes confidence=0.90 kind=no_route"; got != want {
		t.Errorf("FormatCheck = %q, want %q", got, want)
	}
	if got := FormatCheck(CheckVerdictResult{Impossible: false, Confidence: 0.87, Kind: "empty_state"}); got != "impossible=no confidence=0.87 kind=empty_state" {
		t.Errorf("FormatCheck = %q", got)
	}
}

func TestCheckExitCode(t *testing.T) {
	if got := CheckExitCode(CheckVerdictResult{Impossible: true}); got != 2 {
		t.Errorf("impossible → exit %d, want 2", got)
	}
	if got := CheckExitCode(CheckVerdictResult{Impossible: false}); got != 0 {
		t.Errorf("not impossible → exit %d, want 0", got)
	}
}

// --- the request ------------------------------------------------------------

func TestCheckStateShape(t *testing.T) {
	withValues := CheckState{Goal: "g", ProvidedTexts: []string{"a", "b"}, Screenshot: "ref"}
	b, err := json.Marshal(withValues)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"goal":"g","provided_texts":["a","b"],"screenshot":"ref"}`; string(b) != want {
		t.Errorf("state = %s, want %s", b, want)
	}
	empty := CheckState{Goal: "g", Screenshot: "ref"}
	b, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"goal":"g","screenshot":"ref"}`; string(b) != want {
		t.Errorf("empty values omit provided_texts: %s, want %s", b, want)
	}
}

func TestRunCheckSendsOneImage(t *testing.T) {
	png := []byte("fake-png")
	var sent AskRequest
	res, err := RunCheck(CheckOpts{
		Serial: "s",
		Goal:   "build a route",
		Values: []string{"moscow, ID", "saint-petersburg, russia"},
		ShotFn: func(serial string) (ShotResult, error) {
			if serial != "s" {
				t.Errorf("shot serial = %q", serial)
			}
			return ShotResult{Png: png}, nil
		},
		AskFn: func(req AskRequest) (*AskResult, error) {
			sent = req
			return &AskResult{
				Answers: checkAnswers(0.9, "no_route",
					`{"box":{"top":0.1,"bottom":0.2,"left":0.1,"right":0.9},"confidence":0.8}`),
				Usage:     &Usage{InputTokens: 1, OutputTokens: 2},
				LatencyMs: 5,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if !res.Impossible || res.Kind != "no_route" || res.Confidence != 0.9 {
		t.Errorf("verdict = %#v", res.CheckVerdictResult)
	}
	if res.Usage == nil || res.Usage.InputTokens != 1 || res.Usage.OutputTokens != 2 || res.LatencyMs != 5 {
		t.Errorf("usage/latency plumbing = %#v", res)
	}
	if len(sent.Assets) != 1 {
		t.Fatalf("one image asset expected, got %d", len(sent.Assets))
	}
	for ref, asset := range sent.Assets {
		want := Asset{Type: "image", Format: "png", DataBase64: base64.StdEncoding.EncodeToString(png)}
		if asset != want {
			t.Errorf("asset = %#v, want %#v", asset, want)
		}
		state, ok := sent.State.(CheckState)
		if !ok {
			t.Fatalf("state type = %T", sent.State)
		}
		if state.Screenshot != ref {
			t.Errorf("state.screenshot = %q, want the asset ref %q", state.Screenshot, ref)
		}
		if !reflect.DeepEqual(state.ProvidedTexts, []string{"moscow, ID", "saint-petersburg, russia"}) {
			t.Errorf("provided texts = %#v", state.ProvidedTexts)
		}
		if state.Goal != "build a route" {
			t.Errorf("goal = %q", state.Goal)
		}
	}
	if sent.TimeoutMs != 45000 {
		t.Errorf("image request timeout = %d, want 45000", sent.TimeoutMs)
	}
	if sent.Model != "" {
		t.Errorf("the check request pins no model: %q", sent.Model)
	}
	if _, ok := sent.Questions.(CheckQuestions); !ok {
		t.Errorf("questions type = %T", sent.Questions)
	}
}

func TestRunCheckRefusesNonDefaultModelBeforeCapture(t *testing.T) {
	shot := false
	_, err := RunCheck(CheckOpts{
		Serial: "s",
		Goal:   "g",
		Model:  "other",
		ShotFn: func(string) (ShotResult, error) {
			shot = true
			return ShotResult{Png: []byte("x")}, nil
		},
		AskFn: func(AskRequest) (*AskResult, error) {
			t.Error("no request may be sent for a non-default model")
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "pinned image model") {
		t.Fatalf("error = %v, want the pinned-model refusal", err)
	}
	if want := "check needs the pinned image model " + DefaultModel + " (got other)"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if shot {
		t.Error("refusal must happen before the screenshot")
	}
}

func TestRunCheckKeepsVerdictWithoutUsableEvidence(t *testing.T) {
	cases := []struct {
		name  string
		extra string
	}{
		{"evidence missing", ""},
		{"evidence malformed", `{"box":{"top":0.9,"bottom":0.1,"left":0.1,"right":0.9}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := RunCheck(CheckOpts{
				Serial: "s",
				Goal:   "g",
				ShotFn: func(string) (ShotResult, error) { return ShotResult{Png: []byte("png")}, nil },
				AskFn: func(AskRequest) (*AskResult, error) {
					return &AskResult{Answers: checkAnswers(0.8, "unavailable", c.extra), LatencyMs: 1}, nil
				},
			})
			if err != nil {
				t.Fatalf("RunCheck: %v", err)
			}
			if !res.Impossible {
				t.Error("the verdict does not depend on the evidence box")
			}
			if res.Evidence != nil {
				t.Errorf("evidence = %#v, want null", res.Evidence)
			}
		})
	}
}

func TestRunCheckRetriesInvalidAnswerOnce(t *testing.T) {
	calls := 0
	var logs []string
	res, err := RunCheck(CheckOpts{
		Serial: "s",
		Goal:   "g",
		ShotFn: func(string) (ShotResult, error) { return ShotResult{Png: []byte("png")}, nil },
		AskFn: func(AskRequest) (*AskResult, error) {
			calls++
			if calls == 1 {
				return &AskResult{Answers: map[string]json.RawMessage{
					"impossible": json.RawMessage(`{"noul":0.9}`),
					"kind":       json.RawMessage(`{"choice":"no_route"}`), // no probabilities
				}, LatencyMs: 1}, nil
			}
			return &AskResult{Answers: checkAnswers(0.9, "no_route", ""), LatencyMs: 1}, nil
		},
		Log: func(m string) { logs = append(logs, m) },
	})
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls)
	}
	if !res.Impossible {
		t.Error("the retried answer's verdict stands")
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "retrying once") {
		t.Errorf("the retry is logged: %#v", logs)
	}
}

func TestRunCheckGivesUpAfterTwoInvalidAnswers(t *testing.T) {
	calls := 0
	_, err := RunCheck(CheckOpts{
		Serial: "s",
		Goal:   "g",
		ShotFn: func(string) (ShotResult, error) { return ShotResult{Png: []byte("png")}, nil },
		AskFn: func(AskRequest) (*AskResult, error) {
			calls++
			return &AskResult{Answers: map[string]json.RawMessage{
				"impossible": json.RawMessage(`{"noul":0.9}`),
				"kind":       json.RawMessage(`{"choice":"no_route"}`),
			}, LatencyMs: 1}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid typesafe answer") {
		t.Fatalf("error = %v, want the validation error", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one retry, then a clear error)", calls)
	}
}

// --- prompt parity gate -----------------------------------------------------

func TestGoldenCheck(t *testing.T) {
	checked := 0
	for _, f := range loadGoldenFixtures(t) {
		if f.Kind != "check" {
			continue
		}
		checked++
		t.Run(f.Name, func(t *testing.T) {
			q := BuildCheckQuestions(f.Ref, f.Goal, f.Values)
			compareGolden(t, "check-"+f.Name, marshalPrompt(t, q))
		})
	}
	if checked == 0 {
		t.Fatal("no check fixtures found")
	}
}
