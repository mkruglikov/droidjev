# Known limits

- **Blind screens**: some surfaces (WebView content, certain search UIs)
  expose no accessibility nodes → `blockedReason=blind_screen`.
- Typing is ASCII-only (see the `--text` charset in the
  [security model](security.md)); no IME/emoji.
- A locked emulator with a PIN stays blocked (wake + MENU only dismisses a
  non-secure keyguard).
- `find` scrolls top-down only; run it from the top of a list for labels that
  may sit above the current position.
- Default target is emulators; driving a physical phone requires explicit
  `--device`.
