// Ensure a booted emulator exists: resolve --device (serial or AVD name),
// auto-boot the first AVD when nothing is online. The `android` CLI is fine
// here (cold path); the per-step hot loop stays adb-only.
//
// Ported from the Node implementation; every notice string, wait, timeout, and
// error message is kept word for word.

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	androidCLI      = "android"
	bootTimeoutMs   = 240_000
	waitBootMs      = 90_000
	bootSerialMs    = 60_000
	pendingWaitMs   = 120_000
	listAvdsTimeout = 20_000
)

var (
	// An AVD with `hw.gpu.enabled = no` renders on the CPU: a visible emulator
	// then burns a whole core redrawing every frame and starves everything else
	// on the host. The `android` CLI takes no -gpu passthrough, so the AVD's own
	// config.ini is the only lever.
	// JS `.` excludes \n, \r, U+2028 and U+2029; spell that out so a CRLF
	// config keeps its \r, exactly like the Node replace.
	gpuEnabledRe = regexp.MustCompile(`(?m)^hw\.gpu\.enabled[` + JSWhitespaceClass + `]*=[^\n\r\x{2028}\x{2029}]*`)
	gpuModeRe    = regexp.MustCompile(`(?m)^hw\.gpu\.mode[` + JSWhitespaceClass + `]*=[^\n\r\x{2028}\x{2029}]*`)

	wakefulnessRe = regexp.MustCompile(`mWakefulness=Awake`)
)

// animScales are the window/transition/animator scales disabled for the
// duration of fn — the standard UI-automation speedup (Espresso asks for the
// same).
var animScales = []string{"window_animation_scale", "transition_animation_scale", "animator_duration_scale"}

// EnsureResult is the resolved device. Booted is true when this call started
// the emulator; Notices carries the human-readable progress lines.
type EnsureResult struct {
	Serial  string
	Booted  bool
	Notices []string
}

// PinHostGpuIni forces hw.gpu.enabled = yes and hw.gpu.mode = host in an AVD's
// config.ini text, replacing existing lines or appending the keys. Pure
// text-in/text-out so it stays testable.
func PinHostGpuIni(text string) string {
	out := text
	if gpuEnabledRe.MatchString(out) {
		out = replaceFirstRe(gpuEnabledRe, out, "hw.gpu.enabled = yes")
	} else {
		out = jsTrimEnd(out) + "\nhw.gpu.enabled = yes\n"
	}
	if gpuModeRe.MatchString(out) {
		out = replaceFirstRe(gpuModeRe, out, "hw.gpu.mode = host")
	} else {
		out = jsTrimEnd(out) + "\nhw.gpu.mode = host\n"
	}
	return out
}

// replaceFirstRe is JS `s.replace(re, repl)` with a non-global regex: only the
// first match is replaced.
func replaceFirstRe(re *regexp.Regexp, s, repl string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + repl + s[loc[1]:]
}

// jsTrimEnd is `s.trimEnd()`.
func jsTrimEnd(s string) string { return strings.TrimRight(s, jsSpaceCutset) }

// ListAvds lists the AVDs the `android` CLI knows, skipping blank lines and
// usage text.
func ListAvds() ([]string, error) {
	r, err := Run(androidCLI, []string{"emulator", "list"}, RunOpts{TimeoutMs: listAvdsTimeout})
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		out := string(r.Stderr)
		if out == "" {
			out = string(r.Stdout)
		}
		return nil, NewDroidJevError("android emulator list failed: " + UTF16Truncate(Redact(JSTrim(out)), 300))
	}
	avds := []string{}
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		l := JSTrim(line)
		if l == "" || strings.HasPrefix(l, "Usage") {
			continue
		}
		avds = append(avds, l)
	}
	return avds, nil
}

// pinHostGpu patches the AVD's config.ini when it is not already host-pinned.
// An unreadable config is left alone: the boot itself will surface any problem.
func pinHostGpu(avd string, notices *[]string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	ini := filepath.Join(home, ".android", "avd", avd+".avd", "config.ini")
	text, err := os.ReadFile(ini)
	if err != nil {
		return nil
	}
	pinned := PinHostGpuIni(string(text))
	if pinned == string(text) {
		return nil
	}
	if err := os.WriteFile(ini, []byte(pinned), 0o644); err != nil {
		return err
	}
	addNotice(notices, avd+": pinned hw.gpu.mode=host in config.ini (software rendering starves the host CPU)")
	return nil
}

func waitBoot(serial string, timeoutMs int64) (bool, error) {
	deadline := time.Now().UnixMilli() + timeoutMs
	for time.Now().UnixMilli() < deadline {
		done, err := BootCompleted(serial)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
		Sleep(1500)
	}
	return false, nil
}

// WakeAndUnlock wakes the display and dismisses a simple keyguard. An already
// awake screen gets no keys and no settle delay.
//
// ponytail: wake + MENU key only dismisses a non-secure keyguard; a locked
// emulator with a PIN stays blocked. Upgrade path: check `dumpsys window`
// keyguard state and report blocked_locked explicitly.
func WakeAndUnlock(serial string) error {
	pre, err := Shell(serial, []string{"dumpsys", "power"}, RunOpts{TimeoutMs: 8000})
	if err != nil {
		return err
	}
	if wakefulnessRe.MatchString(string(pre.Stdout)) {
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := Shell(serial, []string{"input", "keyevent", "224"}, RunOpts{}); err != nil { // KEYCODE_WAKE_UP
			return err
		}
		Sleep(300)
		r, err := Shell(serial, []string{"dumpsys", "power"}, RunOpts{TimeoutMs: 8000})
		if err != nil {
			return err
		}
		if wakefulnessRe.MatchString(string(r.Stdout)) {
			break
		}
	}
	if _, err := Shell(serial, []string{"input", "keyevent", "82"}, RunOpts{}); err != nil { // MENU dismisses simple keyguard
		return err
	}
	Sleep(250)
	return nil
}

// SIGINTExitCode mirrors the Node `process.exitCode = 130` side effect of the
// first Ctrl-C during WithAnimationsOff: the restore still runs and fn's error
// is returned; the CLI reads this afterwards to exit 130. Set to 0 the rest of
// the time.
var SIGINTExitCode int

// WithAnimationsOff disables window/transition/animator scales for the duration
// of fn and restores them after — transitions complete in ~1 frame instead of
// ~300ms, so the dump after an action sees the settled screen sooner. A `null`
// (never set) value is restored by deleting the key; the framework default is
// 1.0.
//
// Ctrl-C SIGINTs our adb children too, so fn returns an error and the restore
// below runs. The first SIGINT is swallowed (recording 130 in SIGINTExitCode)
// to let that cleanup finish; a second exits at once with 130.
//
// ponytail: no restore if the process is SIGKILLed — an emulator left with
// animations off is cosmetic; `settings put global *_scale 1` fixes it.
func WithAnimationsOff(serial string, fn func() error) (err error) {
	get := func(k string) (string, error) {
		r, err := Shell(serial, []string{"settings", "get", "global", k}, RunOpts{TimeoutMs: 5000})
		if err != nil {
			return "", err
		}
		return JSTrim(string(r.Stdout)), nil
	}
	put := func(k, v string) error {
		_, err := Shell(serial, []string{"settings", "put", "global", k, v}, RunOpts{TimeoutMs: 5000})
		return err
	}
	del := func(k string) error {
		_, err := Shell(serial, []string{"settings", "delete", "global", k}, RunOpts{TimeoutMs: 5000})
		return err
	}
	orig := map[string]string{}
	restore := func() {
		for _, k := range animScales {
			v, ok := orig[k]
			if !ok {
				continue
			}
			if v == "null" || v == "" {
				_ = del(k) // best effort
			} else if v != "0" {
				_ = put(k, v) // best effort
			}
		}
	}

	// Best effort — running with animations on is only slower, not wrong. A
	// failed read skips the disable loop entirely, as the Node catch does.
	ok := true
	for _, k := range animScales {
		v, err := get(k)
		if err != nil {
			ok = false
			break
		}
		orig[k] = v
	}
	if ok {
		for _, k := range animScales {
			if orig[k] != "0" {
				if err := put(k, "0"); err != nil {
					break
				}
			}
		}
	}

	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		sigints := 0
		for {
			select {
			case <-ch:
				sigints++
				if sigints > 1 {
					os.Exit(130)
				}
				SIGINTExitCode = 130
			case <-done:
				return
			}
		}
	}()
	defer func() {
		signal.Stop(ch)
		close(done)
		<-exited
		restore()
	}()
	return fn()
}

// BootAvd binds to an already-starting emulator for avd, or starts one and
// waits for it to come online. Notices go to the caller's accumulator; the
// returned EnsureResult carries Serial and Booted only.
func BootAvd(avd string, notices *[]string) (EnsureResult, error) {
	// Already starting under this AVD? Attach to it instead of double-booting.
	devs, err := AdbDevices()
	if err != nil {
		return EnsureResult{}, err
	}
	for _, d := range devs {
		if d.State != "device" && d.State != "offline" {
			continue
		}
		if !IsEmulatorSerial(d.Serial) {
			continue
		}
		match, err := avdMatches(d.Serial, avd)
		if err != nil {
			return EnsureResult{}, err
		}
		if !match {
			continue
		}
		addNotice(notices, fmt.Sprintf("emulator for %s already starting (%s); waiting for boot", avd, d.Serial))
		ok, err := waitBoot(d.Serial, waitBootMs)
		if err != nil {
			return EnsureResult{}, err
		}
		if !ok {
			return EnsureResult{}, NewDroidJevError(fmt.Sprintf("emulator %s (%s) did not finish booting", avd, d.Serial))
		}
		if err := WakeAndUnlock(d.Serial); err != nil {
			return EnsureResult{}, err
		}
		return EnsureResult{Serial: d.Serial}, nil
	}

	addNotice(notices, fmt.Sprintf("booting AVD %s (android emulator start; this can take ~30-60s)", avd))
	if err := pinHostGpu(avd, notices); err != nil {
		return EnsureResult{}, err
	}
	pre, err := AdbDevices()
	if err != nil {
		return EnsureResult{}, err
	}
	preSerials := make(map[string]struct{}, len(pre))
	for _, d := range pre {
		preSerials[d.Serial] = struct{}{}
	}
	r, err := Run(androidCLI, []string{"emulator", "start", avd}, RunOpts{TimeoutMs: bootTimeoutMs})
	if err != nil {
		return EnsureResult{}, err
	}
	if r.Code != 0 {
		out := string(r.Stderr)
		if out == "" {
			out = string(r.Stdout)
		}
		return EnsureResult{}, NewDroidJevError(fmt.Sprintf(
			"android emulator start %s failed: %s", avd, UTF16Truncate(Redact(JSTrim(out)), 400),
		))
	}
	// `android emulator start` returns when fully booted, but the new serial may
	// take a moment to appear in `adb devices`. Only bind to an emulator that
	// was NOT online before we started — otherwise we'd tap a different AVD.
	serial := ""
	deadline := time.Now().UnixMilli() + bootSerialMs
	for time.Now().UnixMilli() < deadline && serial == "" {
		now, err := AdbDevices()
		if err != nil {
			return EnsureResult{}, err
		}
		for _, d := range now {
			if d.State != "device" || !IsEmulatorSerial(d.Serial) {
				continue
			}
			if _, existed := preSerials[d.Serial]; existed {
				continue
			}
			name, err := AvdName(d.Serial)
			if err != nil {
				return EnsureResult{}, err
			}
			if name == "" || strings.EqualFold(name, avd) {
				serial = d.Serial // exact match, or the only new emulator in town
				break
			}
		}
		if serial == "" {
			Sleep(1500)
		}
	}
	if serial == "" {
		return EnsureResult{}, NewDroidJevError(fmt.Sprintf(
			"emulator %s started but its serial never appeared in adb devices (new emulators seen: none)", avd,
		))
	}
	ok, err := waitBoot(serial, waitBootMs)
	if err != nil {
		return EnsureResult{}, err
	}
	if !ok {
		return EnsureResult{}, NewDroidJevError(fmt.Sprintf(
			"emulator %s (%s) did not reach sys.boot_completed=1", avd, serial,
		))
	}
	if err := WakeAndUnlock(serial); err != nil {
		return EnsureResult{}, err
	}
	return EnsureResult{Serial: serial, Booted: true}, nil
}

// EnsureDevice resolves which emulator to use: an online serial, an online AVD
// name, or (with no device named) a single online emulator, otherwise the first
// AVD.
func EnsureDevice(device string) (EnsureResult, error) {
	notices := []string{}
	devs, err := AdbDevices()
	if err != nil {
		return EnsureResult{}, err
	}
	online := []Device{}
	for _, d := range devs {
		if d.State == "device" {
			online = append(online, d)
		}
	}

	if device != "" {
		for _, d := range online {
			if d.Serial == device {
				if err := WakeAndUnlock(d.Serial); err != nil { // online but possibly dozing — dumps need the screen on
					return EnsureResult{}, err
				}
				return EnsureResult{Serial: d.Serial, Notices: notices}, nil
			}
		}
		for _, d := range online {
			if !IsEmulatorSerial(d.Serial) {
				continue
			}
			match, err := avdMatches(d.Serial, device)
			if err != nil {
				return EnsureResult{}, err
			}
			if match {
				if err := WakeAndUnlock(d.Serial); err != nil {
					return EnsureResult{}, err
				}
				return EnsureResult{Serial: d.Serial, Notices: notices}, nil
			}
		}
		avds, err := ListAvds()
		if err != nil {
			return EnsureResult{}, err
		}
		match := ""
		for _, a := range avds {
			if strings.EqualFold(a, device) {
				match = a
				break
			}
		}
		if match == "" {
			serials := make([]string, len(online))
			for i, d := range online {
				serials[i] = d.Serial
			}
			return EnsureResult{}, NewDroidJevError(
				`"` + device + `" is neither an online device serial nor an AVD name.` + "\n" +
					"online: " + orNone(strings.Join(serials, ", ")) + "\n" +
					"avds:   " + orNone(strings.Join(avds, ", ")),
			)
		}
		res, err := BootAvd(match, &notices)
		if err != nil {
			return EnsureResult{}, err
		}
		res.Notices = notices
		return res, nil
	}

	// A device that is present but not ready (offline/booting) is waited for,
	// never raced with a second emulator boot.
	pending := []Device{}
	for _, d := range devs {
		if d.State != "device" {
			pending = append(pending, d)
		}
	}
	if len(pending) > 0 {
		serial := pending[0].Serial
		addNotice(&notices, fmt.Sprintf(`%s present as "%s" — waiting for it to come online`, serial, pending[0].State))
		deadline := time.Now().UnixMilli() + pendingWaitMs
		for time.Now().UnixMilli() < deadline {
			now, err := AdbDevices()
			if err != nil {
				return EnsureResult{}, err
			}
			var me *Device
			for i := range now {
				if now[i].Serial == serial {
					me = &now[i]
					break
				}
			}
			if me == nil {
				break // vanished; fall through to the boot path
			}
			if me.State == "unauthorized" {
				return EnsureResult{}, NewDroidJevError(fmt.Sprintf(
					"%s is unauthorized — accept the USB debugging dialog on the device, or use --device to pick another", serial,
				))
			}
			if me.State == "device" {
				done, err := BootCompleted(serial)
				if err != nil {
					return EnsureResult{}, err
				}
				if done {
					if err := WakeAndUnlock(serial); err != nil {
						return EnsureResult{}, err
					}
					return EnsureResult{Serial: serial, Notices: notices}, nil
				}
			}
			Sleep(2000)
		}
	}

	// Default target is emulators only; a physical phone requires explicit --device.
	emulators := []Device{}
	for _, d := range online {
		if IsEmulatorSerial(d.Serial) {
			emulators = append(emulators, d)
		}
	}
	if len(emulators) == 1 {
		if err := WakeAndUnlock(emulators[0].Serial); err != nil { // online but possibly dozing
			return EnsureResult{}, err
		}
		return EnsureResult{Serial: emulators[0].Serial, Notices: notices}, nil
	}
	if len(emulators) > 1 {
		lines := make([]string, len(emulators))
		for i, d := range emulators {
			name, err := AvdName(d.Serial)
			if err != nil {
				return EnsureResult{}, err
			}
			if name == "" {
				name = "null" // `${null}` in the Node template literal
			}
			lines[i] = "  --device " + d.Serial + "   (avd " + name + ")"
		}
		return EnsureResult{}, NewDroidJevError("multiple emulators online; pick one:\n" + strings.Join(lines, "\n"))
	}
	if len(online) > 0 {
		serials := make([]string, len(online))
		for i, d := range online {
			serials[i] = d.Serial
		}
		return EnsureResult{}, NewDroidJevError(fmt.Sprintf(
			"only physical devices are online (%s); droidjev drives emulators by default — pass --device <serial> to target one explicitly",
			strings.Join(serials, ", "),
		))
	}

	avds, err := ListAvds()
	if err != nil {
		return EnsureResult{}, err
	}
	if len(avds) == 0 {
		return EnsureResult{}, NewDroidJevError("no devices online and no AVDs found (`android emulator list`)")
	}
	addNotice(&notices, fmt.Sprintf(`no device online; using first AVD "%s"`, avds[0]))
	res, err := BootAvd(avds[0], &notices)
	if err != nil {
		return EnsureResult{}, err
	}
	res.Notices = notices
	return res, nil
}

func avdMatches(serial, avd string) (bool, error) {
	name, err := AvdName(serial)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(name, avd), nil
}

func addNotice(notices *[]string, msg string) {
	if notices != nil {
		*notices = append(*notices, msg)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
