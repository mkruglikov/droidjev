// Goal-specific macro skills: composite actions the host can execute in one
// step for goals that state their own data (a keypad sequence, a table of
// rows, a labeled form). They live outside the generic loop so BuildQuestions
// stays generic, each macro is testable alone, and a host (the decide
// protocol) can later share the same definitions.
//
// Apply returns the criteria mutations it wants, or nil:
//
//	{Options?, Delete []string, DeletePrefixes []string, DeleteScrolls bool}
//
// Anchor is the emission position, because criteria key order is prompt
// order: "assign" folds options in before the fixed ops (digits_0), "post"
// runs after them in registry order, "postStuck" after the loop withdrawals.
//
// Ported from the Node implementation. Two patterns need RE2 emulation: JS's
// `^0+(?=\d)` lookahead and the route pattern's terminator lookahead.

package main

import (
	"regexp"
	"strconv"
	"strings"
)

// QuestionInputs is the registry's input contract: one field per
// buildQuestions input. It is also the shape a replayed question is rebuilt
// from (tableTruncated/tableGrid travel flattened because JS's table carried
// them out of band).
type QuestionInputs struct {
	Table            *Table
	Screen           *Screen
	Texts            []string
	Apps             []string
	Copied           bool
	ClipboardReady   bool
	CanScroll        bool
	Launcher         bool
	Answers          bool
	Clipboard        bool
	Goal             string
	RecentChoices    []string
	RecentActions    []string
	LastUnchanged    *string
	Today            *string
	Guidance         []string
	VerificationNote *string
	TableTruncated   bool
	TableGrid        *GridInfo
}

// GridInfo is the flattened drawn-grid fact for a replayed question: the size
// of one cell ("WxH") and how many cells there were.
type GridInfo struct {
	Size  string `json:"size"`
	Count int    `json:"count"`
}

// GoalTableResult is GoalTable's parsed pipe table: the header row when one
// was recognized (nil otherwise, which the decide protocol renders as JSON
// null) and one cell slice per data row (never null: an empty table is []).
type GoalTableResult struct {
	Header []string   `json:"header"`
	Rows   [][]string `json:"rows"`
}

// GoalField is one labeled value a goal states in quotes; Label names the
// form field it belongs in.
type GoalField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Skill is one macro: ID names it, Anchor places its options in the criteria
// key order, and Apply returns the mutations it wants or nil for no opinion.
type Skill struct {
	ID     string
	Anchor string
	Apply  func(qi QuestionInputs) *SkillResult
}

// SkillResult is what a skill asks for. Options is nil when the skill adds
// none (and may be empty when every option was withdrawn).
type SkillResult struct {
	Options        *OrdMap
	Delete         []string
	DeletePrefixes []string
	DeleteScrolls  bool
}

// --- the goal parsers (pure) ------------------------------------------------

var (
	keypadHoursRe   = regexp.MustCompile(`(\d+)[` + JSWhitespaceClass + `]*hours?\b`)
	keypadMinutesRe = regexp.MustCompile(`(\d+)[` + JSWhitespaceClass + `]*minutes?\b`)
	keypadSecondsRe = regexp.MustCompile(`(\d+)[` + JSWhitespaceClass + `]*seconds?\b`)
	keypadClockRe   = regexp.MustCompile(`\b(\d{1,2}):(\d{2})\b`)

	goalTableHeaderRe = regexp.MustCompile(
		`(?i)^(name|amount\w*|category\w*|note|description|title|date|time|priority|quantity|unit)$`,
	)

	goalFieldRe = regexp.MustCompile(
		`(?i)(titles?|titled|description|location|name[sd]?|subject|body|note|label)` +
			`[` + JSWhitespaceClass + `]*(?:is|:)?[` + JSWhitespaceClass + `]*` +
			`['“]([^'”]{1,120})['”"]`,
	)

	goalQuotedRe = regexp.MustCompile(`['“"]([^'”"]{1,120})['”"]`)

	// ponytail: the `{1,120}` quantifiers above and in goalFieldRe count runes
	// where JS counts UTF-16 code units, so a value made mostly of astral
	// characters (emoji) can be cut at a different point. add() still enforces
	// the 120 UTF-16 cap for every value that survives.

	goalRouteWordRe = regexp.MustCompile(`(?i)\b(route|directions|navigat|drive|walk|transit|cycl)\w*`)
	goalFromRe      = regexp.MustCompile(`(?i)\bfrom[` + JSWhitespaceClass + `]+`)
	goalToSepRe     = regexp.MustCompile(`(?i)[` + JSWhitespaceClass + `]+to[` + JSWhitespaceClass + `]+`)
	// The route terminator `,\s+(?:and|then)\b` is checked at a known offset,
	// so it is anchored; `[.!?]` and end-of-string are scanned in code.
	goalAndThenRe = regexp.MustCompile(`(?i)^,[` + JSWhitespaceClass + `]+(?:and|then)\b`)

	goalNamedRe = regexp.MustCompile(`(?i)\b(?:named|called)[` + JSWhitespaceClass + `]+([A-Za-z][\w'’\-]{0,40})`)
	goalStopRe  = regexp.MustCompile(`(?i)^(?:to|from|the|a|an|and|with|for|at|in|on|by)$`)

	goalPhoneRe = regexp.MustCompile(
		`(?i)\bphone(?:[` + JSWhitespaceClass + `]+number)?[` + JSWhitespaceClass + `]*[:=]?[` +
			JSWhitespaceClass + `]*(\+?\d[\d` + JSWhitespaceClass + `().\-]{5,})`,
	)
	goalPhoneTailRe = regexp.MustCompile(`[.` + JSWhitespaceClass + `]+$`)
	goalEmailRe     = regexp.MustCompile(
		`(?i)\bemail(?:[` + JSWhitespaceClass + `]+address)?[` + JSWhitespaceClass + `]*[:=]?[` +
			JSWhitespaceClass + `]*([^` + JSWhitespaceClass + `,;]+@[^` + JSWhitespaceClass + `,;]+)`,
	)
	goalEmailTailRe = regexp.MustCompile(`[.,;]+$`)

	addRowChoiceRe = regexp.MustCompile(`^add_row_(\d+)$`)

	goalProductRe   = regexp.MustCompile(`(?i)\bproduct\b`)
	clickMeRe       = regexp.MustCompile(`(?i)click me`)
	goalResendRe    = regexp.MustCompile(`(?i)\bresend\b`)
	goalVideoRe     = regexp.MustCompile(`(?i)\b(?:video|record)\b`)
	shutterActionRe = regexp.MustCompile(`(?i)click \[[0-9]+\].*(?:shutter|record)`)
	shutterTextRe   = regexp.MustCompile(`(?i)shutter|record`)
)

// KeypadSequence returns the digit string a keypad must receive when the goal
// states the value in mechanical form: "0 hours, 16 minutes, and 35 seconds" →
// "1635", "3 hours" → "30000" (the odometer reads 03h00m00s), "14:35" → "1435".
// Any subset of units works; "" means the goal names no sequence.
func KeypadSequence(goal string) string {
	g := strings.ReplaceAll(strings.ToLower(goal), ",", " ")
	h := keypadUnit(g, keypadHoursRe)
	m := keypadUnit(g, keypadMinutesRe)
	s := keypadUnit(g, keypadSecondsRe)
	if h != 0 || m != 0 || s != 0 {
		total := h*10000 + m*100 + s
		return stripLeadingZeros(strconv.Itoa(total))
	}
	if clock := keypadClockRe.FindStringSubmatch(g); clock != nil {
		hour, _ := strconv.Atoi(clock[1]) // \d{1,2}: always fits, as Number() would
		return strconv.Itoa(hour) + clock[2]
	}
	return ""
}

func keypadUnit(g string, re *regexp.Regexp) int {
	m := re.FindStringSubmatch(g)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		// ponytail: a \d+ run past int64 overflows to 0; JS would go float.
		// No real goal states an hour count that long.
		return 0
	}
	return n
}

// stripLeadingZeros is `s.replace(/^0+(?=\d)/, ”)`: drop leading zeros as
// long as another digit follows, so a lone "0" — and, with nothing but zeros,
// the final one — survives.
func stripLeadingZeros(s string) string {
	i := 0
	for i < len(s) && s[i] == '0' {
		i++
	}
	switch {
	case i == 0:
		return s
	case i == len(s):
		return s[i-1:]
	case s[i] < '0' || s[i] > '9':
		return s
	default:
		return s[i:]
	}
}

// GoalTable parses a goal that states its data as a pipe-delimited table:
//
//	name|amount_dollars|category_name|note
//	Stationery|$352.34|Others|A need
//
// The header names the columns (not data); each following line is one row.
// Lines with fewer than three pipe segments, and prose, are ignored.
func GoalTable(goal string) GoalTableResult {
	var out GoalTableResult
	for _, line := range strings.Split(goal, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		cells := make([]string, 0, len(parts))
		for _, c := range parts {
			if c = JSTrim(c); c != "" {
				cells = append(cells, c)
			}
		}
		if len(cells) == 0 {
			continue
		}
		allHeader := true
		for _, c := range cells {
			if !goalTableHeaderRe.MatchString(c) {
				allHeader = false
				break
			}
		}
		if allHeader {
			out.Header = cells
			continue
		}
		out.Rows = append(out.Rows, cells)
	}
	return out
}

// GoalFields returns the labeled values the goal states in quotes — `with the
// title 'X' and the description 'Y'` — with the label naming the form field
// the value belongs in. "titled"/"named" normalize to "title"/"name".
func GoalFields(goal string) []GoalField {
	var fields []GoalField
	for _, m := range goalFieldRe.FindAllStringSubmatch(goal, -1) {
		label := strings.ToLower(m[1])
		switch label {
		case "titled", "titles":
			label = "title"
		case "named", "names":
			label = "name"
		}
		fields = append(fields, GoalField{Label: label, Value: m[2]})
	}
	return fields
}

// GoalTexts returns the values the goal itself supplies for typing: quoted
// text, route endpoints, a person's name, a phone or an email, all only when
// the goal unambiguously denotes a value. The goal is operator input, so these
// are as trusted as --text. Dedupe is case-insensitive; values are capped at
// 120 UTF-16 units and collapsed to one line.
func GoalTexts(goal string) []string {
	values := []string{}
	add := func(v string) {
		s := whitespaceRunRe.ReplaceAllString(JSTrim(v), " ")
		if s == "" || UTF16Len(s) > 120 || strings.ContainsAny(s, "\n\r") {
			return
		}
		for _, x := range values {
			if strings.ToLower(x) == strings.ToLower(s) {
				return
			}
		}
		values = append(values, s)
	}
	// Quoted values: the operator spelled the exact text.
	for _, m := range goalQuotedRe.FindAllStringSubmatch(goal, -1) {
		add(m[1])
	}
	// Route endpoints only in a route goal: "build a route from A to B".
	// Commas stay inside a value — "moscow, ID" and "saint-petersburg, russia"
	// are qualified places, not two values each. "scroll from top to bottom"
	// has no route word and must not become two typed values.
	if goalRouteWordRe.MatchString(goal) {
		if from := goalFromRe.FindStringIndex(goal); from != nil {
			rest := goal[from[1]:]
			// Both endpoints need at least one character: the JS pattern's
			// `(.+?)` groups cannot be empty, so a "to " with nothing after it
			// is no route at all.
			if sep := goalToSepRe.FindStringIndex(rest); sep != nil && sep[1] < len(rest) {
				add(rest[:sep[0]])
				add(trimRouteEndpoint(rest[sep[1]:]))
			}
		}
	}
	// A person's name: "named Maria" / "called Bob" — not the stopwords that
	// follow "called" in ordinary prose ("called to order").
	for _, m := range goalNamedRe.FindAllStringSubmatch(goal, -1) {
		if !goalStopRe.MatchString(m[1]) {
			add(m[1])
		}
	}
	// A phone/email only when the value has that shape: "phone is available"
	// must not become a typed value.
	for _, m := range goalPhoneRe.FindAllStringSubmatch(goal, -1) {
		add(goalPhoneTailRe.ReplaceAllString(m[1], ""))
	}
	for _, m := range goalEmailRe.FindAllStringSubmatch(goal, -1) {
		add(goalEmailTailRe.ReplaceAllString(m[1], ""))
	}
	return values
}

// trimRouteEndpoint emulates `(.+?)(?=[.!?]|,\s+(?:and|then)\b|$)`: the value
// stops before the first `[.!?]` or `, and`/`, then` after at least one
// character, or runs to the end of the goal.
func trimRouteEndpoint(s string) string {
	for i := 1; i <= len(s); i++ {
		if i == len(s) {
			return s
		}
		switch s[i] {
		case '.', '!', '?':
			return s[:i]
		case ',':
			if goalAndThenRe.MatchString(s[i:]) {
				return s[:i]
			}
		}
	}
	return s
}

// HasKeypad reports whether the table carries a keypad: at least six rows
// whose whole label is one digit.
func HasKeypad(t *Table) bool {
	n := 0
	for _, r := range tableRows(t) {
		if r == nil || r.El == nil {
			continue
		}
		s := ElementText(r.El)
		if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
			n++
			if n >= 6 {
				return true
			}
		}
	}
	return false
}

// tableRows keeps skill bodies nil-safe: a zero QuestionInputs has no table.
func tableRows(t *Table) []*Row {
	if t == nil {
		return nil
	}
	return t.Rows
}

func hasChoice(recent []string, choice string) bool {
	for _, c := range recent {
		if c == choice {
			return true
		}
	}
	return false
}

// --- the registry -----------------------------------------------------------

// Skills is the macro registry, in JS order. Digits runs at the assign anchor;
// launcherScrolls/addRow/walk/multiply/fill/resend after the fixed ops in
// registry order; shutter after the stuck-op withdrawals.
var Skills = []Skill{
	{
		// The keypad's digit sequence, straight from the goal: one atomic action
		// beats six counted presses (the model loses count mid-sequence).
		ID:     "digits",
		Anchor: "assign",
		Apply: func(qi QuestionInputs) *SkillResult {
			if !HasKeypad(qi.Table) {
				return nil
			}
			seq := KeypadSequence(qi.Goal)
			if seq == "" {
				return nil
			}
			opts := NewOrdMap()
			opts.Set("digits_0", `Type the goal's digit sequence "`+seq+
				`" into the keypad in one action — the host sends every digit; the display fills to the goal's value`)
			return &SkillResult{Options: opts}
		},
	},
	{
		// On the launcher, a launch_<k> that names the goal's app is one step and
		// the drawer hunt is many: the model scrolled the home screen for a whole
		// 10-step budget with launch_0 offered (calendar query, observed). With a
		// launch option on the table, the scrolls only page icons.
		ID:     "launcherScrolls",
		Anchor: "post",
		Apply: func(qi QuestionInputs) *SkillResult {
			if qi.Launcher && len(qi.Apps) > 0 {
				return &SkillResult{DeleteScrolls: true}
			}
			return nil
		},
	},
	{
		// A goal that states its data as a table (pipe-separated rows) is entered
		// row by row into the visible form. Each row is ONE atomic option the host
		// fills and saves; rows already used this run are withdrawn, so the same
		// entry is never saved twice (observed: 4 entries for a 3-entry goal).
		ID:     "addRow",
		Anchor: "post",
		Apply: func(qi QuestionInputs) *SkillResult {
			rows := GoalTable(qi.Goal).Rows
			if len(rows) == 0 {
				return nil
			}
			used := make(map[int]bool)
			for _, choice := range qi.RecentChoices {
				m := addRowChoiceRe.FindStringSubmatch(choice)
				if m == nil {
					continue
				}
				if k, err := strconv.Atoi(m[1]); err == nil {
					used[k] = true
				}
			}
			opts := NewOrdMap()
			for k, row := range rows {
				if used[k] {
					continue
				}
				opts.Set("add_row_"+strconv.Itoa(k), "Add entry "+strconv.Itoa(k+1)+
					" of the goal's table ("+strings.Join(row, " · ")+
					") — the host opens the form if needed, enters every column in its right field, picks the category and saves")
			}
			return &SkillResult{Options: opts}
		},
	},
	{
		// A grid game driven by Up/Down/Left/Right buttons: the host walks the
		// piece to the goal cell, using the piece's own position as feedback (a
		// move that changes its bounds is progress; a wall does not). A small
		// model cannot plan a maze blind, but the host can walk it
		// deterministically. Hand-pressing directions is how the piece wanders;
		// the host walk is the only route while it is offered. Idle and chrome
		// clicks are noise on a one-screen game (the model picks them over a
		// novel op).
		ID:     "walk",
		Anchor: "post",
		Apply: func(qi QuestionInputs) *SkillResult {
			found := 0
			for _, dir := range []string{"up", "down", "left", "right"} {
				for _, r := range tableRows(qi.Table) {
					if r != nil && r.El != nil && strings.ToLower(ElementText(r.El)) == dir {
						found++
						break
					}
				}
			}
			if found != 4 || hasChoice(qi.RecentChoices, "walk_0") {
				return nil
			}
			opts := NewOrdMap()
			opts.Set("walk_0", "Walk the piece (the X) to the goal cell with the Up/Down/Left/Right buttons — "+
				"the host presses them and follows the piece until it arrives")
			return &SkillResult{
				Options: opts,
				// With the host walk available, hand-driven navigation is the
				// wrong route.
				Delete:         []string{"wait", "back", "home"},
				DeletePrefixes: []string{"click_", "scroll_"},
			}
		},
	},
	{
		// "Click the button N times, remember the numbers, enter their product":
		// the displayed numbers are readable, so the host presses, multiplies and
		// submits — a small model cannot reliably hold five numbers and multiply.
		// Re-offered until the game's button is gone: a blocked first attempt (a
		// consent dialog over the page) leaves the game untouched, so the op must
		// stay available after it. Idle ops are noise on a one-screen game and the
		// model is *confidently* idle (wait at 0.74) rather than novel; with those
		// gone it clicked unlabeled browser chrome instead — so every click is
		// withdrawn too. done/blocked remain, and done is checked.
		ID:     "multiply",
		Anchor: "post",
		Apply: func(qi QuestionInputs) *SkillResult {
			hasClickMe := false
			for _, r := range tableRows(qi.Table) {
				if r != nil && r.El != nil && clickMeRe.MatchString(ElementText(r.El)) {
					hasClickMe = true
					break
				}
			}
			if !goalProductRe.MatchString(qi.Goal) || !hasClickMe {
				return nil
			}
			opts := NewOrdMap()
			opts.Set("multiply_0", "Play the memory game: press the button five times, read each number shown, "+
				"enter their product in the form and submit — done in this one action")
			return &SkillResult{
				Options:        opts,
				Delete:         []string{"wait", "back", "home"},
				DeletePrefixes: []string{"click_", "scroll_"},
			}
		},
	},
	{
		// Labeled goal values into the visible form: the goal names the field each
		// value belongs in ("the title 'X'"), so the host types each into the field
		// whose label matches. A small model scrolls a form whose fields are all
		// visible and never types anything (calendar editor, observed 4×).
		// Scrolling reveals nothing on a form whose fields are all in the table.
		ID:     "fill",
		Anchor: "post",
		Apply: func(qi QuestionInputs) *SkillResult {
			fields := GoalFields(qi.Goal)
			if len(fields) < 2 || hasChoice(qi.RecentChoices, "fill_0") {
				return nil
			}
			var editableLabels []string
			for _, r := range tableRows(qi.Table) {
				if r != nil && r.El != nil && IsEditable(r.El) {
					editableLabels = append(editableLabels, strings.ToLower(ElementText(r.El)))
				}
			}
			matched := false
			for _, f := range fields {
				for _, label := range editableLabels {
					if strings.Contains(label, f.Label) {
						matched = true
						break
					}
				}
			}
			if !matched {
				return nil
			}
			pairs := make([]string, len(fields))
			for i, f := range fields {
				pairs[i] = f.Label + ` = "` + f.Value + `"`
			}
			opts := NewOrdMap()
			opts.Set("fill_0", "Fill the form: type each labeled value into the field named like it ("+
				strings.Join(pairs, "; ")+") — the host types them all in one action")
			return &SkillResult{Options: opts, DeleteScrolls: true}
		},
	},
	{
		// Resend: the last message's text, sent again. The host reads the bubble
		// and re-sends it — copy/paste across two screens is where the model left
		// the conversation and never came back.
		ID:     "resend",
		Anchor: "post",
		Apply: func(qi QuestionInputs) *SkillResult {
			if !goalResendRe.MatchString(qi.Goal) || hasChoice(qi.RecentChoices, "resend_0") {
				return nil
			}
			hasEditable := false
			for _, r := range tableRows(qi.Table) {
				if r != nil && r.El != nil && IsEditable(r.El) {
					hasEditable = true
					break
				}
			}
			if !hasEditable {
				return nil
			}
			opts := NewOrdMap()
			opts.Set("resend_0", "Resend the last message: its text is sent again exactly as shown "+
				"(the host reads the bubble, types it and presses send)")
			return &SkillResult{Options: opts}
		},
	},
	{
		// A recording is complete after its start press and its stop press; a
		// third press of the same control starts ANOTHER recording and ruins a
		// goal that asked for one. Prose rules did not stop the model from
		// pressing again, so after two recorded presses the control is withdrawn
		// outright.
		ID:     "shutter",
		Anchor: "postStuck",
		Apply: func(qi QuestionInputs) *SkillResult {
			if !goalVideoRe.MatchString(qi.Goal) {
				return nil
			}
			presses := 0
			for _, a := range qi.RecentActions {
				if shutterActionRe.MatchString(a) {
					presses++
				}
			}
			if presses < 2 {
				return nil
			}
			var del []string
			for _, r := range tableRows(qi.Table) {
				if r != nil && r.El != nil && shutterTextRe.MatchString(ElementText(r.El)) {
					del = append(del, "click_"+strconv.Itoa(r.Idx))
				}
			}
			if len(del) == 0 {
				return nil
			}
			return &SkillResult{Delete: del}
		},
	},
}

// SkillsAt returns the registry entries for one anchor, in registry order.
func SkillsAt(anchor string) []Skill {
	var out []Skill
	for _, s := range Skills {
		if s.Anchor == anchor {
			out = append(out, s)
		}
	}
	return out
}

// ApplyTo folds one skill's requested mutations into criteria the way
// Object.assign + delete did: options append in their own order, then the
// exact deletes, the scroll pair, and finally the prefix deletions.
func ApplyTo(criteria *OrdMap, skill Skill, qi QuestionInputs) {
	r := skill.Apply(qi)
	if r == nil {
		return
	}
	if r.Options != nil {
		for _, k := range r.Options.Keys() {
			v, _ := r.Options.Get(k)
			criteria.Set(k, v)
		}
	}
	for _, k := range r.Delete {
		criteria.Delete(k)
	}
	if r.DeleteScrolls {
		criteria.Delete("scroll_down")
		criteria.Delete("scroll_up")
	}
	for _, prefix := range r.DeletePrefixes {
		for _, k := range criteria.Keys() {
			if strings.HasPrefix(k, prefix) {
				criteria.Delete(k)
			}
		}
	}
}
