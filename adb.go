// Thin adb wrapper. Everything here is argument-array spawns through Run, never
// a shell. The hot loop must never shell out to the `android` CLI (~670ms
// startup tax); adb is ~14ms per call.
//
// Ported from src/adb.js; the call timeouts and the regexes (including JS `\s`,
// spelled with JSWhitespaceClass) are kept exact.

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const adb = "adb"

// Device is one `adb devices -l` row. Model is "" when the listing had no
// `model:` attribute (the Node code's null); --json callers render "" as null.
type Device struct {
	Serial string
	State  string
	Model  string
}

// Screen is a display resolution in px. App is the focused window's package
// when the caller knows it (snapshot merges CurrentApp into the screen); the
// table builder uses it to tell an IME's foreign keys from an in-app dial pad.
type Screen struct {
	W, H int
	App  string
}

// FocusedApp is the package/activity component of the focused window.
type FocusedApp struct {
	Pkg      string
	Activity string
}

// `adb devices -l` rows. JS whitespace is assumed (JSWhitespaceClass), and the
// `.*device` alternative keeps the JS alternation order.
var devicesListRe = regexp.MustCompile(
	`^([^` + JSWhitespaceClass + `]+)[` + JSWhitespaceClass + `]+` +
		`(device|offline|unauthorized|booting|recovery|no permissions.*|.*device)` +
		`[` + JSWhitespaceClass + `]+(.*)$`,
)

var deviceModelRe = regexp.MustCompile(`model:([^ ]+)`)

var (
	wmOverrideRe = regexp.MustCompile(`Override size:[` + JSWhitespaceClass + `]*([0-9]+)x([0-9]+)`)
	wmPhysicalRe = regexp.MustCompile(`Physical size:[` + JSWhitespaceClass + `]*([0-9]+)x([0-9]+)`)
)

var (
	currentFocusRe = regexp.MustCompile(
		`mCurrentFocus[` + JSWhitespaceClass + `]*=[` + JSWhitespaceClass + `]*(.+)`,
	)
	focusComponentRe = regexp.MustCompile(`([a-zA-Z][\w.]*)/([a-zA-Z][\w.$]*)`)
)

// AdbDevices parses `adb devices -l` into the attached devices.
func AdbDevices() ([]Device, error) {
	r, err := Run(adb, []string{"devices", "-l"}, RunOpts{TimeoutMs: 10000})
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, NewDroidJevError("adb devices failed: " + UTF16Truncate(JSTrim(r.Stderr), 300))
	}
	return parseDevicesList(string(r.Stdout)), nil
}

// parseDevicesList skips the `List of devices attached` header and picks the
// serial, state, and `model:` out of each row. A header-only listing is an
// empty, non-nil slice: the CLI marshals it as [] like the Node code, not null.
func parseDevicesList(stdout string) []Device {
	devices := []Device{}
	for _, line := range strings.Split(stdout, "\n")[1:] {
		m := devicesListRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		model := ""
		if mm := deviceModelRe.FindStringSubmatch(m[3]); mm != nil {
			model = mm[1]
		}
		devices = append(devices, Device{Serial: m[1], State: m[2], Model: model})
	}
	return devices
}

// IsEmulatorSerial reports whether the serial names a local emulator.
func IsEmulatorSerial(s string) bool { return emulatorSerialRe.MatchString(s) }

var emulatorSerialRe = regexp.MustCompile(`^emulator-[0-9]+$`)

// AvdName is the AVD display name for a running emulator; "" for physical
// devices, failures, or the bare "OK" reply (the Node null; --json renders it
// as null).
func AvdName(serial string) (string, error) {
	r, err := Run(adb, []string{"-s", serial, "emu", "avd", "name"}, RunOpts{TimeoutMs: 6000})
	if err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", nil
	}
	name := JSTrim(strings.Split(string(r.Stdout), "\n")[0])
	if name == "" || name == "OK" {
		return "", nil
	}
	return name, nil
}

// Shell runs `adb -s SERIAL shell ...`. opts.TimeoutMs defaults to 15s.
func Shell(serial string, args []string, opts RunOpts) (RunResult, error) {
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = 15000
	}
	return Run(adb, append([]string{"-s", serial, "shell"}, args...), opts)
}

// ShellOk is Shell, turning a non-zero exit into a DroidJevError carrying the
// joined argv and the trimmed stderr prefix.
func ShellOk(serial string, args []string, opts RunOpts) (RunResult, error) {
	r, err := Shell(serial, args, opts)
	if err != nil {
		return r, err
	}
	if r.Code != 0 {
		return r, NewDroidJevError(fmt.Sprintf(
			"adb shell %s exited %d: %s",
			strings.Join(args, " "), r.Code, UTF16Truncate(JSTrim(r.Stderr), 300),
		))
	}
	return r, nil
}

// ExecOut runs `adb -s SERIAL exec-out ...`: binary-clean stdout (raw protocol,
// no PTY, so no CRLF translation). Device-side stderr merges into the same
// stream on failure; binary callers must validate the payload length, not
// parse it.
func ExecOut(serial string, args []string, opts RunOpts) (RunResult, error) {
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = 15000
	}
	opts.Binary = true
	return Run(adb, append([]string{"-s", serial, "exec-out"}, args...), opts)
}

// Getprop reads a system property (trimmed).
func Getprop(serial, prop string) (string, error) {
	r, err := Shell(serial, []string{"getprop", prop}, RunOpts{TimeoutMs: 8000})
	if err != nil {
		return "", err
	}
	return JSTrim(string(r.Stdout)), nil
}

// BootCompleted reports whether the boot has finished (sys.boot_completed=1).
func BootCompleted(serial string) (bool, error) {
	v, err := Getprop(serial, "sys.boot_completed")
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// ScreenSize is the screen resolution in px: Override (active display
// resolution) wins over Physical — dump coordinates are in override space when
// one is set. nil when neither is reported.
func ScreenSize(serial string) (*Screen, error) {
	r, err := Shell(serial, []string{"wm", "size"}, RunOpts{TimeoutMs: 8000})
	if err != nil {
		return nil, err
	}
	return parseWmSize(string(r.Stdout)), nil
}

func parseWmSize(stdout string) *Screen {
	m := wmOverrideRe.FindStringSubmatch(stdout)
	if m == nil {
		m = wmPhysicalRe.FindStringSubmatch(stdout)
	}
	if m == nil {
		return nil
	}
	w, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	h, err := strconv.Atoi(m[2])
	if err != nil {
		return nil
	}
	return &Screen{W: w, H: h}
}

// CurrentApp is the focused window, e.g.
// "com.android.settings/com.android.settings.Settings". Format:
// `mCurrentFocus=Window{88d6767 u0 pkg/activity}` (spaces around '=' are
// allowed). nil when no component is focused (the launcher, keyguard, or a
// notification shade).
func CurrentApp(serial string) (*FocusedApp, error) {
	r, err := Shell(serial, []string{"dumpsys", "window"}, RunOpts{TimeoutMs: 10000})
	if err != nil {
		return nil, err
	}
	return parseCurrentFocus(string(r.Stdout)), nil
}

func parseCurrentFocus(dump string) *FocusedApp {
	m := currentFocusRe.FindStringSubmatch(dump)
	if m == nil {
		return nil
	}
	comp := focusComponentRe.FindStringSubmatch(m[1])
	if comp == nil {
		return nil
	}
	return &FocusedApp{Pkg: comp[1], Activity: comp[2]}
}
