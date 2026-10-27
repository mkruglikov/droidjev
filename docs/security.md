# Security model

- The API key is read **only** from `TYPESAFE_API_KEY` at runtime (with a
  login-shell fallback for non-interactive environments). The key value never
  appears in files, logs, traces, `--json` output, process arguments, or error
  messages; keys are format-validated *before* use so no fetch error can quote
  them, and every printed message passes a bearer-token redactor.
- Every subprocess is spawned as an **argument array** — no `sh -c` anywhere.
- Text the model may type is operator input: `--text` values and values the
  goal itself names (quoted strings, `from X to Y`, `named/phone/email`). All
  pass the same charset that is inert in the device shell
  (`[A-Za-z0-9 .,:+=/@%^_-]`, space → `%s`); an explicit `--text` outside it is
  rejected naming the offending characters, and an untypeable goal value is
  simply not offered, never mangled.
- Model answers are strictly validated (choice ∈ offered options, exact
  probability coverage, finite values, sum ≈ 1, argmax consistency, bounded
  confidence); any invalid answer results in **no action executed**.
- The model never produces coordinates, selectors, or commands — every tap
  resolves in code from our element table. The one exception is the vision
  fallback (empty element tables, or a sparse table whose last step changed
  nothing, behind `--hybrid-vision`): the box answer's four edges are
  validated to 0–1 fractions forming an in-bounds rectangle before code
  converts them to a tap point, and a box confidence below the floor
  withholds the tap entirely. Screenshots (downscaled PNG) are sent only on
  that path — and, with `--hybrid-vision`, on low-information screens (≤3
  interactive rows and a step that changed nothing) — behind the
  `TypeSafe-Danger-Zone-Images` header, and never with the key in any log or
  trace; `--no-vision` disables both paths for privacy-sensitive screens.
- `check` sends exactly one downscaled screenshot through the same image path
  (pinned `jev-1.14.0`, `TypeSafe-Danger-Zone-Images` header), refuses a
  non-default model before capturing, and never acts. The verdict is validated
  like any model answer; no key or auth material is printed.
- Every subprocess and HTTP call has a timeout; response bodies are size-capped.
- **Traces are opt-in and owner-only.** `--trace` (or `DROIDJEV_TRACE=1`)
  writes one JSONL file per run under `~/.cache/droidjev/runs/`, created
  `0600`. A trace contains the goal text, screen rows, `--text` values and
  submitted answers — the same material the screen shows the operator — but
  never the API key or auth headers (every line passes `redact()`, and tests
  assert the live key value is absent). Delete the directory to purge. A trace
  that cannot write (full disk, read-only cache) disables itself with one
  notice instead of failing the episode.
- **Helper APKs are pinned.** The uiautomator2 server/test APKs and
  `io.appium.settings` are downloaded from pinned release URLs and verified
  against committed SHA-256 digests before install; the cache key carries the
  digest, so a stale or tampered cached file can never be reused. Scope: this
  covers the downloads droidjev makes — a device that already has the helper
  packages installed is used as-is, unverified.
- **Screen content is data, not instructions.** The prompt labels element text
  and `recent_actions` as untrusted data, and the model cannot execute anything
  outside the validated option set anyway. Residual risk: a hostile page could
  still steer the model toward a legal-but-wrong tap or a false `done`; the
  done-checker verifies every requirement before a done ends the episode.
- Audited by an independent adversarial review; the injection/validation walls
  held and all findings were fixed (tests in `test/unit.test.mjs`).
