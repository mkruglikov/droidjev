# Development

From a checkout: `git clone https://github.com/mkruglikov/droidjev`, then run
`./src/cli.js` in place — Node ≥ 20 is all it needs, there is nothing to build.

```bash
npm test          # unit tests (no network, no device)
npm run lint      # pinned eslint via npx — nothing installed
for f in src/*.js; do node --check "$f"; done   # syntax check — node --check
                                                # validates one file, a bare
                                                # src/*.js glob checks only
                                                # the first
```
