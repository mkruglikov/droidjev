# Development

From a checkout: `git clone https://github.com/mkruglikov/droidjev`, then run
`go run .` in place — Go ≥ 1.27 and the standard library are all it needs;
there are no external modules to fetch.

```bash
go test ./...                 # unit tests (no network, no device)
DROIDJEV_E2E=1 go test -run TestE2E -v ./...   # live e2e: real tokens + a
                                               # booted emulator (+ Chrome)
go vet ./...
gofmt -l .                    # empty output = formatted
go build -o /tmp/droidjev .   # build a binary
```

The prompt goldens under `testdata/golden/` are byte-equality gates: every
question droidjev builds is compared with the fixture. They were captured from
the former Node implementation and are never regenerated in CI; changing a
prompt string means updating the matching fixture deliberately.

`--trace` (or `DROIDJEV_TRACE=1`) records a run to
`~/.cache/droidjev/runs/` as JSONL. These are local diagnostics for one
investigation; they are never committed to the repository.
