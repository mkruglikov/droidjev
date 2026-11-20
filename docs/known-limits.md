# Known limits

- **Blind screens**: some surfaces (WebView content, certain search UIs)
  expose no accessibility nodes. The vision fallback (jev 1.14 images + box,
  enabled by default) drives these by screenshot; `--no-vision` or an
  unreadable screenshot still ends `blockedReason=blind_screen`. With
  `--hybrid-vision`, a sparse table (≤3 interactive rows) whose last step
  changed nothing also gets the screenshot alongside its rows. Image
  grounding is prototype-grade ("Dreamer-ready") — expect occasional
  mis-taps on dense pages. On hybrid screens a box tap that lands on a
  readable interactive row snaps to that row's dump-read center, which
  removes the near-miss class but not wrong-row choices.
- Typing is ASCII-only (see the `--text` charset in the
  [security model](security.md)); no IME/emoji.
- Label truncation cuts at a UTF-16 boundary. When an astral character (an
  emoji or similar) straddles the cut, the whole character is dropped instead
  of leaving half a surrogate pair.
- A device locked with a PIN/pattern/password cannot be driven — keys cannot
  dismiss a secure keyguard. It is reported up front as blocked (exit 2, "secure
  lock screen") instead of leaving the run to flail on the lock screen; unlock
  the device by hand or boot an AVD without a lock screen.
- `find` now scrolls to the top of a list before scanning down, so labels above
  the starting position are reachable — at the cost of a few extra iterations
  (the up-scroll stall dance) when the run starts mid-list.
- Default target is emulators; driving a physical phone requires explicit
  `--device`.
- `--trace` files contain the goal and on-screen values; they live only under
  `~/.cache/droidjev/runs/` (mode 0600) and are never written inside the repo.
