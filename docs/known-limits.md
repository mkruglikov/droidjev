# Known limits

- **Blind screens**: some surfaces (WebView content, certain search UIs)
  expose no accessibility nodes. The vision fallback (jev 1.14 images + box,
  enabled by default) drives these by screenshot; `--no-vision` or an
  unreadable screenshot still ends `blockedReason=blind_screen`. With
  `--hybrid-vision`, a sparse table (≤3 interactive rows) whose last step
  changed nothing also gets the screenshot alongside its rows. Image
  grounding is prototype-grade ("Dreamer-ready") — expect occasional
  mis-taps on dense pages.
- Typing is ASCII-only (see the `--text` charset in the
  [security model](security.md)); no IME/emoji.
- A locked emulator with a PIN stays blocked (wake + MENU only dismisses a
  non-secure keyguard).
- `find` scrolls top-down only; run it from the top of a list for labels that
  may sit above the current position.
- Default target is emulators; driving a physical phone requires explicit
  `--device`.
- `--trace` files contain the goal and on-screen values; they live only under
  `~/.cache/droidjev/runs/` (mode 0600) and are never written inside the repo.
