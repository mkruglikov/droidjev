// Element table: filter the normalized layout to the elements the model
// should see, give them stable indices, and serialize compact rows. The model
// only ever sees rows like `[12] click "About phone"` — never coordinates.
//
// Ported from the Node implementation. JS's non-enumerable array props
// (`truncated`, `grid`) live on Table here.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxRows = 80
	// Top fraction of the screen where the platform's chrome package is a
	// status bar rather than content.
	statusBarFraction = 0.06
)

var chromePkgs = map[string]bool{"com.android.systemui": true}

// Automation companions droidjev itself installs (the io.appium.settings
// clipboard helper, the uiautomator2 server) show up on the launcher as
// ordinary app icons — a goal saying "open Settings" then matches "Appium
// Settings" literally. They are never a legitimate target.
var helperPkgs = map[string]bool{
	"io.appium.settings":                 true,
	"io.appium.uiautomator2.server":      true,
	"io.appium.uiautomator2.server.test": true,
}

var (
	editableRe = regexp.MustCompile(`EditText|Editable|AutoCompleteTextView|TextInput`)
	// A slider is a track with a thumb: the value follows the touch position,
	// so the only way to set it is a drag. SeekBar and Material's Slider both
	// report the value as the element's own text.
	sliderRe = regexp.MustCompile(`SeekBar|RangeSlider|\.Slider$|(^|\.)Slider\b`)
	// A row whose text is a date: clicking it opens a date picker, the fast
	// route to a date the goal names.
	dateLabelRe = regexp.MustCompile(
		`^(?:\d{4}-\d{1,2}-\d{1,2}` +
			`|\d{1,2}/\d{1,2}/\d{2,4}` +
			`|(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.?[` + JSWhitespaceClass + `]+\d{1,2}(?:st|nd|rd|th)?(?:,?[` + JSWhitespaceClass + `]+\d{4})?)` +
			`(?:[` + JSWhitespaceClass + `]|$)`,
	)
	whitespaceRunRe = regexp.MustCompile(`[` + JSWhitespaceClass + `]+`)
	imePkgRe        = regexp.MustCompile(`(?i)(inputmethod|keyboard)`)
	// A single printable character is what an IME key looks like in a dump.
	imeKeyCharRe = regexp.MustCompile(`^[\w,.'"!?-]$`)
	horizontalRe = regexp.MustCompile(`(?i)(HorizontalScrollView|ViewPager|Pager|Carousel|Gallery|TabLayout)`)
	hashFlagRe   = regexp.MustCompile(`\+(focused|selected)`)
)

// Row is one model-facing element line. Idx is its stable index for this
// step; Row is the serialized text; El is the element it resolved from.
// Interactive is true for anything tappable (including a labelled plain text
// row); Tappable marks exactly those plain text rows.
type Row struct {
	Idx         int
	Row         string
	El          *Element
	Interactive bool
	Tappable    bool
}

// DrawnGrid is the group of same-sized, unlabeled, interactive cells a screen
// draws rather than describes. Size is "WxH" in px; the cells are dropped from
// the table and reported through GridNote instead.
type DrawnGrid struct {
	Size  string
	Cells []*Element
}

// Table is the built element table plus the facts callers need about what was
// left out of it.
type Table struct {
	Rows      []*Row
	Truncated bool
	Grid      *DrawnGrid
}

// IsEditable reports whether the element accepts typed text.
func IsEditable(el *Element) bool { return editableRe.MatchString(el.Class) }

// IsSlider reports whether the element is a drag-to-set range control.
func IsSlider(el *Element) bool { return sliderRe.MatchString(el.Class) }

func isDateLabel(text string) bool { return dateLabelRe.MatchString(JSTrim(text)) }

func classShort(cls string) string {
	parts := strings.Split(cls, ".")
	return parts[len(parts)-1]
}

func resourceIDTail(id string) string {
	if id == "" {
		return ""
	}
	parts := strings.Split(id, "/")
	if tail := parts[len(parts)-1]; tail != "" {
		return tail
	}
	return id
}

// cleanLabel is the model-facing label form: quotes become apostrophes so the
// row's own quoting survives, whitespace collapses, and the text is capped at
// 90 UTF-16 units (87 + ellipsis).
func cleanLabel(s string) string {
	t := JSTrim(whitespaceRunRe.ReplaceAllString(strings.ReplaceAll(s, `"`, "'"), " "))
	if UTF16Len(t) > 90 {
		return UTF16Truncate(t, 87) + "…"
	}
	return t
}

func onScreen(el *Element, screen *Screen) bool {
	if el.Bounds == nil {
		return false
	}
	b := el.Bounds
	if b.R <= b.L || b.B <= b.T {
		return false // zero/negative area
	}
	if el.OffScreen {
		return false
	}
	if screen == nil {
		return true
	}
	return b.R > 0 && b.B > 0 && b.L < screen.W && b.T < screen.H // intersects viewport
}

// adoptLabel is geometry-based label adoption: an unlabeled interactive
// element takes the text of a labeled element geometrically contained in it.
// The label must be a substantial part of the container (3% of its area), and
// the smallest qualifying label wins (a row title beats its subtitle and a
// short badge).
func adoptLabel(el *Element, labeled []*Element) string {
	if el.Bounds == nil {
		return ""
	}
	elArea := (el.Bounds.R - el.Bounds.L) * (el.Bounds.B - el.Bounds.T)
	best := ""
	bestArea := 0
	hasBest := false
	for _, c := range labeled {
		if c == el || c.Bounds == nil {
			continue
		}
		if !(c.Bounds.L >= el.Bounds.L-2 && c.Bounds.T >= el.Bounds.T-2 &&
			c.Bounds.R <= el.Bounds.R+2 && c.Bounds.B <= el.Bounds.B+2) {
			continue
		}
		area := (c.Bounds.R - c.Bounds.L) * (c.Bounds.B - c.Bounds.T)
		if float64(area) < float64(elArea)*0.03 {
			continue
		}
		label := cleanLabel(c.Text)
		if label == "" {
			label = cleanLabel(c.ContentDesc)
		}
		if label == "" {
			continue
		}
		if !hasBest || area < bestArea {
			best, bestArea, hasBest = label, area, true
		}
	}
	return best
}

// rowLabel is the flat-provider fallback: an unlabeled switch/checkbox adopts
// the nearest label entirely to its left with its vertical center in the
// control's band — the same preference row.
func rowLabel(el *Element, labeled []*Element) string {
	if !el.Checkable || el.Bounds == nil {
		return ""
	}
	best := ""
	bestDist := 0
	hasBest := false
	for _, c := range labeled {
		if c == el || c.Bounds == nil || c.Bounds.R > el.Bounds.L {
			continue
		}
		mid := float64(c.Bounds.T+c.Bounds.B) / 2
		if mid < float64(el.Bounds.T) || mid > float64(el.Bounds.B) {
			continue
		}
		label := cleanLabel(c.Text)
		if label == "" {
			label = cleanLabel(c.ContentDesc)
		}
		if label == "" {
			continue
		}
		dist := el.Bounds.L - c.Bounds.R
		if !hasBest || dist < bestDist {
			best, bestDist, hasBest = label, dist, true
		}
	}
	return best
}

func isHelperEl(el *Element) bool {
	return helperPkgs[el.Pkg] ||
		cleanLabel(el.Text) == "Appium Settings" ||
		cleanLabel(el.ContentDesc) == "Appium Settings"
}

func isStatusBar(el *Element, screen *Screen) bool {
	return screen != nil && chromePkgs[el.Pkg] && el.Bounds != nil &&
		float64(el.Bounds.B) <= float64(screen.H)*statusBarFraction
}

// drawnGrid finds the largest group of same-sized, unlabeled, interactive
// elements laid out in rows and columns (a calendar month, a canvas, a dial
// pad with drawn digits). Groups keep first-seen order so ties are resolved
// like the JS Map's iteration.
func drawnGrid(elements []*Element) *DrawnGrid {
	type group struct {
		size  string
		cells []*Element
	}
	var groups []*group
	index := map[string]int{}
	for _, el := range elements {
		if el.Bounds == nil || cleanLabel(el.Text) != "" || cleanLabel(el.ContentDesc) != "" {
			continue
		}
		if !(el.Clickable || el.LongClickable || el.Checkable || IsEditable(el)) {
			continue
		}
		key := fmt.Sprintf("%dx%d", el.Bounds.R-el.Bounds.L, el.Bounds.B-el.Bounds.T)
		if i, ok := index[key]; ok {
			groups[i].cells = append(groups[i].cells, el)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, &group{size: key, cells: []*Element{el}})
	}
	var best *DrawnGrid
	for _, g := range groups {
		if len(g.cells) < 6 {
			continue
		}
		lefts := map[int]struct{}{}
		for _, c := range g.cells {
			lefts[c.Bounds.L] = struct{}{}
		}
		if len(lefts) < 3 {
			continue
		}
		if best == nil || len(g.cells) > len(best.Cells) {
			best = &DrawnGrid{Size: g.size, Cells: g.cells}
		}
	}
	return best
}

// tableEntry is an element that earned a row, before indices are assigned.
type tableEntry struct {
	flags       string
	label       string
	own         string
	el          *Element
	interactive bool
	tappable    bool
}

func (e *tableEntry) render(idx int) string {
	body := e.flags
	if body == "" {
		body = "text"
	}
	row := "[" + strconv.Itoa(idx) + "] " + body
	if e.label != "" {
		row += ` "` + e.label + `"`
	}
	if e.el.Hint != "" && e.own == "" {
		row += ` hint="` + cleanLabel(e.el.Hint) + `"`
	}
	if e.label == "" {
		row += " " + classShort(e.el.Class)
	}
	if e.label == "" && e.el.ResourceID != "" {
		row += " id=" + resourceIDTail(e.el.ResourceID)
	}
	return row
}

// BuildTable filters and indexes one parsed dump. The screen supplies the
// viewport for visibility/status-bar checks and App (the focused package) so
// an IME's foreign keys can be told from an in-app dial pad.
func BuildTable(elements []*Element, screen *Screen) *Table {
	// A helper's launcher icon is its clickable container (which carries no
	// label of its own) — drop the container with the helper node (its direct
	// parent), or an anonymous clickable row survives to be tapped blindly.
	// A scrollable parent is kept: all-apps grids sit icons directly in the
	// RecyclerView, and losing it would break scrolling for the whole drawer.
	hidden := make([]bool, len(elements))
	for i, el := range elements {
		if !isHelperEl(el) {
			continue
		}
		hidden[i] = true
		if el.ParentIdx >= 0 && el.ParentIdx < len(elements) {
			if p := elements[el.ParentIdx]; p != nil && !p.Scrollable {
				hidden[el.ParentIdx] = true
			}
		}
	}

	// An open IME floods the table with its keys. Typing is atomic here, so the
	// keys are never the right target. Ten or more clickable single-character
	// labels from ANOTHER package is a keyboard and goes; same-package single
	// chars are an in-app dial pad (the clock timer) and stay.
	appPkg := ""
	if screen != nil {
		appPkg = screen.App
	}
	imeKeys := map[*Element]struct{}{}
	for _, el := range elements {
		if !el.Clickable || el.Pkg == "" {
			continue
		}
		if imePkgRe.MatchString(el.Pkg) {
			imeKeys[el] = struct{}{}
			continue
		}
		label := cleanLabel(el.Text)
		if label == "" {
			label = cleanLabel(el.ContentDesc)
		}
		if appPkg != "" && el.Pkg != appPkg && imeKeyCharRe.MatchString(label) {
			imeKeys[el] = struct{}{}
		}
	}
	if len(imeKeys) < 10 {
		imeKeys = map[*Element]struct{}{}
	}

	visible := make([]*Element, 0, len(elements))
	for i, el := range elements {
		if hidden[i] || isStatusBar(el, screen) {
			continue
		}
		if _, ok := imeKeys[el]; ok {
			continue
		}
		if !onScreen(el, screen) {
			continue
		}
		visible = append(visible, el)
	}

	// A grid of same-sized unlabeled cells is drawn, not described: tapping
	// them is guessing, and they crowd out the rows that carry a label.
	grid := drawnGrid(visible)
	shown := visible
	if grid != nil {
		cells := make(map[*Element]struct{}, len(grid.Cells))
		for _, c := range grid.Cells {
			cells[c] = struct{}{}
		}
		shown = make([]*Element, 0, len(visible))
		for _, el := range visible {
			if _, ok := cells[el]; !ok {
				shown = append(shown, el)
			}
		}
	}

	labeled := make([]*Element, 0, len(shown))
	for _, el := range shown {
		if cleanLabel(el.Text) != "" || cleanLabel(el.ContentDesc) != "" {
			labeled = append(labeled, el)
		}
	}

	entries := make([]*tableEntry, 0, len(shown))
	for _, el := range shown {
		interactive := el.Clickable || el.LongClickable || el.Scrollable || el.Checkable || IsEditable(el)
		own := cleanLabel(el.Text)
		if own == "" {
			own = cleanLabel(el.ContentDesc)
		}
		if own == "" {
			own = cleanLabel(el.Hint)
		}
		if !interactive && own == "" {
			continue // decorative container
		}
		label := own
		if label == "" && interactive {
			label = adoptLabel(el, labeled)
		}
		if label == "" {
			label = rowLabel(el, labeled)
		}

		var flags []string
		if el.Clickable {
			flags = append(flags, "click")
		}
		if el.LongClickable {
			flags = append(flags, "longclick")
		}
		if el.Scrollable {
			flags = append(flags, "scroll")
		}
		if IsEditable(el) {
			flags = append(flags, "edit")
		}
		if el.Checkable {
			if el.Checked {
				flags = append(flags, "check=on")
			} else {
				flags = append(flags, "check=off")
			}
		}
		if IsSlider(el) {
			flags = append(flags, "range")
		}
		if isDateLabel(label) {
			flags = append(flags, "date")
		}
		if el.Selected {
			flags = append(flags, "selected")
		}
		if el.Focused {
			flags = append(flags, "focused")
		}

		// A labelled non-interactive element is a tap candidate: tapping the
		// label's center reaches the container's handler.
		tappable := !interactive && own != ""
		entries = append(entries, &tableEntry{
			flags:       strings.Join(flags, "+"),
			label:       label,
			own:         own,
			el:          el,
			interactive: interactive || tappable,
			tappable:    tappable,
		})
	}

	// Interactive elements win the cap: a dropped clickable target can never be
	// recovered by scrolling blind. Labelled text rows are tappable and thus
	// interactive too, so in practice this is a plain first-80 cap.
	rows := make([]*Row, 0, len(entries))
	for _, e := range entries {
		if !e.interactive {
			continue
		}
		idx := len(rows)
		rows = append(rows, &Row{Idx: idx, Row: e.render(idx), El: e.el, Interactive: e.interactive, Tappable: e.tappable})
		if len(rows) >= maxRows {
			break
		}
	}
	truncated := len(entries) > len(rows)
	for _, e := range entries {
		if e.interactive || len(rows) >= maxRows {
			continue
		}
		idx := len(rows)
		rows = append(rows, &Row{Idx: idx, Row: e.render(idx), El: e.el, Interactive: e.interactive, Tappable: e.tappable})
	}
	truncated = truncated || len(entries) > len(rows)

	table := &Table{Rows: rows, Truncated: truncated, Grid: grid}
	// A container that holds several labeled rows says which ones, so the
	// model can reason about a list or a selector without the hierarchy. A
	// title and subtitle inside a preference row is two, not a list: three
	// items, or a scrollable container with two.
	for _, r := range rows {
		if r.El.Bounds == nil {
			continue
		}
		items := ListItems(table, r.Idx)
		if len(items) >= 3 || (r.El.Scrollable && len(items) >= 2) {
			idxes := make([]string, len(items))
			for i, it := range items {
				idxes[i] = strconv.Itoa(it.Idx)
			}
			r.Row += ` holds [` + strings.Join(idxes, ",") + `]`
		}
	}
	return table
}

// SerializeRows is the model-facing view: rows only, no coordinates, no
// resource internals.
func SerializeRows(t *Table) []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		out = append(out, r.Row)
	}
	return out
}

// ContainsBounds reports whether inner lies within outer, allowing the 2px
// tolerance both dump providers need.
func ContainsBounds(outer, inner Bounds) bool {
	return inner.L >= outer.L-2 && inner.T >= outer.T-2 && inner.R <= outer.R+2 && inner.B <= outer.B+2
}

// centerOf: providers set center alongside bounds; a host-supplied table may
// not, and a gesture needs a point either way. A nil element or bounds yields
// the zero point rather than a panic (the Node code's caught TypeError).
func centerOf(el *Element) Point {
	if el == nil {
		return Point{}
	}
	if el.Center != nil {
		return *el.Center
	}
	b := el.Bounds
	if b == nil {
		return Point{}
	}
	return Point{X: jsRoundHalf(b.L + b.R), Y: jsRoundHalf(b.T + b.B)}
}

// jsRoundHalf is Math.round(sum/2): halves round toward +Infinity.
func jsRoundHalf(n int) int {
	return int(math.Floor(float64(n)/2 + 0.5))
}

// ScrollAnchorRow is the scrollable a gesture must move: the innermost one
// holding the focus, else the largest. Returned as the ROW so a host can size
// its swipe from the element's own bounds.
func ScrollAnchorRow(t *Table) *Row {
	var focused *Row
	for _, r := range t.Rows {
		if r.El != nil && r.El.Focused && r.El.Bounds != nil {
			focused = r
			break
		}
	}
	area := func(r *Row) int {
		b := r.El.Bounds
		return (b.R - b.L) * (b.B - b.T)
	}
	var smallest, largest *Row
	for _, r := range t.Rows {
		if r.El == nil || !r.El.Scrollable || r.El.Bounds == nil {
			continue
		}
		if focused != nil && ContainsBounds(*r.El.Bounds, *focused.El.Bounds) {
			if smallest == nil || area(r) < area(smallest) {
				smallest = r
			}
		}
		if largest == nil || area(r) > area(largest) {
			largest = r
		}
	}
	if smallest != nil {
		return smallest
	}
	return largest
}

// ScrollAnchor is ScrollAnchorRow as a point, or the screen middle when there
// is nothing to scroll.
func ScrollAnchor(t *Table, screen Screen) Point {
	if chosen := ScrollAnchorRow(t); chosen != nil {
		return centerOf(chosen.El)
	}
	return Point{X: jsRoundHalf(screen.W), Y: jsRoundHalf(screen.H)}
}

// HorizontalAnchor is the scrollable a horizontal gesture must start on, or
// nil when no dump signal says the axis is horizontal: a class that names it,
// or a short wide container whose children sit side by side.
func HorizontalAnchor(t *Table, screen *Screen) *Point {
	h := defaultScreenH
	if screen != nil {
		h = screen.H
	}
	for _, r := range t.Rows {
		if r.El == nil || !r.El.Scrollable || r.El.Bounds == nil {
			continue
		}
		if horizontalRe.MatchString(r.El.Class) {
			p := centerOf(r.El)
			return &p
		}
	}
	for _, r := range t.Rows {
		el := r.El
		if el == nil || !el.Scrollable || el.Bounds == nil {
			continue
		}
		b := el.Bounds
		tall := b.B - b.T
		if tall < 1 || float64(tall) > float64(h)*0.4 || b.R-b.L < tall*2 {
			continue
		}
		lefts := map[int]struct{}{}
		for _, x := range t.Rows {
			if x.El == nil || x.El == el || x.El.Bounds == nil {
				continue
			}
			xb := x.El.Bounds
			if xb.T < b.T-2 || xb.B > b.B+2 {
				continue
			}
			if !ContainsBounds(*b, *xb) {
				continue
			}
			lefts[xb.L] = struct{}{}
		}
		if len(lefts) >= 2 {
			p := centerOf(el)
			return &p
		}
	}
	return nil
}

// ListItems is the rows a container holds, in visual order (top, then left)
// with the inner duplicate of an adopted label dropped. Geometry is the only
// parent link every provider and host-supplied table is guaranteed to have.
func ListItems(t *Table, idx int) []*Row {
	var container *Row
	for _, r := range t.Rows {
		if r.Idx == idx {
			container = r
			break
		}
	}
	if container == nil || container.El == nil || container.El.Bounds == nil {
		return []*Row{}
	}
	inside := make([]*Row, 0, len(t.Rows))
	for _, r := range t.Rows {
		if r == container || r.El == nil || r.El.Bounds == nil {
			continue
		}
		if r.El.Text == "" && r.El.ContentDesc == "" {
			continue
		}
		if !ContainsBounds(*container.El.Bounds, *r.El.Bounds) {
			continue
		}
		inside = append(inside, r)
	}
	outermost := make([]*Row, 0, len(inside))
	for _, r := range inside {
		contained := false
		for _, o := range inside {
			if o != r && ContainsBounds(*o.El.Bounds, *r.El.Bounds) {
				contained = true
				break
			}
		}
		if !contained {
			outermost = append(outermost, r)
		}
	}
	sort.SliceStable(outermost, func(i, j int) bool {
		a, b := outermost[i].El.Bounds, outermost[j].El.Bounds
		if a.T != b.T {
			return a.T < b.T
		}
		return a.L < b.L
	})
	seen := map[string]struct{}{}
	out := make([]*Row, 0, len(outermost))
	for _, r := range outermost {
		text := ElementText(r.El)
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, r)
	}
	return out
}

// ElementText is the text a row contributes to the clipboard or an answer:
// the element's own text, single-spaced.
func ElementText(el *Element) string {
	s := el.Text
	if s == "" {
		s = el.ContentDesc
	}
	return JSTrim(whitespaceRunRe.ReplaceAllString(s, " "))
}

// GridNote describes a dropped drawn grid, or "" when there was none.
func GridNote(t *Table) string {
	if t == nil || t.Grid == nil {
		return ""
	}
	return "a " + t.Grid.Size + " grid of " + strconv.Itoa(len(t.Grid.Cells)) +
		" unlabeled cells was left out of the list: what each cell" +
		" holds is drawn, not text, so it cannot be told from its neighbours — reach that data by a text route instead"
}

// TableHash is change detection between consecutive tables: content only
// (labels, states, indices), NOT positions, and not the focused/selected flags
// that flap on scrolls without any content change.
func TableHash(t *Table) string {
	parts := make([]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		parts = append(parts, hashFlagRe.ReplaceAllString(r.Row, ""))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}
