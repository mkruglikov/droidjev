// Goal parsers and macro-registry tests
// (the keypadSequence / goalTable / goalTexts cases plus the skills-registry
// characterization goldens' expected keys and descriptions). The two RE2
// emulations — the `^0+(?=\d)` lookahead and the route pattern's terminator
// lookahead — are exercised directly.

package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// --- parser tests -----------------------------------------------------------

func TestKeypadSequence(t *testing.T) {
	cases := []struct{ goal, want string }{
		{"Create a timer with 14 hours, 35 minutes, and 31 seconds. Do not start the timer.", "143531"},
		{"Create a timer with 0 hours, 16 minutes, and 35 seconds.", "1635"},
		{"Create an alarm at 14:35.", "1435"},
		{"open clock app and set a timer for 3 hours", "30000"},
		{"set a timer for 45 seconds", "45"},
		{"set a timer for 5 minutes", "500"},
		{"set a timer for 50 minutes", "5000"},
		{"Take one video.", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := KeypadSequence(c.goal); got != c.want {
			t.Errorf("KeypadSequence(%q) = %q, want %q", c.goal, got, c.want)
		}
	}
}

// The JS `String(total).replace(/^0+(?=\d)/, ”)` lookahead: leading zeros go
// only while a digit remains after them, so an all-zero string keeps its last
// zero.
func TestStripLeadingZeros(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1635", "1635"},
		{"001", "1"},
		{"0001", "1"},
		{"0", "0"},
		{"000", "0"},
		{"10", "10"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripLeadingZeros(c.in); got != c.want {
			t.Errorf("stripLeadingZeros(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGoalTable(t *testing.T) {
	goal := strings.Join([]string{
		"Add the following expenses into the pro expense:",
		"name|amount_dollars|category_name|note",
		"Stationery|$352.34|Others|A need",
		"Bakery Items|$469.02|Food|A need",
	}, "\n")
	res := GoalTable(goal)
	wantHeader := []string{"name", "amount_dollars", "category_name", "note"}
	if !reflect.DeepEqual(res.Header, wantHeader) {
		t.Errorf("header = %#v, want %#v", res.Header, wantHeader)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %#v, want 2 rows", res.Rows)
	}
	wantRow := []string{"Bakery Items", "$469.02", "Food", "A need"}
	if !reflect.DeepEqual(res.Rows[1], wantRow) {
		t.Errorf("rows[1] = %#v, want %#v", res.Rows[1], wantRow)
	}

	// Prose, short pipe runs (<3 cells), and rows of empty cells are ignored.
	prose := GoalTable("Take one video.\njust|two\n\n| |")
	if prose.Header != nil || len(prose.Rows) != 0 {
		t.Errorf("prose table = %#v, want no header and no rows", prose)
	}

	// A pipe row with an empty cell keeps the remaining cells, in order.
	sparse := GoalTable("name|amount|note\nd||f")
	if len(sparse.Rows) != 1 || !reflect.DeepEqual(sparse.Rows[0], []string{"d", "f"}) {
		t.Errorf("sparse rows = %#v, want [[d f]]", sparse.Rows)
	}
}

func TestGoalFields(t *testing.T) {
	got := GoalFields("Create an event with the title 'Dentist' and the description 'Checkup'")
	want := []GoalField{{"title", "Dentist"}, {"description", "Checkup"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GoalFields = %#v, want %#v", got, want)
	}

	// titled/named normalize to title/name; curly quotes are accepted too.
	got = GoalFields("a note titled “Foo” and a contact named 'Bob'")
	want = []GoalField{{"title", "Foo"}, {"name", "Bob"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GoalFields normalized = %#v, want %#v", got, want)
	}

	if got := GoalFields("Take one video."); len(got) != 0 {
		t.Errorf("GoalFields(prose) = %#v, want none", got)
	}
}

func assertTexts(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GoalTexts = %#v, want %#v", got, want)
	}
}

func TestGoalTexts(t *testing.T) {
	assertTexts(t, GoalTexts("open maps and build a route for car from moscow to saint-petersburg"),
		[]string{"moscow", "saint-petersburg"})
	// A comma qualifies the place; it does not end the value.
	assertTexts(t, GoalTexts("build a route from moscow, ID to saint-petersburg, russia"),
		[]string{"moscow, ID", "saint-petersburg, russia"})
	// The route terminator is not part of the value.
	assertTexts(t, GoalTexts("build a route from Moscow, ID to Saint-Petersburg, Russia."),
		[]string{"Moscow, ID", "Saint-Petersburg, Russia"})
	assertTexts(t, GoalTexts("build a route from A to B, and take the shortest"),
		[]string{"A", "B"})
	assertTexts(t, GoalTexts("build a route from A to B, then press go"),
		[]string{"A", "B"})
	// Both endpoint groups are non-empty in JS: a dangling "to " is no route.
	assertTexts(t, GoalTexts("build a route from A to "), nil)
	assertTexts(t, GoalTexts("create an event with the title 'Workshop on Budget Planning'"),
		[]string{"Workshop on Budget Planning"})
	assertTexts(t, GoalTexts("create a new contact named Maria, phone +79001234567"),
		[]string{"Maria", "+79001234567"})
	assertTexts(t, GoalTexts("called Bob"), []string{"Bob"})
	// Prose that names no value supplies none: the model must not invent text.
	assertTexts(t, GoalTexts("copy the IMEI from settings and paste it into chrome search bar"), nil)
	assertTexts(t, GoalTexts("set a timer for 3 hours"), nil)
	// Words that merely look like labels must not become typed values.
	assertTexts(t, GoalTexts("scroll from top to bottom"), nil)
	assertTexts(t, GoalTexts("from 10 to 20"), nil)
	assertTexts(t, GoalTexts("the phone is available"), nil)
	assertTexts(t, GoalTexts("email app"), nil)
	assertTexts(t, GoalTexts("called to order"), nil)
	assertTexts(t, GoalTexts("named the"), nil)
	assertTexts(t, GoalTexts("email bob@example.com"), []string{"bob@example.com"})
	assertTexts(t, GoalTexts("phone +79001234567"), []string{"+79001234567"})
	assertTexts(t, GoalTexts("phone +7 900 123-45-67"), []string{"+7 900 123-45-67"})
}

func TestGoalTextsNormalization(t *testing.T) {
	// Dedupe is case-insensitive; the first spelling wins.
	assertTexts(t, GoalTexts("build a route from Moscow to moscow"), []string{"Moscow"})
	assertTexts(t, GoalTexts("entry 'Same' and 'same'"), []string{"Same"})
	// Length cap: 120 UTF-16 units, measured after whitespace collapse.
	exact := strings.Repeat("a", 120)
	assertTexts(t, GoalTexts("entry '"+exact+"'"), []string{exact})
	long := strings.Repeat("x", 121)
	assertTexts(t, GoalTexts("build a route from "+long+" to Paris"), []string{"Paris"})
	// Whitespace collapses and no newline survives (the JS check is dead after
	// its own \s+ collapse, but the output must still be one line).
	assertTexts(t, GoalTexts("entry 'a\nb'"), []string{"a b"})
}

func TestHasKeypad(t *testing.T) {
	var els []*Element
	for i := 0; i < 6; i++ {
		els = append(els, skillRow("android.widget.TextView", strconv.Itoa(i), i, true))
	}
	if !HasKeypad(skillTable(els...)) {
		t.Error("HasKeypad = false for six single-digit rows, want true")
	}
	if HasKeypad(skillTable(els[:5]...)) {
		t.Error("HasKeypad = true for five single-digit rows, want false")
	}
	if HasKeypad(nil) {
		t.Error("HasKeypad(nil) = true, want false")
	}
	// A two-digit label is not a keypad key.
	var twoDigits []*Element
	for i := 0; i < 6; i++ {
		twoDigits = append(twoDigits, skillRow("android.widget.TextView", "10", i, true))
	}
	if HasKeypad(skillTable(twoDigits...)) {
		t.Error("HasKeypad = true for two-digit labels, want false")
	}
}

// --- registry tests ---------------------------------------------------------

func skillRow(cls, text string, i int, clickable bool) *Element {
	return elAt(cls, 0, 100+i*120, 1000, 100+i*120+100, func(e *Element) {
		e.Text = text
		e.Clickable = clickable
	})
}

func skillTable(els ...*Element) *Table { return BuildTable(els, testScreen()) }

func keypadTable() *Table {
	var els []*Element
	for i := 0; i < 6; i++ {
		els = append(els, skillRow("android.widget.TextView", strconv.Itoa(i), i, true))
	}
	return skillTable(els...)
}

func applySkillsAt(t *testing.T, criteria *OrdMap, anchor string, qi QuestionInputs) {
	t.Helper()
	for _, s := range SkillsAt(anchor) {
		ApplyTo(criteria, s, qi)
	}
}

func mustOption(t *testing.T, m *OrdMap, key string) string {
	t.Helper()
	v, ok := m.Get(key)
	if !ok {
		t.Fatalf("option %q missing from %#v", key, m.Keys())
	}
	return v
}

func criteriaOf(keys ...string) *OrdMap {
	m := NewOrdMap()
	for _, k := range keys {
		m.Set(k, k)
	}
	return m
}

func TestSkillsRegistryOrder(t *testing.T) {
	wantIDs := []string{"digits", "launcherScrolls", "addRow", "walk", "multiply", "fill", "resend", "shutter"}
	wantAnchors := []string{"assign", "post", "post", "post", "post", "post", "post", "postStuck"}
	if len(Skills) != len(wantIDs) {
		t.Fatalf("registry length = %d, want %d", len(Skills), len(wantIDs))
	}
	for i, s := range Skills {
		if s.ID != wantIDs[i] || s.Anchor != wantAnchors[i] {
			t.Errorf("Skills[%d] = {%q, %q}, want {%q, %q}", i, s.ID, s.Anchor, wantIDs[i], wantAnchors[i])
		}
	}
	post := SkillsAt("post")
	if len(post) != 6 || post[0].ID != "launcherScrolls" || post[5].ID != "resend" {
		t.Errorf("SkillsAt(post) = %#v, want launcherScrolls..resend in registry order", post)
	}
	if got := SkillsAt("assign"); len(got) != 1 || got[0].ID != "digits" {
		t.Errorf("SkillsAt(assign) = %#v, want just digits", got)
	}
	if got := SkillsAt("postStuck"); len(got) != 1 || got[0].ID != "shutter" {
		t.Errorf("SkillsAt(postStuck) = %#v, want just shutter", got)
	}
	if got := SkillsAt("nope"); len(got) != 0 {
		t.Errorf("SkillsAt(nope) = %#v, want none", got)
	}
}

// The digits option is emitted at the assign anchor, i.e. before the fixed
// ops buildQuestions appends after the click rows — the golden key order.
func TestApplyToDigitsAnchored(t *testing.T) {
	qi := QuestionInputs{
		Table: keypadTable(),
		Goal:  "Create a timer with 0 hours, 16 minutes, and 35 seconds.",
	}
	criteria := criteriaOf("click_0", "click_1", "click_2", "click_3", "click_4", "click_5")
	applySkillsAt(t, criteria, "assign", qi)
	for _, k := range []string{"back", "home", "wait", "done", "blocked"} {
		criteria.Set(k, k)
	}
	wantKeys := []string{
		"click_0", "click_1", "click_2", "click_3", "click_4", "click_5",
		"digits_0", "back", "home", "wait", "done", "blocked",
	}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("criteria keys = %#v, want %#v", got, wantKeys)
	}
	want := `Type the goal's digit sequence "1635" into the keypad in one action — the host sends every digit; the display fills to the goal's value`
	if got := mustOption(t, criteria, "digits_0"); got != want {
		t.Errorf("digits_0 = %q, want %q", got, want)
	}

	// No keypad, or no sequence in the goal: no option.
	noKeypad := criteriaOf("click_0")
	ApplyTo(noKeypad, Skills[0], QuestionInputs{Table: skillTable(skillRow("android.widget.TextView", "ok", 0, true)), Goal: qi.Goal})
	if noKeypad.Has("digits_0") {
		t.Error("digits_0 offered without a keypad table")
	}
	noSeq := criteriaOf("click_0")
	ApplyTo(noSeq, Skills[0], QuestionInputs{Table: qi.Table, Goal: "Take one video."})
	if noSeq.Has("digits_0") {
		t.Error("digits_0 offered without a digit sequence")
	}
}

func TestSkillsLauncherScrolls(t *testing.T) {
	criteria := criteriaOf("scroll_down", "scroll_up", "click_0", "done")
	ApplyTo(criteria, Skills[1], QuestionInputs{Launcher: true, Apps: []string{"Chrome"}})
	if got := criteria.Keys(); !reflect.DeepEqual(got, []string{"click_0", "done"}) {
		t.Errorf("keys = %#v, want scrolls withdrawn", got)
	}
	// Without a named app there is nothing to replace the scrolls with.
	keep := criteriaOf("scroll_down", "scroll_up")
	ApplyTo(keep, Skills[1], QuestionInputs{Launcher: true, Apps: nil})
	if !keep.Has("scroll_down") || !keep.Has("scroll_up") {
		t.Error("scrolls withdrawn without a launch option on the table")
	}
}

const goldenGoalTable = "Add the following expenses into the pro expense:\n" +
	"name|amount_dollars|category_name|note\n" +
	"Stationery|352.34|Others|A need\n" +
	"Bakery Items|469.02|Food|A need"

func TestSkillsAddRow(t *testing.T) {
	criteria := criteriaOf("back", "home", "wait", "done", "blocked")
	applySkillsAt(t, criteria, "post", QuestionInputs{Goal: goldenGoalTable})
	wantKeys := []string{"back", "home", "wait", "done", "blocked", "add_row_0", "add_row_1"}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", got, wantKeys)
	}
	want0 := "Add entry 1 of the goal's table (Stationery · 352.34 · Others · A need) — the host opens the form if needed, enters every column in its right field, picks the category and saves"
	if got := mustOption(t, criteria, "add_row_0"); got != want0 {
		t.Errorf("add_row_0 = %q, want %q", got, want0)
	}
	want1 := "Add entry 2 of the goal's table (Bakery Items · 469.02 · Food · A need) — the host opens the form if needed, enters every column in its right field, picks the category and saves"
	if got := mustOption(t, criteria, "add_row_1"); got != want1 {
		t.Errorf("add_row_1 = %q, want %q", got, want1)
	}

	// A row already saved this run is withdrawn.
	used := criteriaOf("back", "home", "wait", "done", "blocked")
	applySkillsAt(t, used, "post", QuestionInputs{Goal: goldenGoalTable, RecentChoices: []string{"add_row_0"}})
	if used.Has("add_row_0") || !used.Has("add_row_1") {
		t.Errorf("used-row withdrawal failed: keys = %#v", used.Keys())
	}

	// No table in the goal: nothing offered.
	none := criteriaOf()
	applySkillsAt(t, none, "post", QuestionInputs{Goal: "Take one video."})
	if none.Has("add_row_0") {
		t.Error("add_row_0 offered without a goal table")
	}
}

func walkTable() *Table {
	return skillTable(
		skillRow("android.widget.TextView", "Up", 0, true),
		skillRow("android.widget.TextView", "Down", 1, true),
		skillRow("android.widget.TextView", "Left", 2, true),
		skillRow("android.widget.TextView", "Right", 3, true),
		skillRow("android.widget.TextView", "Saved", 4, false),
	)
}

func TestSkillsWalk(t *testing.T) {
	qi := QuestionInputs{Table: walkTable(), Goal: "walk the piece to the goal cell"}
	criteria := criteriaOf("click_0", "copy_4", "scroll_down", "scroll_up", "wait", "back", "home", "done", "blocked")
	applySkillsAt(t, criteria, "post", qi)
	wantKeys := []string{"copy_4", "done", "blocked", "walk_0"}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", got, wantKeys)
	}
	want := "Walk the piece (the X) to the goal cell with the Up/Down/Left/Right buttons — the host presses them and follows the piece until it arrives"
	if got := mustOption(t, criteria, "walk_0"); got != want {
		t.Errorf("walk_0 = %q, want %q", got, want)
	}

	// Already offered and used: withdrawn for the next step.
	again := criteriaOf("done", "blocked")
	applySkillsAt(t, again, "post", QuestionInputs{Table: qi.Table, Goal: qi.Goal, RecentChoices: []string{"walk_0"}})
	if again.Has("walk_0") {
		t.Error("walk_0 re-offered after it was chosen")
	}
	// Three of the four directions is not a walkable pad.
	partial := skillTable(
		skillRow("android.widget.TextView", "Up", 0, true),
		skillRow("android.widget.TextView", "Down", 1, true),
		skillRow("android.widget.TextView", "Left", 2, true),
	)
	p := criteriaOf("done")
	applySkillsAt(t, p, "post", QuestionInputs{Table: partial, Goal: qi.Goal})
	if p.Has("walk_0") {
		t.Error("walk_0 offered without all four direction buttons")
	}
}

func TestSkillsMultiply(t *testing.T) {
	qi := QuestionInputs{
		Table: skillTable(
			skillRow("android.widget.Button", "Click Me", 0, true),
			skillRow("android.widget.Button", "Enter", 1, true),
		),
		Goal: "click the button five times and enter their product",
	}
	criteria := criteriaOf("click_0", "scroll_down", "wait", "back", "home", "done", "blocked")
	applySkillsAt(t, criteria, "post", qi)
	wantKeys := []string{"done", "blocked", "multiply_0"}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", got, wantKeys)
	}
	want := "Play the memory game: press the button five times, read each number shown, enter their product in the form and submit — done in this one action"
	if got := mustOption(t, criteria, "multiply_0"); got != want {
		t.Errorf("multiply_0 = %q, want %q", got, want)
	}

	// No product word in the goal, or no Click Me button: nothing.
	noProduct := criteriaOf("done")
	applySkillsAt(t, noProduct, "post", QuestionInputs{Table: qi.Table, Goal: "press the button"})
	if noProduct.Has("multiply_0") {
		t.Error("multiply_0 offered without a product goal")
	}
	noButton := criteriaOf("done")
	applySkillsAt(t, noButton, "post", QuestionInputs{Table: skillTable(skillRow("android.widget.Button", "Enter", 0, true)), Goal: qi.Goal})
	if noButton.Has("multiply_0") {
		t.Error("multiply_0 offered without a Click Me button")
	}
}

func TestSkillsFill(t *testing.T) {
	qi := QuestionInputs{
		Table: skillTable(
			skillRow("android.widget.EditText", "Title", 0, true),
			skillRow("android.widget.EditText", "Description", 1, true),
			skillRow("android.widget.Button", "Save", 2, true),
		),
		Goal: "Create an event with the title 'Dentist' and the description 'Checkup'",
	}
	criteria := criteriaOf("click_2", "scroll_down", "scroll_up", "back", "home", "wait", "done", "blocked")
	applySkillsAt(t, criteria, "post", qi)
	wantKeys := []string{"click_2", "back", "home", "wait", "done", "blocked", "fill_0"}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", got, wantKeys)
	}
	want := `Fill the form: type each labeled value into the field named like it (title = "Dentist"; description = "Checkup") — the host types them all in one action`
	if got := mustOption(t, criteria, "fill_0"); got != want {
		t.Errorf("fill_0 = %q, want %q", got, want)
	}

	// One labeled value is not a form.
	oneField := criteriaOf("done")
	applySkillsAt(t, oneField, "post", QuestionInputs{Table: qi.Table, Goal: "Create an event with the title 'Dentist'"})
	if oneField.Has("fill_0") {
		t.Error("fill_0 offered for a single labeled value")
	}
	// The goal's label must appear on an editable row.
	mismatch := criteriaOf("done")
	applySkillsAt(t, mismatch, "post", QuestionInputs{
		Table: skillTable(
			skillRow("android.widget.EditText", "Name", 0, true),
			skillRow("android.widget.EditText", "Email", 1, true),
		),
		Goal: qi.Goal,
	})
	if mismatch.Has("fill_0") {
		t.Error("fill_0 offered with no editable label matching the goal")
	}
	// Already filled once: not again.
	again := criteriaOf("done")
	applySkillsAt(t, again, "post", QuestionInputs{Table: qi.Table, Goal: qi.Goal, RecentChoices: []string{"fill_0"}})
	if again.Has("fill_0") {
		t.Error("fill_0 re-offered after it was chosen")
	}
}

func TestSkillsResend(t *testing.T) {
	qi := QuestionInputs{
		Table: skillTable(skillRow("android.widget.EditText", "Message", 0, true)),
		Goal:  "Resend the last message",
	}
	criteria := criteriaOf("back", "home", "wait", "done", "blocked")
	applySkillsAt(t, criteria, "post", qi)
	wantKeys := []string{"back", "home", "wait", "done", "blocked", "resend_0"}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", got, wantKeys)
	}
	want := "Resend the last message: its text is sent again exactly as shown (the host reads the bubble, types it and presses send)"
	if got := mustOption(t, criteria, "resend_0"); got != want {
		t.Errorf("resend_0 = %q, want %q", got, want)
	}

	// No editable field, or already re-sent: nothing.
	noField := criteriaOf("done")
	applySkillsAt(t, noField, "post", QuestionInputs{Table: skillTable(skillRow("android.widget.TextView", "Message", 0, true)), Goal: qi.Goal})
	if noField.Has("resend_0") {
		t.Error("resend_0 offered without an editable field")
	}
	again := criteriaOf("done")
	applySkillsAt(t, again, "post", QuestionInputs{Table: qi.Table, Goal: qi.Goal, RecentChoices: []string{"resend_0"}})
	if again.Has("resend_0") {
		t.Error("resend_0 re-offered after it was chosen")
	}
}

func TestSkillsShutter(t *testing.T) {
	table := skillTable(
		skillRow("android.widget.ImageButton", "Shutter", 0, true),
		skillRow("android.widget.TextView", "Mode", 1, true),
	)
	qi := QuestionInputs{
		Table:         table,
		Goal:          "Take one video.",
		RecentActions: []string{`1. click [0] click "Shutter"`, `2. click [0] click "Shutter"`},
	}
	criteria := criteriaOf("click_0", "click_1", "back", "home", "wait", "done", "blocked")
	applySkillsAt(t, criteria, "postStuck", qi)
	wantKeys := []string{"click_1", "back", "home", "wait", "done", "blocked"}
	if got := criteria.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("keys = %#v, want %#v", got, wantKeys)
	}

	// One press is the normal recording-in-progress state: the shutter stays.
	one := criteriaOf("click_0", "click_1")
	ApplyTo(one, Skills[7], QuestionInputs{Table: table, Goal: qi.Goal, RecentActions: qi.RecentActions[:1]})
	if !one.Has("click_0") {
		t.Error("shutter withdrawn after a single press")
	}
	// A non-video goal never touches the shutter.
	photo := criteriaOf("click_0")
	ApplyTo(photo, Skills[7], QuestionInputs{Table: table, Goal: "Take a photo.", RecentActions: qi.RecentActions})
	if !photo.Has("click_0") {
		t.Error("shutter withdrawn for a photo goal")
	}
}
