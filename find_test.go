// Ports the matchRows cases from the frozen Node implementation:
// exact quoted label beats substring, clickable beats not, topmost wins ties.
// The ` holds [..]` containment marker is internal metadata, not searchable
// text — a query like "14" must not hit an index list.
package main

import (
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
