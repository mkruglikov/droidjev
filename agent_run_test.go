// The goal loop's decision tests: the ask/verify/retry policy, the vision
// step, and offline episodes driven through the IO seam. Ported from the Node
// implementation's runGoal section — the stubs replace every device call, so
// no emulator and no network are involved.

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- fixtures ---------------------------------------------------------------

const episodeXMLA = `<hierarchy rotation="0">` +
	`<android.widget.FrameLayout package="com.android.settings" bounds="[0,0][1080,2340]">` +
	`<android.widget.TextView package="com.android.settings" text="Ajustes" bounds="[0,100][500,200]" clickable="true" />` +
	`</android.widget.FrameLayout></hierarchy>`

var episodeXMLB = strings.NewReplacer("Ajustes", "Titel", "com.android.settings", "com.example.b").Replace(episodeXMLA)

// A list screen: scrollable, so CanScroll is true and scroll_down is offered.
const episodeScrollA = `<hierarchy rotation="0">` +
	`<androidx.recyclerview.widget.RecyclerView package="com.android.settings" bounds="[0,0][1080,2340]" scrollable="true">` +
	`<android.widget.TextView package="com.android.settings" text="Ajustes" bounds="[0,100][500,200]" clickable="true" />` +
	`</androidx.recyclerview.widget.RecyclerView></hierarchy>`

var episodeScrollB = strings.NewReplacer("Ajustes", "Titel", "com.android.settings", "com.example.b").Replace(episodeScrollA)

// A screen with a copyable value row and an editable field already holding it.
const episodeEditXML = `<hierarchy rotation="0">` +
	`<android.widget.TextView package="com.example.b" text="IMEI-123456" bounds="[0,100][500,200]" />` +
	`<android.widget.EditText package="com.example.b" text="IMEI-123456" bounds="[0,300][1000,400]" clickable="true" />` +
	`</hierarchy>`

// The same screen with the field empty: a paste must do the work.
const episodeEditEmpty = `<hierarchy rotation="0">` +
	`<android.widget.TextView package="com.example.b" text="IMEI-123456" bounds="[0,100][500,200]" />` +
	`<android.widget.EditText package="com.example.b" text="" bounds="[0,300][1000,400]" clickable="true" />` +
	`</hierarchy>`

// The post-paste screen where the clipboard shows up as a tappable suggestion
// chip (Chrome's omnibox, most IME toolbars) instead of in the field. The copy
// source is off-screen here, so the only row holding the staged text is the
// chip itself.
const episodeEditChip = `<hierarchy rotation="0">` +
	`<android.widget.EditText package="com.example.b" text="" bounds="[0,300][1000,400]" clickable="true" />` +
	`<android.widget.Button package="com.example.b" text="IMEI-123456" bounds="[0,500][1000,600]" clickable="true" />` +
	`</hierarchy>`

// A dump with no model-facing rows: the blind screen the vision fallback exists
// for.
const episodeEmptyXML = `<hierarchy rotation="0">` +
	`<android.widget.FrameLayout package="com.android.settings" bounds="[0,0][1080,2340]" />` +
	`</hierarchy>`

// Six single-digit keys: the clock's keypad.
var episodeKeypadXML = func() string {
	var b strings.Builder
	b.WriteString(`<hierarchy rotation="0">`)
	for i := 0; i < 6; i++ {
		b.WriteString(`<android.widget.TextView package="com.google.android.deskclock" text="` +
			strconv.Itoa(i) + `" bounds="[0,` + strconv.Itoa(100+i*120) + `][1000,` + strconv.Itoa(200+i*120) +
			`]" clickable="true" />`)
	}
	b.WriteString(`</hierarchy>`)
	return b.String()
}()

func episodeLayout(t *testing.T, xml, pkg string) *Layout {
	t.Helper()
	elements, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	return &Layout{Elements: elements, RootPkg: pkg, Xml: xml}
}

// stubIO is the device-io stub: every call succeeds and does nothing; the test
// overrides what it needs. It merges over the defaults exactly like RunGoal.
func stubIO(over IO) IO {
	base := IO{
		GetLayout: func(serial string) (*Layout, error) {
			els, err := ParseUia2Xml(episodeXMLA)
			return &Layout{Elements: els, RootPkg: "com.android.settings", Xml: episodeXMLA}, err
		},
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) {
			els, err := ParseUia2Xml(episodeXMLB)
			return &Layout{Elements: els, RootPkg: "com.example.b", Xml: episodeXMLB}, err
		},
		ScreenSize: func(serial string) (*Screen, error) { return &Screen{W: 1080, H: 2340}, nil },
		Shell: func(serial string, args []string, opts RunOpts) (RunResult, error) {
			return RunResult{}, nil
		},
		Tap:          func(serial string, c Point) error { return nil },
		LongPress:    func(serial string, c Point) error { return nil },
		SlideTo:      func(serial string, el *Element, fraction float64) error { return nil },
		ScrollSwipe:  func(serial string, anchor Point, dy, screenH int, direction string) error { return nil },
		ScrollSwipeX: func(serial string, anchor Point, dx, screenW int, direction string) error { return nil },
		KeyEvent:     func(serial string, code int) error { return nil },
		TypeText:     func(serial, s string) error { return nil },
		TypeDigits:   func(serial, s string) error { return nil },
		SetClipboard: func(serial, text string) error { return nil },
		StartApp:     func(serial, pkg string) (StartResult, error) { return StartResult{Pkg: pkg, Focused: true}, nil },
		ScreenshotPng: func(serial string) (ShotResult, error) {
			return ShotResult{}, errors.New("no vision in this episode")
		},
		Prewarm:     func() {},
		Sleep:       func(ms int) {},
		DeviceToday: func(serial string) (*string, error) { d := "2026-10-27"; return &d, nil },
	}
	return mergeIO(base, over)
}

// --- answer builders --------------------------------------------------------

func choiceRaw(t *testing.T, keys []string, choice string) json.RawMessage {
	t.Helper()
	probs := make(map[string]float64, len(keys))
	for _, k := range keys {
		probs[k] = 0
	}
	probs[choice] = 1
	b, err := json.Marshal(map[string]any{"choice": choice, "probabilities": probs})
	if err != nil {
		t.Fatalf("marshal choice: %v", err)
	}
	return b
}

func choiceRawConf(t *testing.T, keys []string, choice string, conf float64) json.RawMessage {
	t.Helper()
	probs := make(map[string]float64, len(keys))
	for _, k := range keys {
		probs[k] = 0
	}
	probs[choice] = 1
	b, err := json.Marshal(map[string]any{"choice": choice, "probabilities": probs, "confidence": conf})
	if err != nil {
		t.Fatalf("marshal choice: %v", err)
	}
	return b
}

func noulRaw(v float64) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"noul": v})
	return b
}

func boxRaw(t *testing.T, box Box, conf *float64) json.RawMessage {
	t.Helper()
	answer := map[string]any{"box": map[string]float64{
		"top": box.Top, "bottom": box.Bottom, "left": box.Left, "right": box.Right,
	}}
	if conf != nil {
		answer["confidence"] = *conf
	}
	b, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("marshal box: %v", err)
	}
	return b
}

func askResult(answers map[string]json.RawMessage, latencyMs int64) *AskResult {
	return &AskResult{Answers: answers, LatencyMs: latencyMs}
}

func decisionAnswers(t *testing.T, q Questions, choice string, noul float64) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"operation": choiceRaw(t, q.Operation.Criteria.Keys(), choice),
		"goal_met":  noulRaw(noul),
	}
}

func decisionAnswersConf(t *testing.T, q Questions, choice string, noul, conf float64) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"operation": choiceRawConf(t, q.Operation.Criteria.Keys(), choice, conf),
		"goal_met":  noulRaw(noul),
	}
}

// decideStateQuestions builds the pair askDecision tests need: one clickable
// row, scrolling on, the goal's two requirements.
func decideStateQuestions(t *testing.T) (AgentState, Questions) {
	t.Helper()
	table := BuildTable(mustParse(t, episodeXMLA), &Screen{W: 1080, H: 2340})
	q := BuildQuestions(QuestionInputs{Table: table, Screen: &Screen{W: 1080, H: 2340},
		Goal: "Set the timer. Do not start it.", CanScroll: true, Clipboard: true})
	return AgentState{Goal: "Set the timer. Do not start it."}, q
}

// --- askDecision: the blocked retry -----------------------------------------

func TestAskDecisionBlockedRetry(t *testing.T) {
	state, questions := decideStateQuestions(t)
	ids := questions.Operation.Criteria.Keys()
	blockedLow := choiceRawConf(t, ids, "blocked", 0.2)
	withoutBlocked := make([]string, 0, len(ids)-1)
	for _, k := range ids {
		if k != "blocked" {
			withoutBlocked = append(withoutBlocked, k)
		}
	}
	click := choiceRaw(t, withoutBlocked, "click_0")

	var askedKeys [][]string
	askFn := func(req AskRequest) (*AskResult, error) {
		q := req.Questions.(Questions)
		askedKeys = append(askedKeys, q.Operation.Criteria.Keys())
		choice := click
		if len(askedKeys) == 1 {
			choice = blockedLow
		}
		return askResult(map[string]json.RawMessage{"operation": choice, "goal_met": noulRaw(0.5)}, 1), nil
	}

	d, err := AskDecision(AskDecisionOpts{State: state, Questions: questions, AskFn: askFn})
	if err != nil {
		t.Fatalf("AskDecision: %v", err)
	}
	if d.Operation.Choice != "click_0" {
		t.Errorf("choice = %q, want click_0 (the retry's answer)", d.Operation.Choice)
	}
	if !d.Retried {
		t.Error("retried = false, want true")
	}
	if len(askedKeys) != 2 {
		t.Fatalf("asks = %d, want 2 (blocked then retry)", len(askedKeys))
	}
	if containsStr(askedKeys[1], "blocked") {
		t.Errorf("retry still offered blocked: %v", askedKeys[1])
	}
	if !reflect.DeepEqual(d.Criteria, withoutBlocked) {
		t.Errorf("criteria = %v, want %v", d.Criteria, withoutBlocked)
	}
}

// A high-confidence blocked is final: no retry ask.
func TestAskDecisionBlockedHighConfidenceIsFinal(t *testing.T) {
	state, questions := decideStateQuestions(t)
	ids := questions.Operation.Criteria.Keys()
	asks := 0
	askFn := func(req AskRequest) (*AskResult, error) {
		asks++
		return askResult(map[string]json.RawMessage{
			"operation": choiceRawConf(t, ids, "blocked", 0.9),
			"goal_met":  noulRaw(0.2),
		}, 1), nil
	}
	d, err := AskDecision(AskDecisionOpts{State: state, Questions: questions, AskFn: askFn})
	if err != nil {
		t.Fatalf("AskDecision: %v", err)
	}
	if d.Operation.Choice != "blocked" || d.Retried {
		t.Errorf("decision = %+v, want an unretried blocked", d)
	}
	if asks != 1 {
		t.Errorf("asks = %d, want 1", asks)
	}
}

// --- askDecision: done verification -----------------------------------------

func TestAskDecisionVerifiesDone(t *testing.T) {
	state, questions := decideStateQuestions(t)
	ids := questions.Operation.Criteria.Keys()
	verdict := func(v string) json.RawMessage {
		met := 0.0
		unmet := 0.0
		if v == "met" {
			met = 1
		} else {
			unmet = 1
		}
		b, _ := json.Marshal(map[string]any{"choice": v,
			"probabilities": map[string]float64{"met": met, "unmet": unmet}})
		return b
	}
	redoIDs := make([]string, 0, len(ids)-1)
	for _, k := range ids {
		if k != "done" {
			redoIDs = append(redoIDs, k)
		}
	}

	asks := 0
	askFn := func(req AskRequest) (*AskResult, error) {
		asks++
		switch q := req.Questions.(type) {
		case map[string]Question:
			if len(q) != 2 {
				t.Errorf("verify questions = %d, want 2", len(q))
			}
			return askResult(map[string]json.RawMessage{
				"req_0": verdict("met"),
				"req_1": verdict("unmet"),
			}, 2), nil
		case Questions:
			if asks == 1 {
				return askResult(decisionAnswers(t, q, "done", 0.6), 1), nil
			}
			// The re-ask after rejection: done withdrawn, the note spliced in.
			if q.Operation.Criteria.Has("done") {
				t.Error("done still offered after rejection")
			}
			if !containsSubstr(q.Operation.Instructions.Rules, "failed verification") {
				t.Error("the re-ask does not tell the model which requirements are unmet")
			}
			return askResult(decisionAnswers(t, q, "click_0", 0.3), 3), nil
		}
		t.Fatalf("unexpected questions type %T", req.Questions)
		return nil, nil
	}

	d, err := AskDecision(AskDecisionOpts{State: state, Questions: questions, AskFn: askFn})
	if err != nil {
		t.Fatalf("AskDecision: %v", err)
	}
	if d.Operation.Choice != "click_0" {
		t.Errorf("choice = %q, want click_0 (the rejected done becomes the re-asked action)", d.Operation.Choice)
	}
	if d.DoneVerify == nil || !reflect.DeepEqual(d.DoneVerify.Unmet, []string{"Do not start it."}) {
		t.Errorf("doneVerify = %+v, want unmet [Do not start it.]", d.DoneVerify)
	}
	if asks != 3 {
		t.Errorf("asks = %d, want 3 (decide + verify + re-ask)", asks)
	}
	if d.LatencyMs != 1+2+3 {
		t.Errorf("latency = %d, want 6 (all three asks)", d.LatencyMs)
	}

	// All-met verification lets the done through.
	askPass := func(req AskRequest) (*AskResult, error) {
		switch q := req.Questions.(type) {
		case map[string]Question:
			return askResult(map[string]json.RawMessage{
				"req_0": verdict("met"), "req_1": verdict("met"),
			}, 2), nil
		case Questions:
			return askResult(decisionAnswers(t, q, "done", 0.9), 1), nil
		}
		return nil, nil
	}
	pass, err := AskDecision(AskDecisionOpts{State: state, Questions: questions, AskFn: askPass})
	if err != nil {
		t.Fatalf("AskDecision(pass): %v", err)
	}
	if pass.Operation.Choice != "done" || pass.DoneVerify != nil {
		t.Errorf("pass = %+v, want a verified done with no redo", pass)
	}

	// A non-done decision never triggers a verification ask.
	plainCalls := 0
	plain, err := AskDecision(AskDecisionOpts{State: state, Questions: questions,
		AskFn: func(req AskRequest) (*AskResult, error) {
			plainCalls++
			return askResult(decisionAnswers(t, req.Questions.(Questions), "click_0", 0.1), 1), nil
		}})
	if err != nil {
		t.Fatalf("AskDecision(plain): %v", err)
	}
	if plain.Operation.Choice != "click_0" || plainCalls != 1 {
		t.Errorf("plain = %+v after %d asks, want click_0 after 1", plain, plainCalls)
	}
}

// A verify response missing or mangling a req_i verdict counts that requirement
// as unmet: judged by index, never by present keys.
func TestAskDecisionVerifyJudgesByIndex(t *testing.T) {
	state, questions := decideStateQuestions(t)
	ids := questions.Operation.Criteria.Keys()
	for _, verifyAnswers := range []map[string]json.RawMessage{
		{"req_0": json.RawMessage(`{"choice":"met","probabilities":{"met":1,"unmet":0}}`)},
		{
			"req_0": json.RawMessage(`{"choice":"met","probabilities":{"met":1,"unmet":0}}`),
			"req_1": json.RawMessage(`{"choice":"met"}`),
		},
	} {
		call := 0
		askFn := func(req AskRequest) (*AskResult, error) {
			switch q := req.Questions.(type) {
			case map[string]Question:
				return askResult(verifyAnswers, 2), nil
			case Questions:
				call++
				if call == 1 {
					return askResult(decisionAnswers(t, q, "done", 0.5), 1), nil
				}
				if call == 2 {
					return askResult(map[string]json.RawMessage{
						"operation": json.RawMessage(`{"choice":"nope","probabilities":{}}`),
						"goal_met":  noulRaw(0.1),
					}, 3), nil
				}
				redoIDs := make([]string, 0, len(ids)-1)
				for _, k := range ids {
					if k != "done" {
						redoIDs = append(redoIDs, k)
					}
				}
				return askResult(map[string]json.RawMessage{
					"operation": choiceRaw(t, redoIDs, "click_0"),
					"goal_met":  noulRaw(0.1),
				}, 3), nil
			}
			return nil, nil
		}
		d, err := AskDecision(AskDecisionOpts{State: state, Questions: questions, AskFn: askFn})
		if err != nil {
			t.Fatalf("AskDecision: %v", err)
		}
		if d.DoneVerify == nil || !reflect.DeepEqual(d.DoneVerify.Unmet, []string{"Do not start it."}) {
			t.Errorf("doneVerify = %+v, want the missing verdict counted unmet", d.DoneVerify)
		}
		if d.Operation.Choice != "click_0" || !d.InvalidAnswer {
			t.Errorf("decision = %+v, want click_0 with invalidAnswer (the redo asked twice)", d)
		}
	}
}

// A blocked retry that answers done still goes through the done-checker.
func TestAskDecisionBlockedRetryDoneIsVerified(t *testing.T) {
	state, questions := decideStateQuestions(t)
	ids := questions.Operation.Criteria.Keys()
	withoutBlocked := make([]string, 0, len(ids)-1)
	for _, k := range ids {
		if k != "blocked" {
			withoutBlocked = append(withoutBlocked, k)
		}
	}
	verdict := func(v string) json.RawMessage {
		met := 0.0
		unmet := 0.0
		if v == "met" {
			met = 1
		} else {
			unmet = 1
		}
		b, _ := json.Marshal(map[string]any{"choice": v,
			"probabilities": map[string]float64{"met": met, "unmet": unmet}})
		return b
	}
	asks := 0
	d, err := AskDecision(AskDecisionOpts{State: state, Questions: questions,
		AskFn: func(req AskRequest) (*AskResult, error) {
			asks++
			switch req.Questions.(type) {
			case map[string]Question:
				return askResult(map[string]json.RawMessage{
					"req_0": verdict("met"), "req_1": verdict("met"),
				}, 1), nil
			case Questions:
				if asks == 1 {
					return askResult(map[string]json.RawMessage{
						"operation": choiceRawConf(t, ids, "blocked", 0.2),
						"goal_met":  noulRaw(0.5),
					}, 1), nil
				}
				return askResult(map[string]json.RawMessage{
					"operation": choiceRaw(t, withoutBlocked, "done"),
					"goal_met":  noulRaw(0.5),
				}, 1), nil
			}
			return nil, nil
		}})
	if err != nil {
		t.Fatalf("AskDecision: %v", err)
	}
	if d.Operation.Choice != "done" || d.DoneVerify != nil {
		t.Errorf("decision = %+v, want a verified done", d)
	}
	if asks != 3 {
		t.Errorf("asks = %d, want 3 (blocked → retry → verify)", asks)
	}
}

// --- askDecision: invalid answers -------------------------------------------

func TestAskDecisionInvalidAnswerRetriesThenSyntheticWait(t *testing.T) {
	state, questions := decideStateQuestions(t)
	ids := questions.Operation.Criteria.Keys()
	good := choiceRaw(t, ids, "click_0")
	bad := json.RawMessage(`{"choice":"nope","probabilities":{}}`)
	wrap := func(operation json.RawMessage) map[string]json.RawMessage {
		return map[string]json.RawMessage{"operation": operation, "goal_met": noulRaw(0.1)}
	}

	// Invalid once: the retry's valid answer executes.
	n := 0
	once, err := AskDecision(AskDecisionOpts{State: state, Questions: questions,
		AskFn: func(req AskRequest) (*AskResult, error) {
			op := bad
			if n > 0 {
				op = good
			}
			n++
			return askResult(wrap(op), 1), nil
		}})
	if err != nil {
		t.Fatalf("AskDecision(once): %v", err)
	}
	if once.Operation.Choice != "click_0" || !once.InvalidAnswer || once.Retried {
		t.Errorf("once = %+v, want click_0 with invalidAnswer and no retried flag", once)
	}
	if !reflect.DeepEqual(once.Criteria, ids) {
		t.Errorf("criteria = %v, want %v", once.Criteria, ids)
	}
	if once.Answers["operation"] == nil || !strings.Contains(string(once.Answers["operation"]), "click_0") {
		t.Errorf("answers = %s, want the effective (retried) answer", once.Answers["operation"])
	}

	// Invalid twice: a schema-complete synthetic wait, never a throw.
	twice, err := AskDecision(AskDecisionOpts{State: state, Questions: questions,
		AskFn: func(req AskRequest) (*AskResult, error) { return askResult(wrap(bad), 1), nil }})
	if err != nil {
		t.Fatalf("AskDecision(twice): %v", err)
	}
	if twice.Operation.Choice != "wait" {
		t.Errorf("choice = %q, want the synthetic wait", twice.Operation.Choice)
	}
	if !reflect.DeepEqual(twice.Operation.Probabilities, map[string]float64{"wait": 1}) {
		t.Errorf("probabilities = %v, want {wait:1}", twice.Operation.Probabilities)
	}
	if twice.Operation.Confidence != 0 || !twice.InvalidAnswer {
		t.Errorf("synthetic wait = %+v, want confidence 0 and invalidAnswer", twice.Operation)
	}
	if twice.GoalMet.Value != 0 {
		t.Errorf("goalMet = %v, want 0", twice.GoalMet.Value)
	}

	// The blocked-retry ask gets the same treatment.
	withoutBlocked := make([]string, 0, len(ids)-1)
	for _, k := range ids {
		if k != "blocked" {
			withoutBlocked = append(withoutBlocked, k)
		}
	}
	seq := []json.RawMessage{choiceRawConf(t, ids, "blocked", 0.2), bad, choiceRaw(t, withoutBlocked, "click_0")}
	m := 0
	after, err := AskDecision(AskDecisionOpts{State: state, Questions: questions,
		AskFn: func(req AskRequest) (*AskResult, error) {
			op := seq[m]
			if m < len(seq)-1 {
				m++
			}
			return askResult(wrap(op), 1), nil
		}})
	if err != nil {
		t.Fatalf("AskDecision(after blocked): %v", err)
	}
	if after.Operation.Choice != "click_0" || !after.Retried || !after.InvalidAnswer {
		t.Errorf("decision = %+v, want click_0, retried, invalidAnswer", after)
	}
	if !reflect.DeepEqual(after.Criteria, withoutBlocked) {
		t.Errorf("criteria = %v, want %v", after.Criteria, withoutBlocked)
	}
}

// --- askVisionVerdict -------------------------------------------------------

func TestAskVisionVerdictRetriesThenWithholds(t *testing.T) {
	questions := BuildVisionQuestions("00000000-0000-0000-0000-000000000000", false, nil)
	keys := questions.Operation.Criteria.Keys()
	goodOp := choiceRaw(t, keys, "tap_box")
	badOp := json.RawMessage(`{"choice":"tap_box","probabilities":{}}`)
	conf := 0.9
	box := Box{Top: 0.1, Bottom: 0.5, Left: 0.1, Right: 0.5}
	payload := VisionAsk{
		State:     VisionState{Goal: "g"},
		Questions: questions,
		Assets:    map[string]Asset{},
		TimeoutMs: 1,
	}
	good := map[string]json.RawMessage{
		"operation": goodOp, "screen_readable": noulRaw(0.9), "tap_target": boxRaw(t, box, &conf),
	}
	bad := map[string]json.RawMessage{
		"operation": badOp, "screen_readable": noulRaw(0.9), "tap_target": boxRaw(t, box, &conf),
	}

	n := 0
	once, err := AskVisionVerdict(func(req AskRequest) (*AskResult, error) {
		answers := good
		if n == 0 {
			answers = bad
		}
		n++
		return askResult(answers, 5), nil
	}, payload, nil)
	if err != nil {
		t.Fatalf("AskVisionVerdict(once): %v", err)
	}
	if once.Operation.Choice != "tap_box" || !once.InvalidAnswer || once.Withheld {
		t.Errorf("once = %+v, want tap_box after one invalid answer", once)
	}
	if once.APIms != 5 {
		t.Errorf("apiMs = %d, want 5", once.APIms)
	}

	twice, err := AskVisionVerdict(func(req AskRequest) (*AskResult, error) {
		return askResult(bad, 5), nil
	}, payload, nil)
	if err != nil {
		t.Fatalf("AskVisionVerdict(twice): %v", err)
	}
	if !twice.Withheld {
		t.Errorf("twice = %+v, want withheld", twice)
	}
}

// --- visionStep episodes ----------------------------------------------------

func TestRunGoalVisionTapsTheBoxCenter(t *testing.T) {
	layout := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	var taps []Point
	var shots int
	box := Box{Top: 0.1, Bottom: 0.5, Left: 0.1, Right: 0.5}
	conf := 0.9
	var seenAssets map[string]Asset
	var seenRules []string
	var seenTimeout int64

	io := stubIO(IO{
		GetLayout:     func(serial string) (*Layout, error) { return layout, nil },
		Tap:           func(serial string, c Point) error { taps = append(taps, c); return nil },
		ScreenshotPng: func(serial string) (ShotResult, error) { shots++; return ShotResult{Png: []byte("png")}, nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 1, Vision: true, IO: &io,
		Guidance: []string{"use the Drive tab"},
		AskFn: func(req AskRequest) (*AskResult, error) {
			vq, ok := req.Questions.(VisionQuestions)
			if !ok {
				t.Fatalf("questions = %T, want VisionQuestions", req.Questions)
			}
			seenAssets = req.Assets
			seenRules = vq.Operation.Instructions.Rules
			seenTimeout = req.TimeoutMs
			return askResult(map[string]json.RawMessage{
				"operation":       choiceRaw(t, vq.Operation.Criteria.Keys(), "tap_box"),
				"screen_readable": noulRaw(0.9),
				"tap_target":      boxRaw(t, box, &conf),
			}, 7), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if shots != 1 {
		t.Fatalf("screenshots = %d, want 1", shots)
	}
	if len(seenAssets) != 1 {
		t.Fatalf("assets = %v, want exactly one image", seenAssets)
	}
	for _, asset := range seenAssets {
		if asset.Type != "image" || asset.Format != "png" {
			t.Errorf("asset = %+v, want an image/png", asset)
		}
		if asset.DataBase64 != base64.StdEncoding.EncodeToString([]byte("png")) {
			t.Errorf("asset data = %q, want the screenshot's base64", asset.DataBase64)
		}
	}
	if !containsSubstr(seenRules, "Guidance from the operator: use the Drive tab") {
		t.Errorf("vision rules = %v, want the operator guidance", seenRules)
	}
	if seenTimeout != 45000 {
		t.Errorf("timeout = %d, want 45000", seenTimeout)
	}
	if len(taps) != 1 {
		t.Fatalf("taps = %v, want the validated box center", taps)
	}
	if want := (Point{X: 324, Y: 702}); taps[0] != want {
		t.Errorf("tap = %v, want %v", taps[0], want)
	}
	if got := res.Steps[0].Action; got != "tap (by screenshot) 324,702" {
		t.Errorf("action = %q", got)
	}
	if res.Steps[0].Changed == nil || !*res.Steps[0].Changed {
		t.Errorf("changed = %v, want true after the settle", res.Steps[0].Changed)
	}
}

func TestRunGoalVisionUnreadableBlocks(t *testing.T) {
	layout := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	conf := 0.9
	io := stubIO(IO{
		GetLayout:     func(serial string) (*Layout, error) { return layout, nil },
		ScreenshotPng: func(serial string) (ShotResult, error) { return ShotResult{Png: []byte("png")}, nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 4, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			vq := req.Questions.(VisionQuestions)
			return askResult(map[string]json.RawMessage{
				"operation":       choiceRaw(t, vq.Operation.Criteria.Keys(), "wait"),
				"screen_readable": noulRaw(0.2),
				"tap_target":      boxRaw(t, Box{Top: 0, Bottom: 1, Left: 0, Right: 1}, &conf),
			}, 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "blocked" || res.BlockedReason == nil || *res.BlockedReason != "blind_screen" {
		t.Errorf("status = %q reason = %v, want blocked/blind_screen", res.Status, res.BlockedReason)
	}
	if len(res.Steps) != 1 || res.Steps[0].Action != "blocked (vision, screen_readable=0.20)" {
		t.Errorf("steps = %+v", res.Steps)
	}
}

func TestRunGoalVisionScreenshotFailureBlocks(t *testing.T) {
	layout := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) { return layout, nil },
		ScreenshotPng: func(serial string) (ShotResult, error) {
			return ShotResult{}, NewDroidJevError("screencap exited 1")
		},
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 2, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			t.Error("the model must not be asked when the screenshot pipeline fails")
			return nil, errors.New("unreachable")
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "blocked" || res.BlockedReason == nil || *res.BlockedReason != "blind_screen" {
		t.Errorf("status = %q reason = %v, want blocked/blind_screen", res.Status, res.BlockedReason)
	}
	if len(res.Steps) != 1 || res.Steps[0].Action != "blocked (screenshot capture failed on a blind screen)" {
		t.Errorf("steps = %+v", res.Steps)
	}
	if res.Steps[0].Changed != nil {
		t.Errorf("changed = %v, want null", res.Steps[0].Changed)
	}
}

func TestRunGoalVisionInvalidTwiceWithholdsStep(t *testing.T) {
	layout := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	conf := 0.9
	io := stubIO(IO{
		GetLayout:     func(serial string) (*Layout, error) { return layout, nil },
		ScreenshotPng: func(serial string) (ShotResult, error) { return ShotResult{Png: []byte("png")}, nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 1, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			return askResult(map[string]json.RawMessage{
				"operation":       json.RawMessage(`{"choice":"tap_box","probabilities":{}}`),
				"screen_readable": noulRaw(0.9),
				"tap_target":      boxRaw(t, Box{Top: 0, Bottom: 1, Left: 0, Right: 1}, &conf),
			}, 5), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "max_steps" {
		t.Errorf("status = %q, want max_steps (the step is withheld, not fatal)", res.Status)
	}
	rec := res.Steps[0]
	if rec.Choice != nil {
		t.Errorf("choice = %v, want null", *rec.Choice)
	}
	if rec.Action != "withheld (invalid model answer after retry)" {
		t.Errorf("action = %q", rec.Action)
	}
	if rec.Changed == nil || *rec.Changed {
		t.Errorf("changed = %v, want false", rec.Changed)
	}
	if rec.APIMs != 5 {
		t.Errorf("apiMs = %d, want 5", rec.APIMs)
	}
}

func TestRunGoalVisionLowConfidenceBoxIsWithheld(t *testing.T) {
	layout := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	conf := 0.5
	taps := 0
	io := stubIO(IO{
		GetLayout:     func(serial string) (*Layout, error) { return layout, nil },
		Tap:           func(serial string, c Point) error { taps++; return nil },
		ScreenshotPng: func(serial string) (ShotResult, error) { return ShotResult{Png: []byte("png")}, nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 1, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			vq := req.Questions.(VisionQuestions)
			return askResult(map[string]json.RawMessage{
				"operation":       choiceRaw(t, vq.Operation.Criteria.Keys(), "tap_box"),
				"screen_readable": noulRaw(0.9),
				"tap_target":      boxRaw(t, Box{Top: 0.1, Bottom: 0.5, Left: 0.1, Right: 0.5}, &conf),
			}, 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if taps != 0 {
		t.Errorf("taps = %d, want 0 (box confidence below the gate)", taps)
	}
	if res.Steps[0].Action != "tap withheld (box confidence 0.50)" {
		t.Errorf("action = %q", res.Steps[0].Action)
	}
	if res.Steps[0].Changed == nil || *res.Steps[0].Changed {
		t.Errorf("changed = %v, want false", res.Steps[0].Changed)
	}
}

func TestRunGoalCustomModelNeverSentImages(t *testing.T) {
	layout := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	shots := 0
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) { return layout, nil },
		ScreenshotPng: func(serial string) (ShotResult, error) {
			shots++
			return ShotResult{}, errors.New("must not capture")
		},
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open settings", MaxSteps: 2, Vision: true,
		Model: "jev-custom-1", IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			t.Error("the model must not be asked")
			return nil, errors.New("unreachable")
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "blocked" || res.BlockedReason == nil || *res.BlockedReason != "blind_screen" {
		t.Errorf("status = %q reason = %v", res.Status, res.BlockedReason)
	}
	if !strings.Contains(res.Steps[0].Action, "custom model") {
		t.Errorf("action = %q, want the custom-model notice", res.Steps[0].Action)
	}
	if shots != 0 {
		t.Errorf("screenshots = %d, want 0", shots)
	}
}

// --- runGoal: infrastructure containment ------------------------------------

func TestRunGoalInfrastructureFailureMidRun(t *testing.T) {
	taps := 0
	settles := 0
	io := stubIO(IO{
		Tap: func(serial string, c Point) error { taps++; return nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) {
			settles++
			if settles >= 2 {
				return nil, NewDroidJevError("uia2 HTTP 500 on /wd/hub/session/x/source")
			}
			return episodeLayout(t, episodeXMLB, "com.example.b"), nil
		},
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 4, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			q := req.Questions.(Questions)
			choice := "wait"
			for _, k := range q.Operation.Criteria.Keys() {
				if strings.HasPrefix(k, "click_") {
					choice = k
					break
				}
			}
			return askResult(decisionAnswers(t, q, choice, 0.5), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "error" || res.BlockedReason == nil || *res.BlockedReason != "infrastructure" {
		t.Fatalf("status = %q reason = %v, want error/infrastructure", res.Status, res.BlockedReason)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("steps = %d, want the completed record plus the error step", len(res.Steps))
	}
	if res.Steps[0].Choice == nil || *res.Steps[0].Choice != "click_0" {
		t.Errorf("step 1 = %+v, want click_0", res.Steps[0])
	}
	if !strings.Contains(res.Steps[1].Action, "uia2 HTTP 500") {
		t.Errorf("error step = %q", res.Steps[1].Action)
	}
	if res.Steps[1].Choice == nil || *res.Steps[1].Choice != "error" {
		t.Errorf("error step choice = %v", res.Steps[1].Choice)
	}
	if res.Tokens != (Tokens{}) {
		t.Errorf("tokens = %+v, want zero", res.Tokens)
	}
	if res.FinalApp == nil || *res.FinalApp != "com.example.b" {
		t.Errorf("finalApp = %v, want com.example.b", res.FinalApp)
	}
	if len(res.FinalRows) == 0 {
		t.Error("finalRows is empty, want the post-failure table")
	}
	if taps == 0 {
		t.Error("the first step tapped before the transport died")
	}
}

func TestRunGoalFailureBeforeFirstStep(t *testing.T) {
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) {
			return nil, NewDroidJevError("uia2 server did not become ready on port 6790 within 20s")
		},
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open settings", IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			t.Error("askFn must not be reached")
			return nil, errors.New("unreachable")
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "error" || res.BlockedReason == nil || *res.BlockedReason != "infrastructure" {
		t.Fatalf("status = %q reason = %v", res.Status, res.BlockedReason)
	}
	if len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Action, "did not become ready") {
		t.Errorf("steps = %+v", res.Steps)
	}
	if res.FinalRows == nil || len(res.FinalRows) != 0 {
		t.Errorf("finalRows = %#v, want []", res.FinalRows)
	}
	if res.FinalApp != nil || res.Answer != nil {
		t.Errorf("finalApp = %v answer = %v, want null", res.FinalApp, res.Answer)
	}
}

// Prewarm is fire-and-forget: the loop must not join the key-less HEAD before
// the first device call. The stub blocks until released; an awaited pre-warm
// would leave RunGoal stuck and the timeout catches it.
func TestRunGoalPrewarmIsNotAwaited(t *testing.T) {
	called := make(chan struct{}, 1)
	release := make(chan struct{})
	io := stubIO(IO{
		ScreenSize: func(serial string) (*Screen, error) {
			return nil, NewDroidJevError("no device")
		},
		Prewarm: func() {
			called <- struct{}{}
			<-release
		},
	})
	done := make(chan error, 1)
	go func() {
		_, err := RunGoal(GoalOpts{
			Serial: "emulator-5554", Goal: "open settings", IO: &io,
			AskFn: func(AskRequest) (*AskResult, error) { return nil, errors.New("unreachable") },
			Log:   func(string) {},
		})
		done <- err
	}()
	select {
	case err := <-done:
		close(release)
		if err != nil {
			t.Fatalf("RunGoal: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("RunGoal awaited Prewarm")
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Error("Prewarm was never fired")
	}
}

// A non-DroidJevError is a real bug and propagates.
func TestRunGoalNonDroidJevErrorPropagates(t *testing.T) {
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) { return nil, errors.New("boom") },
	})
	_, err := RunGoal(GoalOpts{Serial: "s", Goal: "g", IO: &io, Log: func(string) {}})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want the raw bug propagated", err)
	}
}

// --- runGoal: the goal's own values -----------------------------------------

func TestRunGoalValuesFromGoalTexts(t *testing.T) {
	var captured AgentState
	io := stubIO(IO{})
	_, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: `type "Bad;Value" and "Good"`, Texts: []string{"good"},
		MaxSteps: 1, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			captured = req.State.(AgentState)
			return askResult(decisionAnswers(t, req.Questions.(Questions), "wait", 0.5), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if !reflect.DeepEqual(captured.ProvidedTexts, []string{"good"}) {
		t.Errorf("provided_texts = %#v, want [good] (deduped case-insensitively, untypeable dropped)",
			captured.ProvidedTexts)
	}

	// An explicit --text that cannot be typed fails fast, before any device call.
	dumps := 0
	io = stubIO(IO{GetLayout: func(serial string) (*Layout, error) {
		dumps++
		return episodeLayout(t, episodeXMLA, "com.android.settings"), nil
	}})
	_, err = RunGoal(GoalOpts{Serial: "s", Goal: "g", Texts: []string{"bad;text"}, IO: &io, Log: func(string) {}})
	if err == nil || !strings.Contains(err.Error(), "cannot be typed safely") {
		t.Fatalf("err = %v, want the sanitize failure", err)
	}
	if dumps != 0 {
		t.Errorf("layout dumps = %d, want 0 (fail before touching the device)", dumps)
	}
}

// --- runGoal: state, memory, requirements -----------------------------------

func TestRunGoalRejectedDoneStickyNote(t *testing.T) {
	io := stubIO(IO{})
	var opStates []AgentState
	var opRules [][]string
	verifies := 0
	verdict := func(v string) json.RawMessage {
		met := 0.0
		unmet := 0.0
		if v == "met" {
			met = 1
		} else {
			unmet = 1
		}
		b, _ := json.Marshal(map[string]any{"choice": v,
			"probabilities": map[string]float64{"met": met, "unmet": unmet}})
		return b
	}
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "Set the timer. Do not start it.", MaxSteps: 4, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			switch q := req.Questions.(type) {
			case map[string]Question:
				verifies++
				if verifies == 1 {
					return askResult(map[string]json.RawMessage{
						"req_0": verdict("met"), "req_1": verdict("unmet"),
					}, 1), nil
				}
				return askResult(map[string]json.RawMessage{
					"req_0": verdict("met"), "req_1": verdict("met"),
				}, 1), nil
			case Questions:
				opStates = append(opStates, req.State.(AgentState))
				opRules = append(opRules, q.Operation.Instructions.Rules)
				choice := "done"
				if len(opStates) == 2 {
					choice = "click_0"
				}
				return askResult(decisionAnswers(t, q, choice, 0.5), 1), nil
			}
			return nil, nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "done" {
		t.Errorf("status = %q, want done", res.Status)
	}
	if len(opStates) != 3 {
		t.Fatalf("op asks = %d, want 3 (initial done, redo action, verified done)", len(opStates))
	}
	if !reflect.DeepEqual(opStates[0].Requirements, []string{"Set the timer.", "Do not start it."}) {
		t.Errorf("requirements = %#v, want both from the start", opStates[0].Requirements)
	}
	if opStates[0].VerificationNote != nil {
		t.Errorf("note = %v, want none before any rejection", *opStates[0].VerificationNote)
	}
	if opStates[2].VerificationNote == nil || !strings.Contains(*opStates[2].VerificationNote, "Do not start it.") {
		t.Errorf("note = %v, want the unmet requirement to stick into the next step", opStates[2].VerificationNote)
	}
	if !containsSubstr(opRules[2], "failed verification") || !containsSubstr(opRules[2], "Do not start it.") {
		t.Errorf("rules tail = %v, want the note", opRules[2])
	}
	if verifies != 2 {
		t.Errorf("verifies = %d, want 2 done claims, two verifications", verifies)
	}
}

func TestRunGoalVisitedScreensAndNavigation(t *testing.T) {
	settles := []*Layout{
		episodeLayout(t, episodeXMLB, "com.example.b"),
		episodeLayout(t, episodeXMLA, "com.android.settings"),
	}
	n := 0
	io := stubIO(IO{SettleLayout: func(serial string, maxMs int64) (*Layout, error) {
		l := settles[n]
		if n < len(settles)-1 {
			n++
		}
		return l, nil
	}})
	var opStates []AgentState
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "g", MaxSteps: 3, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			opStates = append(opStates, req.State.(AgentState))
			choice := []string{"wait", "click_0", "back"}[len(opStates)-1]
			return askResult(decisionAnswers(t, req.Questions.(Questions), choice, 0.5), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "max_steps" {
		t.Errorf("status = %q, want max_steps", res.Status)
	}
	if len(opStates) != 3 {
		t.Fatalf("op asks = %d, want 3", len(opStates))
	}
	if opStates[1].SeenBefore {
		t.Error("seen_before = true on the first visit to B")
	}
	if !opStates[2].SeenBefore {
		t.Error("seen_before = false, want true (A arrived a second time)")
	}
	var apps []string
	for _, nav := range opStates[2].Navigation {
		apps = append(apps, nav.App)
	}
	if !reflect.DeepEqual(apps, []string{"com.android.settings", "com.example.b", "com.android.settings"}) {
		t.Errorf("navigation = %v, want the initial app plus each change", apps)
	}
	if opStates[2].Screen.App != "com.android.settings" {
		t.Errorf("screen.app = %q", opStates[2].Screen.App)
	}
}

// The remaining executor branches, one short episode each: every op the
// builders offer must reach the right device call with the right argument.
func TestRunGoalExecutorBranches(t *testing.T) {
	runOne := func(t *testing.T, xml, goal string, texts []string, apps []App, choice string,
		stub func(*IO), check func(t *testing.T, res GoalResult)) {
		t.Helper()
		layout := episodeLayout(t, xml, "com.example.b")
		io := stubIO(IO{
			GetLayout:    func(serial string) (*Layout, error) { return layout, nil },
			SettleLayout: func(serial string, maxMs int64) (*Layout, error) { return layout, nil },
		})
		stub(&io)
		res, err := RunGoal(GoalOpts{
			Serial: "emulator-5554", Goal: goal, Texts: texts, Apps: apps, MaxSteps: 1, Vision: true, IO: &io,
			AskFn: func(req AskRequest) (*AskResult, error) {
				return askResult(decisionAnswers(t, req.Questions.(Questions), choice, 0.5), 1), nil
			},
			Log: func(string) {},
		})
		if err != nil {
			t.Fatalf("RunGoal: %v", err)
		}
		check(t, res)
	}

	t.Run("type taps, waits, then types the provided value", func(t *testing.T) {
		xml := `<hierarchy rotation="0"><android.widget.EditText package="com.example.b" text="" bounds="[0,100][1000,200]" clickable="true" /></hierarchy>`
		var tapped, typed string
		var slept int
		runOne(t, xml, `type "Maria" into the field`, nil, nil, "type_0_0", func(io *IO) {
			io.Tap = func(serial string, c Point) error { tapped = "tap"; return nil }
			io.Sleep = func(ms int) { slept = ms }
			io.TypeText = func(serial, s string) error { typed = s; return nil }
		}, func(t *testing.T, res GoalResult) {
			if tapped != "tap" || slept != 350 || typed != "Maria" {
				t.Errorf("tap=%q sleep=%d typed=%q, want tap/350/Maria", tapped, slept, typed)
			}
			if !strings.Contains(res.Steps[0].Action, `type "Maria"`) {
				t.Errorf("action = %q", res.Steps[0].Action)
			}
		})
	})

	t.Run("hold long-presses the row", func(t *testing.T) {
		xml := `<hierarchy rotation="0"><android.widget.LinearLayout package="com.example.b" text="Note" bounds="[0,100][1000,200]" long-clickable="true" /></hierarchy>`
		var held Point
		runOne(t, xml, "delete the note", nil, nil, "hold_0", func(io *IO) {
			io.LongPress = func(serial string, c Point) error { held = c; return nil }
		}, func(t *testing.T, res GoalResult) {
			if held != (Point{X: 500, Y: 150}) {
				t.Errorf("hold at %v, want 500,150", held)
			}
			if res.Steps[0].Action != `hold [0] longclick "Note"` {
				t.Errorf("action = %q", res.Steps[0].Action)
			}
		})
	})

	t.Run("set drags the slider to the named stop", func(t *testing.T) {
		xml := `<hierarchy rotation="0"><android.widget.SeekBar package="com.android.settings" text="197.0" bounds="[0,100][1000,200]" /></hierarchy>`
		var fraction float64 = -1
		runOne(t, xml, "set the brightness to maximum", nil, nil, "set_0_max", func(io *IO) {
			io.SlideTo = func(serial string, el *Element, f float64) error { fraction = f; return nil }
		}, func(t *testing.T, res GoalResult) {
			if fraction != 1 {
				t.Errorf("fraction = %v, want 1 (the maximum)", fraction)
			}
			if !strings.Contains(res.Steps[0].Action, "to its maximum") {
				t.Errorf("action = %q", res.Steps[0].Action)
			}
		})
	})

	t.Run("launch opens the named app", func(t *testing.T) {
		var pkg string
		apps := []App{{Label: "Settings", Pkg: "com.android.settings"}}
		runOne(t, episodeXMLA, "open Settings", nil, apps, "launch_0", func(io *IO) {
			io.StartApp = func(serial, p string) (StartResult, error) {
				pkg = p
				return StartResult{Pkg: p, Focused: true}, nil
			}
		}, func(t *testing.T, res GoalResult) {
			if pkg != "com.android.settings" {
				t.Errorf("started %q", pkg)
			}
			if res.Steps[0].Action != "open Settings" {
				t.Errorf("action = %q", res.Steps[0].Action)
			}
		})
	})

	t.Run("clip stages a provided value on the clipboard", func(t *testing.T) {
		var staged string
		runOne(t, episodeXMLA, "copy the text to the clipboard", []string{"abc"}, nil, "clip_0",
			func(io *IO) {
				io.SetClipboard = func(serial, text string) error { staged = text; return nil }
			}, func(t *testing.T, res GoalResult) {
				if staged != "abc" {
					t.Errorf("clipboard = %q", staged)
				}
				if res.Steps[0].StagedText == nil || *res.Steps[0].StagedText != "abc" {
					t.Errorf("stagedText = %v", res.Steps[0].StagedText)
				}
			})
	})
}

func TestGoalResultJSONShape(t *testing.T) {
	got, err := json.Marshal(GoalResult{
		Status: "max_steps", Goal: "g", Serial: "s", Steps: []StepRecord{},
		Tokens: Tokens{}, FinalRows: []string{},
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	want := `{"status":"max_steps","blockedReason":null,"goal":"g","serial":"s","steps":[],` +
		`"answer":null,"tokens":{"input":0,"output":0},"tookMs":0,"finalRows":[],"finalApp":null}`
	if string(got) != want {
		t.Errorf("result = %s\nwant     %s", got, want)
	}

	// Every optional key present, in the JS record's order.
	full, err := json.Marshal(StepRecord{
		Step: 1, Choice: strPtr("click_0"), Action: "a", Changed: nil, APIMs: 5,
		ActMs: int64Ptr(7), Confidence: floatPtr(0.5), GoalMetNoul: floatPtr(0.9),
		StagedText: strPtr("x"), Answer: strPtr("y"),
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	want = `{"step":1,"choice":"click_0","action":"a","changed":null,"apiMs":5,"actMs":7,` +
		`"confidence":0.5,"goalMetNoul":0.9,"stagedText":"x","answer":"y"}`
	if string(full) != want {
		t.Errorf("record = %s\nwant     %s", full, want)
	}

	// A withheld step: null choice, false changed, no actMs.
	withheld, err := json.Marshal(StepRecord{
		Step: 2, Choice: nil, Action: "w", Changed: boolPtr(false), APIMs: 0,
	})
	if err != nil {
		t.Fatalf("marshal withheld: %v", err)
	}
	if want := `{"step":2,"choice":null,"action":"w","changed":false,"apiMs":0}`; string(withheld) != want {
		t.Errorf("withheld = %s\nwant      %s", withheld, want)
	}
}

func TestAgentStateKeyOrder(t *testing.T) {
	note := "n"
	b, err := json.Marshal(AgentState{
		Goal: "g", Requirements: []string{"r"}, Navigation: []NavigationEntry{{App: "a", Step: 0}},
		RecentActions: []string{"1. wait"}, VerificationNote: &note,
	})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"goal":`, `"provided_texts":`, `"today":`, `"screen":`, `"elements":`, `"elements_note":`,
		`"requirements":`, `"navigation":`, `"recent_actions":`, `"verification_note":`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("state is missing %s: %s", want, s)
		}
	}
	if strings.Index(s, `"requirements"`) > strings.Index(s, `"recent_actions"`) {
		t.Error("requirements must precede recent_actions")
	}
	if !strings.HasSuffix(s, `"verification_note":"n"}`) {
		t.Errorf("verification_note must trail the state: %s", s)
	}
	if strings.Contains(s, `"seen_before"`) {
		t.Errorf("seen_before must be omitted when false: %s", s)
	}
	// No note is JSON null, present-not-omitted (the Node `elements_note: null`).
	if !strings.Contains(s, `"elements_note":null`) {
		t.Errorf("empty elements_note must be present and null: %s", s)
	}
}

// --- runGoal: actions -------------------------------------------------------

func TestRunGoalKeypadDigits(t *testing.T) {
	layout := episodeLayout(t, episodeKeypadXML, "com.google.android.deskclock")
	var typed []string
	io := stubIO(IO{
		GetLayout:    func(serial string) (*Layout, error) { return layout, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) { return layout, nil },
		TypeDigits:   func(serial, seq string) error { typed = append(typed, seq); return nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "Create a timer with 0 hours, 16 minutes, and 35 seconds.",
		MaxSteps: 1, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			return askResult(decisionAnswers(t, req.Questions.(Questions), "digits_0", 0.5), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "max_steps" {
		t.Errorf("status = %q", res.Status)
	}
	if !reflect.DeepEqual(typed, []string{"1635"}) {
		t.Errorf("typed = %v, want one atomic digit entry", typed)
	}
	if res.Steps[0].Action != "type digits 1635 into the keypad" {
		t.Errorf("action = %q", res.Steps[0].Action)
	}
}

func TestRunGoalCopySkipsSettle(t *testing.T) {
	layout := episodeLayout(t, episodeEditXML, "com.example.b")
	settles := 0
	var copied []string
	io := stubIO(IO{
		GetLayout:    func(serial string) (*Layout, error) { return layout, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) { settles++; return layout, nil },
		SetClipboard: func(serial, text string) error { copied = append(copied, text); return nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "copy the IMEI", MaxSteps: 1, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			return askResult(decisionAnswersConf(t, req.Questions.(Questions), "copy_0", 0.5, 0.8), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if settles != 0 {
		t.Errorf("settles = %d, want 0 (a copy cannot change the hierarchy)", settles)
	}
	if !reflect.DeepEqual(copied, []string{"IMEI-123456"}) {
		t.Errorf("clipboard = %v", copied)
	}
	rec := res.Steps[0]
	if rec.Changed != nil {
		t.Errorf("changed = %v, want null", rec.Changed)
	}
	if rec.StagedText == nil || *rec.StagedText != "IMEI-123456" {
		t.Errorf("stagedText = %v", rec.StagedText)
	}
	if rec.ActMs == nil {
		t.Error("actMs missing on the copy record")
	}
	if rec.Confidence == nil || *rec.Confidence != 0.8 {
		t.Errorf("confidence = %v", rec.Confidence)
	}
}

func TestRunGoalPasteAlreadyInFieldIsSkipped(t *testing.T) {
	layout := episodeLayout(t, episodeEditXML, "com.example.b")
	var keys []int
	io := stubIO(IO{
		GetLayout:    func(serial string) (*Layout, error) { return layout, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) { return layout, nil },
		KeyEvent:     func(serial string, code int) error { keys = append(keys, code); return nil },
	})
	asks := 0
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "copy the IMEI and paste it", MaxSteps: 2, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			asks++
			choice := "copy_0"
			if asks == 2 {
				choice = "paste"
			}
			return askResult(decisionAnswers(t, req.Questions.(Questions), choice, 0.7), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	for _, k := range keys {
		if k == KeyPaste {
			t.Error("the paste key ran into a field that already held the text")
		}
	}
	if !strings.Contains(res.Steps[1].Action, "already in the field") {
		t.Errorf("action = %q, want the idempotent skip", res.Steps[1].Action)
	}
	if res.Steps[1].Changed != nil {
		t.Errorf("changed = %v, want null (no settle for a no-op)", res.Steps[1].Changed)
	}
}

func TestRunGoalPasteSuggestionChipRetry(t *testing.T) {
	initial := episodeLayout(t, episodeEditEmpty, "com.example.b")
	chip := episodeLayout(t, episodeEditChip, "com.example.b")
	filled := episodeLayout(t, episodeEditXML, "com.example.b")
	var taps []Point
	var keys []int
	settles := 0
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) { return initial, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) {
			settles++
			if settles == 1 {
				return chip, nil
			}
			return filled, nil
		},
		Tap:      func(serial string, c Point) error { taps = append(taps, c); return nil },
		KeyEvent: func(serial string, code int) error { keys = append(keys, code); return nil },
	})
	asks := 0
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "copy the IMEI and paste it", MaxSteps: 2, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			asks++
			choice := "copy_0"
			if asks == 2 {
				choice = "paste"
			}
			return askResult(decisionAnswers(t, req.Questions.(Questions), choice, 0.7), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if !containsInt(keys, KeyPaste) {
		t.Error("the paste key never ran")
	}
	if len(taps) != 1 {
		t.Fatalf("taps = %v, want the suggestion chip", taps)
	}
	if taps[0] != (Point{X: 500, Y: 550}) {
		t.Errorf("chip tap = %v, want 500,550", taps[0])
	}
	if !strings.Contains(res.Steps[1].Action, "(via clipboard suggestion)") {
		t.Errorf("action = %q, want the chip attribution", res.Steps[1].Action)
	}
}

func TestRunGoalScrollPacingGrows(t *testing.T) {
	a := episodeLayout(t, episodeScrollA, "com.android.settings")
	b := episodeLayout(t, episodeScrollB, "com.example.b")
	settleN := 0
	var dys []int
	var dirs []string
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) { return a, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) {
			out := b
			if settleN%2 == 1 {
				out = a
			}
			settleN++
			return out, nil
		},
		ScrollSwipe: func(serial string, anchor Point, dy, screenH int, direction string) error {
			dys = append(dys, dy)
			dirs = append(dirs, direction)
			return nil
		},
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "find the thing", MaxSteps: 3, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			return askResult(decisionAnswers(t, req.Questions.(Questions), "scroll_down", 0.5), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "max_steps" {
		t.Fatalf("status = %q, want max_steps", res.Status)
	}
	if !reflect.DeepEqual(dys, []int{936, 1170, 1404}) {
		t.Errorf("scroll distances = %v, want the same-direction growth 936,1170,1404", dys)
	}
	if !reflect.DeepEqual(dirs, []string{"down", "down", "down"}) {
		t.Errorf("directions = %v", dirs)
	}
}

func TestRunGoalEmptyFirstDumpRetriesThenVision(t *testing.T) {
	empty := episodeLayout(t, episodeEmptyXML, "com.android.settings")
	dumps := 0
	var slept []int
	io := stubIO(IO{
		GetLayout: func(serial string) (*Layout, error) { dumps++; return empty, nil },
		Sleep:     func(ms int) { slept = append(slept, ms) },
		ScreenshotPng: func(serial string) (ShotResult, error) {
			return ShotResult{Png: []byte("png")}, nil
		},
	})
	visionAsks := 0
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 1, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			visionAsks++
			vq, ok := req.Questions.(VisionQuestions)
			if !ok {
				t.Fatalf("questions = %T, want the vision fallback", req.Questions)
			}
			conf := 0.9
			return askResult(map[string]json.RawMessage{
				"operation":       choiceRaw(t, vq.Operation.Criteria.Keys(), "wait"),
				"screen_readable": noulRaw(0.9),
				"tap_target":      boxRaw(t, Box{Top: 0.1, Bottom: 0.5, Left: 0.1, Right: 0.5}, &conf),
			}, 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if dumps != 2 {
		t.Errorf("layout dumps = %d, want the initial dump plus one retry", dumps)
	}
	if len(slept) == 0 || slept[0] != 900 {
		t.Errorf("sleeps = %v, want the 900ms retry wait", slept)
	}
	if visionAsks != 1 {
		t.Errorf("vision asks = %d, want 1 (the still-empty screen goes to the fallback)", visionAsks)
	}
	if res.Steps[0].Action != "wait" {
		t.Errorf("action = %q", res.Steps[0].Action)
	}
}

func TestRunGoalAnswersAndTokens(t *testing.T) {
	xml := `<hierarchy rotation="0">` +
		`<android.widget.TextView package="com.android.settings" text="87%" bounds="[0,100][500,200]" />` +
		`</hierarchy>`
	layout := episodeLayout(t, xml, "com.android.settings")
	io := stubIO(IO{
		GetLayout:    func(serial string) (*Layout, error) { return layout, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) { return layout, nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "what is the battery level?", MaxSteps: 2, Answers: true, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			r := askResult(decisionAnswersConf(t, req.Questions.(Questions), "answer_0", 0.5, 0.9), 1)
			r.Usage = &Usage{InputTokens: 10, OutputTokens: 5}
			return r, nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "answered" {
		t.Fatalf("status = %q, want answered", res.Status)
	}
	if res.Answer == nil || *res.Answer != "87%" {
		t.Errorf("answer = %v, want 87%%", res.Answer)
	}
	if res.Steps[0].Answer == nil || *res.Steps[0].Answer != "87%" {
		t.Errorf("step answer = %v", res.Steps[0].Answer)
	}
	if res.Steps[0].Action != `answer "87%" (conf=0.90)` {
		t.Errorf("action = %q", res.Steps[0].Action)
	}
	if res.Steps[0].Changed != nil {
		t.Errorf("changed = %v, want null", res.Steps[0].Changed)
	}
	if res.Tokens != (Tokens{Input: 10, Output: 5}) {
		t.Errorf("tokens = %+v, want 10/5", res.Tokens)
	}
}

func TestRunGoalDoneRecordsGoalMet(t *testing.T) {
	layout := episodeLayout(t, episodeXMLA, "com.android.settings")
	io := stubIO(IO{
		GetLayout:    func(serial string) (*Layout, error) { return layout, nil },
		SettleLayout: func(serial string, maxMs int64) (*Layout, error) { return layout, nil },
	})
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 2, Vision: true, IO: &io,
		AskFn: func(req AskRequest) (*AskResult, error) {
			switch q := req.Questions.(type) {
			case map[string]Question:
				b, _ := json.Marshal(map[string]any{"choice": "met",
					"probabilities": map[string]float64{"met": 1, "unmet": 0}})
				return askResult(map[string]json.RawMessage{"req_0": b}, 1), nil
			case Questions:
				return askResult(decisionAnswers(t, q, "done", 0.75), 1), nil
			}
			return nil, nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "done" {
		t.Fatalf("status = %q, want done", res.Status)
	}
	rec := res.Steps[0]
	if rec.Action != "done (goal_met=0.75, conf=1.00)" {
		t.Errorf("action = %q", rec.Action)
	}
	if rec.GoalMetNoul == nil || *rec.GoalMetNoul != 0.75 {
		t.Errorf("goalMetNoul = %v", rec.GoalMetNoul)
	}
}

// --- runGoal: tracing -------------------------------------------------------

func TestRunGoalFeedsATrace(t *testing.T) {
	io := stubIO(IO{})
	dir := t.TempDir()
	trace := OpenTrace(true, "open the thing", "emulator-5554", dir)
	res, err := RunGoal(GoalOpts{
		Serial: "emulator-5554", Goal: "open the thing", MaxSteps: 2, Vision: true, IO: &io, Trace: trace,
		AskFn: func(req AskRequest) (*AskResult, error) {
			q := req.Questions.(Questions)
			choice := "wait"
			for _, k := range q.Operation.Criteria.Keys() {
				if strings.HasPrefix(k, "click_") {
					choice = k
					break
				}
			}
			return askResult(decisionAnswers(t, q, choice, 0.5), 1), nil
		},
		Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	if res.Status != "max_steps" {
		t.Errorf("status = %q", res.Status)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("trace files = %v (%v), want one", files, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("trace line is not JSON: %v", err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 3 {
		t.Fatalf("trace lines = %d, want header + 2 steps", len(lines))
	}
	if lines[0]["type"] != "header" || lines[0]["goal"] != "open the thing" {
		t.Errorf("header = %v", lines[0])
	}
	step := lines[1]
	if step["type"] != "step" || step["step"] != float64(1) {
		t.Errorf("step line = %v", step)
	}
	rec, ok := step["record"].(map[string]any)
	if !ok || rec["choice"] != "click_0" {
		t.Errorf("record = %v", step["record"])
	}
	if _, ok := step["questions"].(map[string]any); !ok {
		t.Errorf("questions missing from the trace line: %v", step)
	}
	if _, ok := step["answers"].(map[string]any); !ok {
		t.Errorf("answers missing from the trace line: %v", step)
	}
	criteria, ok := step["criteria"].([]any)
	if !ok || len(criteria) == 0 {
		t.Errorf("criteria = %v", step["criteria"])
	}
	if hash, _ := step["hash"].(string); hash == "" {
		t.Error("hash missing from the trace line")
	}
}

// --- helpers ----------------------------------------------------------------

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsSubstr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func containsInt(list []int, want int) bool {
	for _, n := range list {
		if n == want {
			return true
		}
	}
	return false
}
