// JavaScript string semantics the Node implementation depends on. The prompts
// must be byte-identical to the Node port, so every place that measured with
// `.length`, cut with `.slice()`, trimmed with `.trim()` or quoted with
// `JSON.stringify()` goes through these helpers instead of the Go defaults.

package main

import "strings"

// JSWhitespaceClass is what JavaScript's `\s` matches, spelled for use inside
// a Go (RE2) character class: `[\s\v\x{00a0}...]`. JS `\s` is written
// `\s\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff`; Go's
// regexp has no `\uXXXX` escape, so those are `\x{...}` here, and `\v` is
// spelled out because RE2's own `\s` is [\t\n\f\r ] — ASCII, without \v.
// The class is mandatory wherever a JS pattern was matched against text.
const JSWhitespaceClass = `\s\v\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsSpaceCutset is JSWhitespaceClass as a cutset: `\s` expanded to the ASCII
// members and the \u2000-\u200a range spelled out. It is the exact JS set, not
// unicode.IsSpace — that one also trims U+0085 (not JS whitespace) and misses
// U+FEFF (JS whitespace). Keep in step with JSWhitespaceClass.
const jsSpaceCutset = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

// UTF16Len is JavaScript's `s.length`: UTF-16 code units, so astral runes
// (emoji) count 2.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xffff {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// UTF16Truncate is `s.slice(0, maxUnits)`, except that it never splits a
// surrogate pair: JS would return a lone high surrogate (which no longer
// re-encodes to the same text), so the pair is dropped whole instead.
func UTF16Truncate(s string, maxUnits int) string {
	if maxUnits <= 0 {
		return ""
	}
	n := 0
	for i, r := range s {
		w := 1
		if r > 0xffff {
			w = 2
		}
		if n+w > maxUnits {
			return s[:i]
		}
		n += w
	}
	return s
}

// JSTrim is `s.trim()`.
func JSTrim(s string) string {
	return strings.Trim(s, jsSpaceCutset)
}

// JSONStringify is what `JSON.stringify(s)` returns for a string: the quotes
// included, `"` and `\` escaped, the short control escapes where JS has them,
// other C0 controls as lowercase `\u00xx`, and nothing else touched — notably
// no HTML escaping of <>& (encoding/json does that) and U+2028/U+2029 left
// literal (encoding/json escapes those too). Invalid UTF-8 becomes U+FFFD,
// as it would passing through encoding/json.
func JSONStringify(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[r>>4])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
