package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Redact cases (single- and multi-line bearer values)
// and pins the JS regex /Bearer\s+[\s\S]*?(?="|$)/gi behavior exactly: the
// match runs from "Bearer" plus its whitespace to the next `"` or the end of
// the string, the replacement is always capitalized, and everything else is
// untouched.
func TestRedactBearerVariants(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"mid-string multi-line",
			"Headers.append: \"Bearer line1\nSECRET123\" is invalid",
			"Headers.append: \"Bearer [REDACTED]\" is invalid",
		},
		{"no trailing quote", "Bearer abc123 tail", "Bearer [REDACTED]"},
		{"multi-line without quote", "Bearer\nSECRET123", "Bearer [REDACTED]"},
		{"lowercase is matched and re-capitalized", "prefix bearer token\" suffix", "prefix Bearer [REDACTED]\" suffix"},
		{"two headers", "\"Bearer a\" and \"Bearer b\"", "\"Bearer [REDACTED]\" and \"Bearer [REDACTED]\""},
		{"tab run", "Bearer\t\tsecret", "Bearer [REDACTED]"},
		{"quote right after the whitespace run", "Bearer \"secret\"", "Bearer [REDACTED]\"secret\""},
		{"nothing to redact", "", ""},
		{"other auth schemes untouched", "Authorization: Basic dXNlcjpwYXNz", "Authorization: Basic dXNlcjpwYXNz"},
		{"bearer without whitespace untouched", "Bearer", "Bearer"},
		{"bearer as a word prefix untouched", "Bearerx abc", "Bearerx abc"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("%s: Redact(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	if got := Redact(cases[0].in); strings.Contains(got, "SECRET123") {
		t.Errorf("Redact leaked the token: %q", got)
	}
}

func TestDroidJevError(t *testing.T) {
	e := NewDroidJevError("boom")
	if e.Error() != "boom" {
		t.Errorf("Error() = %q, want %q", e.Error(), "boom")
	}
	if e.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", e.ExitCode)
	}
	var target *DroidJevError
	if !errors.As(error(e), &target) {
		t.Error("DroidJevError does not satisfy errors.As")
	}
}

// requireBin resolves a tool through PATH: the plan's /bin/false is Linux's
// location, and macOS keeps `false` in /usr/bin.
func requireBin(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed: %v", name, err)
	}
	return p
}

func TestRunExitCode(t *testing.T) {
	falseBin := requireBin(t, "false")
	t.Run("non-zero exit is not an error", func(t *testing.T) {
		res, err := Run(falseBin, nil, RunOpts{})
		if err != nil {
			t.Fatalf("Run(%s) errored: %v", falseBin, err)
		}
		if res.Code != 1 {
			t.Errorf("Code = %d, want 1", res.Code)
		}
		if len(res.Stdout) != 0 {
			t.Errorf("Stdout = %q, want empty", res.Stdout)
		}
	})

	t.Run("stderr is captured on failure", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		catBin := requireBin(t, "cat")
		res, err := Run(catBin, []string{missing}, RunOpts{})
		if err != nil {
			t.Fatalf("Run(%s %s) errored: %v", catBin, missing, err)
		}
		if res.Code == 0 {
			t.Error("Code = 0, want non-zero")
		}
		if !strings.Contains(res.Stderr, "No such file") {
			t.Errorf("Stderr = %q, want the cat error", res.Stderr)
		}
	})
}

func TestRunTimeout(t *testing.T) {
	sleepBin := requireBin(t, "sleep")
	start := time.Now()
	_, err := Run(sleepBin, []string{"5"}, RunOpts{TimeoutMs: 200})
	if err == nil {
		t.Fatalf("Run(%s 5, 200ms) returned no error", sleepBin)
	}
	if want := fmt.Sprintf("%q timed out after 200ms", sleepBin); err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run took %v, the process was not killed", elapsed)
	}
	var dj *DroidJevError
	if !errors.As(err, &dj) || dj.ExitCode != 3 {
		t.Errorf("error = %v, want a DroidJevError with ExitCode 3", err)
	}
}

func TestRunMissing(t *testing.T) {
	_, err := Run("droidjev-nonexistent-bin-xyz", nil, RunOpts{})
	if err == nil {
		t.Fatal("Run with a nonexistent binary returned no error")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("error = %q, want a failed-to-start message", err.Error())
	}
	var dj *DroidJevError
	if !errors.As(err, &dj) || dj.ExitCode != 3 {
		t.Errorf("error = %v, want a DroidJevError with ExitCode 3", err)
	}

	// The ENOENT branch maps known tools to their install hints.
	hinted := spawnError("adb", &exec.Error{Name: "adb", Err: exec.ErrNotFound})
	if !strings.Contains(hinted.Error(), "platform-tools") {
		t.Errorf("adb hint = %q, want the platform-tools hint", hinted.Error())
	}
	// A path-style ENOENT still maps to the generic failed-to-start message.
	generic := spawnError("/nope", &exec.Error{Name: "/nope", Err: fs.ErrNotExist})
	if !strings.Contains(generic.Error(), `failed to start "/nope"`) {
		t.Errorf("generic spawn error = %q", generic.Error())
	}
}

func TestInstallHint(t *testing.T) {
	for _, f := range []string{"adb", "android", "zsh", "bash"} {
		if installHint(f) == "" {
			t.Errorf("installHint(%q) is empty", f)
		}
	}
	if got := installHint("zsh"); got != `"zsh" not found` {
		t.Errorf("installHint(zsh) = %q", got)
	}
	if got := installHint("droidjev-nonexistent-bin-xyz"); got != "" {
		t.Errorf("installHint(unknown) = %q, want empty", got)
	}
}

func TestRunCap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	data := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Run(requireBin(t, "cat"), []string{p}, RunOpts{MaxBytes: 1000})
	if err != nil {
		t.Fatalf("Run errored: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("Code = %d, want 0", res.Code)
	}
	if len(res.Stdout) != 1000 {
		t.Errorf("len(Stdout) = %d, want the 1000-byte cap", len(res.Stdout))
	}
	if !bytes.Equal(res.Stdout, data[:1000]) {
		t.Error("Stdout is not the prefix of the output")
	}
}
