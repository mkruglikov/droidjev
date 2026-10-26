---
name: droidjev
description: Fast Android emulator UI automation, view-hierarchy first. Turns a natural-language goal into real taps on an Android emulator/AVD using the Typesafe jev model — open Settings, toggle a switch, scroll a list, type into a field; screens without accessibility nodes fall back to screenshot+box answers. Also provides deterministic primitives: find a label in any scrollable list, launch an app by package. Use whenever the user asks to open, click, tap, press, toggle, navigate to, scroll, search, or otherwise interact with anything on an Android emulator, AVD, or virtual device — even if they never say "click" (e.g. "open screen settings on emulator", "turn off wifi on the emulator", "go to About phone").
---

# droidjev

DroidJev (`droidjev`) acts on an Android emulator from a natural-language goal.
It dumps the view hierarchy (screenshots only for a blind screen or a `check`
verdict), asks the Typesafe jev model which indexed element
advances the goal, taps it via adb, and verifies the screen changed — all in
one CLI invocation. A typical 2-step goal finishes in ~6s. A powered-off
emulator is fine: it auto-boots the first AVD (adds ~30–60s).

**How to invoke:** run the `droidjev` binary from the repo if it is on PATH
(`droidjev …`), otherwise `node <repo>/droidjev …` with the DroidJev
repository path. Requires Node.js ≥ 20, `adb`, the `android` CLI on PATH, and
`TYPESAFE_API_KEY` in the environment. Missing prerequisites produce a clear
warning and stop — fix what they name, do not work around them.

## Preferred usage: ONE act call with the full flow

Pass the whole user request to a single `act` invocation. `act` navigates,
types, scrolls, copies and pastes, fills forms, and verifies the end state
inside one run:

```bash
droidjev act "copy the IMEI from settings and paste it into chrome search bar"
droidjev act "open the Contacts app and create a new contact named Maria, phone +79001234567" --text Maria --text +79001234567
droidjev act "open language settings, pick Русский, confirm the dialog"
```

Give `act` the complete compound goal in one shot, not a chain of micro-goals
— the model keeps context inside one run, and every restart re-navigates from
scratch. Do NOT step through snapshot → decide → tap manually. Values the goal
itself states — quoted strings, `from X to Y`, `named/phone/email` — are typed
from the goal; pass `--text` only for a value the goal does not spell out.
`act` never guesses text. The step budget (default 32) is a safety cap, not a
target: normal flows finish well below it.

## Deterministic helpers (optional)

When the caller already knows the exact package or label, these skip the
model and are useful for setup or a single targeted lookup:

```bash
droidjev start com.android.settings   # launch an app by package (am start/monkey)
droidjev find "About phone" --tap     # search scrollable lists top-down for a
                                      # label, tap the exact match (~0.6s/iteration)
droidjev snapshot --grep "battery"    # filtered view of the current screen
```

`find` suits a labeled item in a long list; `start` suits a goal that names an
app whose package is known (`adb shell pm list packages` lists them). For a
multi-step user task, still prefer one `act` call over composing these.

Beyond taps, typing and scrolling (both axes), the loop can long-press an item
to open its context menu (rename/delete/share), drag a slider to its minimum,
maximum, middle or a percentage, copy on-screen text to the clipboard and
paste it into a focused field, and — with `--answer` — submit an answer to a
question goal instead of changing the screen.

Read the last line: `status=done steps=3 6.3s tokens=4430/770 app=com.android.settings`.
Trust `status=done` — the model answers done only when the current screen
shows the goal achieved, and `app=` shows the focused app.

Exit codes: `0` done/found · `2` blocked/not found · `3` error (missing adb,
missing android CLI where needed, missing TYPESAFE_API_KEY, no AVD).

## Flags

| flag | meaning |
|---|---|
| `--device SERIAL\|AVD` | target when several emulators run (else: sole online, else auto-boot first AVD) |
| `--text "…"` | value to type when the goal does not state it — the model picks the field, never the text; values named in the goal (quoted, `from X to Y`, `named/phone/email`) are typed without it |
| `--guidance "…"` | (act) operator/app lore for this run — a plan, an app's mechanics, a constraint; repeatable. The model still chooses each step |
| `--app "Label=package"` | an app the model may open by name in one step — repeatable; a bare package is labelled by its last segment. It removes the walk through the launcher (and the ambiguity of two icons with the same label); the model still chooses which app the goal means |
| `--answer` | the goal is a question: the `answer_*` options are offered and the submitted text is printed as `answer:` on exit 0 |
| `--max-steps N` | action budget for act (default 32; a safety cap, not a target) |
| `--max-iterations N` | (find) scroll budget (default 20) |
| `--no-animations` | disable device animations for the run (restored after; steps verify sooner) |
| `--no-vision` | never send screenshots: a screen without accessibility nodes blocks (blind_screen) instead of falling back to image+box answers |
| `--hybrid-vision` | also send a screenshot when the element list is sparse (≤3 interactive rows) and the screen stopped changing; `--no-vision` wins |
| `--model ID` | model override for `act`/`decide` (default `jev-1.14.0`); a non-default model is never sent images — the vision fallback degrades to `blind_screen`, and `check` refuses it |
| `--tap` | (find) tap the best match instead of just reporting it |
| `--grep "…"` | (snapshot) print only rows containing the substring |
| `--trace` | (act) record every step to `~/.cache/droidjev/runs/` as JSONL (goal, screen rows, typed values, answers); also `DROIDJEV_TRACE=1`; owner-only file |
| `--json` | full machine-readable trace |

Typing example: `droidjev act "search settings for battery" --text battery`.
A value the goal states is typed from the goal (no flag needed): `droidjev act
"open maps and build a route for car from moscow to saint-petersburg"`. Pass
`--text` for a value the goal only implies; the tool never guesses text, and a
goal whose value can neither be read off the screen nor typed ends `blocked`.

## Is it impossible? (`check`)

`check` is the independent verdict: one screenshot, one jev image request, no
actions. Use it when the screen looks terminal (an explicit "no route", "not
available", "cannot be reached") or before giving up after `max_steps`.

```bash
droidjev check "build a route from moscow, ID to saint-petersburg, russia"
# impossible=yes confidence=0.91 kind=no_route   (exit 2)
# impossible=no  confidence=0.12 kind=empty_state (exit 0)
```

Only `no_route` / `unavailable` at confidence ≥ 0.7 count as impossible; an
empty list never does. A non-default `--model` is refused (images need
`jev-1.14.0`). The verdict is a confidence, not a quoted phrase — pair it with
`snapshot` when the same message is in the element table.

## Supervisor pattern (big model + act)

Keep a larger calling model out of the per-step loop: plan once, pass the plan
as guidance, and let jev execute.

1. The big model plans the flow.
2. `droidjev act "<full goal>" --guidance "<plan/app lore>"`.
3. If `act` blocks or hits `max_steps`, run `droidjev check "<goal>"`.
4. `impossible=yes` → stop; otherwise re-plan (a new `act` or a narrower
   subgoal).

Do not drive `decide` per step from the big model — that is two model round-trips
per step and gives up the loop's speed. Use `decide` only when the caller already
owns its observation/action pipeline.

## When it blocks

- `blockedReason=blind_screen` or an empty element list: the screen exposes no
  accessibility nodes (WebView or exotic renderer) AND the vision fallback
  could not run (unreadable screenshot, `--no-vision`). With vision on (the
  default), droidjev already drives such screens itself by screenshot — do not
  re-route them by hand unless it blocks here.
- `blockedReason=model`: the model judged the goal unreachable with the
  available actions (missing app, permission wall, login required, or a value
  the goal only implies — rerun with `--text`). Use `snapshot` to see what the
  screen actually offers.
- `status=max_steps`: the budget ran out mid-navigation — the goal may still
  be reachable; re-issue `act` (state persists on screen), decompose the
  goal, or raise `--max-steps`.

## Good to know

- Sleeping display is handled automatically (wake before every gesture); an
  emulator with a PIN lock stays blocked.
- A host that already has its own observation and action pipeline (a test
  harness, a web driver) can embed just the decision: `droidjev decide` reads a
  JSON request on stdin (`goal`, `elements`, `screen`, `provided_texts`,
  `apps`, `recent_actions`, `recent_choices`, `last_unchanged`, `can_scroll`,
  `copied`, `clipboard`, `clipboard_ready`, `answers`, `today`, `guidance`)
  and answers with the chosen operation, plus everything the host needs to
  execute it without re-deriving anything: `centers` (tap points), `texts`
  (each row's own text, for copy/answer), `drags` (the swipe a `set_*` slider
  stop needs) and `answer` (the resolved answer text). Same element table,
  same questions, same strict validation as `act`, no adb. Use `--serve` to
  keep one process for a whole run.
- `guidance` (array of strings) is how YOU teach droidjev the app it is
  driving: behavior you know that a screen dump cannot show ("this camera
  saves the video on the second shutter press", "this picker is one
  horizontally scrolling row"). droidjev hardcodes only platform mechanics —
  app lore belongs to the caller. Keep each entry one fact, stated as
  behavior, never as task steps.
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
