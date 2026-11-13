// Insertion-ordered collections. JavaScript object property order is prompt
// order: buildQuestions' criteria key order is semantic (the model can see
// it), while Go maps marshal alphabetically. OrdMap/OrdSet keep the order the
// Node implementation got for free; MarshalNumericKeys reproduces JS's
// integer-first key ordering for maps that arrive as plain Go maps.

package main

import (
	"sort"
	"strings"
)

// ordEntry is one key/value pair; the slice order is the insertion order.
type ordEntry struct {
	key, val string
}

// OrdMap is a string map that iterates and marshals in insertion order.
type OrdMap struct {
	entries []ordEntry
	index   map[string]int
}

// NewOrdMap returns an empty ordered map.
func NewOrdMap() *OrdMap {
	return &OrdMap{index: make(map[string]int)}
}

// Set stores v under k, or overwrites it in place when k already exists —
// overwriting does not move the key, matching JS object assignment.
func (m *OrdMap) Set(k, v string) {
	if m.index == nil {
		m.index = make(map[string]int)
	}
	if i, ok := m.index[k]; ok {
		m.entries[i].val = v
		return
	}
	m.index[k] = len(m.entries)
	m.entries = append(m.entries, ordEntry{k, v})
}

// Get returns the value for k and whether it is present.
func (m *OrdMap) Get(k string) (string, bool) {
	if i, ok := m.index[k]; ok {
		return m.entries[i].val, true
	}
	return "", false
}

// Has reports whether k is present.
func (m *OrdMap) Has(k string) bool {
	_, ok := m.index[k]
	return ok
}

// Delete removes k. A later Set of the same key appends it at the end, as
// deleting and re-adding a JS object property does.
func (m *OrdMap) Delete(k string) {
	i, ok := m.index[k]
	if !ok {
		return
	}
	delete(m.index, k)
	copy(m.entries[i:], m.entries[i+1:])
	m.entries[len(m.entries)-1] = ordEntry{}
	m.entries = m.entries[:len(m.entries)-1]
	for j := i; j < len(m.entries); j++ {
		m.index[m.entries[j].key] = j
	}
}

// Keys returns the keys in insertion order.
func (m *OrdMap) Keys() []string {
	keys := make([]string, len(m.entries))
	for i, e := range m.entries {
		keys[i] = e.key
	}
	return keys
}

// Len returns the number of entries.
func (m *OrdMap) Len() int {
	return len(m.entries)
}

// MarshalJSON renders {"k":"v",...} in insertion order with JSON.stringify
// string escaping.
func (m *OrdMap) MarshalJSON() ([]byte, error) {
	return marshalEntries(m.entries), nil
}

// OrdSet is a string set that iterates and joins in insertion order.
type OrdSet struct {
	items []string
	index map[string]struct{}
}

// NewOrdSet returns an empty ordered set.
func NewOrdSet() *OrdSet {
	return &OrdSet{index: make(map[string]struct{})}
}

// Add inserts s unless it is already present.
func (s *OrdSet) Add(v string) {
	if s.index == nil {
		s.index = make(map[string]struct{})
	}
	if _, ok := s.index[v]; ok {
		return
	}
	s.index[v] = struct{}{}
	s.items = append(s.items, v)
}

// Has reports whether v is present.
func (s *OrdSet) Has(v string) bool {
	_, ok := s.index[v]
	return ok
}

// Slice returns the members in insertion order.
func (s *OrdSet) Slice() []string {
	return append([]string(nil), s.items...)
}

// Join joins the members in insertion order with sep.
func (s *OrdSet) Join(sep string) string {
	return strings.Join(s.items, sep)
}

// Len returns the number of members.
func (s *OrdSet) Len() int {
	return len(s.items)
}

// MarshalNumericKeys marshals a plain string map the way JSON.stringify does
// for a JS object: keys that are array indices ("0", "12"; canonical decimal,
// below 2^32-1) come first in ascending numeric order, the remaining keys
// after them in lexical order. The Go map has no insertion order to preserve.
func MarshalNumericKeys(m map[string]string) ([]byte, error) {
	var numeric []ordEntry
	var other []string
	for k, v := range m {
		if _, ok := jsArrayIndex(k); ok {
			numeric = append(numeric, ordEntry{k, v})
		} else {
			other = append(other, k)
		}
	}
	sort.Slice(numeric, func(i, j int) bool {
		a, _ := jsArrayIndex(numeric[i].key)
		b, _ := jsArrayIndex(numeric[j].key)
		return a < b
	})
	sort.Strings(other)

	entries := make([]ordEntry, 0, len(m))
	entries = append(entries, numeric...)
	for _, k := range other {
		entries = append(entries, ordEntry{k, m[k]})
	}
	return marshalEntries(entries), nil
}

// marshalEntries writes a JSON object from entries in slice order. Both
// marshalers share it so there is exactly one string escaper in play.
func marshalEntries(entries []ordEntry) []byte {
	var b strings.Builder
	b.Grow(2 + len(entries)*8)
	b.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(JSONStringify(e.key))
		b.WriteByte(':')
		b.WriteString(JSONStringify(e.val))
	}
	b.WriteByte('}')
	return []byte(b.String())
}

// jsArrayIndex reports whether k is an array index in ECMAScript terms: a
// canonical decimal integer in [0, 2^32-2]. Those are the object keys
// JSON.stringify emits first, in numeric order.
func jsArrayIndex(k string) (uint32, bool) {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
		if n > 0xffffffff {
			return 0, false
		}
	}
	if n == 0xffffffff {
		return 0, false
	}
	return uint32(n), true
}
