// Episode tracing: every step's exact question inputs, the questions sent, the
// effective answers, and the executed record, one JSON line each under
// ~/.cache/droidjev/runs/. Opt-in on purpose — a trace contains the goal,
// screen text, typed values and answers. The file is created 0o600 and is
// never written inside the repo.
//
// Ported from the Node implementation: the header/step/result shapes, the
// millisecond stamp plus random suffix, the slug, and the
// disable-on-error-once behavior are kept exact.

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// traceStderr receives the one-time "trace disabled" notice. It is a package
// var so tests can capture it, the same way the Node tests stub console.error.
var traceStderr io.Writer = os.Stderr

// Trace is an open episode trace. File is "" for a trace disabled by an
// unusable home directory; every method is a no-op on a nil or disabled Trace
// (the Node code's `trace?.recordStep`).
type Trace struct {
	File     string
	dir      string
	disabled bool
	noticed  bool
}

// TraceStep is the recordStep argument: the exact per-step inputs, the
// questions sent, the effective answers, the criteria key order, the executed
// record, and the table hash. Kind == "vision" writes the reduced vision shape
// (kind + record only), as the original did.
type TraceStep struct {
	Step           int
	Kind           string
	QuestionInputs any
	Questions      any
	Answers        any
	Criteria       []string
	Record         any
	Hash           string
}

// OpenTrace starts a trace file and writes its header. A nil Trace (traced
// disabled) mirrors the Node `null`; every method tolerates it.
//
// dir == "" means the default ~/.cache/droidjev/runs. When even the home
// directory is unavailable the trace disables itself rather than writing into
// the current working directory.
func OpenTrace(enabled bool, goal, serial, dir string) *Trace {
	if !enabled {
		return nil
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return &Trace{disabled: true, noticed: true}
		}
		dir = filepath.Join(home, ".cache", "droidjev", "runs")
	}
	t := &Trace{dir: dir}
	t.File = filepath.Join(dir, traceStamp()+"-"+traceSerial(serial)+"-"+traceSlug(goal)+".jsonl")
	t.write([]traceField{
		{"v", 1},
		{"type", "header"},
		{"goal", goal},
		{"serial", serial},
		{"startedAt", traceISO()},
	})
	return t
}

// RecordStep writes one step line in the normal or the vision shape.
func (t *Trace) RecordStep(rec TraceStep) {
	if t == nil || t.disabled {
		return
	}
	fields := []traceField{{"type", "step"}, {"step", rec.Step}}
	if rec.Kind == "vision" {
		fields = append(fields, traceField{"kind", "vision"}, traceField{"record", rec.Record})
	} else {
		if rec.QuestionInputs != nil {
			fields = append(fields, traceField{"questionInputs", rec.QuestionInputs})
		}
		if rec.Questions != nil {
			fields = append(fields, traceField{"questions", rec.Questions})
		}
		if rec.Answers != nil {
			fields = append(fields, traceField{"answers", rec.Answers})
		}
		if rec.Criteria != nil {
			fields = append(fields, traceField{"criteria", rec.Criteria})
		}
		fields = append(fields, traceField{"record", rec.Record})
		if rec.Hash != "" {
			fields = append(fields, traceField{"hash", rec.Hash})
		}
	}
	t.write(fields)
}

// RecordResult writes the episode result, picking the fields the original
// picked
// (`status`, `blockedReason`, `tookMs`, `tokens`). Map membership is the
// "defined" test: a present nil value is written as JSON null, a missing one is
// omitted.
func (t *Trace) RecordResult(result map[string]any) {
	if t == nil || t.disabled {
		return
	}
	fields := []traceField{{"type", "result"}}
	for _, k := range []string{"status", "blockedReason", "tookMs", "tokens"} {
		if v, ok := result[k]; ok {
			fields = append(fields, traceField{k, v})
		}
	}
	t.write(fields)
}

// write serializes one line, redacts it, and appends it. A full disk or an
// unwritable cache directory must never end an episode that is otherwise
// succeeding: say so once, then go quiet.
func (t *Trace) write(fields []traceField) {
	if t == nil || t.disabled {
		return
	}
	line, err := marshalTraceLine(fields)
	if err == nil {
		// Redact runs on the serialized line: the JSON stays valid (the
		// pattern stops at a quote), and no bearer material reaches disk.
		err = t.appendLine(Redact(line) + "\n")
	}
	if err != nil {
		t.disabled = true
		if !t.noticed {
			t.noticed = true
			fmt.Fprintf(traceStderr, "# trace disabled (%s)\n", err)
		}
	}
}

func (t *Trace) appendLine(line string) error {
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(t.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// traceField is one key/value pair of a JSON object, kept in insertion order
// like the Node object literal.
type traceField struct {
	key string
	val any
}

func marshalTraceLine(fields []traceField) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(JSONStringify(f.key))
		b.WriteByte(':')
		val, err := marshalTraceValue(f.val)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
	}
	b.WriteByte('}')
	return b.String(), nil
}

func marshalTraceValue(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case string:
		return JSONStringify(x), nil
	case int:
		return strconv.Itoa(x), nil
	}
	// encoding/json escapes <>& by default; JSON.stringify does not.
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

var traceSerialRe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

var traceSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// traceSlug is the goal's filename form: lowercase, runs of non-alnum to "-",
// 40 chars max, "goal" when nothing survives.
func traceSlug(goal string) string {
	s := strings.TrimPrefix(traceSlugRe.ReplaceAllString(strings.ToLower(goal), "-"), "-")
	s = strings.TrimSuffix(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		return "goal"
	}
	return s
}

func traceSerial(serial string) string { return traceSerialRe.ReplaceAllString(serial, "_") }

func traceISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// traceStamp is the millisecond stamp plus a random suffix: two runs of the
// same goal in the same second must not share a file.
func traceStamp() string {
	s := strings.NewReplacer(":", "-", ".", "-", "T", "_").Replace(traceISO())
	if len(s) > 23 {
		s = s[:23]
	}
	var r [2]byte
	_, _ = rand.Read(r[:]) // crypto/rand.Read never fails on supported platforms
	return s + "-" + hex.EncodeToString(r[:])
}
