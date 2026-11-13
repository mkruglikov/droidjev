package main

import (
	"strings"
	"testing"
)

func TestOrdMapMarshalOrder(t *testing.T) {
	m := NewOrdMap()
	m.Set("b", "2")
	m.Set("a", "1")
	m.Set("c", "3")
	m.Set("b", "20") // overwriting must keep the original position

	if got, want := strings.Join(m.Keys(), ","), "b,a,c"; got != want {
		t.Fatalf("Keys() = %q, want %q", got, want)
	}
	if v, ok := m.Get("b"); !ok || v != "20" {
		t.Errorf("Get(b) = %q, %v; want \"20\", true", v, ok)
	}
	if _, ok := m.Get("zz"); ok {
		t.Error("Get(zz) reported present")
	}
	if !m.Has("a") || m.Has("zz") {
		t.Error("Has disagrees with the map contents")
	}
	if got := m.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}

	got, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error: %v", err)
	}
	if want := `{"b":"20","a":"1","c":"3"}`; string(got) != want {
		t.Errorf("MarshalJSON() = %s, want %s", got, want)
	}

	var zero OrdMap // the zero value is usable too
	zero.Set("z", "1")
	if got, want := strings.Join(zero.Keys(), ","), "z"; got != want {
		t.Errorf("zero-value OrdMap Keys() = %q, want %q", got, want)
	}
}

func TestOrdMapDeleteReAdd(t *testing.T) {
	m := NewOrdMap()
	m.Set("b", "2")
	m.Set("a", "1")
	m.Delete("a")
	m.Set("a", "1")

	if got, want := strings.Join(m.Keys(), ","), "b,a"; got != want {
		t.Fatalf("Keys() after delete+re-add = %q, want %q", got, want)
	}
	got, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error: %v", err)
	}
	if want := `{"b":"2","a":"1"}`; string(got) != want {
		t.Errorf("MarshalJSON() after delete+re-add = %s, want %s", got, want)
	}

	m.Delete("a")
	if m.Has("a") || m.Len() != 1 {
		t.Errorf("Delete(a) left Has=%v Len=%d, want false/1", m.Has("a"), m.Len())
	}
	m.Delete("a") // deleting a missing key is a no-op
	if got, want := strings.Join(m.Keys(), ","), "b"; got != want {
		t.Errorf("Keys() after deleting missing key = %q, want %q", got, want)
	}
}

func TestOrdMapMarshalEscaping(t *testing.T) {
	m := NewOrdMap()
	m.Set("k&<>", `AT&T <x> "q" \`)

	got, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error: %v", err)
	}
	// JSONStringify semantics: <>& stay literal, quotes and backslashes escape.
	if want := `{"k&<>":"AT&T <x> \"q\" \\"}`; string(got) != want {
		t.Errorf("MarshalJSON() = %s, want %s", got, want)
	}
}

func TestOrdSetJoin(t *testing.T) {
	s := NewOrdSet()
	s.Add("b")
	s.Add("a")
	s.Add("b") // duplicate: ignored, no reordering

	if got, want := strings.Join(s.Slice(), ","), "b,a"; got != want {
		t.Errorf("Slice() = %q, want %q", got, want)
	}
	if got, want := s.Join(","), "b,a"; got != want {
		t.Errorf("Join(\",\") = %q, want %q", got, want)
	}
	if got, want := s.Join("-"), "b-a"; got != want {
		t.Errorf("Join(\"-\") = %q, want %q", got, want)
	}
	if got := s.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
	if !s.Has("a") || s.Has("zz") {
		t.Error("Has disagrees with the set contents")
	}
	if got := NewOrdSet().Join(","); got != "" {
		t.Errorf("empty Join = %q, want \"\"", got)
	}

	var zero OrdSet // the zero value is usable too
	zero.Add("z")
	if got, want := zero.Join(","), "z"; got != want {
		t.Errorf("zero-value OrdSet Join = %q, want %q", got, want)
	}
}

func TestMarshalNumericKeys(t *testing.T) {
	m := map[string]string{
		"12":         "x",
		"3":          "y",
		"b":          "2",
		"a":          "1",
		"01":         "z",   // leading zero: not a JS array index, stays a string key
		"4294967294": "hi",  // 2^32-2: still an array index
		"4294967295": "max", // 2^32-1: excluded from array indices in JS
	}
	got, err := MarshalNumericKeys(m)
	if err != nil {
		t.Fatalf("MarshalNumericKeys() error: %v", err)
	}
	// Array indices ascending numerically first, then the rest lexically.
	want := `{"3":"y","12":"x","4294967294":"hi","01":"z","4294967295":"max","a":"1","b":"2"}`
	if string(got) != want {
		t.Errorf("MarshalNumericKeys() = %s, want %s", got, want)
	}

	got, err = MarshalNumericKeys(map[string]string{})
	if err != nil {
		t.Fatalf("MarshalNumericKeys(empty) error: %v", err)
	}
	if want := `{}`; string(got) != want {
		t.Errorf("MarshalNumericKeys(empty) = %s, want %s", got, want)
	}
}
