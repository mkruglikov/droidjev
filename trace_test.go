// Tracing: a
// traced episode is one 0600 JSONL file per run, every line passes through
// Redact, and a trace that cannot write disables itself with one notice
// instead of failing the episode.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceRedactsAndDisables(t *testing.T) {
	t.Run("redacts secret material and writes 0600 JSONL", func(t *testing.T) {
		dir := t.TempDir()
		const sentinel = "sk-live-TRACE-SENTINEL-123456"
		tr := OpenTrace(true, "Open Settings now!", "emulator-5554", dir)
		if tr == nil {
			t.Fatal("OpenTrace(true) = nil")
		}
		tr.RecordStep(TraceStep{
			Step: 1,
			Record: map[string]any{
				"note": "Authorization: Bearer " + sentinel,
			},
			Hash: "abc123",
		})
		tr.RecordResult(map[string]any{
			"status":        "done",
			"blockedReason": nil,
			"tookMs":        int64(12),
			"tokens":        map[string]int{"input": 1, "output": 2},
		})

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("trace files = %d, want 1", len(entries))
		}
		name := entries[0].Name()
		if !strings.HasSuffix(name, "-emulator-5554-open-settings-now.jsonl") {
			t.Fatalf("trace file = %q, want a stamp-serial-slug name", name)
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("trace mode = %o, want 600", perm)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), sentinel) {
			t.Fatalf("trace leaked the secret:\n%s", raw)
		}
		if !strings.Contains(string(raw), "Bearer [REDACTED]") {
			t.Fatalf("trace did not redact the bearer token:\n%s", raw)
		}
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("trace lines = %d, want header + step + result:\n%s", len(lines), raw)
		}
		var header, step, result map[string]any
		for i, dst := range []*map[string]any{&header, &step, &result} {
			if err := json.Unmarshal([]byte(lines[i]), dst); err != nil {
				t.Fatalf("line %d is not JSON: %v\n%s", i, err, lines[i])
			}
		}
		if header["type"] != "header" || header["goal"] != "Open Settings now!" || header["serial"] != "emulator-5554" {
			t.Fatalf("header = %#v", header)
		}
		if header["startedAt"] == "" {
			t.Fatalf("header has no startedAt: %#v", header)
		}
		if step["type"] != "step" || step["step"] != float64(1) || step["hash"] != "abc123" {
			t.Fatalf("step = %#v", step)
		}
		if result["type"] != "result" || result["status"] != "done" || result["tookMs"] != float64(12) {
			t.Fatalf("result = %#v", result)
		}
		if v, ok := result["blockedReason"]; !ok || v != nil {
			t.Fatalf("result.blockedReason = %#v (present=%v), want an explicit null", v, ok)
		}
		if toks, ok := result["tokens"].(map[string]any); !ok || toks["input"] != float64(1) || toks["output"] != float64(2) {
			t.Fatalf("result.tokens = %#v", result["tokens"])
		}
	})

	t.Run("vision steps carry only the record", func(t *testing.T) {
		dir := t.TempDir()
		tr := OpenTrace(true, "g", "s", dir)
		tr.RecordStep(TraceStep{Step: 2, Kind: "vision", Record: map[string]any{"action": "wait"}})
		raw := readOnlyTrace(t, dir)
		line := strings.TrimSpace(strings.Split(raw, "\n")[1])
		if strings.Contains(line, "questionInputs") || !strings.Contains(line, `"kind":"vision"`) {
			t.Fatalf("vision step = %s, want kind and record only", line)
		}
	})

	t.Run("unwritable directory disables with one notice", func(t *testing.T) {
		base := t.TempDir()
		asFile := filepath.Join(base, "not-a-dir")
		if err := os.WriteFile(asFile, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		prev := traceStderr
		traceStderr = &buf
		defer func() { traceStderr = prev }()

		tr := OpenTrace(true, "g", "s", asFile)
		if tr == nil {
			t.Fatal("OpenTrace(true) = nil")
		}
		tr.RecordStep(TraceStep{Step: 1, Record: map[string]any{"step": 1}})
		tr.RecordResult(map[string]any{"status": "done"})
		if n := strings.Count(buf.String(), "trace disabled"); n != 1 {
			t.Fatalf("notices = %d, want exactly one:\n%s", n, buf.String())
		}
	})

	if OpenTrace(false, "g", "s", t.TempDir()) != nil {
		t.Fatal("OpenTrace(false) must be nil")
	}
}

func readOnlyTrace(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("trace files = %d, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
