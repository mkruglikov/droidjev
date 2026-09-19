# AGENTS.md — droidjev

Zero-dependency Node.js ≥ 20 CLI (ESM, `"type": "module"`) that automates
Android emulators without screenshots: dumps the view hierarchy via a resident
appium-uiautomator2-server, asks the TypeSafe jev model which indexed element
advances the goal, taps it via `adb`, verifies the screen changed. Do not add
npm dependencies or a `package-lock.json`; stdlib only. Published to npm as
`droidjev` (`npm i -g droidjev` / `npx droidjev`) — releases go through CI,
see Releasing.

## Commands

```bash
npm test                     # unit tests — no network, no device (default suite)
DROIDJEV_E2E=1 npm test      # live e2e — spends real TypeSafe tokens, needs a
                             # booted emulator (+ Chrome for the paste scenario)
npm run test:coverage        # unit tests + built-in coverage report
npm run lint                 # pinned eslint via npx — nothing installed, no deps added
npm run format               # pinned prettier via npx — same zero-install trick
npm run format:check         # prettier --check — what CI enforces
for f in src/*.js; do node --check "$f"; done   # syntax check (node --check
                             # validates ONE file — a bare src/*.js glob checks
                             # only the first and exits 0)
./src/cli.js <cmd>           # run the CLI in place (shebang'd entry; package.json
                             # bin points here — the command name stays `droidjev`)
npm pack --dry-run           # inspect the npm tarball before publishing
```

## Releasing

- CI (`.github/workflows/ci.yml`) gates every push/PR: tests on node 20 and
  24 (`engines` floor + `.nvmrc`), eslint, prettier --check, per-file
  `node --check`.
- Publish by creating a GitHub Release tagged `vX.Y.Z`: `release.yml` runs the
  tests and `npm stage publish --provenance` (needs the `NPM_TOKEN` repo
  secret; staging works with any token type, no 2FA bypass required). The
  version goes live only after a maintainer runs `npm stage approve
  <stage-id>` locally (interactive 2FA). Don't stage from outside CI —
  provenance attestation only works there — and don't bump `version`
  without releasing; the release tag must match it.
- Tarball contents are the `files` field (`droidjev`, `src`, `skill`) plus
  README/LICENSE/package.json; `prepublishOnly` re-runs the tests as a local
  backstop.

## Layout

- `src/cli.js` — CLI entry (shebang'd, `package.json` `bin` → this file): arg
  parsing, dispatch, `bootstrap()` with the main-module guard.
- `src/agent.js` — the `act` goal loop: builds the single per-step question
  (operation + goal_met in parallel), executes argmax as-is, double-checks `done`.
- `src/act.js` — executor: validated decision → adb input. Owns
  `sanitizeInputText` (the shell-inert `--text` charset). WAKE_UP rides in the
  same adb invocation as each gesture (doze-proof input).
- `src/uia2.js` — resident uia2-server lifecycle (download/install/start, HTTP
  over an adb-forwarded port; APKs cached in `~/.cache/droidjev`).
- `src/layout.js` / `src/elements.js` — parse uia2 XML → numbered element table,
  hierarchy hash, scroll anchor. One dump per step; the settle polls dumps until
  two consecutive hashes match.
- `src/adb.js` — thin adb wrapper; `src/boot.js` — device pick, AVD boot,
  wake/unlock, `withAnimationsOff`; `src/find.js` — deterministic label search;
  `src/start.js` — launch by package; `src/snapshot.js` — table print.
- `src/typesafe.js` — API key handling + strict response validation.
- `src/util.js` — `run()` spawner, `DroidJevError`, `redact()`. Shared floor for
  the security rules below; import from here, don't reimplement.
- `test/unit.test.mjs` (offline), `test/e2e.test.mjs` (opt-in, see Commands).
- `skill/SKILL.md` — source of the installable agent skill; keep in sync with
  CLI behavior when commands/flags change.
- `.github/workflows/` — `ci.yml` and `release.yml` (see Releasing).
  `.nvmrc` pins node 24; `.prettierrc` — singleQuote, printWidth 120.

## Hard invariants (security model — tested in unit.test.mjs)

- Every subprocess is spawned as an **argument array** via `util.run()` — never
  `sh -c`, never an interpolated command string.
- The API key is read only from `TYPESAFE_API_KEY` at runtime and must never
  appear in files, logs, traces, `--json`, argv, or error messages; everything
  printed passes through `redact()`.
- Model answers are strictly validated in `typesafe.js` (choice ∈ offered
  options, probability sums, argmax consistency); an invalid answer executes
  **no action**. The model never produces coordinates/selectors/commands — only
  an index into our element table, resolved to a tap position in code.
- `--text` values pass `sanitizeInputText`; anything outside
  `[A-Za-z0-9 .,:+=/@%^_-]` is rejected naming the offending characters.
- Every subprocess/HTTP call has a timeout; response bodies are size-capped.

## Gotchas

- Exit codes: `0` done/found · `2` blocked (blockedReason in output) · `3`
  error (`DroidJevError` carries `exitCode`).
- The hot loop must never shell out to the `android` CLI (~670 ms startup tax);
  raw adb only. The `android` CLI is used solely to boot an AVD.
- UiAutomation is exclusive device-wide: the resident uia2 server displaces the
  `android` CLI's layout instrumentation
  (`adb shell am force-stop io.appium.uiautomator2.server` hands it back).
- Clipboard writes can't change the hierarchy — copy steps skip the settle and
  re-dump entirely (`copy_<idx>`/`paste` ops).
- `find` scrolls top-down only; it stops as soon as scrolling provably cannot
  help, and retries once after waking before declaring end-of-list.
- `src/cli.js` is the npm `bin` target: keep the `#!/usr/bin/env node` shebang
  and the main-module guard with `realpathSync(process.argv[1])` — Node
  realpaths module URLs but not argv[1], so the bare comparison silently
  never fires through npm's bin symlink. The guard makes the file runnable as
  the `droidjev` command while keeping imports (tests) inert.
- README.md documents user-facing behavior and links the deep dives in `docs/`
  (how-it-works, speed, security, known-limits, development) — update them
  alongside behavior changes.
