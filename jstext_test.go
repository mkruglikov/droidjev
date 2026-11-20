package main

import (
	"regexp"
	"testing"
	"unicode/utf8"
)

func TestUTF16LenEmoji(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"\U0001F44D", 2}, // 👍 is a surrogate pair in JS
		{"a", 1},
		{" Maria ", 7},
		{"", 0},
		{"a\U0001F44Db", 4},
	}
	for _, c := range cases {
		if got := UTF16Len(c.in); got != c.want {
			t.Errorf("UTF16Len(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestUTF16TruncateEmoji(t *testing.T) {
	const thumb = "\U0001F44D"
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{thumb + thumb, 3, thumb}, // never split the pair, JS would emit a lone surrogate
		{thumb + thumb, 2, thumb},
		{thumb + thumb, 4, thumb + thumb},
		{thumb + thumb, 1, ""},
		{thumb + thumb, 0, ""},
		{"abcdef", 3, "abc"},
		{"abc", 10, "abc"},
		{"a" + thumb, 2, "a"},
		{"a" + thumb, 3, "a" + thumb},
	}
	for _, c := range cases {
		if got := UTF16Truncate(c.in, c.max); got != c.want {
			t.Errorf("UTF16Truncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestJSTrimNBSP(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\u00a0x\u00a0", "x"},             // NBSP is JS whitespace
		{"\ufeffx\u2028", "x"},             // BOM and LINE SEPARATOR too
		{"\u3000x\u3000", "x"},             // ideographic space
		{" \t\n\r\v\fx\f\r\n\t ", "x"},     // the ASCII set
		{"\u0085x\u0085", "\u0085x\u0085"}, // NEL is NOT JS whitespace
		{"\u00a0\u00a0", ""},
		{"x", "x"},
		{"", ""},
	}
	for _, c := range cases {
		if got := JSTrim(c.in); got != c.want {
			t.Errorf("JSTrim(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJSONStringify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"AT&T", `"AT&T"`},                       // no HTML escaping
		{`a"b`, `"a\"b"`},                        // quotes escaped
		{`a\b`, `"a\\b"`},                        // backslashes escaped
		{"\u0007", `"\u0007"`},                   // controls as \u00XX, lowercase hex
		{"\u001b", `"\u001b"`},                   // ... including 0x1b
		{"\u000b", `"\u000b"`},                   // vertical tab has no short form
		{"a\nb\tc\rd\be\f", `"a\nb\tc\rd\be\f"`}, // the short forms
		{"\u2028\u2029", "\"\u2028\u2029\""},     // NOT escaped (unlike encoding/json)
		{"<>&", `"<>&"`},                         // no HTML escaping either
		{"\U0001F44D", "\"\U0001F44D\""},         // non-ASCII stays literal
		{"", `""`},
	}
	for _, c := range cases {
		if got := JSONStringify(c.in); got != c.want {
			t.Errorf("JSONStringify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJSWhitespaceClass(t *testing.T) {
	jsSpace := regexp.MustCompile("[" + JSWhitespaceClass + "]")
	plain := regexp.MustCompile(`\s`)

	if !jsSpace.MatchString("\u00a0") {
		t.Errorf("%q does not match NBSP", JSWhitespaceClass)
	}
	if plain.MatchString("\u00a0") {
		t.Error(`RE2 \s alone matches NBSP; the class is pointless`)
	}
	for _, s := range []string{" ", "\t", "\n", "\r", "\f", "\v"} {
		if !jsSpace.MatchString(s) {
			t.Errorf("%q does not match %q", JSWhitespaceClass, s)
		}
	}
	for _, s := range []string{"\u2007", "\u202f", "\ufeff", "\u1680", "\u3000", "\u200a"} {
		if !jsSpace.MatchString(s) {
			t.Errorf("%q does not match %q", JSWhitespaceClass, s)
		}
	}
	if jsSpace.MatchString("x") {
		t.Errorf("%q matches a non-space", JSWhitespaceClass)
	}

	// JSTrim's cutset duplicates the class; lock them together over the BMP
	// (no JS whitespace is astral).
	for r := rune(0); r <= 0xffff; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		s := string(r)
		if got, want := JSTrim(s) == "", jsSpace.MatchString(s); got != want {
			t.Fatalf("rune %U: JSTrim trims = %v, class matches = %v", r, got, want)
		}
	}
}
