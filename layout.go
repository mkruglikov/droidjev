// Layout: the UI hierarchy dump from the resident appium-uiautomator2-server.
// A dump is one adb-forwarded HTTP call; the server is installed on first use
// and kept resident across commands.
//
// Ported from the Node implementation. The tokenizer is hand-written on
// purpose: the server's XML is machine-generated and mostly attribute-only,
// and the original tolerated shapes encoding/xml rejects (a literal '>' inside
// a quoted attribute, unknown class-named tags, mismatched close tags). Keep
// it byte compatible with the original scanner.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bounds is a screen rectangle in dump coordinates (px).
type Bounds struct {
	L int `json:"l"`
	T int `json:"t"`
	R int `json:"r"`
	B int `json:"b"`
}

// Point is a tap position in dump coordinates (px).
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Element is one parsed node. It is the data model the whole program consumes
// (element tables, taps, decide requests). ParentIdx is the index of the
// parent element, or -1 when the parent is the <hierarchy> root (the Node
// code's null); Children holds indices of direct children in dump order.
//
// Pkg is "" where the Node code had null (the tag omitted package).
type Element struct {
	Class         string  `json:"class"`
	Pkg           string  `json:"pkg"`
	Text          string  `json:"text"`
	ResourceID    string  `json:"resourceId"`
	ContentDesc   string  `json:"contentDesc"`
	Hint          string  `json:"hint"`
	Bounds        *Bounds `json:"bounds,omitempty"`
	Center        *Point  `json:"center,omitempty"`
	OffScreen     bool    `json:"offScreen,omitempty"`
	ParentIdx     int     `json:"parentIdx"`
	Children      []int   `json:"children,omitempty"`
	Clickable     bool    `json:"clickable,omitempty"`
	LongClickable bool    `json:"longClickable,omitempty"`
	Scrollable    bool    `json:"scrollable,omitempty"`
	Checkable     bool    `json:"checkable,omitempty"`
	Checked       bool    `json:"checked,omitempty"`
	Focusable     bool    `json:"focusable,omitempty"`
	Focused       bool    `json:"focused,omitempty"`
	Selected      bool    `json:"selected,omitempty"`
	Password      bool    `json:"password,omitempty"`
}

// UnmarshalJSON maps a null or absent parentIdx to -1 (the wire's "no parent")
// instead of Go's zero value, which BuildTable would read as "parent is
// element 0".
func (el *Element) UnmarshalJSON(b []byte) error {
	type elementAlias Element
	aux := struct {
		ParentIdx *int `json:"parentIdx"`
		*elementAlias
	}{elementAlias: (*elementAlias)(el)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if aux.ParentIdx == nil {
		el.ParentIdx = -1
	} else {
		el.ParentIdx = *aux.ParentIdx
	}
	return nil
}

// boolAttrs is the JS BOOL_ATTRS vocabulary. Both spellings of
// long-clickable are accepted (uiautomator uses the hyphenated one).
var boolAttrs = []string{
	"clickable",
	"long-clickable",
	"longClickable",
	"scrollable",
	"checkable",
	"checked",
	"focusable",
	"focused",
	"selected",
	"password",
}

// boolField maps an attribute name to the Element field the JS loop assigns.
func (el *Element) boolField(attr string) *bool {
	switch attr {
	case "clickable":
		return &el.Clickable
	case "long-clickable", "longClickable":
		return &el.LongClickable
	case "scrollable":
		return &el.Scrollable
	case "checkable":
		return &el.Checkable
	case "checked":
		return &el.Checked
	case "focusable":
		return &el.Focusable
	case "focused":
		return &el.Focused
	case "selected":
		return &el.Selected
	case "password":
		return &el.Password
	}
	return nil
}

var (
	attrRe          = regexp.MustCompile(`([\w-]+)="([^"]*)"`)
	boundsRe        = regexp.MustCompile(`\[(\d+),(\d+)\]\[(\d+),(\d+)\]`)
	numericEntityRe = regexp.MustCompile(`&#(\d+);`)
	tagNameRe       = regexp.MustCompile(`^[\w.$]+`)
)

// decodeEntities decodes XML entities in the JS port's order: lt, gt, quot,
// apos, decimal numeric, and amp LAST. The order is load-bearing — decoding
// &amp; first would double-decode `&amp;lt;` into `<`.
func decodeEntities(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = numericEntityRe.ReplaceAllStringFunc(s, func(m string) string {
		cp, err := strconv.Atoi(m[2 : len(m)-1])
		if err != nil {
			// Atoi overflow: the code point is far beyond the valid range.
			return ""
		}
		if cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff) {
			return "" // out of range or a surrogate half, as String.fromCodePoint rejects them
		}
		return string(rune(cp))
	})
	return strings.ReplaceAll(s, "&amp;", "&")
}

// parseBounds is `[l,t][r,b]`; nil when absent, malformed, or inverted.
func parseBounds(s string) *Bounds {
	m := boundsRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	var n [4]int
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return nil
		}
		n[i] = v
	}
	if !(n[2] >= n[0] && n[3] >= n[1]) {
		return nil
	}
	return &Bounds{L: n[0], T: n[1], R: n[2], B: n[3]}
}

// ParseUia2Xml tokenizes the server's hierarchy dump into elements in document
// order. It never errors on odd node content — unknown tags and mismatched
// close tags are tolerated — but a dump with no <hierarchy> (screen off) or no
// nodes is an error: the agent treats those as a blind screen.
func ParseUia2Xml(xml string) ([]*Element, error) {
	start := strings.Index(xml, "<hierarchy")
	if start == -1 {
		return nil, NewDroidJevError("uia2 source contained no <hierarchy> (screen may be off)")
	}
	body := xml[start:]
	out := []*Element{}
	stack := []int{} // element indices; -1 = the <hierarchy> root itself
	i := 0
	for i < len(body) {
		lt := strings.IndexByte(body[i:], '<')
		if lt == -1 {
			break
		}
		lt += i
		if lt+1 < len(body) && body[lt+1] == '/' {
			// any closing tag pops one nesting level
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			gt := strings.IndexByte(body[lt:], '>')
			if gt == -1 {
				break
			}
			i = lt + gt + 1
			continue
		}
		// Scan to the tag's real end, ignoring '>' inside quoted attribute
		// values.
		j := lt + 1
		var quote byte
		for ; j < len(body); j++ {
			ch := body[j]
			if quote != 0 {
				if ch == quote {
					quote = 0
				}
			} else if ch == '"' || ch == '\'' {
				quote = ch
			} else if ch == '>' {
				break
			}
		}
		if j >= len(body) {
			break
		}
		selfClosing := body[j-1] == '/'
		name := tagNameRe.FindString(body[lt+1:])
		rawTag := body[lt : j+1]
		attrs := map[string]string{}
		for _, am := range attrRe.FindAllStringSubmatch(rawTag, -1) {
			attrs[am[1]] = decodeEntities(am[2])
		}
		if name == "hierarchy" {
			if !selfClosing {
				stack = append(stack, -1)
			}
		} else {
			b := parseBounds(attrs["bounds"])
			parentIdx := -1
			if top := stackTop(stack); top >= 0 {
				parentIdx = top
			}
			el := &Element{
				Class:       attrs["class"],
				Pkg:         attrs["package"],
				Text:        attrs["text"],
				ResourceID:  attrs["resource-id"],
				ContentDesc: attrs["content-desc"],
				Hint:        "",
				OffScreen:   attrs["displayed"] == "false",
				ParentIdx:   parentIdx,
				Children:    []int{},
			}
			if el.Class == "" {
				el.Class = name
			}
			if b != nil {
				el.Bounds = b
				// Math.round((l+r)/2): half rounds up; bounds are non-negative.
				el.Center = &Point{X: (b.L + b.R + 1) / 2, Y: (b.T + b.B + 1) / 2}
			}
			for _, k := range boolAttrs {
				if attrs[k] == "true" {
					*el.boolField(k) = true
				}
			}
			if parentIdx >= 0 {
				out[parentIdx].Children = append(out[parentIdx].Children, len(out))
			}
			out = append(out, el)
			if !selfClosing {
				stack = append(stack, len(out)-1)
			}
		}
		i = j + 1
	}
	if len(out) == 0 {
		return nil, NewDroidJevError("uia2 XML parsed to zero nodes")
	}
	return out, nil
}

// stackTop is the JS `stack[stack.length - 1]` with -1 for an empty stack (the
// JS undefined is treated the same as the <hierarchy> root marker).
func stackTop(stack []int) int {
	if len(stack) == 0 {
		return -1
	}
	return stack[len(stack)-1]
}

// Layout is one parsed dump. tookMs is wall time for the HTTP dump plus parse.
// RootPkg is the root element's package — the focused window's package, the
// same answer `dumpsys window` gives without an extra adb roundtrip.
type Layout struct {
	Elements []*Element
	TookMs   int64
	Xml      string
	RootPkg  string
}

// uia2Source is the dump seam: production is uia2.go's resident-server Source;
// tests replace it so GetLayout runs without a device.
var uia2Source = Source

// GetLayout dumps and parses the hierarchy once.
func GetLayout(serial string) (*Layout, error) {
	t0 := time.Now()
	xml, err := uia2Source(serial)
	if err != nil {
		return nil, err
	}
	elements, err := ParseUia2Xml(xml)
	if err != nil {
		return nil, err
	}
	rootPkg := ""
	if len(elements) > 0 {
		rootPkg = elements[0].Pkg
	}
	return &Layout{
		Elements: elements,
		TookMs:   time.Since(t0).Milliseconds(),
		Xml:      xml,
		RootPkg:  rootPkg,
	}, nil
}

// xmlHash is the first 16 hex chars of the SHA-256 of the raw dump.
func xmlHash(xml string) string {
	sum := sha256.Sum256([]byte(xml))
	return hex.EncodeToString(sum[:])[:16]
}

// SettleLayout waits out screen motion by polling the dump: two consecutive
// identical hierarchy hashes mean nothing (elements, states, bounds) is
// changing anymore. The final layout is returned so the caller reuses it as
// its dump instead of sleeping a fixed time and dumping once more.
func SettleLayout(serial string, maxMs int64) (*Layout, error) {
	deadline := time.Now().UnixMilli() + maxMs
	prev, err := GetLayout(serial)
	if err != nil {
		return nil, err
	}
	prevH := xmlHash(prev.Xml)
	for {
		Sleep(80)
		if time.Now().UnixMilli() >= deadline {
			return prev, nil
		}
		next, err := GetLayout(serial)
		if err != nil {
			return nil, err
		}
		h := xmlHash(next.Xml)
		if h == prevH {
			return next, nil
		}
		prev, prevH = next, h
	}
}
