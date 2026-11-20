// Executor: turns a validated decision into adb input. All taps come from
// coordinates computed in code — from our element table, or (the vision
// fallback) from a strictly validated box of 0-1 fractions. The model never
// produces raw coordinates, selectors, or commands.
//
// Wire shapes, gesture math, messages, and the typed text charset are kept
// exact.

package main

import (
	"math"
	"strconv"
	"strings"
)

// paste (279) rides the focused field, so
// callers must focus one first (a tap).
const (
	KeyBack  = 4
	KeyHome  = 3
	KeyEnter = 66
	KeyWake  = 224
	KeyPaste = 279
)

// Swipe is one `input swipe` gesture. The JSON tags match the original's
// slideGesture object, which decide's drags replay verbatim.
type Swipe struct {
	X1         int `json:"x1"`
	Y1         int `json:"y1"`
	X2         int `json:"x2"`
	Y2         int `json:"y2"`
	DurationMs int `json:"durationMs"`
}

// awakeArgs prefixes a gesture with the wake keyevent. Emulators
// intermittently report "Awake" while ignoring input (doze/AOD edge states);
// WAKE_UP before each gesture is idempotent. It rides in the same adb
// invocation as the gesture — the `;` separates commands in the device shell,
// and every other token is code-generated (integers), so the model can never
// reach this string.
func awakeArgs(rest ...string) []string {
	args := make([]string, 0, 4+len(rest))
	args = append(args, "input", "keyevent", strconv.Itoa(KeyWake), ";")
	return append(args, rest...)
}

// Tap taps a code-computed point.
func Tap(serial string, c Point) error {
	_, err := ShellOk(serial, awakeArgs("input", "tap", strconv.Itoa(c.X), strconv.Itoa(c.Y)), RunOpts{TimeoutMs: 8000})
	return err
}

// swipe is one gesture, clamped inside the screen by its callers: `input
// swipe` silently no-ops on negative coordinates.
func swipe(serial string, s Swipe) error {
	_, err := ShellOk(serial, awakeArgs(
		"input", "swipe",
		strconv.Itoa(s.X1), strconv.Itoa(s.Y1), strconv.Itoa(s.X2), strconv.Itoa(s.Y2), strconv.Itoa(s.DurationMs),
	), RunOpts{TimeoutMs: 8000})
	return err
}

// swipeSpan clamps a symmetric span [anchor-half, anchor+half] into the
// screen with a 40px margin. forward puts the larger coordinate in from (a
// downward or rightward finger); otherwise the finger travels the other way.
func swipeSpan(anchor, delta, size int, forward bool) (from, to int) {
	const margin = 40
	if delta < 0 {
		delta = -delta
	}
	half := (delta + 1) / 2 // Math.round(|delta|/2), halves up
	if forward {
		return min(anchor+half, size-margin), max(anchor-half, margin)
	}
	return max(anchor-half, margin), min(anchor+half, size-margin)
}

// ScrollSwipe scrolls a list vertically around the anchor. durationMs <= 0
// means the 300ms default; direction is "down" (also the empty default) or
// "up".
func ScrollSwipe(serial string, anchor Point, dy, screenH int, direction string, durationMs int) error {
	if durationMs <= 0 {
		durationMs = 300
	}
	from, to := swipeSpan(anchor.Y, dy, screenH, direction == "down" || direction == "")
	return swipe(serial, Swipe{X1: anchor.X, Y1: from, X2: anchor.X, Y2: to, DurationMs: durationMs})
}

// ScrollSwipeX is the same on the other axis: the finger travels toward the
// content the goal is looking for, so "right" reveals what is off to the
// right. direction is "right" (also the empty default) or "left".
func ScrollSwipeX(serial string, anchor Point, dx, screenW int, direction string, durationMs int) error {
	if durationMs <= 0 {
		durationMs = 300
	}
	from, to := swipeSpan(anchor.X, dx, screenW, direction == "right" || direction == "")
	return swipe(serial, Swipe{X1: from, Y1: anchor.Y, X2: to, Y2: anchor.Y, DurationMs: durationMs})
}

// SlideGesture is a slider's access gesture. A slider's value follows the
// touch, so a tap lands where the finger did — and near the ends it lands
// short of them (a tap 3px from the right edge of the brightness slider left
// 251/255). Press the track to grab the thumb (Android's SeekBar jumps it to
// the press point), drag on to the target, and for the extremes overshoot off
// the track so the value clamps to min/max. The numbers are returned on their
// own so a host that executes the decision itself sends the very same gesture.
func SlideGesture(el *Element, fraction float64) Swipe {
	if el == nil || el.Bounds == nil {
		return Swipe{} // no rectangle, no drag (the JS would throw on a nil destructure)
	}
	b := el.Bounds
	const margin = 40
	cy := (b.T + b.B + 1) / 2 // Math.round((t+b)/2); bounds are non-negative
	var x2 int
	switch {
	case fraction <= 0:
		x2 = b.L - margin
	case fraction >= 1:
		x2 = b.R + margin
	default:
		x2 = int(math.Round(float64(b.L) + fraction*float64(b.R-b.L)))
	}
	return Swipe{X1: (b.L + b.R + 1) / 2, Y1: cy, X2: x2, Y2: cy, DurationMs: 400}
}

// SlideTo executes SlideGesture on the device.
func SlideTo(serial string, el *Element, fraction float64) error {
	return swipe(serial, SlideGesture(el, fraction))
}

// LongPress presses and holds: identical endpoints with a long duration is the
// only press-and-hold `input` can send, and a context menu (rename, delete,
// move, share) is where most lists keep the operations that have no visible
// button. holdMs <= 0 means the 800ms default.
func LongPress(serial string, c Point, holdMs int) error {
	if holdMs <= 0 {
		holdMs = 800
	}
	return swipe(serial, Swipe{X1: c.X, Y1: c.Y, X2: c.X, Y2: c.Y, DurationMs: holdMs})
}

// KeyEvent sends one key event as-is: no WAKE_UP prefix, matching the
// original (back/home/paste are forwarded straight through).
func KeyEvent(serial string, code int) error {
	_, err := ShellOk(serial, []string{"input", "keyevent", strconv.Itoa(code)}, RunOpts{TimeoutMs: 8000})
	return err
}

// SanitizeInputText is the device-shell injection boundary. `input text`
// reaches the shell, so the string must be reduced to a charset that is inert
// there (no ; & | < > ( ) $ ` " ' \ * ? [ ] { } ~ !); space maps to %s (the
// only escape `input text` supports). Anything outside the safe set is
// rejected with the offending characters named, never mangled.
//
// ponytail: no unicode/emoji typing; upgrade path is a base64 insertion bridge.
func SanitizeInputText(text string) (string, error) {
	if JSTrim(text) == "" {
		return "", NewDroidJevError("--text is empty")
	}
	if UTF16Len(text) > 500 {
		return "", NewDroidJevError("--text longer than 500 chars")
	}
	bad := NewOrdSet()
	for _, r := range text {
		if !safeInputRune(r) {
			bad.Add(string(r))
		}
	}
	if bad.Len() > 0 {
		return "", NewDroidJevError(
			"--text contains characters that cannot be typed safely: " + JSONStringify(bad.Join(" ")),
		)
	}
	// %s from the user stays %s on device (becomes a space) — documented ceiling.
	return strings.ReplaceAll(text, " ", "%s"), nil
}

// safeInputRune is SAFE_TEXT_RE's class [A-Za-z0-9 .,:+=/@%^_-] applied one
// code point at a time, which is how the JS filter tests it.
func safeInputRune(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune(" .,:+=/@%^_-", r)
}

// TypeText types sanitized text with "set" semantics, not "append":
// select-all (CTRL+A) + delete first, so a repeated type — the model retrying
// into the same field — is idempotent instead of doubling the content
// ("bluetoothbluetooth"). Older devices without `input keycombination` fall
// back to appending, as before: the failed combo skips the delete too, and
// both errors are ignored.
func TypeText(serial, text string) error {
	safe, err := SanitizeInputText(text)
	if err != nil {
		return err
	}
	if _, err := ShellOk(serial, []string{"input", "keycombination", "113", "29"}, RunOpts{TimeoutMs: 5000}); err == nil {
		_, _ = ShellOk(serial, []string{"input", "keyevent", "67"}, RunOpts{TimeoutMs: 5000})
	}
	_, err = ShellOk(serial, []string{"input", "text", safe}, RunOpts{TimeoutMs: 10000})
	return err
}

// DigitsArgs is keypad digits in ONE adb invocation: every KEYCODE_0..9 token
// (7..16) is code-generated, the `;` join is the same contract as awakeArgs,
// and one shell call per digit is pure overhead. Non-digits are skipped — the
// sequence comes from keypadSequence, which yields digits only.
func DigitsArgs(seq string) []string {
	args := awakeArgs()
	first := true
	for _, r := range seq {
		if r < '0' || r > '9' {
			continue
		}
		if !first {
			args = append(args, ";")
		}
		first = false
		args = append(args, "input", "keyevent", strconv.Itoa(7+int(r-'0')))
	}
	return args
}

// TypeDigits sends DigitsArgs in one adb invocation.
func TypeDigits(serial, seq string) error {
	_, err := ShellOk(serial, DigitsArgs(seq), RunOpts{TimeoutMs: 15000})
	return err
}
