package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Ports the `adb devices -l` parse from src/adb.js (frozen): the header line is
// dropped, the state alternatives are tried in JS order, and `model:` is picked
// out of the trailing attribute list. A missing model is the empty string (the
// Node code's null).
func TestAdbDevicesParse(t *testing.T) {
	stdout := "List of devices attached\n" +
		"emulator-5554\tdevice product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:1\n" +
		"emulator-5556\toffline transport_id:2\n" +
		"0123456789ABCDEF\tunauthorized usb:3-1 transport_id:3\n" +
		"R58M42XXXX\tdevice usb:1-1 transport_id:4\n" +
		"192.168.1.2:5555\tdevice model:Pixel_7 transport_id:5\n" +
		"garbage line without state\n" +
		"\n"
	want := []Device{
		{Serial: "emulator-5554", State: "device", Model: "sdk_gphone64_arm64"},
		{Serial: "emulator-5556", State: "offline"},
		{Serial: "0123456789ABCDEF", State: "unauthorized"},
		{Serial: "R58M42XXXX", State: "device"},
		{Serial: "192.168.1.2:5555", State: "device", Model: "Pixel_7"},
	}
	if got := parseDevicesList(stdout); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDevicesList = %#v, want %#v", got, want)
	}

	// The Node function resolves [] for a header-only listing, and [] marshals
	// as `[]` where nil would marshal as `null` — keep it non-nil.
	empty := parseDevicesList("List of devices attached\n")
	if empty == nil || len(empty) != 0 {
		t.Fatalf("parseDevicesList(header only) = %#v, want a non-nil empty slice", empty)
	}
}

func TestIsEmulatorSerial(t *testing.T) {
	cases := []struct {
		serial string
		want   bool
	}{
		{"emulator-5554", true},
		{"emulator-0", true},
		{"emulator-", false},
		{"emulator-abc", false},
		{"emulator-5554x", false},
		{"emulator-5554 ", false},
		{"Emulator-5554", false},
		{"192.168.1.2:5555", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsEmulatorSerial(c.serial); got != c.want {
			t.Errorf("IsEmulatorSerial(%q) = %v, want %v", c.serial, got, c.want)
		}
	}
}

// parseWmSize mirrors src/adb.js's screenSize: Override (the active display
// resolution) beats Physical, and neither means no screen.
func TestScreenSizeParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *Screen
	}{
		{"physical only", "Physical size: 1080x2340\n", &Screen{W: 1080, H: 2340}},
		{"override beats physical", "Physical size: 1080x2340\nOverride size: 720x1560\n", &Screen{W: 720, H: 1560}},
		{"override first", "Override size: 720x1560\nPhysical size: 1080x2340\n", &Screen{W: 720, H: 1560}},
		{"no space after the colon", "Physical size:1080x2340\n", &Screen{W: 1080, H: 2340}},
		{"tab after the colon", "Override size:\t720x1560\n", &Screen{W: 720, H: 1560}},
		{"neither", "Could not get display size\n", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		got := parseWmSize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parseWmSize(%q) = %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

// parseCurrentFocus mirrors src/adb.js's currentApp: take the mCurrentFocus
// line, then the first package/activity component on it.
func TestCurrentAppParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *FocusedApp
	}{
		{
			"settings window",
			"  mCurrentFocus=Window{88d6767 u0 com.android.settings/com.android.settings.Settings}\n",
			&FocusedApp{Pkg: "com.android.settings", Activity: "com.android.settings.Settings"},
		},
		{
			"spaces around the equals sign",
			"mCurrentFocus = Window{1 u0 com.foo.bar/com.foo.bar.MainActivity}",
			&FocusedApp{Pkg: "com.foo.bar", Activity: "com.foo.bar.MainActivity"},
		},
		{
			"inner class activity",
			"mCurrentFocus=Window{1 u0 com.foo/com.foo.Outer$Inner}",
			&FocusedApp{Pkg: "com.foo", Activity: "com.foo.Outer$Inner"},
		},
		{
			"first focus line wins",
			"mCurrentFocus=Window{1 u0 com.a/com.a.A}\nmCurrentFocus=Window{2 u0 com.b/com.b.B}\n",
			&FocusedApp{Pkg: "com.a", Activity: "com.a.A"},
		},
		{"no focus line", "mFocusedApp=AppWindowToken{abc}\n", nil},
		{"null focus", "mCurrentFocus=null\n", nil},
		{"package without an activity", "mCurrentFocus=Window{1 u0 com.foo}\n", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		got := parseCurrentFocus(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parseCurrentFocus(%q) = %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

// fakeAdb puts a script named `adb` first on PATH, so the wrappers can be
// exercised without a device or a real adb install.
func fakeAdb(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "adb"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A failing shell call reports the joined argv and the trimmed stderr, capped
// like the Node code, as a DroidJevError (exit 3).
func TestShellOkError(t *testing.T) {
	fakeAdb(t, "echo 'boom: denied' >&2\nexit 7")
	_, err := ShellOk("emulator-5554", []string{"input", "tap", "10", "20"}, RunOpts{})
	want := "adb shell input tap 10 20 exited 7: boom: denied"
	if err == nil || err.Error() != want {
		t.Fatalf("ShellOk error = %v, want %q", err, want)
	}
	var dje *DroidJevError
	if !errors.As(err, &dje) || dje.ExitCode != 3 {
		t.Fatalf("ShellOk error = %#v, want a *DroidJevError with exit 3", err)
	}
}

// The wrappers keep adb's argv shape: -s SERIAL before the subcommand, and
// exec-out forcing the binary path. No shell interpolation anywhere.
func TestShellAndExecOutArgv(t *testing.T) {
	fakeAdb(t, `echo "$@"`)
	r, err := Shell("emulator-5554", []string{"echo", "hi"}, RunOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(r.Stdout); got != "-s emulator-5554 shell echo hi\n" {
		t.Errorf("Shell argv = %q, want %q", got, "-s emulator-5554 shell echo hi\n")
	}

	r, err = ExecOut("emulator-5554", []string{"screencap"}, RunOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(r.Stdout); got != "-s emulator-5554 exec-out screencap\n" {
		t.Errorf("ExecOut argv = %q, want %q", got, "-s emulator-5554 exec-out screencap\n")
	}
}
