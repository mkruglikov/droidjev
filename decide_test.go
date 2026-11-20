// decide tests and the decide goldens: the host-facing
// single decision, no device.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func decideElements() []*Element {
	return []*Element{
		{
			Pkg:       "com.android.settings",
			Class:     "android.widget.TextView",
			Text:      "Wi-Fi",
			Bounds:    &Bounds{L: 0, T: 100, R: 500, B: 200},
			Center:    &Point{X: 250, Y: 150},
			ParentIdx: -1,
			Clickable: true,
		},
		{
			Pkg:       "com.android.settings",
			Class:     "android.widget.Switch",
			Bounds:    &Bounds{L: 800, T: 120, R: 1000, B: 180},
			Center:    &Point{X: 900, Y: 150},
			ParentIdx: -1,
			Checkable: true,
			Checked:   true,
			Clickable: true,
		},
		{
			Pkg:       "com.android.settings",
			Class:     "android.widget.EditText",
			Hint:      "Search",
			Bounds:    &Bounds{L: 0, T: 300, R: 1000, B: 400},
			Center:    &Point{X: 500, Y: 350},
			ParentIdx: -1,
		},
		{
			Pkg:       "com.android.settings",
			Class:     "android.widget.TextView",
			Text:      "15",
			Bounds:    &Bounds{L: 0, T: 500, R: 200, B: 600},
			Center:    &Point{X: 100, Y: 550},
			ParentIdx: -1,
		},
	}
}

func decideRequest(over func(*DecideRequest)) DecideRequest {
	size := "1080x2400"
	req := DecideRequest{
		Goal: "turn Wi-Fi off",
		Screen: &DecideScreen{
			App:  "com.android.settings",
			Size: &size,
			W:    1080,
			H:    2400,
		},
		Elements:      decideElements(),
		ProvidedTexts: TextList{"hello", "world"},
		CanScroll:     boolPtr(false),
		Clipboard:     boolPtr(false),
	}
	if over != nil {
		over(&req)
	}
	return req
}

func rawAnswer(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	return b
}

// answerFn builds an AskFn returning fixed answers for one call.
func answerFn(t *testing.T, answers map[string]any, usage *Usage, latencyMs int64) AskFn {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(answers))
	for k, v := range answers {
		raw[k] = rawAnswer(t, v)
	}
	return func(AskRequest) (*AskResult, error) {
		return &AskResult{Answers: raw, Usage: usage, LatencyMs: latencyMs}, nil
	}
}

func TestBuildDecideRequestRequiresGoalAndElements(t *testing.T) {
	_, err := BuildDecideRequest(decideRequest(func(r *DecideRequest) { r.Goal = "" }))
	if err == nil || err.Error() != "decide needs a goal string" {
		t.Errorf("missing goal error = %v", err)
	}
	_, err = BuildDecideRequest(decideRequest(func(r *DecideRequest) { r.Elements = nil }))
	if err == nil || err.Error() != "decide needs an elements array (the host's UI dump)" {
		t.Errorf("missing elements error = %v", err)
	}
}

func TestDecideElementParentIdxNullIsNoParent(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"null", `{"parentIdx":null}`, -1},
		{"absent", `{}`, -1},
		{"explicit zero", `{"parentIdx":0}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req DecideRequest
			raw := `{"goal":"g","elements":[` + tc.body + `]}`
			if err := json.Unmarshal([]byte(raw), &req); err != nil {
				t.Fatalf("unmarshal decide request: %v", err)
			}
			if len(req.Elements) != 1 || req.Elements[0] == nil {
				t.Fatalf("elements = %#v", req.Elements)
			}
			if req.Elements[0].ParentIdx != tc.want {
				t.Errorf("parentIdx = %d, want %d", req.Elements[0].ParentIdx, tc.want)
			}
		})
	}
}

func TestBuildDecideRequestKeepsExplicitEmptyApp(t *testing.T) {
	unknown, err := BuildDecideRequest(decideRequest(func(r *DecideRequest) { r.Screen = nil }))
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	if unknown.State.Screen.App != "unknown" {
		t.Errorf("absent screen app = %q, want unknown", unknown.State.Screen.App)
	}
	empty, err := BuildDecideRequest(decideRequest(func(r *DecideRequest) {
		r.Screen = &DecideScreen{App: "", W: 1080, H: 2400}
	}))
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	if empty.State.Screen.App != "" {
		t.Errorf("explicit empty app = %q, want \"\"", empty.State.Screen.App)
	}
}

func TestBuildDecideRequestOffersOperations(t *testing.T) {
	built, err := BuildDecideRequest(decideRequest(nil))
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	keys := built.Questions.Operation.Criteria.Keys()
	has := func(k string) bool {
		for _, x := range keys {
			if x == k {
				return true
			}
		}
		return false
	}
	if !has("click_0") {
		t.Error("clickable labeled row is offered")
	}
	if !has("type_2_0") || !has("type_2_1") {
		t.Errorf("each provided text gets a type option on editable rows: %v", keys)
	}
	for _, k := range []string{"back", "blocked", "done", "home", "wait"} {
		if !has(k) {
			t.Errorf("fixed operation %s is always offered", k)
		}
	}
	if has("scroll_down") {
		t.Error("scroll must be withheld when can_scroll is false")
	}
	for _, k := range keys {
		if strings.HasPrefix(k, "copy_") || k == "paste" {
			t.Errorf("no clipboard ops when the host cannot copy, got %s", k)
		}
	}
	wantRows := SerializeRows(BuildTable(decideElements(), &Screen{W: 1080, H: 2400, App: "com.android.settings"}))
	if !reflect.DeepEqual(built.State.Elements, wantRows) {
		t.Errorf("state carries the serialized rows: %v != %v", built.State.Elements, wantRows)
	}
	if len(built.State.ProvidedTexts) != 2 {
		t.Errorf("provided_texts = %v", built.State.ProvidedTexts)
	}
	if built.State.Screen.App != "com.android.settings" {
		t.Errorf("screen.app = %q", built.State.Screen.App)
	}
	if built.State.ElementsNote != nil {
		t.Errorf("elements_note = %v, want null", *built.State.ElementsNote)
	}
}

func TestBuildDecideRequestKeepsClipboardOpsAndBoundsHistory(t *testing.T) {
	recent := make([]string, 14)
	for i := range recent {
		recent[i] = strconv.Itoa(i) + ". click_" + strconv.Itoa(i)
	}
	built, err := BuildDecideRequest(decideRequest(func(r *DecideRequest) {
		r.Clipboard = boolPtr(true)
		r.Copied = true
		r.CanScroll = boolPtr(true)
		r.Screen = &DecideScreen{App: "com.android.launcher3", W: 1080, H: 2400}
		r.RecentActions = recent
	}))
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	has := func(k string) bool {
		for _, x := range built.Questions.Operation.Criteria.Keys() {
			if x == k {
				return true
			}
		}
		return false
	}
	if !has("copy_3") {
		t.Error("a row with text can be copied")
	}
	if !has("paste") {
		t.Error("paste appears once a copy ran")
	}
	if !has("scroll_down") || !has("scroll_up") {
		t.Error("scroll offered when the host allows it")
	}
	if built.State.ElementsNote == nil || !strings.Contains(*built.State.ElementsNote, "home screen (launcher)") {
		t.Errorf("launcher note = %v", built.State.ElementsNote)
	}
	if len(built.State.RecentActions) != 10 {
		t.Errorf("only the last 10 actions are sent, got %d", len(built.State.RecentActions))
	}
	if built.State.RecentActions[9] != "13. click_13" {
		t.Errorf("last recent action = %q", built.State.RecentActions[9])
	}
}

func TestDecideValidatesAnswer(t *testing.T) {
	built, err := BuildDecideRequest(decideRequest(nil))
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	probabilities := map[string]float64{}
	for _, k := range built.Questions.Operation.Criteria.Keys() {
		probabilities[k] = 0
	}
	probabilities["click_0"] = 1
	askFn := func(AskRequest) (*AskResult, error) {
		return &AskResult{
			Answers: map[string]json.RawMessage{
				"operation": rawAnswer(t, map[string]any{"choice": "click_0", "probabilities": probabilities}),
				"goal_met":  rawAnswer(t, map[string]any{"noul": 0.2}),
			},
			Usage:     &Usage{InputTokens: 5},
			LatencyMs: 7,
		}, nil
	}
	res, err := Decide(decideRequest(nil), askFn)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Operation.Choice != "click_0" {
		t.Errorf("choice = %q", res.Operation.Choice)
	}
	if res.Operation.Confidence != 1 {
		t.Errorf("confidence = %v", res.Operation.Confidence)
	}
	if res.GoalMet.Noul != 0.2 {
		t.Errorf("goal_met.noul = %v", res.GoalMet.Noul)
	}
	if res.App != "com.android.settings" {
		t.Errorf("app = %q", res.App)
	}
	if res.Usage == nil || res.Usage.InputTokens != 5 {
		t.Errorf("usage = %#v", res.Usage)
	}

	// Probabilities that do not cover the offered options, and a choice that is
	// not the argmax, must never become an action: a persistently invalid answer
	// degrades the step to a schema-complete synthetic wait.
	degraded := func(choice string, probabilities map[string]float64) {
		ask := func(AskRequest) (*AskResult, error) {
			return &AskResult{
				Answers: map[string]json.RawMessage{
					"operation": rawAnswer(t, map[string]any{"choice": choice, "probabilities": probabilities}),
					"goal_met":  rawAnswer(t, map[string]any{"noul": 0.2}),
				},
				LatencyMs: 1,
			}, nil
		}
		res, err := Decide(decideRequest(nil), ask)
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if res.Operation.Choice != "wait" || !res.Operation.Synthetic {
			t.Errorf("invalid answer must degrade to a synthetic wait, got %#v", res.Operation)
		}
		if !reflect.DeepEqual(res.Operation.Probabilities, map[string]float64{"wait": 1}) {
			t.Errorf("synthetic probabilities = %v", res.Operation.Probabilities)
		}
	}
	missing := map[string]float64{}
	for k := range probabilities {
		if k != "wait" {
			missing[k] = probabilities[k]
		}
	}
	degraded("click_0", missing)
	notArgmax := map[string]float64{}
	for k := range probabilities {
		notArgmax[k] = probabilities[k]
	}
	notArgmax["click_0"] = 0.4
	notArgmax["done"] = 0.6
	degraded("click_0", notArgmax)
}

func TestDecideReportsSourceElements(t *testing.T) {
	offScreen := &Element{
		Pkg:       "com.android.settings",
		Class:     "android.widget.FrameLayout",
		Bounds:    &Bounds{L: 2000, T: 3000, R: 2100, B: 3100},
		Center:    &Point{X: 2050, Y: 3050},
		ParentIdx: -1,
	}
	elements := append([]*Element{offScreen}, decideElements()...)
	req := decideRequest(func(r *DecideRequest) { r.Elements = elements })
	built, err := BuildDecideRequest(req)
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	if v := built.Sources["0"]; v == nil || *v != 1 {
		t.Errorf("row 0 is the first surviving caller element, not element 0: %v", v)
	}
	if v := built.Sources["1"]; v == nil || *v != 2 {
		t.Errorf("sources[1] = %v", v)
	}
	wantCenter := &Point{X: 250, Y: 150}
	if !reflect.DeepEqual(built.Centers["0"], wantCenter) {
		t.Errorf("centers[0] = %v, want %v", built.Centers["0"], wantCenter)
	}

	probabilities := map[string]float64{}
	for _, k := range built.Questions.Operation.Criteria.Keys() {
		probabilities[k] = 0
	}
	probabilities["click_0"] = 1
	res, err := Decide(req, answerFn(t, map[string]any{
		"operation": map[string]any{"choice": "click_0", "probabilities": probabilities},
		"goal_met":  map[string]any{"noul": 0},
	}, nil, 1))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if v := res.Sources["0"]; v == nil || *v != 1 {
		t.Errorf("result sources[0] = %v", v)
	}
	if !reflect.DeepEqual(res.Centers["0"], wantCenter) {
		t.Errorf("result centers[0] = %v", res.Centers["0"])
	}
	if res.Answer != nil {
		t.Errorf("answer = %v without answers", *res.Answer)
	}
}

func TestDecideDragsEverySliderStopAndResolvesAnswer(t *testing.T) {
	slider := &Element{
		Pkg:       "com.android.settings",
		Class:     "android.widget.SeekBar",
		Text:      "50",
		Bounds:    &Bounds{L: 100, T: 700, R: 900, B: 760},
		Center:    &Point{X: 500, Y: 730},
		ParentIdx: -1,
	}
	req := decideRequest(func(r *DecideRequest) {
		r.Goal = "set the volume to maximum and 50%"
		r.Elements = append(decideElements(), slider)
		r.Answers = true
	})
	built, err := BuildDecideRequest(req)
	if err != nil {
		t.Fatalf("BuildDecideRequest: %v", err)
	}
	probabilities := map[string]float64{}
	for _, k := range built.Questions.Operation.Criteria.Keys() {
		probabilities[k] = 0
	}
	probabilities["answer_3"] = 1
	res, err := Decide(req, answerFn(t, map[string]any{
		"operation": map[string]any{"choice": "answer_3", "probabilities": probabilities},
		"goal_met":  map[string]any{"noul": 0},
	}, nil, 1))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(res.Drags) != 2 {
		t.Fatalf("drags = %#v, want one per slider stop", res.Drags)
	}
	row := built.Table.Rows[len(built.Table.Rows)-1]
	maxDrag, ok := res.Drags["set_"+strconv.Itoa(row.Idx)+"_max"]
	if !ok || !reflect.DeepEqual(maxDrag, SlideGesture(slider, 1)) {
		t.Errorf("max drag = %#v, want %#v", maxDrag, SlideGesture(slider, 1))
	}
	p50, ok := res.Drags["set_"+strconv.Itoa(row.Idx)+"_p50"]
	if !ok || !reflect.DeepEqual(p50, SlideGesture(slider, 0.5)) {
		t.Errorf("50%% drag = %#v, want %#v", p50, SlideGesture(slider, 0.5))
	}
	if res.Answer == nil || *res.Answer != "15" {
		t.Errorf("answer = %v, want the chosen row's text", res.Answer)
	}
	if res.Truncated {
		t.Error("a small table is not truncated")
	}
	if res.App != "com.android.settings" || res.Retried {
		t.Errorf("app/retried = %q/%v", res.App, res.Retried)
	}
	if res.KeypadSequence != nil {
		t.Errorf("keypad_sequence = %v, want null", *res.KeypadSequence)
	}
}

// --- golden parity ----------------------------------------------------------

type decideGoldenFixture struct {
	Kind    string        `json:"kind"`
	Name    string        `json:"name"`
	Request DecideRequest `json:"request"`
}

func loadDecideFixtures(t *testing.T) []decideGoldenFixture {
	t.Helper()
	path := filepath.Join("testdata", "golden-fixtures.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var fixtures []decideGoldenFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return fixtures
}

func TestGoldenDecide(t *testing.T) {
	checked := 0
	for _, f := range loadDecideFixtures(t) {
		if f.Kind != "decide" {
			continue
		}
		checked++
		t.Run(f.Name, func(t *testing.T) {
			built, err := BuildDecideRequest(f.Request)
			if err != nil {
				t.Fatalf("BuildDecideRequest: %v", err)
			}
			compareGolden(t, "decide-"+f.Name, marshalPrompt(t, built))
		})
	}
	if checked == 0 {
		t.Fatal("no decide fixtures found")
	}
}

// --- serve mode -------------------------------------------------------------

func waitAnswer(t *testing.T, seen *[]string) AskFn {
	t.Helper()
	return func(req AskRequest) (*AskResult, error) {
		state, ok := req.State.(DecideState)
		if !ok {
			t.Fatalf("decide state type = %T", req.State)
		}
		*seen = append(*seen, state.Goal)
		questions, ok := req.Questions.(Questions)
		if !ok {
			t.Fatalf("questions type = %T", req.Questions)
		}
		probabilities := map[string]float64{}
		for _, k := range questions.Operation.Criteria.Keys() {
			probabilities[k] = 0
		}
		probabilities["wait"] = 1
		return &AskResult{
			Answers: map[string]json.RawMessage{
				"operation": rawAnswer(t, map[string]any{"choice": "wait", "probabilities": probabilities}),
				"goal_met":  rawAnswer(t, map[string]any{"noul": 0}),
			},
			LatencyMs: 1,
		}, nil
	}
}

func TestServeDecideAnswersEachLine(t *testing.T) {
	input := strings.NewReader(`{"goal":"turn wifi off","elements":[]}` + "\n" +
		`{"goal":"open settings","elements":[]}` + "\n")
	var out bytes.Buffer
	var seen []string
	if err := ServeDecide(input, &out, waitAnswer(t, &seen)); err != nil {
		t.Fatalf("ServeDecide: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("one answer line per request line, got %d: %q", len(lines), out.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, `{"ok":true,"operation":`) {
			t.Errorf("reply must start ok then the decide fields: %q", line)
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("reply is not JSON: %v", err)
		}
		if reply["ok"] != true {
			t.Errorf("ok = %v", reply["ok"])
		}
	}
	if !reflect.DeepEqual(seen, []string{"turn wifi off", "open settings"}) {
		t.Errorf("goals seen = %v", seen)
	}
}

func TestServeMalformedLine(t *testing.T) {
	input := strings.NewReader("not json\n" + `{"goal":"g","elements":[]}` + "\n")
	var out bytes.Buffer
	var seen []string
	if err := ServeDecide(input, &out, waitAnswer(t, &seen)); err != nil {
		t.Fatalf("ServeDecide: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("two replies, got %d: %q", len(lines), out.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first reply: %v", err)
	}
	if first["ok"] != false || first["error"] == "" {
		t.Errorf("malformed line reply = %v", first)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second reply: %v", err)
	}
	if second["ok"] != true {
		t.Errorf("the next request is still served: %v", second)
	}
}

func TestServeLargeLine(t *testing.T) {
	goal := strings.Repeat("x", 4<<20) // 4 MiB request line
	line, err := json.Marshal(map[string]any{"goal": goal, "elements": []any{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out bytes.Buffer
	var seen []string
	if err := ServeDecide(bytes.NewReader(append(line, '\n')), &out, waitAnswer(t, &seen)); err != nil {
		t.Fatalf("ServeDecide: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one reply, got %d", len(lines))
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &reply); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply["ok"] != true {
		t.Errorf("large line reply = %v", reply["ok"])
	}
}

func TestServeAskErrorBecomesOkFalse(t *testing.T) {
	failing := func(AskRequest) (*AskResult, error) {
		return nil, errors.New("upstream said Bearer abc123")
	}
	input := strings.NewReader(`{"goal":"g","elements":[]}` + "\n" + `{"goal":"h","elements":[]}` + "\n")
	var out bytes.Buffer
	if err := ServeDecide(input, &out, failing); err != nil {
		t.Fatalf("ServeDecide: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("two replies, got %d", len(lines))
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &reply); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply["ok"] != false {
		t.Errorf("ok = %v", reply["ok"])
	}
	if msg, _ := reply["error"].(string); !strings.Contains(msg, "Bearer [REDACTED]") || strings.Contains(msg, "abc123") {
		t.Errorf("error must be redacted: %q", msg)
	}
}
