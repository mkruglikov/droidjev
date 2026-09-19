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
- Every subprocess and HTTP call has a timeout; response bodies are size-capped.
- Audited by an independent adversarial review; the injection/validation walls
  held and all findings were fixed (tests in `test/unit.test.mjs`).
