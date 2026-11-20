package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// recordAdb puts a fake `adb` first on PATH that appends each invocation's
// arguments (space-joined, one line per call) to a temp file. Nothing here
// touches a device: the tests assert the argv a gesture would produce.
func recordAdb(t *testing.T) func() []string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "adb.log")
	fakeAdb(t, `printf '%s\n' "$*" >> '`+log+`'`)
	return func() []string {
		t.Helper()
		b, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adb calls =\n%q\nwant\n%q", got, want)
	}
}

// Ports the `sanitizeInputText` accept side: the exact charset
// [A-Za-z0-9 .,:+=/@%^_-] passes through untouched, except that ASCII spaces
// become %s (the only escape `input text` knows). A user-supplied %s stays %s
// on device — the documented ceiling.
func TestSanitizeAccepts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc", "abc"},
		{"ABC", "ABC"},
		{"0123456789", "0123456789"},
		{"a.b,c:d/e+f@g%h^i", "a.b,c:d/e+f@g%h^i"},
		{"a_b-c", "a_b-c"},
		{" hello world ", "%shello%sworld%s"},
		{"hello world", "hello%sworld"},
		{"a  b", "a%s%sb"},
		{"a%sb", "a%sb"},
	}
	for _, c := range cases {
		got, err := SanitizeInputText(c.in)
		if err != nil {
			t.Errorf("SanitizeInputText(%q) error = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("SanitizeInputText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every rejected string names its offending characters: each distinct code
// point once, in first-occurrence order, joined
// with a space and JSON.stringify-quoted.
func TestSanitizeRejects(t *testing.T) {
	badMsg := func(bad string) string {
		return "--text contains characters that cannot be typed safely: " + JSONStringify(bad)
	}
	cases := []struct{ in, bad string }{
		{"a;b", ";"},
		{"a&b", "&"},
		{"a|b", "|"},
		{"$(x)", "$ ( )"},
		{"a`b`", "`"},
		{`a"b`, `"`},
		{"a'b", "'"},
		{`a\b`, `\`},
		{"a<b", "<"},
		{"rm -rf /tmp/x*", "*"},
		{"a{b}", "{ }"},
		{"café", "é"},
		{"a;b&c;d", "; &"},   // first occurrence order, duplicates collapsed
		{`x"y'z;w`, `" ' ;`}, // order follows the string, not the charset
		{"a\nb", "\n"},
		{"a\tb", "\t"},
	}
	for _, c := range cases {
		_, err := SanitizeInputText(c.in)
		if err == nil {
			t.Errorf("SanitizeInputText(%q) accepted, want rejection", c.in)
			continue
		}
		if want := badMsg(c.bad); err.Error() != want {
			t.Errorf("SanitizeInputText(%q) error = %q, want %q", c.in, err.Error(), want)
		}
		var dje *DroidJevError
		if !errors.As(err, &dje) || dje.ExitCode != 3 {
			t.Errorf("SanitizeInputText(%q) error = %#v, want a *DroidJevError with exit 3", c.in, err)
		}
	}

	for _, empty := range []string{"", "   ", "\t\n"} {
		if _, err := SanitizeInputText(empty); err == nil || err.Error() != "--text is empty" {
			t.Errorf("SanitizeInputText(%q) error = %v, want %q", empty, err, "--text is empty")
		}
	}

	// The 500 ceiling counts UTF-16 code units (JS .length), so a 251-emoji
	// string is 502 units and fails the length check, not the charset one.
	if _, err := SanitizeInputText(strings.Repeat("a", 500)); err != nil {
		t.Errorf("SanitizeInputText(500 a's) error = %v, want accepted", err)
	}
	long := "--text longer than 500 chars"
	if _, err := SanitizeInputText(strings.Repeat("a", 501)); err == nil || err.Error() != long {
		t.Errorf("SanitizeInputText(501 a's) error = %v, want %q", err, long)
	}
	if _, err := SanitizeInputText(strings.Repeat("😀", 251)); err == nil || err.Error() != long {
		t.Errorf("SanitizeInputText(251 emoji) error = %v, want %q (UTF-16 units)", err, long)
	}
	if _, err := SanitizeInputText(strings.Repeat("😀", 250)); err == nil || err.Error() != badMsg("😀") {
		t.Errorf("SanitizeInputText(250 emoji) error = %v, want %q", err, badMsg("😀"))
	}
}

// Spaces map to %s, the escape `input text` supports; nothing else is escaped.
func TestSanitizeSpaceEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a b", "a%sb"},
		{"a b c", "a%sb%sc"},
	}
	for _, c := range cases {
		got, err := SanitizeInputText(c.in)
		if err != nil || got != c.want {
			t.Errorf("SanitizeInputText(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := SanitizeInputText(" "); err == nil || err.Error() != "--text is empty" {
		t.Errorf("SanitizeInputText(\" \") error = %v, want %q", err, "--text is empty")
	}
}

// The track midpoint is x1, the
// target is l + fraction*(r-l), and the extremes overshoot the track by 40px
// so the value clamps to min/max. Duration is always 400ms.
func TestSlideGesture(t *testing.T) {
	el := &Element{Bounds: &Bounds{L: 100, T: 500, R: 700, B: 560}}
	cases := []struct {
		fraction float64
		want     Swipe
	}{
		{0, Swipe{X1: 400, Y1: 530, X2: 60, Y2: 530, DurationMs: 400}},
		{0.25, Swipe{X1: 400, Y1: 530, X2: 250, Y2: 530, DurationMs: 400}},
		{0.5, Swipe{X1: 400, Y1: 530, X2: 400, Y2: 530, DurationMs: 400}},
		{1, Swipe{X1: 400, Y1: 530, X2: 740, Y2: 530, DurationMs: 400}},
		{-1, Swipe{X1: 400, Y1: 530, X2: 60, Y2: 530, DurationMs: 400}},
		{2, Swipe{X1: 400, Y1: 530, X2: 740, Y2: 530, DurationMs: 400}},
	}
	for _, c := range cases {
		if got := SlideGesture(el, c.fraction); got != c.want {
			t.Errorf("SlideGesture(fraction %v) = %#v, want %#v", c.fraction, got, c.want)
		}
	}

	// Odd sums round halves up, as Math.round does.
	odd := &Element{Bounds: &Bounds{L: 101, T: 501, R: 700, B: 560}}
	oddCases := []struct {
		fraction float64
		want     Swipe
	}{
		{0.5, Swipe{X1: 401, Y1: 531, X2: 401, Y2: 531, DurationMs: 400}},
		{0.25, Swipe{X1: 401, Y1: 531, X2: 251, Y2: 531, DurationMs: 400}},
		{0.1, Swipe{X1: 401, Y1: 531, X2: 161, Y2: 531, DurationMs: 400}},
	}
	for _, c := range oddCases {
		if got := SlideGesture(odd, c.fraction); got != c.want {
			t.Errorf("SlideGesture(odd, fraction %v) = %#v, want %#v", c.fraction, got, c.want)
		}
	}

	if got := SlideGesture(nil, 0.5); got != (Swipe{}) {
		t.Errorf("SlideGesture(nil) = %#v, want the zero Swipe", got)
	}
	if got := SlideGesture(&Element{}, 0.5); got != (Swipe{}) {
		t.Errorf("SlideGesture(no bounds) = %#v, want the zero Swipe", got)
	}
}

// Digits ride ONE adb invocation: the AWAKE prefix, then `input keyevent
// <7+d>` for each digit joined by `;` (KEYCODE_0..9 are 7..16).
func TestDigitsArgs(t *testing.T) {
	want := []string{
		"input", "keyevent", "224", ";",
		"input", "keyevent", "8", ";",
		"input", "keyevent", "13", ";",
		"input", "keyevent", "10", ";",
		"input", "keyevent", "12",
	}
	if got := DigitsArgs("1635"); !reflect.DeepEqual(got, want) {
		t.Errorf("DigitsArgs(\"1635\") = %q, want %q", got, want)
	}
	if got, want := DigitsArgs("0"), []string{"input", "keyevent", "224", ";", "input", "keyevent", "7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DigitsArgs(\"0\") = %q, want %q", got, want)
	}
	if got, want := DigitsArgs(""), []string{"input", "keyevent", "224", ";"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DigitsArgs(\"\") = %q, want %q", got, want)
	}
}

// Scroll gestures are symmetric around the anchor and clamped inside the
// screen with a 40px margin: `input swipe` silently no-ops on negative
// coordinates. Default duration is 300ms, an explicit one is kept.
func TestScrollSwipeClamp(t *testing.T) {
	read := recordAdb(t)
	serial := "emulator-5554"
	gestures := []error{
		ScrollSwipe(serial, Point{X: 500, Y: 100}, 300, 200, "down", 0),     // y1 clamped to H-40, y2 to 40
		ScrollSwipe(serial, Point{X: 500, Y: 2000}, 600, 2340, "down", 250), // in range, explicit duration
		ScrollSwipe(serial, Point{X: 500, Y: 2000}, 600, 2340, "up", 0),     // up: finger travels the other way
		ScrollSwipe(serial, Point{X: 500, Y: 1000}, 601, 2340, "", 0),       // empty direction means down
		ScrollSwipeX(serial, Point{X: 60, Y: 1000}, 300, 1080, "right", 0),  // x1 clamped to W-40, x2 to 40
		ScrollSwipeX(serial, Point{X: 60, Y: 1000}, 300, 1080, "left", 0),
	}
	for i, err := range gestures {
		if err != nil {
			t.Fatalf("scroll call %d error = %v", i, err)
		}
	}
	assertCalls(t, read(), []string{
		"-s emulator-5554 shell input keyevent 224 ; input swipe 500 160 500 40 300",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 500 2300 500 1700 250",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 500 1700 500 2300 300",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 500 1301 500 699 300",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 210 1000 40 1000 300",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 40 1000 210 1000 300",
	})
}

// Tap, long-press and slide each ride one adb invocation behind the AWAKE
// prefix, with the coordinates computed in code.
func TestTapLongPressSlideArgv(t *testing.T) {
	read := recordAdb(t)
	serial := "emulator-5554"
	if err := Tap(serial, Point{X: 5, Y: 6}); err != nil {
		t.Fatal(err)
	}
	if err := LongPress(serial, Point{X: 10, Y: 20}, 0); err != nil {
		t.Fatal(err) // 0 means the 800ms default
	}
	if err := LongPress(serial, Point{X: 10, Y: 20}, 1200); err != nil {
		t.Fatal(err)
	}
	if err := SlideTo(serial, &Element{Bounds: &Bounds{L: 100, T: 500, R: 700, B: 560}}, 1); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, read(), []string{
		"-s emulator-5554 shell input keyevent 224 ; input tap 5 6",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 10 20 10 20 800",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 10 20 10 20 1200",
		"-s emulator-5554 shell input keyevent 224 ; input swipe 400 530 740 530 400",
	})
}

// keyevent is the one call with no AWAKE prefix (back/home/paste input is
// forwarded as-is, as the original did).
func TestKeyEventArgv(t *testing.T) {
	read := recordAdb(t)
	serial := "emulator-5554"
	for _, code := range []int{KeyBack, KeyHome, KeyEnter, KeyWake, KeyPaste} {
		if err := KeyEvent(serial, code); err != nil {
			t.Fatal(err)
		}
	}
	assertCalls(t, read(), []string{
		"-s emulator-5554 shell input keyevent 4",
		"-s emulator-5554 shell input keyevent 3",
		"-s emulator-5554 shell input keyevent 66",
		"-s emulator-5554 shell input keyevent 224",
		"-s emulator-5554 shell input keyevent 279",
	})
}

// typeText has "set" semantics: select-all + delete first (so a retry into the
// same field is idempotent), then the sanitized text.
func TestTypeTextSetSemantics(t *testing.T) {
	read := recordAdb(t)
	if err := TypeText("emulator-5554", "hello world"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, read(), []string{
		"-s emulator-5554 shell input keycombination 113 29",
		"-s emulator-5554 shell input keyevent 67",
		"-s emulator-5554 shell input text hello%sworld",
	})
}

// Older devices without `input keycombination` fall back to appending: the
// failed combo skips the delete too, and the text still goes in.
func TestTypeTextWithoutKeycombination(t *testing.T) {
	log := filepath.Join(t.TempDir(), "adb.log")
	fakeAdb(t, `printf '%s\n' "$*" >> '`+log+`'
case "$*" in *keycombination*) exit 1 ;; esac`)
	if err := TypeText("emulator-5554", "abc"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	assertCalls(t, strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), []string{
		"-s emulator-5554 shell input keycombination 113 29",
		"-s emulator-5554 shell input text abc",
	})
}

// The sanitizer runs before the first adb call: an unsafe string never
// reaches the device shell.
func TestTypeTextRejectsBeforeAdb(t *testing.T) {
	read := recordAdb(t)
	err := TypeText("emulator-5554", "a;b")
	if err == nil || err.Error() != `--text contains characters that cannot be typed safely: ";"` {
		t.Fatalf("TypeText(a;b) error = %v", err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("TypeText made adb calls for rejected text: %q", got)
	}
}

// typeDigits sends DigitsArgs in one invocation.
func TestTypeDigitsOneInvocation(t *testing.T) {
	read := recordAdb(t)
	if err := TypeDigits("emulator-5554", "16"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, read(), []string{
		"-s emulator-5554 shell input keyevent 224 ; input keyevent 8 ; input keyevent 13",
	})
}
