# Security model

- The API key is read **only** from `TYPESAFE_API_KEY` at runtime (with a
  login-shell fallback for non-interactive environments). The key value never
  appears in files, logs, traces, `--json` output, process arguments, or error
  messages; keys are format-validated *before* use so no fetch error can quote
  them, and every printed message passes a bearer-token redactor.
- Every subprocess is spawned as an **argument array** — no `sh -c` anywhere.
- `--text` is reduced to a charset that is inert in the device shell
  (`[A-Za-z0-9 .,:+=/@%^_-]`, space → `%s`); everything else is rejected with
  the offending characters named, never mangled.
- Model answers are strictly validated (choice ∈ offered options, exact
  probability coverage, finite values, sum ≈ 1, argmax consistency, bounded
  confidence); any invalid answer results in **no action executed**.
- The model never produces coordinates, selectors, or commands — every tap
  resolves in code from our element table. The one exception is the vision
  fallback (empty element table only): the box answer's four edges are
  validated to 0–1 fractions forming an in-bounds rectangle before code
  converts them to a tap point, and a box confidence below the floor
  withholds the tap entirely. Screenshots (downscaled PNG) are sent only on
  that path, behind the `TypeSafe-Danger-Zone-Images` header, and never with
  the key in any log or trace; `--no-vision` disables the path for
  privacy-sensitive screens.
- Every subprocess and HTTP call has a timeout; response bodies are size-capped.
- Audited by an independent adversarial review; the injection/validation walls
  held and all findings were fixed (tests in `test/unit.test.mjs`).
