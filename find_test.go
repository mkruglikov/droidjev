// matchRows ranking:
// exact quoted label beats substring, clickable beats not, topmost wins ties.
// The ` holds [..]` containment marker is internal metadata, not searchable
// text — a query like "14" must not hit an index list.
package main

import (
	"strconv"
	"strings"
	"testing"
)

func matchRow(idx int, row string, clickable bool) *Row {
	return &Row{
		Idx: idx,
		Row: row,
		El: &Element{
			Clickable: clickable,
			Bounds:    &Bounds{L: 0, T: 0, R: 5, B: 5},
			Center:    &Point{X: 10, Y: 20},
		},
	}
}

func rowTexts(rows []*Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Row
	}
	return out
}

func TestMatchRows(t *testing.T) {
	table := &Table{Rows: []*Row{
		matchRow(0, `[0] text "Idioma del dispositivo"`, false),
		matchRow(1, `[1] click "Español (España)"`, true),
		matchRow(2, `[2] click "Español"`, false),
		matchRow(3, `[3] text "Español (Latinoamérica)"`, false),
	}}

	exact, err := MatchRows(table, "español") // exact match, case-insensitive non-ASCII
	if err != nil {
		t.Fatalf("MatchRows exact: %v", err)
	}
	if got := rowTexts(exact); len(got) != 1 || got[0] != `[2] click "Español"` {
		t.Fatalf("exact matches = %q, want only the exact label", got)
	}

	partial, err := MatchRows(table, "español (españa)")
	if err != nil {
		t.Fatalf("MatchRows partial: %v", err)
	}
	if got := rowTexts(partial); len(got) != 1 || got[0] != `[1] click "Español (España)"` {
		t.Fatalf("partial matches = %q, want the clickable exact label first", got)
	}

	many, err := MatchRows(table, "esp")
	if err != nil {
		t.Fatalf("MatchRows many: %v", err)
	}
	if len(many) != 3 {
		t.Fatalf("substring matches = %q, want 3 (Idioma del dispositivo does not contain \"esp\")", rowTexts(many))
	}

	if _, err := MatchRows(table, "  "); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("MatchRows(blank) error = %v, want a non-empty label error", err)
	}
}

// Clickable beats not, then the smallest index (topmost) wins ties.
func TestMatchRowsRanking(t *testing.T) {
	table := &Table{Rows: []*Row{
		matchRow(0, `[0] text "Open"`, false),
		matchRow(1, `[1] click "Open"`, true),
		matchRow(2, `[2] text "Open"`, false),
	}}
	got, err := MatchRows(table, "open")
	if err != nil {
		t.Fatalf("MatchRows: %v", err)
	}
	want := []string{`[1] click "Open"`, `[0] text "Open"`, `[2] text "Open"`}
	if texts := rowTexts(got); strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("ranked matches = %q, want %q", texts, want)
	}
}

// The ` holds [...]` suffix is stripped before the substring search, so an
// index list never satisfies a query; the row's own label still matches.
func TestMatchRowsIgnoresHoldsMetadata(t *testing.T) {
	table := &Table{Rows: []*Row{
		matchRow(0, `[0] click "Wrap" holds [13,14,15]`, true),
	}}
	if got, err := MatchRows(table, "14"); err != nil || len(got) != 0 {
		t.Fatalf("MatchRows(\"14\") = %q, %v; want no match from the marker", rowTexts(got), err)
	}
	if got, err := MatchRows(table, "wrap"); err != nil || len(got) != 1 {
		t.Fatalf("MatchRows(\"wrap\") = %q, %v; want the row itself", rowTexts(got), err)
	}
}

// findScript drives findScan with a scripted list: one Snapshot per position,
// recording every scroll direction and wake. findPos builds a position whose
// rows are the given labels plus the scrollable container every real list
// carries.
func findPos(rows ...string) *Snapshot {
	table := &Table{Rows: []*Row{}}
	for i, r := range rows {
		table.Rows = append(table.Rows, matchRow(i, `[`+strconv.Itoa(i)+`] click "`+r+`"`, true))
	}
	table.Rows = append(table.Rows, matchRow(len(rows), `[`+strconv.Itoa(len(rows))+`] scroll`, false))
	table.Rows[len(rows)].El.Scrollable = true
	return &Snapshot{Serial: "emulator-5554", Table: table}
}

type findScript struct {
	snaps []*Snapshot
	calls int
	dirs  []string
	wakes int
}

func (s *findScript) io() findIO {
	return findIO{
		Snapshot: func(string) (*Snapshot, error) {
			if s.calls >= len(s.snaps) {
				return nil, NewDroidJevError("snapshot script exhausted")
			}
			snap := s.snaps[s.calls]
			s.calls++
			return snap, nil
		},
		Scroll: func(_ string, _ *Snapshot, dir string) error {
			s.dirs = append(s.dirs, dir)
			return nil
		},
		Wake: func(string) error { s.wakes++; return nil },
		Tap:  func(string, *Element) error { return nil },
	}
}

func TestFindLabelAboveStartPosition(t *testing.T) {
	script := &findScript{snaps: []*Snapshot{
		findPos("Gamma", "Delta"),    // mid-list: the run starts scrolled down
		findPos("Alpha", "Settings"), // one up-scroll reaches the top
	}}
	res, err := findScan(FindOpts{Serial: "emulator-5554", Label: "settings"}, script.io())
	if err != nil {
		t.Fatalf("findScan: %v", err)
	}
	if !res.Found || res.Iterations != 2 {
		t.Fatalf("result = found:%v iterations:%d, want a hit at iteration 2", res.Found, res.Iterations)
	}
	if strings.Join(script.dirs, ",") != "up" {
		t.Fatalf("scrolls = %v, want one up", script.dirs)
	}
}

func TestFindLabelVisibleImmediately(t *testing.T) {
	script := &findScript{snaps: []*Snapshot{findPos("Alpha", "Settings")}}
	res, err := findScan(FindOpts{Serial: "emulator-5554", Label: "settings"}, script.io())
	if err != nil {
		t.Fatalf("findScan: %v", err)
	}
	if !res.Found || res.Iterations != 1 || len(script.dirs) != 0 {
		t.Fatalf("result = found:%v iterations:%d scrolls:%v, want an immediate hit", res.Found, res.Iterations, script.dirs)
	}
}

// From a mid-list position, the scan walks to the top (up, up-stall, wake,
// still-stall), then down through every position to a down-stall: end_of_list
// is now an exhaustive verdict, not "bottom reached from wherever we started".
func TestFindLabelExhaustiveEndOfList(t *testing.T) {
	script := &findScript{snaps: []*Snapshot{
		findPos("Gamma", "Delta"), // 1: start mid-list → up
		findPos("Alpha", "Beta"),  // 2: upper → up
		findPos("Alpha", "Beta"),  // 3: unchanged → wake, up (no-op)
		findPos("Alpha", "Beta"),  // 4: unchanged → the top → down
		findPos("Gamma", "Delta"), // 5: mid again → down
		findPos("Epsilon"),        // 6: bottom → down
		findPos("Epsilon"),        // 7: unchanged → wake, down (no-op)
		findPos("Epsilon"),        // 8: unchanged → end_of_list
	}}
	io := script.io()
	res, err := findScan(FindOpts{Serial: "emulator-5554", Label: "zeta"}, io)
	if err != nil {
		t.Fatalf("findScan: %v", err)
	}
	if res.Found || res.Reason != "end_of_list" {
		t.Fatalf("result = found:%v reason:%q, want end_of_list", res.Found, res.Reason)
	}
	if res.Iterations != 8 {
		t.Fatalf("iterations = %d, want 8", res.Iterations)
	}
	wantDirs := "up,up,up,down,down,down,down"
	if strings.Join(script.dirs, ",") != wantDirs {
		t.Fatalf("scrolls = %v, want %s", script.dirs, wantDirs)
	}
	if script.wakes != 2 {
		t.Fatalf("wakes = %d, want 2 (one per stall dance)", script.wakes)
	}
}

func TestFindLabelBudgetShared(t *testing.T) {
	// A list that always shows new content never stalls: the budget, not the
	// content, stops it.
	snaps := []*Snapshot{}
	for i := 0; i < 5; i++ {
		snaps = append(snaps, findPos("Row"+strconv.Itoa(i)))
	}
	script := &findScript{snaps: snaps}
	res, err := findScan(FindOpts{Serial: "emulator-5554", Label: "never", MaxIterations: 3}, script.io())
	if err != nil {
		t.Fatalf("findScan: %v", err)
	}
	if res.Reason != "max_iterations" || res.Iterations != 3 {
		t.Fatalf("result = reason:%q iterations:%d, want max_iterations at 3", res.Reason, res.Iterations)
	}
}
