---
name: droidjev
description: Fast Android emulator UI automation without screenshots. Turns a natural-language goal into real taps on an Android emulator/AVD using the view hierarchy and the Typesafe jev model — open Settings, toggle a switch, scroll a list, type into a field. Also provides deterministic primitives: find a label in any scrollable list, launch an app by package. Use whenever the user asks to open, click, tap, press, toggle, navigate to, scroll, search, or otherwise interact with anything on an Android emulator, AVD, or virtual device — even if they never say "click" (e.g. "open screen settings on emulator", "turn off wifi on the emulator", "go to About phone").
---

# droidjev

DroidJev (`droidjev`) acts on an Android emulator from a natural-language goal.
It dumps the view hierarchy (never screenshots), asks the Typesafe jev model
which indexed element advances the goal, taps it via adb, and verifies the
screen changed — all in one CLI invocation. A typical 2-step goal finishes in
~6s. A powered-off emulator is fine: it auto-boots the first AVD (adds
~30–60s).

**How to invoke:** run the `droidjev` binary from the repo if it is on PATH
(`droidjev …`), otherwise `node <repo>/droidjev …` with the DroidJev
repository path. Requires Node.js ≥ 20, `adb`, the `android` CLI on PATH, and
`TYPESAFE_API_KEY` in the environment. Missing prerequisites produce a clear
warning and stop — fix what they name, do not work around them.

## Fast paths first (deterministic, no model, no flailing)

Do these directly when the task matches; they are faster and more reliable
than `act`:

```bash
droidjev start com.android.settings   # launch an app by package (am start/monkey)
droidjev find "About phone" --tap     # search scrollable lists top-down for a
                                      # label, tap the exact match (~0.6s/iteration)
droidjev snapshot --grep "battery"    # filtered view of the current screen
```

Use `find` whenever the target is a labeled item in a long list (settings,
language pickers, app lists). Use `start` whenever the goal names an app and
you know or can resolve its package (`adb shell pm list packages` lists them).

## Primary usage: ONE act call per goal

For anything not covered by the fast paths, run the whole goal as a single
`act` command — do NOT step through snapshot → decide → tap manually; that
defeats the tool's speed.

```bash
droidjev act "open Settings"
droidjev act "open About phone"
droidjev act "toggle Airplane mode on"
```

Give `act` the complete compound goal in one shot ("open language settings,
pick Русский, confirm the dialog"), not a chain of micro-goals — the model
keeps context inside one run and every restart re-navigates from scratch.

Read the last line: `status=done steps=3 6.3s tokens=4430/770 app=com.android.settings`.
Trust `status=done` — the model answers done only when the current screen
shows the goal achieved, and `app=` shows the focused app.

Exit codes: `0` done/found · `2` blocked/not found · `3` error (missing adb,
missing android CLI where needed, missing TYPESAFE_API_KEY, no AVD).

## Flags

| flag | meaning |
|---|---|
| `--device SERIAL\|AVD` | target when several emulators run (else: sole online, else auto-boot first AVD) |
| `--text "…"` | value to type when the goal needs typing — the model picks the field, never the text; repeat once per value the goal names (`--text Maria --text +79001234567`) |
| `--max-steps N` | action budget for act (default 12) |
| `--max-iterations N` | (find) scroll budget (default 20) |
| `--no-animations` | disable device animations for the run (restored after; steps verify sooner) |
| `--tap` | (find) tap the best match instead of just reporting it |
| `--grep "…"` | (snapshot) print only rows containing the substring |
| `--json` | full machine-readable trace |

Typing example: `droidjev act "search settings for battery" --text battery`.
Goals involving typing REQUIRE `--text`; without it the tool cannot guess what
to type (by design — no text generation). Pass one `--text` per value the goal
names — a goal mentioning a value you did not pass ends `blocked` (the model
will not type a wrong value into the field).

## When it blocks

- `blockedReason=blind_screen` or an empty element list: the screen exposes no
  accessibility nodes (WebView or exotic renderer). Fall back to the
  android-cli screenshot flow (skill `android-cli`, reference `interact.md`).
- `blockedReason=model`: the model judged the goal unreachable with the
  available actions (missing app, permission wall, login required, or the goal
  names a value that was not passed via `--text` — rerun with it). Use
  `snapshot` to see what the screen actually offers.
- `status=max_steps`: the budget ran out mid-navigation — the goal may still
  be reachable; re-issue `act` (state persists on screen), decompose the
  goal, or raise `--max-steps`.

## Good to know

- Sleeping display is handled automatically (wake before every gesture); an
  emulator with a PIN lock stays blocked.
- Do not call `uiautomator` by hand — UiAutomation is exclusive device-wide
  and conflicts with droidjev's resident layout server; `snapshot` already
  exposes the same data.
- Do not run host tools (`rg`, `jq`) inside `adb shell` — the device has its
  own limited toolbox. Capture output and process it on the host
  (`snapshot --json` emits `rows` + `centers`).
- After risky changes (language, network), verify state programmatically
  (e.g. `adb shell settings get global airplane_mode_on`) instead of re-reading
  the screen.
- Between two `snapshot` calls there should always be an action — two
  identical snapshots are wasted seconds.
