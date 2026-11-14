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
- Label truncation cuts at a UTF-16 boundary. When an astral character (an
  emoji or similar) straddles the cut, the whole character is dropped instead
  of leaving half a surrogate pair (the Node implementation kept the lone
  surrogate).
- A locked emulator with a PIN stays blocked (wake + MENU only dismisses a
  non-secure keyguard).
- `find` scrolls top-down only; run it from the top of a list for labels that
  may sit above the current position.
- **Stale taps when a scroll opens a drawer**: the post-scroll settle compares
  table content only (labels and states — positions are deliberately ignored
  so a scroll's fling noise cannot fake progress). An opening animation that
  is a pure translation (the Pixel launcher's app drawer) keeps identical
  content while it moves, so the settled dump can carry mid-flight bounds and
  the tap lands one icon off (observed: tapping "Contacts" opened Messages,
  the icon directly beneath). `--no-animations` makes the drawer snap in
  ~1 frame and removes the race; older launchers (API 33) settle before the
  first poll.
- Default target is emulators; driving a physical phone requires explicit
  `--device`.
- `--trace` files contain the goal and on-screen values; they live only under
  `~/.cache/droidjev/runs/` (mode 0600) and are never written inside the repo.
