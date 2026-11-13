// main.go tests: the CLI surface ported from src/cli.js (test/unit.test.mjs's
// cli section), plus the USAGE/version gate and the act exit-code mapping.

package main

import (
	"strings"
	"testing"
)

func TestParseApp(t *testing.T) {
	app, err := ParseApp("Settings=com.android.settings")
	if err != nil {
		t.Fatalf("ParseApp: %v", err)
	}
	if want := (App{Label: "Settings", Pkg: "com.android.settings"}); app != want {
		t.Fatalf("ParseApp = %+v, want %+v", app, want)
	}

	app, err = ParseApp("com.android.settings")
	if err != nil {
		t.Fatalf("ParseApp bare package: %v", err)
	}
	if want := (App{Label: "settings", Pkg: "com.android.settings"}); app != want {
		t.Fatalf("ParseApp bare package = %+v, want %+v", app, want)
	}

	for _, bad := range []string{"=com.android.settings", "Settings=", "Settings=com.foo=bar", "=", ""} {
		_, err := ParseApp(bad)
		if err == nil {
			t.Fatalf("ParseApp(%q) did not error", bad)
		}
		if !strings.Contains(err.Error(), `needs "Label=package"`) {
			t.Fatalf("ParseApp(%q) error = %q, want the Label=package message", bad, err)
		}
	}
}

func TestParseArgs(t *testing.T) {
	// A value flag refuses the next flag as its value, with the JS message.
	_, err := ParseArgs([]string{"act", "goal", "--device", "--json"})
	if err == nil || !strings.Contains(err.Error(), `flag --device needs a value (got "--json")`) {
		t.Fatalf("--device --json error = %v", err)
	}
	_, err = ParseArgs([]string{"act", "goal", "--text"})
	if err == nil || !strings.Contains(err.Error(), `flag --text needs a value (got nothing)`) {
		t.Fatalf("trailing --text error = %v", err)
	}

	// Integer flags: JS Number semantics for the value, 1..100 / 1..50 range,
	// and the exact message.
	for _, v := range []string{"-5", "abc", "0", "101", "2.5"} {
		if _, err := ParseArgs([]string{"act", "goal", "--max-steps", v}); err == nil ||
			!strings.Contains(err.Error(), `--max-steps must be an integer 1..100 (got "`+v+`")`) {
			t.Fatalf("--max-steps %q error = %v", v, err)
		}
	}
	for _, v := range []string{"-1", "51", "x", "1.5"} {
		if _, err := ParseArgs([]string{"find", "x", "--max-iterations", v}); err == nil ||
			!strings.Contains(err.Error(), `--max-iterations must be an integer 1..50 (got "`+v+`")`) {
			t.Fatalf("--max-iterations %q error = %v", v, err)
		}
	}

	_, err = ParseArgs([]string{"act", "goal", "--provider", "appium"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --provider") {
		t.Fatalf("unknown flag error = %v", err)
	}

	f, err := ParseArgs([]string{"act", "goal", "--no-animations"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !f.NoAnimations {
		t.Fatal("--no-animations did not set NoAnimations")
	}

	// Non-flag positionals, repeatable --text, and --max-steps in one call.
	f, err = ParseArgs([]string{"act", "open x", "--text", "hi there", "--max-steps", "5"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if len(f.Args) != 2 || f.Args[0] != "act" || f.Args[1] != "open x" {
		t.Fatalf("Args = %#v", f.Args)
	}
	if len(f.Texts) != 1 || f.Texts[0] != "hi there" || f.MaxSteps != 5 {
		t.Fatalf("flags = %+v", f)
	}

	// Repeatable --guidance.
	f, err = ParseArgs([]string{"act", "goal", "--guidance", "use Drive", "--guidance", "pick the city"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if len(f.Guidance) != 2 || f.Guidance[0] != "use Drive" || f.Guidance[1] != "pick the city" {
		t.Fatalf("Guidance = %#v", f.Guidance)
	}

	// Repeatable --app, each parsed by ParseApp.
	f, err = ParseArgs([]string{"act", "g", "--app", "Settings=com.android.settings", "--app", "com.android.deskclock"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	want := []App{{Label: "Settings", Pkg: "com.android.settings"}, {Label: "deskclock", Pkg: "com.android.deskclock"}}
	if len(f.Apps) != len(want) || f.Apps[0] != want[0] || f.Apps[1] != want[1] {
		t.Fatalf("Apps = %#v, want %#v", f.Apps, want)
	}

	// Every boolean flag, and -h.
	f, err = ParseArgs([]string{"-h", "--json", "--serve", "--tap", "--answer", "--trace", "--no-vision", "--hybrid-vision"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !f.Help || !f.JSON || !f.Serve || !f.Tap || !f.Answer || !f.Trace || !f.NoVision || !f.HybridVision {
		t.Fatalf("boolean flags = %+v", f)
	}

	// String flags.
	f, err = ParseArgs([]string{"devices", "--device", "emulator-5554", "--grep", "WLAN", "--model", "jev-9.9.9"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if f.Device != "emulator-5554" || f.Grep != "WLAN" || f.Model != "jev-9.9.9" {
		t.Fatalf("string flags = %+v", f)
	}

	// A JS-Number value like "5.0" is still an integer.
	f, err = ParseArgs([]string{"act", "g", "--max-steps", "5.0"})
	if err != nil || f.MaxSteps != 5 {
		t.Fatalf("--max-steps 5.0 = %+v, %v", f, err)
	}
}

func TestEnvTrace(t *testing.T) {
	if !EnvTrace("1") {
		t.Fatal(`EnvTrace("1") = false`)
	}
	for _, v := range []string{"0", "false", "no", "", "true", " 1"} {
		if EnvTrace(v) {
			t.Fatalf("EnvTrace(%q) = true", v)
		}
	}
}

func TestUsageContainsVersion(t *testing.T) {
	if !strings.HasPrefix(USAGE, "droidjev "+version+" — ") {
		t.Fatalf("USAGE does not start with the versioned banner: %q", USAGE[:min(60, len(USAGE))])
	}
	for _, want := range []string{
		"usage: droidjev <command> [args] [flags]",
		"a screenshot is sent only for a blind screen or a `check` verdict",
		"exit codes: 0 done, 2 blocked, 3 error",
	} {
		if !strings.Contains(USAGE, want) {
			t.Fatalf("USAGE missing %q", want)
		}
	}
}

func TestActExitCode(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   int
	}{
		{"error", 3},
		{"done", 0},
		{"answered", 0},
		{"blocked", 2},
		{"max_steps", 2},
		{"", 2},
	} {
		if got := actExitCode(tc.status); got != tc.want {
			t.Fatalf("actExitCode(%q) = %d, want %d", tc.status, got, tc.want)
		}
	}
}
