// Element-table tests ported from the Node implementation's element-table
// section.
// Expected rows were captured from the frozen Node implementation; the one
// deliberate divergence is UTF16Truncate dropping a straddled emoji whole
// (ruling R6b) instead of leaving the lone surrogate JS's slice() produces.

package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func testScreen() *Screen { return &Screen{W: 1344, H: 2992} }

// screenOf keeps the ad-hoc screen literals below readable.
func screenOf(w, h int) *Screen { return &Screen{W: w, H: h} }

// elAt builds an element the way the Node tests' `mk` helper did: empty
// text/ids, rounded center, every flag false, then mod applies the test's own
// fields.
func elAt(cls string, l, t, r, b int, mod func(*Element)) *Element {
	el := &Element{
		Class:  cls,
		Bounds: &Bounds{L: l, T: t, R: r, B: b},
		Center: &Point{X: (l + r + 1) / 2, Y: (t + b + 1) / 2},
	}
	if mod != nil {
		mod(el)
	}
	return el
}

func mustParse(t *testing.T, xml string) []*Element {
	t.Helper()
	els, err := ParseUia2Xml(xml)
	if err != nil {
		t.Fatalf("ParseUia2Xml: %v", err)
	}
	return els
}

func assertRows(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows =\n%#v\nwant\n%#v", got, want)
	}
}

// Label adoption: an unlabeled clickable row takes a contained label; the
// smallest qualifying label wins, and a scrollable takes its own contained
// label when it clears the 3%-of-area floor.
func TestBuildTableAdoptsContainedLabels(t *testing.T) {
	table := BuildTable(mustParse(t, sampleXML), testScreen())
	assertRows(t, SerializeRows(table), []string{
		`[0] scroll "Wi-Fi & Network"`,
		`[1] text "Wi-Fi & Network"`,
		`[2] click "About <phone>"`,
		`[3] text "About <phone>"`,
	})
}

// Decorative containers (non-interactive, unlabeled) never become rows, and
// off-screen elements are dropped entirely.
func TestBuildTableDropsDecorativeAndOffscreen(t *testing.T) {
	els := mustParse(t, sampleXML)
	table := BuildTable(els, testScreen())
	for _, row := range SerializeRows(table) {
		if strings.Contains(row, "FrameLayout") {
			t.Errorf("decorative container listed: %q", row)
		}
	}
	offscreen := []*Element{{
		Class:  els[0].Class,
		Bounds: &Bounds{L: 0, T: 3100, R: 100, B: 3200},
		Center: &Point{X: 50, Y: 3150},
	}}
	if got := len(BuildTable(offscreen, testScreen()).Rows); got != 0 {
		t.Errorf("off-screen table length = %d, want 0", got)
	}
	if got := len(BuildTable(nil, testScreen()).Rows); got != 0 {
		t.Errorf("empty input table length = %d, want 0", got)
	}
}

// Row titles are adopted by containment; a flat provider's switch adopts the
// label to its left on the same band; a full-screen scrollable wrapper never
// adopts a tiny stray child.
func TestBuildTableAdoptsRowTitlesAndSwitchLabels(t *testing.T) {
	els := []*Element{
		// full-width preference row, title ~6% of container area
		elAt("LinearLayout", 0, 347, 1344, 518, func(e *Element) { e.Clickable = true }),
		elAt("TextView", 252, 375, 463, 440, func(e *Element) { e.Text = "WLAN" }),
		// airplane-mode switch beside its title on the same row band
		elAt("TextView", 252, 850, 576, 915, func(e *Element) { e.Text = "Flugmodus" }),
		elAt("Switch", 1092, 811, 1248, 955, func(e *Element) {
			e.Checkable = true
			e.Clickable = true
		}),
		// full-screen scrollable wrapper with a tiny stray child (~0.07%)
		elAt("ScrollView", 0, 0, 1344, 2992, func(e *Element) { e.Scrollable = true }),
		elAt("TextView", 600, 100, 700, 130, func(e *Element) { e.Text = "Suche" }),
	}
	assertRows(t, SerializeRows(BuildTable(els, testScreen())), []string{
		`[0] click "WLAN"`,
		`[1] text "WLAN"`,
		`[2] text "Flugmodus"`,
		`[3] click+check=off "Flugmodus"`,
		`[4] scroll ScrollView holds [5,1,2]`,
		`[5] text "Suche"`,
	})
}

// Automation helper icons (and the anonymous clickable container around one)
// are hidden; a scrollable parent survives so the drawer keeps scrolling.
func TestBuildTableHidesHelperApps(t *testing.T) {
	const xml = `<hierarchy rotation="0">
  <node package="com.android.launcher3" class="android.widget.FrameLayout" bounds="[0,0][1344,2992]">
    <node package="com.android.launcher3" class="android.widget.RecyclerView" scrollable="true" bounds="[0,100][1344,2800]">
      <node package="com.android.launcher3" class="android.widget.FrameLayout" clickable="true" bounds="[100,300][400,500]">
        <node package="com.android.launcher3" class="android.widget.TextView" text="Appium Settings" bounds="[100,400][400,460]"/>
      </node>
      <node package="com.android.launcher3" class="android.widget.TextView" text="Settings" clickable="true" bounds="[500,300][800,460]"/>
      <node package="io.appium.settings" class="android.widget.TextView" text="whatever" clickable="true" bounds="[500,600][800,700]"/>
    </node>
  </node>
</hierarchy>`
	assertRows(t, SerializeRows(BuildTable(mustParse(t, xml), testScreen())), []string{
		`[0] scroll RecyclerView`,
		`[1] click "Settings"`,
	})
}

// The platform's status bar is chrome only in the top 6% and only for the
// chrome package; foreign dialogs below it and app rows in the band stay.
func TestBuildTableStatusBarChrome(t *testing.T) {
	els := []*Element{
		{Pkg: "com.simplemobiletools.calendar.pro", Class: "android.widget.TextView", Text: "October 15 (Sun)", Bounds: &Bounds{L: 0, T: 300, R: 400, B: 360}},
		{Pkg: "com.simplemobiletools.calendar.pro", Class: "android.widget.TextView", Text: "Search", Clickable: true, Bounds: &Bounds{L: 0, T: 80, R: 200, B: 120}},
		{Pkg: "com.android.systemui", Class: "android.widget.TextView", Text: "15:34", Clickable: true, Bounds: &Bounds{L: 700, T: 0, R: 800, B: 60}},
		{Pkg: "com.android.systemui", Class: "android.widget.SeekBar", Text: "Display brightness", Bounds: &Bounds{L: 42, T: 149, R: 1038, B: 275}},
	}
	assertRows(t, SerializeRows(BuildTable(els, screenOf(1080, 2400))), []string{
		`[0] date "October 15 (Sun)"`,
		`[1] click "Search"`,
		`[2] range "Display brightness"`,
	})
}

// An open IME floods the table with foreign single-char keys; ten or more is a
// keyboard and goes, fewer are stray buttons and stay. Same-package single
// chars are an in-app dial pad (screen.App) and always stay.
func TestBuildTableDropsIMEKeys(t *testing.T) {
	key := func(pkg, text string) *Element {
		return &Element{
			Pkg:       pkg,
			Class:     "android.widget.KeyboardView$Key",
			Text:      text,
			Clickable: true,
			Bounds:    &Bounds{L: 0, T: 2000, R: 100, B: 2100},
		}
	}
	appRow := &Element{Pkg: "com.money.manager.pro", Class: "android.widget.TextView", Text: "Social", Bounds: &Bounds{L: 0, T: 400, R: 200, B: 460}}
	letters := func(pkg string) []*Element {
		var out []*Element
		for _, ch := range strings.Split("qwertyuiopasdfghjklzxcvbnm", "") {
			out = append(out, key(pkg, ch))
		}
		return out
	}

	full := append([]*Element{appRow}, letters("com.google.android.inputmethod.latin")...)
	assertRows(t, SerializeRows(BuildTable(full, testScreen())), []string{`[0] text "Social"`})

	few := append([]*Element{appRow}, key("com.google.android.inputmethod.latin", "a"), key("com.google.android.inputmethod.latin", "b"), key("com.google.android.inputmethod.latin", "c"))
	assertRows(t, SerializeRows(BuildTable(few, testScreen())), []string{
		`[0] text "Social"`,
		`[1] click "a"`,
		`[2] click "b"`,
		`[3] click "c"`,
	})

	// A foreign package with no "inputmethod"/"keyboard" hint: ten single
	// chars against a different focused app still count as a keyboard.
	foreign := []*Element{key("com.example.other", "a")}
	for _, ch := range strings.Split("bcdefghij", "") {
		foreign = append(foreign, key("com.example.other", ch))
	}
	foreignScreen := &Screen{W: 1344, H: 2992, App: "com.example.app"}
	if got := len(BuildTable(foreign, foreignScreen).Rows); got != 0 {
		t.Errorf("foreign single-char rows = %d, want 0", got)
	}

	// Dial pad: same package as the focused app, so it is content.
	var pad []*Element
	for _, d := range strings.Split("123456789", "") {
		pad = append(pad, key("com.google.android.deskclock", d))
	}
	padScreen := &Screen{W: 1344, H: 2992, App: "com.google.android.deskclock"}
	assertRows(t, SerializeRows(BuildTable(pad, padScreen)), []string{
		`[0] click "1"`,
		`[1] click "2"`,
		`[2] click "3"`,
		`[3] click "4"`,
		`[4] click "5"`,
		`[5] click "6"`,
		`[6] click "7"`,
		`[7] click "8"`,
		`[8] click "9"`,
	})
}

// A drawn grid (same-sized, unlabeled, interactive cells in columns) is left
// out of the table and reported as a note instead.
func TestBuildTableDrawnGridAndNote(t *testing.T) {
	var els []*Element
	for row := 0; row < 2; row++ {
		for col := 0; col < 4; col++ {
			l := 100 + col*200
			t0 := 400 + row*200
			els = append(els, elAt("android.widget.FrameLayout", l, t0, l+200, t0+200, func(e *Element) { e.Clickable = true }))
		}
	}
	els = append(els, elAt("TextView", 100, 100, 900, 160, func(e *Element) { e.Text = "October 2025" }))
	table := BuildTable(els, screenOf(1080, 2400))
	assertRows(t, SerializeRows(table), []string{`[0] text "October 2025"`})
	if table.Grid == nil {
		t.Fatal("Grid = nil, want the drawn grid")
	}
	if table.Grid.Size != "200x200" || len(table.Grid.Cells) != 8 {
		t.Errorf("Grid = {%q, %d cells}, want {\"200x200\", 8 cells}", table.Grid.Size, len(table.Grid.Cells))
	}
	want := "a 200x200 grid of 8 unlabeled cells was left out of the list: what each cell holds is drawn, not text, " +
		"so it cannot be told from its neighbours — reach that data by a text route instead"
	if got := GridNote(table); got != want {
		t.Errorf("GridNote = %q, want %q", got, want)
	}
	if GridNote(&Table{}) != "" {
		t.Error("GridNote on a gridless table should be empty")
	}
}

// The 80-row cap keeps the earliest rows; labelled text rows are tappable and
// therefore interactive too, so the cap never reorders them.
func TestBuildTableTruncatesInteractiveFirst(t *testing.T) {
	els := make([]*Element, 0, 95)
	for i := 0; i < 90; i++ {
		els = append(els, elAt("android.widget.Button", 0, i*10, 100, i*10+10, func(e *Element) {
			e.Clickable = true
			e.Focusable = true
		}))
	}
	for i := 0; i < 5; i++ {
		text := "deco " + string(rune('0'+i))
		els = append(els, elAt("android.widget.TextView", 0, 2000+i*10, 100, 2010+i*10, func(e *Element) { e.Text = text }))
	}
	table := BuildTable(els, testScreen())
	if len(table.Rows) != 80 {
		t.Fatalf("table length = %d, want 80", len(table.Rows))
	}
	if !table.Truncated {
		t.Error("Truncated = false, want true")
	}
	for _, r := range table.Rows {
		if !r.Interactive || !r.El.Clickable {
			t.Fatalf("row %d kept a non-interactive entry: %q", r.Idx, r.Row)
		}
	}
	if table.Rows[79].Row != `[79] click Button` {
		t.Errorf("last row = %q", table.Rows[79].Row)
	}

	// Exactly at the cap is not truncated.
	if got := BuildTable(els[:80], testScreen()); len(got.Rows) != 80 || got.Truncated {
		t.Errorf("80-entry table = %d rows, truncated=%v; want 80, false", len(got.Rows), got.Truncated)
	}
}

// `holds [i,j,k]` names a container's labelled contents: three items for any
// container, two for a scrollable one.
func TestBuildTableHoldsAnnotation(t *testing.T) {
	els := []*Element{
		elAt("LinearLayout", 0, 0, 1000, 300, func(e *Element) { e.Clickable = true }),
		elAt("TextView", 20, 20, 900, 60, func(e *Element) { e.Text = "A1" }),
		elAt("TextView", 20, 150, 900, 190, func(e *Element) { e.Text = "A2" }),
		elAt("androidx.recyclerview.widget.RecyclerView", 0, 400, 1000, 800, func(e *Element) { e.Scrollable = true }),
		elAt("TextView", 20, 420, 900, 460, func(e *Element) { e.Text = "B1" }),
		elAt("TextView", 20, 550, 900, 590, func(e *Element) { e.Text = "B2" }),
	}
	assertRows(t, SerializeRows(BuildTable(els, testScreen())), []string{
		`[0] click "A1"`,
		`[1] text "A1"`,
		`[2] text "A2"`,
		`[3] scroll "B1" holds [4,5]`,
		`[4] text "B1"`,
		`[5] text "B2"`,
	})
}

// A label's quotes become apostrophes so the row's own quoting survives, and
// an over-90-unit label truncates at 87 units plus an ellipsis without cutting
// an emoji in half.
func TestCleanLabelQuoteSwap(t *testing.T) {
	long := strings.Repeat("a", 86) + "👍" + strings.Repeat("b", 5) // 93 UTF-16 units
	if got := UTF16Len(long); got != 93 {
		t.Fatalf("fixture UTF16Len = %d, want 93", got)
	}
	els := []*Element{
		elAt("android.widget.TextView", 0, 0, 500, 60, func(e *Element) {
			e.Text = `He said "yes"`
			e.Clickable = true
		}),
		elAt("android.widget.TextView", 0, 100, 500, 160, func(e *Element) {
			e.Text = long
			e.Clickable = true
		}),
	}
	rows := SerializeRows(BuildTable(els, screenOf(1080, 2400)))
	assertRows(t, rows, []string{
		`[0] click "He said 'yes'"`,
		`[1] click "` + strings.Repeat("a", 86) + "…\"",
	})
	if !utf8.ValidString(rows[1]) {
		t.Errorf("truncated row is not valid UTF-8: %q", rows[1])
	}
}

// The table hash is content-only: positions may shift and focused/selected may
// flap without changing it, but different text does.
func TestTableHashIgnoresPositionFocusSelected(t *testing.T) {
	t1 := BuildTable(mustParse(t, sampleXML), testScreen())
	if got := TableHash(t1); got != "39fec4bff84f65dc" {
		t.Errorf("TableHash = %q, want %q", got, "39fec4bff84f65dc")
	}
	if got := TableHash(BuildTable(mustParse(t, sampleXML), testScreen())); got != TableHash(t1) {
		t.Error("hash differs for identical dumps")
	}

	shifted := mustParse(t, sampleXML)
	for _, el := range shifted {
		if el.Bounds == nil {
			continue
		}
		el.Bounds.T -= 200
		el.Bounds.B -= 200
		if el.Center != nil {
			el.Center.Y -= 200
		}
	}
	if got := TableHash(BuildTable(shifted, testScreen())); got != TableHash(t1) {
		t.Error("hash changed on a position-only shift")
	}

	// focused/selected are stripped from a row that has other flags (the JS
	// regex removes only prefixed +focused/+selected, so flag the clickable
	// row, not a bare text row).
	flapped := mustParse(t, sampleXML)
	row := findElement(flapped, func(e *Element) bool { return e.Clickable })
	if row == nil {
		t.Fatal("clickable row element missing")
	}
	row.Focused = true
	row.Selected = true
	flappedTable := BuildTable(flapped, testScreen())
	if !strings.Contains(strings.Join(SerializeRows(flappedTable), "\n"), "click+selected+focused") {
		t.Fatalf("focused/selected flags missing from rows: %#v", SerializeRows(flappedTable))
	}
	if got := TableHash(flappedTable); got != TableHash(t1) {
		t.Error("hash changed on focused/selected flags")
	}

	other := mustParse(t, sampleXML)
	for _, el := range other {
		if el.Text != "" {
			el.Text = "Different"
		}
	}
	if got := TableHash(BuildTable(other, testScreen())); got == TableHash(t1) {
		t.Error("hash unchanged after a content change")
	}
}

// Non-ASCII labels travel byte-exact.
func TestCyrillicLabelRoundTrip(t *testing.T) {
	els := []*Element{elAt("android.widget.TextView", 0, 0, 500, 60, func(e *Element) {
		e.Text = "Настройки устройства"
		e.Clickable = true
	})}
	rows := SerializeRows(BuildTable(els, screenOf(1080, 2400)))
	if len(rows) != 1 || rows[0] != `[0] click "Настройки устройства"` {
		t.Fatalf("rows = %#v", rows)
	}
	if !utf8.ValidString(rows[0]) {
		t.Error("Cyrillic row is not valid UTF-8")
	}
}

// A date label is flagged so the picker route is visible; a clock time is not
// a date.
func TestDateLabelFlagged(t *testing.T) {
	date := elAt("android.widget.TextView", 0, 0, 400, 60, func(e *Element) {
		e.Text = "October 15 (Sun)"
		e.Clickable = true
	})
	if rows := SerializeRows(BuildTable([]*Element{date}, screenOf(1080, 2400))); len(rows) != 1 || rows[0] != `[0] click+date "October 15 (Sun)"` {
		t.Errorf("date row = %#v", rows)
	}
	clock := elAt("android.widget.TextView", 0, 0, 400, 60, func(e *Element) {
		e.Text = "15:34"
		e.Clickable = true
	})
	if rows := SerializeRows(BuildTable([]*Element{clock}, screenOf(1080, 2400))); len(rows) != 1 || strings.Contains(rows[0], "date") {
		t.Errorf("clock row must not be a date: %#v", rows)
	}
}

// The innermost scrollable holding the focus wins; otherwise the largest; a
// table with no scrollable falls back to the screen middle.
func TestScrollAnchorPrefersLargestAndFocusHolder(t *testing.T) {
	table := BuildTable(mustParse(t, sampleXML), testScreen())
	if got := ScrollAnchor(table, *testScreen()); got != (Point{X: 672, Y: 1500}) {
		t.Errorf("ScrollAnchor = %#v, want {672 1500}", got)
	}

	empty := &Table{Rows: []*Row{}}
	if got := ScrollAnchorRow(empty); got != nil {
		t.Errorf("ScrollAnchorRow(empty) = %#v, want nil", got)
	}
	if got := ScrollAnchor(empty, Screen{W: 1080, H: 2400}); got != (Point{X: 540, Y: 1200}) {
		t.Errorf("ScrollAnchor(empty) = %#v, want {540 1200}", got)
	}

	focus := &Table{Rows: []*Row{
		{Idx: 0, Row: `[0] scroll "Page"`, El: &Element{Scrollable: true, Bounds: &Bounds{L: 0, T: 0, R: 1080, B: 2400}, Center: &Point{X: 540, Y: 1200}}},
		{Idx: 1, Row: `[1] scroll "Hours"`, El: &Element{Scrollable: true, Bounds: &Bounds{L: 700, T: 800, R: 900, B: 1100}, Center: &Point{X: 800, Y: 950}}},
		{Idx: 2, Row: `[2] text "10"`, El: &Element{Focused: true, Text: "10", Bounds: &Bounds{L: 720, T: 850, R: 880, B: 950}, Center: &Point{X: 800, Y: 900}}},
	}}
	if got := ScrollAnchorRow(focus); got == nil || got.Idx != 1 {
		t.Fatalf("ScrollAnchorRow(focus) = %#v, want row 1", got)
	}
	if got := ScrollAnchor(focus, Screen{W: 1080, H: 2400}); got != (Point{X: 800, Y: 950}) {
		t.Errorf("ScrollAnchor(focus) = %#v, want {800 950}", got)
	}

	// A missing center falls back to the bounds midpoint.
	noCenter := &Table{Rows: []*Row{{Idx: 0, El: &Element{Scrollable: true, Bounds: &Bounds{L: 10, T: 10, R: 30, B: 30}}}}}
	if got := ScrollAnchor(noCenter, Screen{W: 100, H: 100}); got != (Point{X: 20, Y: 20}) {
		t.Errorf("ScrollAnchor(no center) = %#v, want {20 20}", got)
	}
}

// Horizontal gestures start on a class-named horizontal scrollable, or on a
// short wide container whose children sit side by side. Everything else is
// unknowable from the tree.
func TestHorizontalAnchor(t *testing.T) {
	named := &Table{Rows: []*Row{
		{Idx: 0, El: &Element{Scrollable: true, Class: "androidx.viewpager.widget.ViewPager", Bounds: &Bounds{L: 0, T: 100, R: 1080, B: 900}, Center: &Point{X: 540, Y: 500}}},
	}}
	if got := HorizontalAnchor(named, screenOf(1080, 2400)); got == nil || *got != (Point{X: 540, Y: 500}) {
		t.Errorf("named HorizontalAnchor = %#v, want {540 500}", got)
	}

	geo := &Table{Rows: []*Row{
		{Idx: 0, El: &Element{Scrollable: true, Bounds: &Bounds{L: 0, T: 100, R: 1000, B: 500}, Center: &Point{X: 500, Y: 300}}},
		{Idx: 1, El: &Element{Clickable: true, Text: "A", Bounds: &Bounds{L: 0, T: 120, R: 200, B: 480}}},
		{Idx: 2, El: &Element{Clickable: true, Text: "B", Bounds: &Bounds{L: 500, T: 120, R: 700, B: 480}}},
	}}
	if got := HorizontalAnchor(geo, screenOf(1080, 2400)); got == nil || *got != (Point{X: 500, Y: 300}) {
		t.Errorf("geometric HorizontalAnchor = %#v, want {500 300}", got)
	}
	// nil screen uses the 2340 default height, so the 400px band still fits.
	if got := HorizontalAnchor(geo, nil); got == nil || *got != (Point{X: 500, Y: 300}) {
		t.Errorf("nil-screen HorizontalAnchor = %#v, want {500 300}", got)
	}
	// On a short screen the band exceeds 40% of the height.
	if got := HorizontalAnchor(geo, screenOf(1080, 500)); got != nil {
		t.Errorf("short-screen HorizontalAnchor = %#v, want nil", got)
	}

	flat := &Table{Rows: []*Row{
		{Idx: 0, El: &Element{Clickable: true, Text: "x", Bounds: &Bounds{L: 0, T: 0, R: 10, B: 10}}},
	}}
	if got := HorizontalAnchor(flat, screenOf(1080, 2400)); got != nil {
		t.Errorf("flat HorizontalAnchor = %#v, want nil", got)
	}
}

// A host-supplied element without bounds must not panic: code falls back to
// the zero point instead of crashing the CLI.
func TestCenterOfNilBounds(t *testing.T) {
	if got := centerOf(&Element{}); got != (Point{}) {
		t.Errorf("centerOf(nil bounds) = %#v, want the zero point", got)
	}
	if got := centerOf(nil); got != (Point{}) {
		t.Errorf("centerOf(nil element) = %#v, want the zero point", got)
	}
}

// A container's items read in screen order, with the inner duplicate of an
// adopted label dropped.
func TestListItemsAndElementText(t *testing.T) {
	table := &Table{Rows: []*Row{
		{Idx: 0, Row: `[0] scroll "List"`, El: &Element{Scrollable: true, Bounds: &Bounds{L: 0, T: 0, R: 1080, B: 2000}}},
		{Idx: 1, Row: `[1] click "Check the tires"`, El: &Element{Clickable: true, Text: "Check the tires", Bounds: &Bounds{L: 20, T: 400, R: 900, B: 500}}},
		{Idx: 2, Row: `[2] text "Check the tires"`, El: &Element{Text: "Check the tires", Bounds: &Bounds{L: 40, T: 420, R: 400, B: 480}}},
		{Idx: 3, Row: `[3] click "Book a flight"`, El: &Element{Clickable: true, Text: "Book a flight", Bounds: &Bounds{L: 20, T: 300, R: 900, B: 400}}},
	}}
	items := ListItems(table, 0)
	var texts []string
	for _, r := range items {
		texts = append(texts, ElementText(r.El))
	}
	assertRows(t, texts, []string{"Book a flight", "Check the tires"})
	if got := ListItems(table, 99); len(got) != 0 {
		t.Errorf("ListItems(missing idx) = %#v, want empty", got)
	}

	if got := ElementText(&Element{Text: "  a   b "}); got != "a b" {
		t.Errorf("ElementText(spaced) = %q, want %q", got, "a b")
	}
	if got := ElementText(&Element{ContentDesc: " c\t d "}); got != "c d" {
		t.Errorf("ElementText(contentDesc) = %q, want %q", got, "c d")
	}
	if got := ElementText(&Element{}); got != "" {
		t.Errorf("ElementText(empty) = %q, want empty", got)
	}
}

func TestIsEditableAndIsSlider(t *testing.T) {
	editable := []struct {
		class string
		want  bool
	}{
		{"android.widget.EditText", true},
		{"androidx.appcompat.widget.AppCompatAutoCompleteTextView", true},
		{"android.widget.TextView", false},
		{"", false},
	}
	for _, c := range editable {
		if got := IsEditable(&Element{Class: c.class}); got != c.want {
			t.Errorf("IsEditable(%q) = %v, want %v", c.class, got, c.want)
		}
	}
	sliders := []struct {
		class string
		want  bool
	}{
		{"android.widget.SeekBar", true},
		{"com.google.android.material.slider.RangeSlider", true},
		{"android.widget.Slider", true},
		{"com.google.android.material.slider.Slider", true},
		{"android.widget.TextView", false},
	}
	for _, c := range sliders {
		if got := IsSlider(&Element{Class: c.class}); got != c.want {
			t.Errorf("IsSlider(%q) = %v, want %v", c.class, got, c.want)
		}
	}
}

// Containment allows the 2px tolerance the tree-based providers need.
func TestContainsBounds(t *testing.T) {
	outer := Bounds{L: 0, T: 0, R: 100, B: 100}
	cases := []struct {
		name  string
		inner Bounds
		want  bool
	}{
		{"nested", Bounds{L: 10, T: 10, R: 90, B: 90}, true},
		{"equal", outer, true},
		{"within tolerance", Bounds{L: -2, T: -2, R: 102, B: 102}, true},
		{"left outside", Bounds{L: -3, T: 0, R: 100, B: 100}, false},
		{"too wide", Bounds{L: 0, T: 0, R: 103, B: 100}, false},
	}
	for _, c := range cases {
		if got := ContainsBounds(outer, c.inner); got != c.want {
			t.Errorf("%s: ContainsBounds = %v, want %v", c.name, got, c.want)
		}
	}
}

// SerializeRows yields the row strings and nothing else.
func TestSerializeRowsOnlyRows(t *testing.T) {
	table := &Table{Rows: []*Row{
		{Idx: 0, Row: `[0] click "A"`, El: &Element{}, Interactive: true},
		{Idx: 1, Row: `[1] text "B"`, El: &Element{}, Tappable: true},
	}}
	assertRows(t, SerializeRows(table), []string{`[0] click "A"`, `[1] text "B"`})
}
