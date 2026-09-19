# How it works

How `droidjev act` turns a natural-language goal into taps: the agent loop,
the single per-step model request, and the resident layout server behind it.

jev matches by meaning across locales — a Russian "Настройки" goal finds
"Settings" on an English UI and vice versa. `find` is literal: its label must
be written in the UI's language.

```
        ┌────────────────────── one droidjev act invocation ───────────────────┐
        │                                                                      │
  layout dump ──► element table ──► ONE TypeSafe request ──► validate ──► adb input
  (≈50ms)        [12] click      operation   (choice:        strict      tap x y
                 "About phone"   click_12 /   click_<idx>,    checks      swipe /
                                 goal_met     scroll, …)                 keyevent
        ▲                                                            │
        └────────────── next dump verifies the action ◄───────────────┘
```

- **No screenshots.** The model consumes a compact, numbered element table
  built from the accessibility view hierarchy. It never sees pixels, and it
  never produces coordinates, selectors, or commands — only an index into our
  table, which code resolves to a tap position.
- **One decision per step.** A single request asks `operation` — with
  `click_<idx>`/`type_<idx>`/`copy_<idx>` options for concrete elements offered
  alongside scroll/back/home/paste/wait/done/blocked — and `goal_met` in parallel. The
  model cannot point at the right element while declining to click it: the
  target and the action are the same judgment, and its argmax is executed
  as-is. A key-less HEAD pre-warms the HTTPS connection during the initial
  dump, so the first request skips the per-process TLS handshake (~0.5s).
- **One dump per step.** The post-action settle polls the dump (~20–50 ms
  each) until two consecutive hierarchy hashes match — nothing is animating —
  then that settled dump doubles as the previous action's verification (hash
  comparison). Scroll flings get a short cap (a mid-flight dump is readable);
  screen transitions wait for motion to complete.
- **`copy_<idx>` copies text that cannot be selected.** Static labels (the
  "17" next to "Android version") have no long-press copy menu; the model
  picks the value row and its exact text goes to the device clipboard through
  the resident uia2 server — any Unicode, no shell, no typing, capped at the
  same 500 chars as `--text`. A clipboard write cannot change the view
  hierarchy, so a copy step skips the settle and re-dump entirely. The `paste`
  op completes the round trip: after a copy this run, the model taps a field,
  then pastes the clipboard into it (KEYCODE_PASTE) — "copy the Device name,
  then paste it into Chrome's search bar" is one goal. Paste is idempotent
  and verified: a field already holding the staged text is left alone
  (Chrome resumes with the omnibox still filled), and after each paste the
  loop checks the text reached an edit field — if not, it taps the clipboard
  suggestion most surfaces offer, and a remaining miss is recorded on the
  step ("did not land") so `done` cannot paper over it. `paste` is offered only
  once a copy has run in the same goal: it always moves the current clipboard
  content, so pre-existing clipboard text the run never copied is never typed
  anywhere, and the step log shows the exact value moved.
- **Animations on by default, `--no-animations` for speed.** Animations run
  unless asked otherwise; `--no-animations` zeroes the
  window/transition/animator scales for the run and restores them after —
  transitions snap to their final state, so the settle's hash polling exits in
  ~2 dumps. WAKE_UP rides in the same adb invocation as each gesture.
- **`done` is accepted on the first verdict.** One judgment per step, taken
  as-is: a former second-confirm question persistently disagreed on
  already-successful screens (ping-ponging runs to `max_steps`), so it was
  removed — the caller remains the source of truth for side-effects. The
  parallel `goal_met` probability is logged as a diagnostic only.
- **Budget-bounded, not guard-bounded.** The step budget is the only stop.
  A wrong tap is recoverable next step (back/home exist), so the model's
  chosen target is executed as-is; tuned early-stop guards turned
  slow-but-recoverable runs into false `blocked`s. Scroll options are offered
  only when the screen holds a scrollable container at least 40% of the screen
  tall — dialogs and other dead ends never offer a scroll whose fallback
  anchor (screen center) would land on the keyboard and glide-type into the
  focused field. Consecutive same-direction
  scrolls only pace the distance (long lists get longer swipes). Long labeled
  lists are better served by the deterministic `find`.
- **Doze-proof input.** Emulators intermittently ignore gestures while
  half-asleep; every gesture is preceded by WAKE_UP and `find` retries once
  after waking before declaring end-of-list.

## First run on a device

First layout use (`snapshot`/`find`/`act`) downloads
appium-uiautomator2-server plus its io.appium.settings companion (~21 MB,
three APKs) onto the emulator and starts a server that stays resident across
commands. The helper APKs are never a tap target: every element table hides
them and their launcher icons — an icon literally labeled "Appium Settings"
otherwise traps "open Settings" goals. Missing runtime prerequisites fail
fast with a warning that names the fix — before any emulator boot or API
call — and the tool stops (exit 3).
