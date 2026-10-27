# DroidJev (`droidjev`)

A fast, text-first Android emulator clicker powered by
[TypeSafe](https://docs.typesafe.ai)'s **jev** (System One) model.

You give it a natural-language goal; it dumps the **view hierarchy**, asks jev
which indexed element advances the goal, taps it via `adb`, verifies the screen
changed, and repeats until the goal is achieved. One CLI call per goal. A
powered-off emulator is fine — it auto-boots the first AVD.

## Demo

```bash
droidjev act "copy the IMEI from settings and paste it into chrome search bar"
```

![Reading the IMEI from Settings](assets/IMEI.gif)

```bash
droidjev act "open the Contacts app and create a new contact named Maria, phone +79001234567" --text Maria --text +79001234567
```

![Adding a contact](assets/contact.gif)

## What `act` can do

Each step, the model picks from what the screen actually offers: tap a visible
element, **long-press** one for its context menu (rename, delete, share), **type**
a `--text` value into a field, **drag a slider** to its minimum, maximum, middle
or a percentage, **scroll** vertically and horizontally, **copy** on-screen text
to the clipboard and **paste** it into a focused field, **open an app by name**
(`--app`, one step from anywhere), and — for a question goal — **answer** with
the text it found on screen (`--answer`) instead of changing anything. It never
types a value you did not pass, and it never invents coordinates: every action
resolves to a row of the dumped element table. The one exception is the
**vision fallback**: when a screen exposes no accessibility nodes at all (WebView
content, canvas renderers — previously a guaranteed dead end), the model is sent
a downscaled screenshot and may answer with a strictly validated bounding box
that code resolves to the tap point (`--no-vision` keeps even that off).

## Prerequisites

1. **Node.js ≥ 20** — required for everything; droidjev has zero npm
   dependencies.
   Install: `brew install node` · `nvm install 20` · [nodejs.org](https://nodejs.org).
   Verify: `node -v` prints `v20` or newer.
2. **`adb`** (Android platform-tools) — required by every device command.
   Install: `brew install --cask android-platform-tools`.
   Verify: `adb version`.
3. **`android` CLI** — optional; needed only when droidjev must boot an AVD
   itself (no emulator online, `boot`, or `--device <avd>`). Skip it if you
   start emulators yourself.
   Install: [developer.android.com/cli](https://developer.android.com/cli/install).
   Verify: `android emulator list` prints your AVDs.

## Installation

1. **`TYPESAFE_API_KEY`** — `act` and `check` talk to the model.
   Get a key at [docs.typesafe.ai](https://docs.typesafe.ai), then add
   `export TYPESAFE_API_KEY=…` to your shell profile.
2. **droidjev itself** — from npm (zero deps, exactly one package), or try it
   without installing:
   ```bash
   npm install -g droidjev
   npx droidjev devices            # no global install needed
   ```
3. **Verify:** `droidjev devices` — with an emulator up it lists it;
   `no devices online` is fine too (`droidjev boot` boots the first AVD).

The first `snapshot`/`find`/`act` downloads appium-uiautomator2-server
(~21 MB, three APKs) onto the emulator and starts a resident server — see
[How it works](https://github.com/mkruglikov/droidjev/blob/master/docs/how-it-works.md).

## Install the AI skill

`skill/SKILL.md` is the source of an agent skill: plain Markdown, usable by
any agent/harness where skills are folders containing a SKILL.md. Install by
copying it into your agent's skills directory, e.g.:

```bash
mkdir -p ~/.agent/skills/droidjev && cp skill/SKILL.md ~/.agent/skills/droidjev/SKILL.md
```

Or fetch it straight from the repo into your agent's skills directory
(`<AGENT_SKILLS_DIR>` = wherever your agent looks for skills, e.g.
`~/.agent/skills` or `~/.zcode/skills`):

```bash
mkdir -p <AGENT_SKILLS_DIR>/droidjev && curl -fsSL https://raw.githubusercontent.com/mkruglikov/droidjev/master/skill/SKILL.md -o <AGENT_SKILLS_DIR>/droidjev/SKILL.md
```

It teaches agents the fast paths (`start`, `find`), the one-call-per-goal rule
for `act`, and the fallbacks for blocked runs.

## Commands

```
droidjev devices                 list emulators with AVD names (raw adb, ~15ms)
droidjev boot [avd]              ensure an emulator is booted (auto-picks first AVD)
droidjev start <package>         launch an app by package (deterministic, no model)
droidjev snapshot [--grep "…"]   indexed element table of the current screen (no model)
droidjev find "<label>" [--tap]  deterministically search scrollable content top-down
                                 for a label and optionally tap it (~0.6s/iteration,
                                 no tokens); stops as soon as scrolling provably cannot help
droidjev act "<goal>" [flags]    run the agent loop until done/blocked/answered
                                 (answers a question goal with --answer)
droidjev check "<goal>" [flags]  one screenshot verdict: does the CURRENT screen
                                 explicitly say the goal cannot be done? 0 = not
                                 proven impossible, 2 = impossible (never acts)
droidjev decide [--serve]        one decision from a JSON request on stdin (one per
                                 line with --serve), no adb: the same element table,
                                 questions and strict validation `act` uses, for a host
                                 that already has its own observation and action
                                 pipeline (a benchmark harness, a web driver)
```

Flags: `--device SERIAL|AVD` · `--text "…"` (repeatable — one per form value;
values the goal itself states — quoted strings, `from X to Y`, `named/phone/email`
— are typed from the goal, so `--text` is only needed for a value the goal does
not spell out; text is never guessed) ·
`--app "Label=package"` (repeatable — an app the model may open by name in one
step; a bare package is labelled by its last segment) · `--guidance "…"`
(repeatable, act — operator/app lore for this run: a plan, an app's mechanics, a
constraint; the model still chooses each step) · `--answer` (the goal is a
question: offer the answer options and print the submitted answer) ·
`--max-steps N` (act, default 32) · `--max-iterations N` (find, default 20) ·
`--json` (full trace) · `--trace` (act: record every step to
`~/.cache/droidjev/runs/` as JSONL — the goal, screen rows, typed values and
answers; opt-in, also `DROIDJEV_TRACE=1`) · `--no-animations` (disable
animations for the run; restored after) · `--no-vision` (never send
screenshots — a screen with no accessibility nodes blocks instead of falling
back to image+box answers) · `--hybrid-vision` (also send a screenshot when
the element list is sparse — ≤3 interactive rows — and the screen stopped
changing; `--no-vision` wins) · `--model ID` (act/decide model override, default
`jev-1.14.0`; a non-default model is never sent screenshots, and `check` refuses
one outright).

Exit codes: `0` done/found · `2` not achieved — `status=blocked` with
`blockedReason` `model` (no action sequence can reach the goal, including a
value the goal only implies and that cannot be typed) or
`blind_screen` (the screen exposes no accessibility nodes and the vision
fallback is off or its screenshot is unreadable), or
`status=max_steps` (budget exhausted mid-navigation; the goal may still be
reachable — re-run, decompose, or raise `--max-steps`). `check` shares the
codes: `0` the screen does not prove impossibility, `2` it explicitly says the
goal cannot be done (`impossible=yes`) · `3` error —
including `status=error` with `blockedReason=infrastructure`: a device or
transport failure mid-run ends the episode, and the JSON still carries every
step taken so far.

## Documentation

- **[How it works](https://github.com/mkruglikov/droidjev/blob/master/docs/how-it-works.md)** —
  the agent loop, the numbered element table, copy/paste, cross-locale
  matching, and the resident uiautomator2 server.
- **[Speed](https://github.com/mkruglikov/droidjev/blob/master/docs/speed.md)** —
  per-step cost table and where the milliseconds go.
- **[Security model](https://github.com/mkruglikov/droidjev/blob/master/docs/security.md)** —
  API-key handling, subprocess spawning, and model-answer validation.
- **[Known limits](https://github.com/mkruglikov/droidjev/blob/master/docs/known-limits.md)** —
  blind screens and the vision fallback, ASCII-only typing, top-down `find`,
  emulator-first targeting.
- **[Development](https://github.com/mkruglikov/droidjev/blob/master/docs/development.md)** —
  running from a checkout, tests, lint.

## License

MIT License

Copyright (c) 2026 DroidJev contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
