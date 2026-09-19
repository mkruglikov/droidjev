// The goal loop: one layout dump + one TypeSafe request per step. The model
// answers a single `operation` choice whose options include click_<idx> /
// type_<idx>_<k> / copy_<idx> for concrete elements (plus scroll/back/home/
// paste/wait/done/blocked)
// alongside a parallel `goal_met` judgment; the validated argmax is executed
// as-is and verified by the next step's dump (one dump per step, which
// matters: a dump costs ~1s and dominates latency).
import { getLayout, settleLayout } from './layout.js';
import { setClipboard } from './uia2.js';
import { buildTable, serializeRows, scrollAnchor, tableHash, isEditable } from './elements.js';
import { screenSize } from './adb.js';
import { ask, prewarm, validatedChoice, validatedNoul } from './typesafe.js';
import { tap, scrollSwipe, keyevent, typeText, sanitizeInputText, KEYS } from './act.js';
import { DroidJevError, sleep } from './util.js';

const MAX_ROWS_NOTE = 'the row cap';
// Mirrors the --text ceiling (act.js sanitizeInputText): the clipboard must
// never hold more via a copy than the operator could have typed by hand.
const MAX_COPY_CHARS = 500;

// Short JSON-quoted preview for step actions — the operator sees what moved.
const preview = (s) => JSON.stringify(s.length > 60 ? `${s.slice(0, 60)}…` : s);

// Adaptive scroll distance: each further scroll in the same direction covers
// more ground (long lists), capped at 60% of the screen — longer flings skip
// whole pages of paged content (launcher workspaces) and overshoot lists.
export function scrollDistance(screenH, sameDirRun) {
  return Math.min(Math.round(screenH * 0.4 * 1.25 ** sameDirRun), Math.round(screenH * 0.6));
}

// Scroll is offered only where it can reveal something: a scrollable
// container spanning a real part of the screen. Tiny dialog scrollviews and
// screens with no scrollable at all don't qualify — the anchor would fall
// back to screen center, which with the keyboard up lands ON the keyboard,
// and a swipe across keys glide-types garbage into the focused field.
export function scrollOffered(table, screen) {
  const h = screen?.h ?? 2340;
  return table.some((r) => r.el.scrollable && r.el.bounds && r.el.bounds.b - r.el.bounds.t >= h * 0.4);
}

// Did a paste land, and if not, is the staged text offered as a tappable
// clipboard suggestion (Chrome omnibox chip, IME toolbar)? Label matching is
// substring-on-row: cleanLabel keeps plain ASCII values verbatim.
// ponytail: values containing quotes or repeated spaces can miss — upgrade
// path is matching on the element's own text, not the serialized row.
export function pasteOutcome(table, staged) {
  const landed = table.some((r) => isEditable(r.el) && r.row.includes(staged));
  const chip = landed ? null : (table.find((r) => (r.el.clickable || r.tappable) && r.row.includes(staged)) ?? null);
  return { landed, chip };
}

const FIXED_OPS = {
  scroll_down:
    'The goal target is probably further down the current list/screen — but if a visible row opens a section that would CONTAIN the target (an "About phone"/system row holds IMEI, serial number, Android version), click that row instead of scrolling a long list hunting the value itself. Exception — launcher (screen.launcher): the drawer already lists ALL apps; if no icon matches the goal text, the goal lives inside an app — click the Settings app icon for any system/device page (network, Wi-Fi, Bluetooth, location, display, sound, battery, language, apps, security), do not scroll more.',
  scroll_up: 'The goal target is probably further up the current list/screen, or the previous scroll overshot',
  back: 'Leaving the current screen/dialog moves toward the goal',
  home: 'Going to the home screen moves toward the goal',
  paste:
    'The clipboard content (the copy in recent_actions) goes into the currently focused field — pick this right after clicking that field',
  wait: 'The screen is loading or animating; act next step',
  done: 'The goal is already achieved on the current screen',
  blocked:
    'No sequence of available actions can achieve the goal (missing app, permission wall, login required, a value the goal asks to enter that is neither provided nor shown on screen to copy)',
};

// One decision per step: the operation question offers click_<idx>/type_<idx>
// for concrete elements alongside the fixed ops. A separate "which element?"
// question let the model point at the right row (e.g. "WLAN" for a Wi-Fi
// goal on a Russian UI) while hedging the operation into endless scrolling —
// merging the choices forces the comparison the model actually understands.
export function buildQuestions({ table, texts = [], copied = false, canScroll = true }) {
  const criteria = {};
  for (const r of table) {
    if (r.el.clickable || r.el.longClickable || r.tappable) criteria[`click_${r.idx}`] = `Tap ${r.row}`;
    texts.forEach((t, k) => {
      if (isEditable(r.el)) criteria[`type_${r.idx}_${k}`] = `Tap ${r.row}, then type provided_texts[${k}] into it`;
    });
    if (r.el.text) criteria[`copy_${r.idx}`] = `Copy the text of ${r.row} to the device clipboard`;
  }
  // paste rides the CURRENT clipboard, so it is offered only after a copy this
  // run — otherwise a legal model answer could type pre-existing clipboard
  // content the operator never asked to move (see README: --text is the only
  // text the model may type; paste must stay bound to this run's copies).
  if (copied) Object.assign(criteria, { paste: FIXED_OPS.paste });
  Object.assign(criteria, {
    ...(canScroll ? { scroll_down: FIXED_OPS.scroll_down, scroll_up: FIXED_OPS.scroll_up } : {}),
    back: FIXED_OPS.back,
    home: FIXED_OPS.home,
    wait: FIXED_OPS.wait,
    done: FIXED_OPS.done,
    blocked: FIXED_OPS.blocked,
  });
  return {
    operation: {
      type: 'choice',
      instructions: {
        question: 'Pick the single next action that best advances the goal.',
        rules: [
          'click_<idx> options name a visible element — pick one whenever a visible element plausibly leads toward the goal, even if the goal words do not appear in its label. Match by MEANING, not literal text: the goal and the UI may be in different languages (a goal saying "Ajustes" matches "Settings" on an English UI; an English "Wi-Fi" goal matches "WLAN" on a German UI), and a settings page often hides one screen deeper than its title suggests.',
          'When the goal asks to turn something on/off, pick the click option whose row shows check=on/check=off — that is the actual toggle; a same-labeled row without check= is only a container. If the toggle already shows the state the goal needs, choose done instead of tapping it again.',
          'When the goal asks to copy text shown on screen, pick copy_<idx> on the row holding the exact text — the value element (e.g. the number beside "Android version"), not its label. Copies never change the screen: once recent_actions shows the right text copied, choose done.',
          'When the goal asks to paste copied text into a field, first click_<idx> the field to focus it, then pick paste — paste puts the clipboard content into the focused field. paste is offered only after a copy_<idx> step this run; the goal must name a copy first. If the focused field already shows the copied text, choose done — pasting again would duplicate it. If paste ran with no visible change, the field may not take the paste key: many surfaces offer the clipboard content as a tappable suggestion instead (the Chrome address bar shows it as a suggestion chip, keyboards as a toolbar clip) — click_<idx> the row showing the copied text to insert it.',
          'When screen.launcher is true you are on the home screen: scroll to open the app drawer, then click the app icon you need as soon as it is visible — scrolling further only pages through icons and never enters an app.',
          'If the goal target is a screen inside an app rather than the app itself (e.g. "About phone" inside Settings), first click the app icon, then navigate inside the app.',
          'When hunting a setting inside an app, visit each section at most once: if recent_actions shows you already entered and left a section without finding the target, do not enter it again — its rows were all seen; pick a different section row instead.',
          'A dialog or editor the goal did not ask for (e.g. renaming the device) is a wrong turn — close it with back or its cancel button and continue on the screen beneath.',
          'Search fields require typing, which is only possible when provided_texts is set; if provided_texts is null, never open a search field to type — scroll lists and read their items instead. Pasting a copied value into a search field is not typing: after a copy_<idx> step, click the field and paste.',
          "type_<idx>_<k> types provided_texts[k] and replaces that field's content entirely. When provided_texts has several values, each belongs in a DIFFERENT field — match value to field by meaning (a person's name is not a phone number). If recent_actions already shows the same value typed into a field, it is already there — judge the results on screen instead of typing again.",
          'Every text you may type comes from provided_texts. If the goal asks to enter a specific value (a name, an address) that is neither among provided_texts nor shown on the screen to copy, typing it is impossible — choose blocked right away instead of tapping the field repeatedly or typing a provided value that does not belong in it. A value the goal tells you to copy from the screen never needs provided_texts: copy_<idx> stages it on the clipboard, paste puts it into the focused field.',
          'Never repeat a click, type, or same-direction scroll that recent_actions shows already ran with no visible change — the same input on the same screen will not behave differently. Pick a different element or a different strategy (the other scroll direction, back, home), or conclude done/blocked.',
          'Use scroll_down/scroll_up when the target is likely just off-screen, or when you do not know which list item hides it — scroll and read the item labels and subtitles. If scrolling stops showing new content, check the other direction before deciding the target is not on this screen.',
          'Choose done only when the current screen shows the goal is achieved.',
          'If the goal names an app by name (e.g. "open Settings", "open Chrome"), done is valid only when screen.app is that app — settings screens inside a different app do not count.',
          'Choose blocked only when no available action sequence can achieve the goal.',
        ],
      },
      criteria,
    },
    goal_met: {
      type: 'noul',
      instructions:
        'Judging the elements and screen.app: is the goal already achieved? If the goal names an app, screen.app must be that app.',
      // docs.typesafe.ai/primitives/noul: pass criteria when the yes/no boundary
      // is subtle — goal achievement over a screen dump is exactly that.
      criteria: {
        true: 'Every part of the goal is achieved: the named app/screen is open (screen.app matches), toggles show the requested state, text the goal asked to type or paste is visible in a field, and the requested copy appears in recent_actions.',
        false:
          'Some part of the goal is not yet achieved, or cannot be confirmed from the current screen and recent_actions.',
      },
    },
  };
}

export async function runGoal({ serial, goal, texts = null, maxSteps = 12, animationsOff = false, log = () => {} }) {
  if (!goal || typeof goal !== 'string')
    throw new DroidJevError('a goal is required, e.g. droidjev act "open Settings"');
  // Accept one string or an array (repeatable --text). Validate at the trust
  // boundary so an untypable value fails before the first tap, not mid-form.
  const values = Array.isArray(texts) ? texts : texts ? [texts] : [];
  values.forEach((v) => sanitizeInputText(v));
  const t0 = performance.now();
  // The first dump starts the on-device uia2 server (~1s); the key-less
  // connection warm-up rides inside it.
  const [screen, first] = await Promise.all([screenSize(serial), getLayout(serial), prewarm()]);
  const { elements } = first;
  // Focused app from the dump's root package — dumpsys would cost an extra
  // adb roundtrip per step for the same answer.
  let rootPkg = first.rootPkg;
  const tokens = { input: 0, output: 0 };

  let table = buildTable(elements, screen);
  // Retry once on an empty dump: entry animations can momentarily hide nodes.
  // ponytail: one retry, 900ms — a hard ceiling for blind screens (WebView),
  // which the skill routes to the android-cli screenshot flow instead.
  if (table.length === 0) {
    await sleep(900);
    const retry = await getLayout(serial);
    rootPkg = retry.rootPkg;
    table = buildTable(retry.elements, screen);
  }
  let prevHash = tableHash(table);
  const steps = [];
  let copied = false; // a copy_<idx> step ran this run — gates the paste op
  let sameDirRun = 0; // consecutive same-direction scrolls; paces scroll length only
  let lastScrollDir = null;
  let status = 'max_steps';
  let blockedReason = null;

  for (let step = 1; step <= maxSteps; step++) {
    const appPkg = rootPkg ?? 'unknown';
    const launcher = /launcher/i.test(appPkg);
    const state = {
      goal,
      provided_texts: values.length ? values : null,
      screen: {
        app: appPkg,
        size: screen ? `${screen.w}x${screen.h}` : null,
        launcher: launcher || undefined,
      },
      elements: serializeRows(table),
      elements_note:
        table.length === 0
          ? 'empty element list (screen may be a WebView or off)'
          : table.truncated
            ? `element list truncated at ${table.length} rows (${MAX_ROWS_NOTE} reached) — more elements exist below; scrolling may reveal them`
            : launcher
              ? 'home screen (launcher): apps open by clicking their icon; system pages (Wi-Fi, display, dark theme, airplane mode) live inside the Settings app'
              : null,
      recent_actions: steps
        .slice(-10)
        .map((s) => `${s.step}. ${s.action}${s.changed === false ? ' (no visible change)' : ''}`),
    };
    const questions = buildQuestions({ table, texts: values, copied, canScroll: scrollOffered(table, screen) });
    const { answers, usage, latencyMs: apiMs } = await ask({ state, questions });
    if (usage && Number.isFinite(usage.input_tokens)) tokens.input += usage.input_tokens;
    if (usage && Number.isFinite(usage.output_tokens)) tokens.output += usage.output_tokens;

    const op = validatedChoice(answers.operation, Object.keys(questions.operation.criteria), 'operation');
    const goalMet = validatedNoul(answers.goal_met, 'goal_met');

    // done is the model's verdict, accepted as-is — one judgment per step.
    // A second confirm question persistently disagreed with the primary one
    // on already-successful screens (ping-pong to max_steps); the caller
    // remains the source of truth for side-effects. goal_met is logged as a
    // diagnostic only.
    if (op.choice === 'done') {
      status = 'done';
      steps.push({
        step,
        action: `done (goal_met=${goalMet.noul.toFixed(2)}, conf=${op.confidence.toFixed(2)})`,
        changed: null,
        apiMs,
        goalMetNoul: goalMet.noul,
      });
      break;
    }
    if (op.choice === 'blocked') {
      status = 'blocked';
      blockedReason = table.length === 0 ? 'blind_screen' : 'model';
      steps.push({
        step,
        action: `blocked (goal_met=${goalMet.noul.toFixed(2)}, conf=${op.confidence.toFixed(2)})`,
        changed: null,
        apiMs,
        goalMetNoul: goalMet.noul,
      });
      break;
    }

    let action = op.choice;
    let changed = null;
    // Set when a paste runs this step: the raw staged text to verify against
    // the post-settle table (see the paste branch and the settle below).
    let stagedForVerify = null;
    const stepT0 = performance.now();

    // The validated argmax choice is executed as-is; a wrong tap is
    // recoverable next step (back/home exist). No confidence gate —
    // iteration is the safety net, not a threshold.
    const clickM = /^click_(\d+)$/.exec(op.choice);
    const typeM = /^type_(\d+)_(\d+)$/.exec(op.choice);
    const copyM = /^copy_(\d+)$/.exec(op.choice);
    if (clickM) {
      const row = table.find((r) => String(r.idx) === clickM[1]);
      if (!row) throw new DroidJevError(`click target index ${clickM[1]} not in current table; no action executed`);
      log(`step ${step}: tap ${row.row} (conf ${op.confidence.toFixed(2)})`);
      await tap(serial, row.el.center);
      // History must record the state a tap PRODUCES, not the one it found:
      // "tap check=on" in recent_actions after a flip reads as success when
      // the tap actually turned the toggle off.
      action = row.el.checkable ? `tap ${row.row} (now check=${row.el.checked ? 'off' : 'on'})` : `tap ${row.row}`;
    } else if (typeM) {
      if (!values.length) throw new DroidJevError('model chose a type action but no --text was provided');
      const row = table.find((r) => String(r.idx) === typeM[1]);
      if (!row) throw new DroidJevError(`type target index ${typeM[1]} not in current table; no action executed`);
      const value = values[Number(typeM[2])];
      if (value === undefined)
        throw new DroidJevError(
          `type choice names provided_texts[${typeM[2]}] which does not exist; no action executed`,
        );
      await tap(serial, row.el.center);
      await sleep(350);
      await typeText(serial, value);
      log(`step ${step}: type ${JSON.stringify(value)} into ${row.row}`);
      action = `type ${JSON.stringify(value)} into ${row.row}`;
    } else if (copyM) {
      const row = table.find((r) => String(r.idx) === copyM[1]);
      if (!row || !row.el.text)
        throw new DroidJevError(
          `copy target index ${copyM[1]} not in current table or has no text; no action executed`,
        );
      // Same ceiling as operator-typed --text (act.js): a copy stages text on
      // the clipboard that a later paste types into a focused field, so it
      // must never exceed what the operator could have typed by hand.
      const staged = row.el.text.slice(0, MAX_COPY_CHARS);
      await setClipboard(serial, staged);
      const shown = preview(staged);
      log(`step ${step}: copy ${shown} to the clipboard`);
      copied = true;
      // The clipboard is invisible to the view hierarchy: a copy cannot change
      // the screen, so skip the settle + re-dump and keep the current table.
      steps.push({
        step,
        action: `copy ${shown} from ${row.row}`,
        changed: null,
        apiMs,
        actMs: performance.now() - stepT0,
        confidence: op.confidence,
        stagedText: staged, // raw value a later paste verifies against the screen
      });
      continue;
    } else if (op.choice === 'scroll_down' || op.choice === 'scroll_up') {
      const dir = op.choice === 'scroll_down' ? 'down' : 'up';
      // Consecutive same-direction scrolls pace the distance (long lists);
      // a reversal resets the pacing.
      sameDirRun = dir === lastScrollDir ? sameDirRun + 1 : 0;
      lastScrollDir = dir;
      const anchor = scrollAnchor(table, screen || { w: 1080, h: 2340 });
      const dy = scrollDistance(screen?.h ?? 2340, sameDirRun);
      await scrollSwipe(serial, anchor, dy, screen?.h ?? 2340, dir);
      log(`step ${step}: ${op.choice} (${Math.round((dy / (screen?.h ?? 2340)) * 100)}% of screen)`);
    } else if (op.choice === 'back') {
      await keyevent(serial, KEYS.back);
      lastScrollDir = null;
    } else if (op.choice === 'home') {
      await keyevent(serial, KEYS.home);
      lastScrollDir = null;
    } else if (op.choice === 'paste') {
      // Fail closed even though buildQuestions no longer offers paste without
      // a prior copy: validation only proves the choice was a legal member of
      // the offered set, and both layers must enforce the same invariant.
      if (!copied) throw new DroidJevError('model chose paste but no copy ran this run; no action executed');
      const last = [...steps].reverse().find((s) => /^copy /.test(s.action));
      const shown = last ? last.action.replace(/^copy /, '').replace(/ from .*$/, '') : 'the copied text';
      const staged = last?.stagedText ?? null;
      // Idempotency, same contract as typeText's set-semantics: pasting into
      // a field that already holds the staged text would duplicate it (Chrome
      // resumes with the omnibox still focused and filled). A no-op step
      // needs no settle — the screen cannot have changed.
      if (staged && pasteOutcome(table, staged).landed) {
        log(`step ${step}: paste ${shown} — already in the field, skipped`);
        steps.push({
          step,
          action: `paste ${shown} (already in the field)`,
          changed: null,
          apiMs,
          actMs: performance.now() - stepT0,
          confidence: op.confidence,
        });
        continue;
      }
      await keyevent(serial, KEYS.paste);
      log(`step ${step}: paste ${shown} into the focused field`);
      action = `paste ${shown} into the focused field`;
      stagedForVerify = staged;
    } else if (op.choice === 'wait') {
      await sleep(700);
    } else {
      throw new DroidJevError(`unhandled operation ${op.choice}`);
    }
    // Wait out screen motion by polling the dump: two consecutive identical
    // hierarchy hashes mean nothing is animating anymore — a fast screen is
    // verified in ~2 dumps instead of a fixed sleep + dump. Scroll flings
    // keep the hierarchy moving for their whole momentum (~0.5-1s), but a
    // mid-flight dump is readable, so scrolls get a short cap; screen
    // transitions (tap/type/back/home) wait for motion to complete. The
    // settled dump doubles as verification and the next step's table.
    const settleMs = op.choice.startsWith('scroll_') ? (animationsOff ? 150 : 250) : animationsOff ? 400 : 600;
    const next = await settleLayout(serial, { maxMs: settleMs });
    table = buildTable(next.elements, screen);
    rootPkg = next.rootPkg;
    const h = tableHash(table);
    changed = h !== prevHash;
    prevHash = h;

    // Paste verification: the paste key only works while an editable field
    // holds focus, and focus is the model's guess — a miss is silent. If the
    // staged text did not reach an edit row, try the clipboard suggestion
    // (Chrome and most IMEs surface one); if that is missing too, record the
    // miss in the step so recent_actions steers the next decision instead of
    // a blind `done`.
    if (stagedForVerify) {
      const outcome = () => pasteOutcome(table, stagedForVerify);
      if (!outcome().landed) {
        const chip = outcome().chip;
        if (chip) {
          log(`        → paste not in a field — tapping clipboard suggestion ${chip.row}`);
          await tap(serial, chip.el.center);
          const retry = await settleLayout(serial, { maxMs: settleMs });
          table = buildTable(retry.elements, screen);
          rootPkg = retry.rootPkg;
          const h2 = tableHash(table);
          changed = changed || h2 !== h;
          prevHash = h2;
        }
        action += outcome().landed ? ' (via clipboard suggestion)' : ' (did not land)';
      }
    }

    steps.push({ step, action, changed, apiMs, actMs: performance.now() - stepT0, confidence: op.confidence });
    log(`        → ${changed ? 'screen changed' : 'no visible change'}`);

    // The step budget is the stop. `changed` is context for the model (it
    // steers reversal decisions) and diagnostics for the caller — not an
    // early-stop signal; tuned guards converted slow-but-recoverable runs
    // into false `blocked`s.
  }

  return {
    status,
    blockedReason,
    goal,
    serial,
    steps,
    tokens,
    tookMs: performance.now() - t0,
    finalRows: serializeRows(table).slice(0, 15),
    finalApp: rootPkg ?? null,
  };
}
