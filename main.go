// Command droidjev is a fast jev-powered Android emulator clicker, text-first:
// it dumps the view hierarchy and taps the element that advances the goal.
// Ported from the Node implementation; messages, JSON shapes and exit codes
// are kept word for word.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// version is the build version, overridden at release time with
// -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

var USAGE = "droidjev " + version + ` — fast jev-powered Android emulator clicker (text-first;
a screenshot is sent only for a blind screen or a ` + "`check`" + ` verdict)

usage: droidjev <command> [args] [flags]

commands:
  devices                    list adb devices with AVD names (fast: raw adb)
  boot [avd]                 ensure an emulator is booted; auto-boots when none is online
  start <package>            launch an app by package (deterministic, no model)
  snapshot [flags]           dump the current screen as an indexed element table
  find "<label>" [flags]     deterministically search scrollable content for a
                             label (snap → match → scroll), optionally tapping it
  act "<goal>" [flags]       run the agent loop until the goal is achieved
  check "<goal>" [flags]     judge whether the CURRENT screen explicitly says the
                             goal cannot be done (one screenshot, jev image): exits
                             0 when it might still be done, 2 when the screen proves
                             it impossible. It never acts.
  decide [--serve]           one decision for a caller-supplied state: reads a JSON
                             request on stdin (goal, screen, elements, recent_actions,
                             provided_texts, can_scroll, clipboard, answers) and prints
                             the validated choice. No device, no adb — for hosts that
                             already own their observation/action pipeline.
                             --serve keeps reading requests, one JSON object per
                             line, and answers one JSON object per line: hosts that
                             decide every step should not fork per step.

flags:
  --device SERIAL|AVD        target device (serial or AVD name; default: sole online
                             emulator, else auto-boot the first AVD)
  --text "..."               value to type when the goal needs typing (never
                             guessed); repeat for multi-field forms — one --text
                             per value, the model matches each to its field.
                             Values the goal itself names are typed without it.
  --guidance "..."           (act) operator/app lore for this run: a plan, an
                             app's mechanics, a constraint; repeatable. The
                             model still chooses each step.
  --app "Label=package"      an app the model may open by name in one step
                             ("--app Settings=com.android.settings"); repeat for
                             more. A bare package name is labelled by its last
                             segment. The model still chooses which one the goal
                             means — this only makes opening it cost one step.
  --max-steps N              action budget for act (default 32)
  --max-iterations N         (find) scroll budget (default 20)
  --tap                      (find) tap the best match instead of just reporting it
  --answer                   (act) the goal is a question: offer the answer_ options
                             and report the submitted answer instead of stopping at
                             done
  --no-vision                (act) never send screenshots: a screen with no
                             accessibility nodes blocks (blind_screen) instead of
                             falling back to image+box answers
  --hybrid-vision            (act) also send a screenshot when the element list
                             is sparse (≤3 interactive rows) and the screen
                             refused to change; --no-vision still wins
  --model ID                 (act/decide) model override (default jev-1.14.0);
                             a non-default model is never sent images — the
                             vision fallback degrades to blind_screen. check
                             refuses a non-default model outright
  --grep "..."               (snapshot) print only rows containing the substring
  --json                     machine-readable output
  --trace                    (act) record every step to ~/.cache/droidjev/runs/
                             as JSONL (goal, screen rows, typed values, answers);
                             also DROIDJEV_TRACE=1. Owner-only file (0600)
  --no-animations            disable window/transition animations for the run
                             (restored after) — screens settle in ~1 frame, so
                             steps verify sooner

env: TYPESAFE_API_KEY (required for act, check and decide)

exit codes: 0 done, 2 blocked, 3 error
             (decide exits 0 with the decision on stdout; done/blocked come back as
              choices in the JSON, not as exit codes)`

var valueFlags = map[string]bool{
	"--device":         true,
	"--text":           true,
	"--app":            true,
	"--guidance":       true,
	"--max-steps":      true,
	"--grep":           true,
	"--max-iterations": true,
	"--model":          true,
}

// Flags is parseArgs' result. The zero value is "flag absent": empty Texts /
// Apps / Guidance are the Node null, and MaxSteps/MaxIterations 0 choose the
// command defaults.
type Flags struct {
	Args          []string
	Device        string
	Texts         []string
	Apps          []App
	Guidance      []string
	MaxSteps      int
	MaxIterations int
	Grep          string
	Model         string
	JSON          bool
	Serve         bool
	Tap           bool
	Answer        bool
	Trace         bool
	NoVision      bool
	HybridVision  bool
	NoAnimations  bool
	Help          bool
}

// ParseApp parses one --app value: "Label=package", or a bare package
// labelled by its last segment (com.android.settings → "settings").
func ParseApp(value string) (App, error) {
	eq := strings.Index(value, "=")
	var label, pkg string
	if eq == -1 {
		parts := strings.Split(value, ".")
		label = parts[len(parts)-1]
		pkg = value
	} else {
		label = value[:eq]
		pkg = value[eq+1:]
	}
	label = JSTrim(label)
	pkg = JSTrim(pkg)
	if label == "" || pkg == "" || strings.Contains(pkg, "=") {
		return App{}, NewDroidJevError(`--app needs "Label=package" or a package name (got "` + value + `")`)
	}
	return App{Label: label, Pkg: pkg}, nil
}

// ParseArgs walks argv. Value flags consume the next argv entry; a value that
// looks like a flag is rejected instead of being swallowed. Repeatable flags
// append, everything else sets.
func ParseArgs(argv []string) (Flags, error) {
	f := Flags{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if valueFlags[a] {
			if i+1 >= len(argv) {
				return Flags{}, NewDroidJevError("flag " + a + " needs a value (got nothing)")
			}
			v := argv[i+1]
			if strings.HasPrefix(v, "--") {
				return Flags{}, NewDroidJevError(`flag ` + a + ` needs a value (got "` + v + `")`)
			}
			i++
			switch a {
			case "--device":
				f.Device = v
			case "--text":
				f.Texts = append(f.Texts, v)
			case "--app":
				app, err := ParseApp(v)
				if err != nil {
					return Flags{}, err
				}
				f.Apps = append(f.Apps, app)
			case "--guidance":
				f.Guidance = append(f.Guidance, v)
			case "--grep":
				f.Grep = v
			case "--model":
				f.Model = v
			case "--max-iterations":
				n, err := intFlag(v, a, 1, 50)
				if err != nil {
					return Flags{}, err
				}
				f.MaxIterations = n
			default: // --max-steps
				n, err := intFlag(v, a, 1, 100)
				if err != nil {
					return Flags{}, err
				}
				f.MaxSteps = n
			}
			continue
		}
		switch {
		case a == "--json":
			f.JSON = true
		case a == "--serve":
			f.Serve = true
		case a == "--tap":
			f.Tap = true
		case a == "--answer":
			f.Answer = true
		case a == "--trace":
			f.Trace = true
		case a == "--no-vision":
			f.NoVision = true
		case a == "--hybrid-vision":
			f.HybridVision = true
		case a == "--no-animations":
			f.NoAnimations = true
		case a == "--help" || a == "-h":
			f.Help = true
		case strings.HasPrefix(a, "--"):
			return Flags{}, NewDroidJevError("unknown flag: " + a)
		default:
			f.Args = append(f.Args, a)
		}
	}
	return f, nil
}

// intFlag validates a numeric flag with JS Number + Number.isInteger
// semantics, in the range lo..hi.
func intFlag(v, name string, lo, hi int) (int, error) {
	n, ok := jsInteger(v)
	if !ok || n < lo || n > hi {
		return 0, NewDroidJevError(name + " must be an integer " + strconv.Itoa(lo) + ".." + strconv.Itoa(hi) + ` (got "` + v + `")`)
	}
	return n, nil
}

// jsInteger is Number(v) + Number.isInteger(v): a JS-whitespace-trimmed
// decimal or exponent literal with no fractional part.
func jsInteger(v string) (int, bool) {
	t := JSTrim(v)
	if t == "" {
		return 0, true // Number('') === 0
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, false
	}
	if f < -1e9 || f > 1e9 {
		return 0, false
	}
	return int(f), true
}

// EnvTrace is the DROIDJEV_TRACE test: only an explicit "1" enables tracing.
// "0"/"false" are what an operator writes to turn a feature OFF, and a trace
// writes screen text to disk.
func EnvTrace(v string) bool { return v == "1" }

func outln(s string) { fmt.Fprintln(os.Stdout, s) }
func errln(s string) { fmt.Fprintln(os.Stderr, s) }

// printJSON is JSON.stringify(v, null, 2): a trailing newline, two-space
// indent, and no HTML escaping (so `<`/`&` stay literal).
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// withUia2 ensures the resident uia2 server, then runs fn. The server stays
// up across invocations (boot costs ~1.2s).
func withUia2(serial string, fn func() error) error {
	if err := Ensure(serial); err != nil {
		return err
	}
	return fn()
}

// actExitCode maps a finished goal's status to the CLI exit code.
func actExitCode(status string) int {
	if status == "error" {
		return 3
	}
	if status == "done" || status == "answered" {
		return 0
	}
	return 2
}

type deviceRow struct {
	Serial string  `json:"serial"`
	State  string  `json:"state"`
	Model  *string `json:"model"`
	Avd    *string `json:"avd"`
}

func cmdDevices(f Flags) (int, error) {
	devs, err := AdbDevices()
	if err != nil {
		return 0, err
	}
	rows := make([]deviceRow, 0, len(devs))
	for _, d := range devs {
		var avd *string
		if IsEmulatorSerial(d.Serial) {
			name, err := AvdName(d.Serial)
			if err != nil {
				return 0, err
			}
			avd = nilIfEmptyStr(name)
		}
		rows = append(rows, deviceRow{Serial: d.Serial, State: d.State, Model: nilIfEmptyStr(d.Model), Avd: avd})
	}
	if f.JSON {
		return 0, printJSON(struct {
			Devices []deviceRow `json:"devices"`
		}{rows})
	}
	if len(rows) == 0 {
		outln("no devices online (see: droidjev boot)")
	}
	for _, r := range rows {
		line := r.Serial + "  " + r.State + "  "
		if r.Avd != nil {
			line += "avd=" + *r.Avd + "  "
		}
		if r.Model != nil {
			line += "model=" + *r.Model
		}
		outln(line)
	}
	return 0, nil
}

func cmdBoot(f Flags) (int, error) {
	device := f.Device
	if len(f.Args) > 1 {
		device = f.Args[1] // optional positional AVD
	}
	res, err := EnsureDevice(device)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notices {
		errln("# " + n)
	}
	var avd *string
	if f.JSON {
		// Only the --json shape carries the AVD name; the human line must not
		// pay for (or fail on) an extra adb call.
		if IsEmulatorSerial(res.Serial) {
			name, err := AvdName(res.Serial)
			if err != nil {
				return 0, err
			}
			avd = nilIfEmptyStr(name)
		}
		return 0, printJSON(struct {
			Serial string  `json:"serial"`
			Avd    *string `json:"avd"`
			Booted bool    `json:"booted"`
		}{res.Serial, avd, res.Booted})
	}
	line := res.Serial + " ready"
	if res.Booted {
		line += " (freshly booted)"
	}
	outln(line)
	return 0, nil
}

type screenOut struct {
	W int `json:"w"`
	H int `json:"h"`
}

func cmdSnapshot(f Flags) (int, error) {
	res, err := EnsureDevice(f.Device)
	if err != nil {
		return 0, err
	}
	var s *Snapshot
	if err := withUia2(res.Serial, func() error {
		var err error
		s, err = TakeSnapshot(res.Serial)
		return err
	}); err != nil {
		return 0, err
	}
	table := s.Table
	if f.Grep != "" {
		needle := strings.ToLower(f.Grep)
		filtered := &Table{}
		for _, r := range s.Table.Rows {
			if strings.Contains(strings.ToLower(r.Row), needle) {
				filtered.Rows = append(filtered.Rows, r)
			}
		}
		table = filtered
	}
	if f.JSON {
		var screen *screenOut
		if s.Screen != nil {
			screen = &screenOut{W: s.Screen.W, H: s.Screen.H}
		}
		var app *string
		if s.App != nil {
			app = &s.App.Pkg
		}
		centers := CentersMap{}
		for _, r := range table.Rows {
			centers[strconv.Itoa(r.Idx)] = r.El.Center
		}
		return 0, printJSON(struct {
			Serial  string     `json:"serial"`
			App     *string    `json:"app"`
			Screen  *screenOut `json:"screen"`
			TookMs  int64      `json:"tookMs"`
			Rows    []string   `json:"rows"`
			Centers CentersMap `json:"centers"`
		}{res.Serial, app, screen, s.TookMs, SerializeRows(table), centers})
	}
	app := "?"
	if s.App != nil {
		app = s.App.Pkg
	}
	flt := ""
	if f.Grep != "" {
		flt = ` matching="` + f.Grep + `"`
	}
	errln(fmt.Sprintf("# %s app=%s dump=%dms rows=%d%s", res.Serial, app, s.TookMs, len(table.Rows), flt))
	for _, row := range SerializeRows(table) {
		outln(row)
	}
	return 0, nil
}

type findOut struct {
	Found      bool     `json:"found"`
	Iterations int      `json:"iterations"`
	Match      *string  `json:"match"`
	Center     *Point   `json:"center,omitempty"`
	Matches    []string `json:"matches"`
	Tapped     bool     `json:"tapped"`
	Reason     string   `json:"reason,omitempty"`
}

func cmdFind(f Flags) (int, error) {
	label := ""
	if len(f.Args) > 1 {
		label = f.Args[1]
	}
	if label == "" {
		return 0, NewDroidJevError(`find needs a label, e.g. droidjev find "About phone"`)
	}
	res, err := EnsureDevice(f.Device)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notices {
		errln("# " + n)
	}
	if err := WakeAndUnlock(res.Serial); err != nil {
		return 0, err
	}
	maxIterations := f.MaxIterations
	if maxIterations == 0 {
		maxIterations = 20
	}
	var found FindResult
	if err := withUia2(res.Serial, func() error {
		var err error
		found, err = FindLabel(FindOpts{
			Serial:        res.Serial,
			Label:         label,
			TapIt:         f.Tap,
			MaxIterations: maxIterations,
			Log:           func(m string) { errln("# " + m) },
		})
		return err
	}); err != nil {
		return 0, err
	}
	matches := found.Matches
	if matches == nil {
		matches = []string{}
	}
	out := findOut{
		Found:      found.Found,
		Iterations: found.Iterations,
		Matches:    matches,
		Tapped:     found.Tapped,
		Reason:     found.Reason,
	}
	if found.Found {
		match := found.Match
		out.Match = &match
		out.Center = found.Center
	}
	if f.JSON {
		if err := printJSON(out); err != nil {
			return 0, err
		}
	} else if found.Found {
		x, y := 0, 0
		if found.Center != nil {
			x, y = found.Center.X, found.Center.Y
		}
		verb := "found"
		if found.Tapped {
			verb = "tapped"
		}
		outln(fmt.Sprintf("%s %s center=%d,%d iterations=%d", verb, found.Match, x, y, found.Iterations))
		for i := 1; i < len(found.Matches); i++ { // JS matches.slice(1)
			outln("  also matched: " + found.Matches[i])
		}
	} else {
		outln(fmt.Sprintf("not found (%s) after %d snapshots", found.Reason, found.Iterations))
	}
	if found.Found {
		return 0, nil
	}
	return 2, nil
}

type focusOut struct {
	Pkg      string `json:"pkg"`
	Activity string `json:"activity"`
}

func cmdStart(f Flags) (int, error) {
	pkg := ""
	if len(f.Args) > 1 {
		pkg = f.Args[1]
	}
	if pkg == "" {
		return 0, NewDroidJevError("start needs a package, e.g. droidjev start com.android.settings")
	}
	res, err := EnsureDevice(f.Device)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notices {
		errln("# " + n)
	}
	if err := WakeAndUnlock(res.Serial); err != nil {
		return 0, err
	}
	started, err := StartApp(res.Serial, pkg)
	if err != nil {
		return 0, err
	}
	if f.JSON {
		var app *focusOut
		if started.App != nil {
			app = &focusOut{Pkg: started.App.Pkg, Activity: started.App.Activity}
		}
		if err := printJSON(struct {
			Pkg       string    `json:"pkg"`
			Component *string   `json:"component"`
			Via       string    `json:"via"`
			App       *focusOut `json:"app"`
			Focused   bool      `json:"focused"`
		}{started.Pkg, nilIfEmptyStr(started.Component), started.Via, app, started.Focused}); err != nil {
			return 0, err
		}
	} else {
		line := "started " + started.Pkg + " via " + started.Via + fmt.Sprintf(" (focused=%t", started.Focused)
		if started.App != nil {
			line += ", app=" + started.App.Pkg
		}
		outln(line + ")")
	}
	if started.Focused {
		return 0, nil
	}
	return 2, nil
}

func cmdAct(f Flags) (int, error) {
	goal := ""
	if len(f.Args) > 1 {
		goal = f.Args[1]
	}
	if goal == "" {
		return 0, NewDroidJevError(`act needs a goal, e.g. droidjev act "open Settings"`)
	}
	res, err := EnsureDevice(f.Device)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notices {
		errln("# " + n)
	}
	if err := WakeAndUnlock(res.Serial); err != nil {
		return 0, err
	}
	trace := OpenTrace(f.Trace || EnvTrace(os.Getenv("DROIDJEV_TRACE")), goal, res.Serial, "")
	if trace != nil && trace.File != "" {
		errln("# trace: " + trace.File)
	}
	opts := GoalOpts{
		Serial:        res.Serial,
		Goal:          goal,
		Texts:         f.Texts,
		Apps:          f.Apps,
		MaxSteps:      f.MaxSteps,
		Guidance:      f.Guidance,
		AnimationsOff: f.NoAnimations,
		Answers:       f.Answer,
		Vision:        !f.NoVision,
		HybridVision:  !f.NoVision && f.HybridVision,
		Model:         f.Model,
		Log:           func(m string) { errln("# " + m) },
		Trace:         trace,
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 32
	}
	var result GoalResult
	runErr := withUia2(res.Serial, func() error {
		if f.NoAnimations {
			return WithAnimationsOff(res.Serial, func() error {
				var err error
				result, err = RunGoal(opts)
				return err
			})
		}
		var err error
		result, err = RunGoal(opts)
		return err
	})
	// A swallowed Ctrl-C wins the exit code, but the run error it caused is
	// still reported on stderr — the restore above already ran.
	if SIGINTExitCode != 0 {
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "droidjev: "+Redact(runErr.Error()))
		}
		return SIGINTExitCode, nil
	}
	if runErr != nil {
		return 0, runErr
	}
	trace.RecordResult(map[string]any{
		"status":        result.Status,
		"blockedReason": result.BlockedReason,
		"tookMs":        result.TookMs,
		"tokens":        result.Tokens,
	})
	if f.JSON {
		if err := printJSON(result); err != nil {
			return 0, err
		}
	} else {
		for _, s := range result.Steps {
			suffix := ""
			if s.Changed != nil {
				if *s.Changed {
					suffix = " ✓"
				} else {
					suffix = " ✗(no change)"
				}
			}
			outln(s.Action + suffix)
		}
		app := "?"
		if result.FinalApp != nil {
			app = *result.FinalApp
		}
		outln(fmt.Sprintf("status=%s steps=%d %.1fs tokens=%s/%s app=%s",
			result.Status, len(result.Steps), float64(result.TookMs)/1000,
			tokenNumber(result.Tokens.Input), tokenNumber(result.Tokens.Output), app))
		if result.Answer != nil {
			outln("answer: " + *result.Answer)
		}
	}
	return actExitCode(result.Status), nil
}

// tokenNumber is String(n) for the token counters: no exponent switch.
func tokenNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func cmdCheck(f Flags) (int, error) {
	goal := ""
	if len(f.Args) > 1 {
		goal = f.Args[1]
	}
	if goal == "" {
		return 0, NewDroidJevError(`check needs a goal, e.g. droidjev check "build a route from A to B"`)
	}
	res, err := EnsureDevice(f.Device)
	if err != nil {
		return 0, err
	}
	for _, n := range res.Notices {
		errln("# " + n)
	}
	if err := WakeAndUnlock(res.Serial); err != nil {
		return 0, err
	}
	verdict, err := RunCheck(CheckOpts{
		Serial: res.Serial,
		Goal:   goal,
		Values: GoalTexts(goal),
		Model:  f.Model,
		Log:    func(m string) { errln("# " + m) },
	})
	if err != nil {
		return 0, err
	}
	if f.JSON {
		if err := printJSON(verdict); err != nil {
			return 0, err
		}
	} else {
		outln(FormatCheck(verdict.CheckVerdictResult))
	}
	return CheckExitCode(verdict.CheckVerdictResult), nil
}

func cmdDecide(f Flags) (int, error) {
	if f.Serve {
		if err := ServeDecide(os.Stdin, os.Stdout, Ask); err != nil {
			return 0, err
		}
		return 0, nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0, err
	}
	var req DecideRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return 0, NewDroidJevError("decide needs a JSON request on stdin (" + Redact(err.Error()) + ")")
	}
	if req.Model == nil && f.Model != "" {
		req.Model = &f.Model
	}
	res, err := Decide(req, Ask)
	if err != nil {
		return 0, err
	}
	return 0, printJSON(res)
}

// preflight fails fast on missing prerequisites instead of failing mid-run:
// every device command needs adb; act/check/decide additionally need the
// Typesafe key BEFORE we spend a minute booting an emulator.
func preflight(cmd string) error {
	if cmd == "decide" {
		_, err := RequireApiKey() // no device involved: the caller supplies the element table
		return err
	}
	r, err := Run("adb", []string{"version"}, RunOpts{TimeoutMs: 8000})
	if err != nil {
		return err
	}
	if r.Code != 0 && !strings.Contains(string(r.Stdout), "Android Debug Bridge") {
		return NewDroidJevError(`"adb" is not usable — install Android platform-tools and ensure adb is in PATH`)
	}
	if cmd == "act" || cmd == "check" {
		_, err := RequireApiKey() // clear message when TYPESAFE_API_KEY is missing
		return err
	}
	return nil
}

// preflightFn is the preflight seam: tests replace it to exercise dispatch
// without an adb on PATH.
var preflightFn = preflight

// reportError prints one redacted error line and returns its exit code:
// DroidJevError's own code, else 3.
func reportError(err error) int {
	code := 3
	var dje *DroidJevError
	if errors.As(err, &dje) {
		code = dje.ExitCode
	}
	fmt.Fprintln(os.Stderr, "droidjev: "+Redact(err.Error()))
	return code
}

// runCLI is main without os.Exit: it returns the process exit code. Output is
// written straight to the unbuffered os.Stdout/os.Stderr, so nothing is lost
// on exit.
func runCLI(argv []string) int {
	flags, err := ParseArgs(argv)
	if err != nil {
		return reportError(err)
	}
	cmd := ""
	if len(flags.Args) > 0 {
		cmd = flags.Args[0]
	}
	if flags.Help || cmd == "" {
		outln(USAGE)
		if flags.Help {
			return 0
		}
		return 3
	}
	switch cmd {
	case "version":
		outln(version)
		return 0
	case "help":
		outln(USAGE)
		return 0
	}
	if err := preflightFn(cmd); err != nil {
		return reportError(err)
	}
	var code int
	switch cmd {
	case "devices":
		code, err = cmdDevices(flags)
	case "boot":
		code, err = cmdBoot(flags)
	case "snapshot":
		code, err = cmdSnapshot(flags)
	case "find":
		code, err = cmdFind(flags)
	case "start":
		code, err = cmdStart(flags)
	case "act":
		code, err = cmdAct(flags)
	case "check":
		code, err = cmdCheck(flags)
	case "decide":
		code, err = cmdDecide(flags)
	default:
		err = NewDroidJevError("unknown command: " + cmd + "\n\n" + USAGE)
	}
	if err != nil {
		return reportError(err)
	}
	return code
}

func main() {
	os.Exit(runCLI(os.Args[1:]))
}
