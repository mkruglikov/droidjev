# AGENTS.md — droidjev

Zero-dependency Go CLI (stdlib only, single `package main`) that automates
Android emulators text-first: dumps the view hierarchy via a resident
appium-uiautomator2-server, asks the TypeSafe jev model which indexed element
advances the goal, taps it via `adb`, verifies the screen changed. Screenshots
are sent to the model only as a blind-screen fallback (see `shot.go`). Released
as a static darwin/arm64 binary through GitHub Releases — see Releasing.

## Commands

```bash
go test ./...                 # unit tests — no network, no device (default suite)
DROIDJEV_E2E=1 go test -run TestE2E -v ./...   # live e2e — spends real TypeSafe
                              # tokens, needs a booted emulator (+ Chrome for the
                              # paste scenario)
go vet ./...
gofmt -l .                    # empty output = formatted; CI fails otherwise
go build -o /tmp/droidjev .   # build the binary
go run . <cmd>                # run the CLI in place
```

## Releasing

- CI (`.github/workflows/ci.yml`) gates every push/PR: gofmt, `go vet`,
  `go test`, and a darwin/arm64 cross-compile of the release target.
- Publish by creating a GitHub Release tagged `X.Y.Z`: `release.yml`
  cross-compiles `GOOS=darwin GOARCH=arm64` (CGO off, `-trimpath`, `-s -w`,
  `-X main.version=${VERSION}` with the tag's leading `v` stripped), uploads
  the naked `droidjev-VERSION-darwin-arm64` binary and a `.sha256` next to it
  as release assets.
- The version lives in the release tag; `main.version` is `"dev"` in a local
  build. Nothing else in the tree carries a version to bump — the release tag
  is the version.
- There is no staging step: the uploaded assets are the release.

## Layout

- `main.go` — CLI entry: arg parsing, dispatch, `usage`, `main()`.
- `agent.go` — the `act` goal loop: builds the single per-step question
  (operation + goal_met in parallel), executes argmax as-is, double-checks
  `done`. An empty element table diverts to the vision step: one request with a
  screenshot asset carrying a validated choice + noul gate + box tap target.
- `check.go` — independent impossibility verdict: one screenshot, one jev
  image request (`noul` explicit-statement gate + `kind` choice + evidence
  `box`), strictly validated. Never acts; refuses a non-default model;
  `empty_state` is never impossible.
- `act.go` — executor: validated decision → adb input. Owns
  `SanitizeInputText` (the shell-inert `--text` charset). WAKE_UP rides in the
  same adb invocation as each gesture (doze-proof input).
- `uia2.go` — resident uia2-server lifecycle (download/install/start, HTTP
  over an adb-forwarded port; APKs cached in `~/.cache/droidjev`).
- `layout.go` / `elements.go` — parse uia2 XML → numbered element table,
  hierarchy hash, scroll anchor. One dump per step; the settle polls dumps until
  two consecutive hashes match.
- `adb.go` — thin adb wrapper; `boot.go` — device pick, AVD boot, wake/unlock,
  `WithAnimationsOff`; `find.go` — deterministic label search; `start.go` —
  launch by package; `snapshot.go` — table print.
- `decide.go` — the device-free `decide` protocol (one JSON request in, one
  validated answer out; `--serve` keeps the process).
- `skills.go` — goal macros (keypad sequence, route texts, table rows); `skills_test.go`
  pins the parsers.
- `typesafe.go` — API key handling + strict response validation (choice, noul,
  box). Model is pinned to `jev-1.14.0` (box/images are 1.14-only).
- `shot.go` — vision-fallback capture: raw `adb exec-out screencap` (RGBA,
  12/16-byte header detected by payload length) → box-average downscale into
  TypeSafe's image limits → stdlib `image/png` encode.
- `jstext.go` / `ordered.go` — JS string semantics the prompts depend on
  (UTF-16 length, slice, trim, `JSON.stringify` quoting) and insertion-ordered
  maps/sets.
- `trace.go` — opt-in JSONL episode tracing under `~/.cache/droidjev/runs/`.
- `util.go` — `Run()` spawner (argument arrays), `DroidJevError`, `Redact()`.
  Shared floor for the security rules below; import from here, don't
  reimplement.
- `*_test.go` — offline unit tests; `e2e_test.go` is opt-in (see Commands).
  `testdata/golden/` + `testdata/golden-fixtures.json` are the byte-equality
  prompt goldens.
- `skill/SKILL.md` — source of the installable agent skill; keep in sync with
  CLI behavior when commands/flags change.
- `.github/workflows/` — `ci.yml` and `release.yml` (see Releasing).
- All `.go` files live at the repo root in one `package main` on purpose: the
  CLI is one binary, and tests reach unexported helpers without an `internal/`
  split.

## Hard invariants (security model — tested in `*_test.go`)

- Every subprocess is spawned as an **argument array** via `util.Run()` — never
  `sh -c`, never an interpolated command string.
- The API key is read only from `TYPESAFE_API_KEY` at runtime and must never
  appear in files, logs, traces, `--json`, argv, or error messages; everything
  printed passes through `Redact()`.
- Model answers are strictly validated in `typesafe.go` (choice ∈ offered
  options, probability sums, argmax consistency); an invalid answer executes
  **no action**. The model never produces coordinates/selectors/commands — only
  an index into our element table, resolved to a tap position in code. The one
  exception: on a blind screen (empty table) the vision fallback sends a
  downscaled screenshot and the box answer's four 0–1 fractions are validated
  to an in-bounds rectangle (docs/security.md) before code resolves them to a
  tap point; `--no-vision` disables that path. Images require the
  `TypeSafe-Danger-Zone-Images` header — sent only on vision requests.
- Text the model may type is operator input: `--text` values plus values the
  goal itself names (quoted, `from X to Y`, `named/phone/email`). All pass
  `SanitizeInputText`; an explicit value outside `[A-Za-z0-9 .,:+=/@%^_-]` is
  rejected naming the offending characters, an untypeable goal value is dropped.
- `check` sends exactly one screenshot through the same danger-zone image path
  (`TypeSafe-Danger-Zone-Images`), refuses a non-default model before capture,
  and never acts. A `no_route`/`unavailable` answer below `CHECK_CONFIDENCE`
  (0.7), or any `empty_state`/`other` answer, is not reported impossible.
- Every subprocess/HTTP call has a timeout; response bodies are size-capped.

## Gotchas

- Exit codes: `0` done/found · `2` blocked (blockedReason in output) · `3`
  error (`DroidJevError` carries `ExitCode`).
- **Criteria order is prompt order.** `BuildQuestions` assembles its criteria
  into `*OrdMap`/`*OrdSet` (`ordered.go`) in the exact original insertion order;
  a bare Go map marshals alphabetically and silently changes the prompt. Never
  use a plain map where criteria are built.
- **Four RE2 emulations** stand in for JS lookaround, each hand-written:
  `Redact`'s bearer pattern (`[\s\S]*?(?="|$)` lookahead), `GoalRequirements`'
  sentence split (`(?<=[.!?])\s+` lookbehind), `GoalTexts`' route-endpoint
  terminator (`(?=[.!?]|,\s+(?:and|then)\b|$)`), and `KeypadSequence`'s
  leading-zero strip (`^0+(?=\d)`). Change them only with the goldens.
- **Golden fixtures are byte-equality gates.** The Go tests rebuild every
  question and compare bytes against `testdata/golden/`. There is no generator
  in the tree — regenerate only by checking out the commit before the rewrite
  and running `test/golden-generate.mjs` there.
- `UTF16Truncate` drops a straddled surrogate pair whole (the emoji is removed,
  not halved) — a deliberate choice recorded in docs/known-limits.md.
- The hot loop must never shell out to the `android` CLI (~670 ms startup tax);
  raw adb only. The `android` CLI is used solely to boot an AVD.
- UiAutomation is exclusive device-wide: the resident uia2 server displaces the
  `android` CLI's layout instrumentation
  (`adb shell am force-stop io.appium.uiautomator2.server` hands it back).
- Clipboard writes can't change the hierarchy — copy steps skip the settle and
  re-dump entirely (`copy_<idx>`/`paste` ops).
- `find` scrolls to the top of the list first (an up-scroll that changes
  nothing twice), then scans down; it stops as soon as scrolling provably
  cannot help, and retries once after waking before declaring end-of-list.
- Vision fallback is jev-1.14-only (`box`) and blind-screens-only; a failed
  screenshot pipeline degrades to `blocked:blind_screen`, never to an error.
  A box tap that lands on a readable interactive row snaps to that row's exact
  center (`SnapBoxToRow`; container-sized rows never snap).
  Two blind screens hash identically (both tables are empty), so vision steps
  cannot rely on the hash for change detection.
- README.md documents user-facing behavior and links the deep dives in `docs/`
  (how-it-works, speed, security, known-limits, development) — update them
  alongside behavior changes.
