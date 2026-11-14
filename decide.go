// One decision, no device.
//
// The act loop owns the device: dump → ask → tap. A host that already has its
// own observation and action pipeline (a test harness, a web driver) only
// needs the decision, so this module exposes exactly that — the same table
// build, the same questions, the same strict validation — as one JSON request
// in and one JSON answer out. Nothing here touches adb: the caller supplies the
// element list and executes whatever action comes back.
//
// Ported from the Node implementation. `ServeDecide` is the long-lived mode:
// one JSON request per line on stdin, one JSON answer per line on stdout,
// because forking a process per decision is what deadlocks under a threaded
// host.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// --- request -----------------------------------------------------------------

// TextList is the wire form of provided_texts/apps/guidance: a single string or
// an array. null and "" are the empty list, matching the JS truthiness test.
type TextList []string

// UnmarshalJSON accepts null, a string, or an array of strings.
func (t *TextList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*t = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one == "" {
			*t = nil
		} else {
			*t = TextList{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*t = many
	return nil
}

// DecideScreen is the host's screen block: the focused app, its display size
// string, and the pixel viewport BuildTable needs.
type DecideScreen struct {
	App  string  `json:"app"`
	Size *string `json:"size"`
	W    int     `json:"w"`
	H    int     `json:"h"`
}

// DecideRequest is the wire protocol's request. Pointers carry the JS defaults
// that differ from Go's zero value: can_scroll null means "ask the table" and
// clipboard defaults to true.
type DecideRequest struct {
	Goal           string        `json:"goal"`
	Screen         *DecideScreen `json:"screen"`
	Elements       []*Element    `json:"elements"`
	RecentActions  []string      `json:"recent_actions"`
	ProvidedTexts  TextList      `json:"provided_texts"`
	Apps           TextList      `json:"apps"`
	CanScroll      *bool         `json:"can_scroll"`
	Copied         bool          `json:"copied"`
	Clipboard      *bool         `json:"clipboard"`
	ClipboardReady bool          `json:"clipboard_ready"`
	Answers        bool          `json:"answers"`
	RecentChoices  []string      `json:"recent_choices"`
	LastUnchanged  *string       `json:"last_unchanged"`
	Today          *string       `json:"today"`
	Guidance       TextList      `json:"guidance"`
	Model          *string       `json:"model"`
}

// --- state -------------------------------------------------------------------

// DecideStateScreen is the state's screen block. Launcher is omitted where the
// JS had undefined; size stays null when the host did not state it.
type DecideStateScreen struct {
	App      string  `json:"app"`
	Size     *string `json:"size"`
	Launcher bool    `json:"launcher,omitempty"`
}

// DecideState is the prompt state of one host decision. Field order IS JSON key
// order: the JS decide state's stable keys, then the verification note the done
// re-ask appends. It deliberately carries no requirements/navigation — the act
// loop adds those; a host's state is just what its dump says.
type DecideState struct {
	Goal             string            `json:"goal"`
	ProvidedTexts    []string          `json:"provided_texts"`
	Today            *string           `json:"today,omitempty"`
	Screen           DecideStateScreen `json:"screen"`
	Elements         []string          `json:"elements"`
	ElementsNote     *string           `json:"elements_note"`
	RecentActions    []string          `json:"recent_actions"`
	VerificationNote *string           `json:"verification_note,omitempty"`
}

// decisionGoal implements decisionState.
func (s DecideState) decisionGoal() string { return s.Goal }

// withVerificationNote implements decisionState.
func (s DecideState) withVerificationNote(note string) decisionState {
	s.VerificationNote = &note
	return s
}

// --- numeric-keyed maps ------------------------------------------------------

// SourcesMap maps a table row to the caller element it came from (null when the
// row resolved from an element the caller did not list).
type SourcesMap map[string]*int

// CentersMap carries each row's tap point.
type CentersMap map[string]*Point

// TextsMap carries each row's copyable/answerable text.
type TextsMap map[string]string

// DragsMap carries the gesture a slider stop needs replayed.
type DragsMap map[string]Swipe

// MarshalJSON reproduces JSON.stringify's integer-first key order.
func (m SourcesMap) MarshalJSON() ([]byte, error) { return marshalNumericKeyed(m) }

// MarshalJSON reproduces JSON.stringify's integer-first key order.
func (m CentersMap) MarshalJSON() ([]byte, error) { return marshalNumericKeyed(m) }

// MarshalJSON reproduces JSON.stringify's integer-first key order.
func (m TextsMap) MarshalJSON() ([]byte, error) { return marshalNumericKeyed(m) }

// MarshalJSON reproduces JSON.stringify's integer-first key order.
func (m DragsMap) MarshalJSON() ([]byte, error) { return marshalNumericKeyed(m) }

// marshalNumericKeyed marshals a plain map the way JSON.stringify orders object
// keys: canonical array indices ascending first, then the remaining keys
// lexically. Values marshal as JSON.stringify would (no HTML escaping).
func marshalNumericKeyed[T any](m map[string]T) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aok := jsArrayIndex(keys[i])
		bi, bok := jsArrayIndex(keys[j])
		if aok != bok {
			return aok
		}
		if aok {
			return ai < bi
		}
		return keys[i] < keys[j]
	})
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		v, err := marshalJSONValue(m[k])
		if err != nil {
			return nil, err
		}
		b.WriteString(JSONStringify(k))
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func marshalJSONValue(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// --- build -------------------------------------------------------------------

// DecideBuilt is the assembled request without sending it: pure, so callers
// (and tests) can inspect exactly what the model would be asked. Table and
// Screen are execution-only and do not travel on the wire.
type DecideBuilt struct {
	State     DecideState `json:"state"`
	Questions Questions   `json:"questions"`
	Table     *Table      `json:"-"`
	Sources   SourcesMap  `json:"sources"`
	Centers   CentersMap  `json:"centers"`
	Screen    *Screen     `json:"-"`
}

// BuildDecideRequest assembles the request without sending it.
func BuildDecideRequest(req DecideRequest) (DecideBuilt, error) {
	if req.Goal == "" {
		return DecideBuilt{}, NewDroidJevError("decide needs a goal string")
	}
	if req.Elements == nil {
		return DecideBuilt{}, NewDroidJevError("decide needs an elements array (the host's UI dump)")
	}
	texts := []string(req.ProvidedTexts)
	labels := []string(req.Apps)
	var screen *Screen
	if req.Screen != nil {
		screen = &Screen{W: req.Screen.W, H: req.Screen.H, App: req.Screen.App}
	}
	table := BuildTable(req.Elements, screen)
	clipboard := true
	if req.Clipboard != nil {
		clipboard = *req.Clipboard
	}
	// The host's explicit answer must not pay for a table scan it overrides.
	canScroll := false
	if req.CanScroll == nil {
		canScroll = ScrollOffered(table, screen)
	} else {
		canScroll = *req.CanScroll
	}
	// appOrUnknown is nullish-only: an explicit "" stays "", only a null/absent
	// screen becomes "unknown" (the JS `?? 'unknown'`).
	app := "unknown"
	if req.Screen != nil {
		app = req.Screen.App
	}
	launcher := launcherRe.MatchString(app)
	guidance := make([]string, 0, len(req.Guidance))
	for _, g := range req.Guidance {
		if g != "" {
			guidance = append(guidance, g)
		}
	}
	questions := BuildQuestions(QuestionInputs{
		Table:          table,
		Screen:         screen,
		Texts:          texts,
		Apps:           labels,
		Copied:         req.Copied && clipboard,
		ClipboardReady: req.ClipboardReady,
		CanScroll:      canScroll,
		Launcher:       launcher,
		Answers:        req.Answers,
		Clipboard:      clipboard,
		Goal:           req.Goal,
		RecentChoices:  req.RecentChoices,
		RecentActions:  req.RecentActions,
		LastUnchanged:  req.LastUnchanged,
		Today:          req.Today,
		Guidance:       guidance,
	})
	if !clipboard {
		// The host has no clipboard operation: offering copy_/paste would invite
		// a decision it cannot execute. The rules describing them stay verbatim,
		// so the prompt is still the one droidjev ships.
		for _, key := range questions.Operation.Criteria.Keys() {
			if strings.HasPrefix(key, "copy_") || key == "paste" {
				questions.Operation.Criteria.Delete(key)
			}
		}
	}

	var providedTexts []string
	if len(texts) > 0 {
		providedTexts = texts
	}
	var today *string
	if req.Today != nil && *req.Today != "" {
		today = req.Today
	}
	elements := SerializeRows(table)
	if elements == nil {
		elements = []string{}
	}
	recent := req.RecentActions
	if recent == nil {
		recent = []string{}
	}
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}
	note := ElementsNote(table, launcher)
	var notePtr *string
	if note != "" {
		notePtr = &note
	}
	var size *string
	if req.Screen != nil {
		size = req.Screen.Size
	}
	state := DecideState{
		Goal:          req.Goal,
		ProvidedTexts: providedTexts,
		Today:         today,
		Screen:        DecideStateScreen{App: app, Size: size, Launcher: launcher},
		Elements:      elements,
		ElementsNote:  notePtr,
		RecentActions: recent,
	}

	// BuildTable drops elements and renumbers the rest, so `click_3` means row 3
	// of the table, not element 3 of the caller's list. Row objects keep the
	// caller's element, so map identity back to its position.
	sourceOf := make(map[*Element]int, len(req.Elements))
	for i, el := range req.Elements {
		sourceOf[el] = i
	}
	sources := make(SourcesMap, len(table.Rows))
	centers := make(CentersMap, len(table.Rows))
	for _, r := range table.Rows {
		key := strconv.Itoa(r.Idx)
		if i, ok := sourceOf[r.El]; ok {
			idx := i
			sources[key] = &idx
		} else {
			sources[key] = nil
		}
		centers[key] = r.El.Center
	}
	return DecideBuilt{State: state, Questions: questions, Table: table, Sources: sources, Centers: centers, Screen: screen}, nil
}

// --- decide ------------------------------------------------------------------

// DecideOperation is the validated choice, JSON-shaped like validatedChoice
// (synthetic is present only on the degraded no-op).
type DecideOperation struct {
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Synthetic     bool               `json:"synthetic,omitempty"`
}

// DecideGoalMet is the parallel noul judgment.
type DecideGoalMet struct {
	Noul float64 `json:"noul"`
}

// ScrollAnchorOut is the swipe target a host replaying a scroll must use.
type ScrollAnchorOut struct {
	Bounds *Bounds `json:"bounds"`
	Center Point   `json:"center"`
}

// DecideResult is one decision's JSON shape. Field order IS the JS object
// literal order; null is explicit wherever the JS wrote null.
type DecideResult struct {
	Operation      DecideOperation  `json:"operation"`
	GoalMet        DecideGoalMet    `json:"goal_met"`
	DoneVerify     *DoneVerify      `json:"done_verify"`
	KeypadSequence *string          `json:"keypad_sequence"`
	GoalTable      GoalTableResult  `json:"goal_table"`
	GoalFields     []GoalField      `json:"goal_fields"`
	ScrollAnchor   *ScrollAnchorOut `json:"scroll_anchor"`
	Criteria       []string         `json:"criteria"`
	Rows           []string         `json:"rows"`
	Sources        SourcesMap       `json:"sources"`
	Centers        CentersMap       `json:"centers"`
	Drags          DragsMap         `json:"drags"`
	Texts          TextsMap         `json:"texts"`
	Answer         *string          `json:"answer"`
	Truncated      bool             `json:"truncated"`
	App            string           `json:"app"`
	Retried        bool             `json:"retried"`
	LatencyMs      int64            `json:"latencyMs"`
	Usage          *Usage           `json:"usage"`
}

// Decide is one decision: build, ask, validate. askFn is injectable so tests
// drive the whole path without the network.
func Decide(req DecideRequest, askFn AskFn) (DecideResult, error) {
	built, err := BuildDecideRequest(req)
	if err != nil {
		return DecideResult{}, err
	}
	model := ""
	if req.Model != nil {
		model = *req.Model
	}
	decided, err := AskDecision(AskDecisionOpts{
		State:     built.State,
		Questions: built.Questions,
		AskFn:     askFn,
		Model:     model,
	})
	if err != nil {
		return DecideResult{}, err
	}
	values := []string(req.ProvidedTexts)
	// A slider stop is a drag, and a drag is not a click: hand the host the four
	// numbers of the gesture it must replay, computed from the same dump the
	// decision was made on.
	drags := DragsMap{}
	stops := SliderStops(req.Goal)
	for _, r := range built.Table.Rows {
		if r.El == nil || !IsSlider(r.El) || r.El.Bounds == nil {
			continue
		}
		for _, s := range stops {
			drags["set_"+strconv.Itoa(r.Idx)+"_"+s.Key] = SlideGesture(r.El, s.Fraction)
		}
	}

	var keypad *string
	if seq := KeypadSequence(req.Goal); seq != "" {
		keypad = &seq
	}
	goalTable := GoalTable(req.Goal)
	if goalTable.Rows == nil {
		goalTable.Rows = [][]string{}
	}
	goalFields := GoalFields(req.Goal)
	if goalFields == nil {
		goalFields = []GoalField{}
	}
	// Where a scroll must swipe: the innermost scrollable holding the focus,
	// else the largest, with its bounds so the host sizes the gesture.
	var scrollAnchor *ScrollAnchorOut
	if row := ScrollAnchorRow(built.Table); row != nil {
		var screen Screen
		if built.Screen != nil {
			screen = *built.Screen
		}
		scrollAnchor = &ScrollAnchorOut{Bounds: row.El.Bounds, Center: ScrollAnchor(built.Table, screen)}
	}

	texts := make(TextsMap, len(built.Table.Rows))
	for _, r := range built.Table.Rows {
		texts[strconv.Itoa(r.Idx)] = ElementText(r.El)
	}

	// What a copy_/answer_ choice acts on, resolved here: a host that executes
	// the decision itself should not have to re-derive which text the row held.
	var answer *string
	if req.Answers {
		text, err := AnswerFor(decided.Operation.Choice, built.Table, values)
		if err != nil {
			return DecideResult{}, err
		}
		if text != "" {
			answer = &text
		}
	}

	return DecideResult{
		Operation: DecideOperation{
			Choice:        decided.Operation.Choice,
			Probabilities: decided.Operation.Probabilities,
			Confidence:    decided.Operation.Confidence,
			Synthetic:     decided.Operation.Synthetic,
		},
		GoalMet:        DecideGoalMet{Noul: decided.GoalMet.Value},
		DoneVerify:     decided.DoneVerify,
		KeypadSequence: keypad,
		GoalTable:      goalTable,
		GoalFields:     goalFields,
		ScrollAnchor:   scrollAnchor,
		Criteria:       built.Questions.Operation.Criteria.Keys(),
		Rows:           built.State.Elements,
		Sources:        built.Sources,
		Centers:        built.Centers,
		Drags:          drags,
		Texts:          texts,
		Answer:         answer,
		Truncated:      built.Table.Truncated,
		App:            built.State.Screen.App,
		Retried:        decided.Retried,
		LatencyMs:      decided.LatencyMs,
		Usage:          decided.Usage,
	}, nil
}

// --- serve -------------------------------------------------------------------

// decideReply is one successful line: ok leads, then the decide fields in their
// JS spread order.
type decideReply struct {
	OK bool `json:"ok"`
	DecideResult
}

// decideErrorReply is one failed line.
type decideErrorReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// serveLine parses and answers one request line. A panic is contained to its
// line, like the JS try/catch; the process keeps serving.
func serveLine(line string, askFn AskFn) (reply any) {
	defer func() {
		if r := recover(); r != nil {
			reply = decideErrorReply{Error: Redact(fmt.Sprint(r))}
		}
	}()
	var req DecideRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return decideErrorReply{Error: Redact(err.Error())}
	}
	res, err := Decide(req, askFn)
	if err != nil {
		return decideErrorReply{Error: Redact(err.Error())}
	}
	return decideReply{OK: true, DecideResult: res}
}

// ServeDecide is the long-lived mode: one JSON request per line on in, one JSON
// answer per line on out, flushed immediately (hosts read line by line). The
// 16 MiB cap fits a large hierarchy dump; a longer line is a protocol error.
func ServeDecide(in io.Reader, out io.Writer, askFn AskFn) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	buffered := bufio.NewWriter(out)
	enc := json.NewEncoder(buffered)
	enc.SetEscapeHTML(false)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := enc.Encode(serveLine(line, askFn)); err != nil {
			return err
		}
		if err := buffered.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}
