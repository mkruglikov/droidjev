// Shared helpers. Security rules live here so every caller inherits them:
// - subprocesses are spawned as argument arrays (never through a shell)
// - every subprocess has a timeout and bounded output collection
// - anything headed for logs/errors passes through Redact
//
// Ported from the Node implementation; messages are kept word for word.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DroidJevError is the error the CLI reports and turns into an exit code: 3
// for errors (NewDroidJevError's default), 2 for blocked-on-device states that
// callers build directly.
type DroidJevError struct {
	Msg      string
	ExitCode int
}

func (e *DroidJevError) Error() string { return e.Msg }

// NewDroidJevError is `new DroidJevError(message)`: exit code 3.
func NewDroidJevError(msg string) *DroidJevError {
	return &DroidJevError{Msg: msg, ExitCode: 3}
}

// Sleep is the JS `sleep(ms)` promise, blocking. Fine for a CLI whose waits
// are all on the critical path.
func Sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// bearerRe is the lookahead-free prefix of the JS regex
// /Bearer\s+[\s\S]*?(?="|$)/gi: "Bearer", then a run of JS whitespace
// (JSWhitespaceClass, because RE2's \s is ASCII-only and misses \v).
var bearerRe = regexp.MustCompile(`(?i)bearer[` + JSWhitespaceClass + `]+`)

// Redact is defense in depth: no string we ever print may contain a bearer
// token. It over-redacts (may eat a few following words) rather than
// under-redact, and handles multi-line header values that fetch quotes in
// error messages.
//
// RE2 has no lookahead, so `[\s\S]*?(?="|$)` is emulated: after the whitespace
// run, the match ends at the next `"` (left in place, so scanning resumes
// there like JS's global replace) or at the end of the string.
func Redact(s string) string {
	var b strings.Builder
	for {
		loc := bearerRe.FindStringIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:loc[0]])
		b.WriteString("Bearer [REDACTED]") // capitalized, as JS replaces the whole match
		rest := s[loc[1]:]
		i := strings.IndexByte(rest, '"')
		if i < 0 {
			return b.String()
		}
		s = rest[i:]
	}
}

// installHints are the human-readable install hints for missing external
// tools, word for word from the Node implementation.
var installHints = map[string]string{
	"adb":     "\"adb\" is not installed or not in PATH — install Android platform-tools:\n  https://developer.android.com/tools/releases/platform-tools",
	"android": "the \"android\" CLI is not installed or not in PATH — install it:\n  https://developer.android.com/cli/install\n(only auto-booting an AVD needs it; pass --device to target a running emulator)",
	"zsh":     "\"zsh\" not found",
	"bash":    "\"bash\" not found",
}

func installHint(file string) string { return installHints[file] }

// spawnError is the ENOENT + generic start-failure message: a known tool gets
// its install hint, anything else gets `failed to start "<file>": <cause>`.
func spawnError(file string, err error) *DroidJevError {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		if hint := installHint(file); hint != "" {
			return NewDroidJevError(hint)
		}
	}
	return NewDroidJevError(fmt.Sprintf("failed to start %q: %v", file, err))
}

// RunOpts mirrors run()'s options object; zero values take the Node defaults.
// Binary is accepted for parity with the Node API but has no effect: Stdout is
// always the raw bytes (`binary: true` only opted out of a UTF-8 decode Go
// never does).
type RunOpts struct {
	TimeoutMs int64
	MaxBytes  int64
	Binary    bool
}

// RunResult mirrors the object run() resolves with. Stdout is []byte instead
// of Node's string/Buffer split; Stderr is decoded, as it always was.
type RunResult struct {
	Code   int
	Stdout []byte
	Stderr string
}

const (
	defaultTimeoutMs int64 = 15000
	defaultMaxBytes  int64 = 32 * 1024 * 1024 // 32 MiB
)

// Run runs a file with an argument array and returns its exit code and output.
// It never errors on a non-zero exit (callers decide), only on spawn failure
// (ENOENT → the install hints above) or timeout (the process is SIGKILLed and
// the error reads `"file" timed out after Nms`). Output is capped at MaxBytes:
// the child keeps writing and we keep reading, but only the prefix is stored.
func Run(file string, args []string, opts RunOpts) (RunResult, error) {
	timeoutMs := opts.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, file, args...)
	stdout := &cappedBuffer{limit: maxBytes}
	stderr := &cappedBuffer{limit: maxBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// exec.CommandContext's default Cancel kills the process (SIGKILL on
	// unix); WaitDelay stops Wait from hanging on a pipe a grandchild kept
	// open after that kill.
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return RunResult{}, NewDroidJevError(fmt.Sprintf("%q timed out after %dms", file, timeoutMs))
		}
		// ProcessState is nil only when the process never started.
		if cmd.ProcessState == nil {
			return RunResult{}, spawnError(file, err)
		}
		// Non-zero exit (or a drained-by-WaitDelay pipe): not our call.
	}
	return RunResult{
		Code:   cmd.ProcessState.ExitCode(),
		Stdout: stdout.buf,
		Stderr: string(stderr.buf),
	}, nil
}

// cappedBuffer keeps the first limit bytes written and discards the rest. It
// always reports a full write so the cap can never block the child.
type cappedBuffer struct {
	limit int64
	buf   []byte
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := w.limit - int64(len(w.buf)); room > 0 {
		if int64(n) > room {
			w.buf = append(w.buf, p[:room]...)
		} else {
			w.buf = append(w.buf, p...)
		}
	}
	return n, nil
}
