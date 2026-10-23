// The goal loop: one layout dump + one TypeSafe request per step. The model
// answers a single `operation` choice whose options include click_<idx> /
// type_<idx>_<k> / copy_<idx> for concrete elements (plus scroll/back/home/
// paste/wait/done/blocked)
// alongside a parallel `goal_met` judgment; the validated argmax is executed
// as-is and verified by the next step's dump (one dump per step, which
// matters: a dump costs ~1s and dominates latency).
import { getLayout, settleLayout } from './layout.js';
import { setClipboard } from './uia2.js';
import {
  buildTable,
  serializeRows,
  scrollAnchor,
  horizontalAnchor,
  listItems,
  tableHash,
  isEditable,
  isSlider,
  elementText,
  gridNote,
} from './elements.js';
import { screenSize, shell } from './adb.js';
import { startApp } from './start.js';
import { screenshotPng } from './shot.js';
import { ask, prewarm, validatedChoice, validatedNoul, validatedBox } from './typesafe.js';
import {
  tap,
  longPress,
  slideTo,
  scrollSwipe,
  scrollSwipeX,
  keyevent,
  typeText,
  sanitizeInputText,
  KEYS,
} from './act.js';
import { DroidJevError, sleep } from './util.js';
import { randomUUID } from 'node:crypto';

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

// Prompt-side description of the element list: what the rows are and what is
// missing from them. Shared with decide.js so a host-supplied table is
// described exactly as the act loop describes its own dump.
export function elementsNote(table, launcher) {
  if (table.length === 0) return 'empty element list (screen may be a WebView or off)';
  const notes = [];
  if (table.truncated) {
    notes.push(
      `element list truncated at ${table.length} rows (${MAX_ROWS_NOTE} reached) — more elements exist below; scrolling may reveal them`,
    );
  }
  if (launcher) {
    notes.push(
      'home screen (launcher): apps open by clicking their icon; system pages (Wi-Fi, display, dark theme, airplane mode) live inside the Settings app',
    );
  }
  const grid = gridNote(table);
  if (grid) notes.push(grid);
  return notes.length ? notes.join('; ') : null;
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
  scroll_left:
    'The target is probably off to the LEFT in a horizontally scrolling area (a pager, a carousel, a wide row of cards)',
  scroll_right:
    'The target is probably off to the RIGHT in a horizontally scrolling area (a pager, a carousel, a wide row of cards)',
  back: 'Leave the current screen/dialog and return to the one before it — pick it to close a dialog the goal did not ask for or to undo a wrong turn, not to look for the target elsewhere',
  home: 'Go to the home screen — pick it only when the goal is about the launcher itself or you need to start over from a known screen',
  paste:
    'The clipboard content (the copy in recent_actions) goes into the currently focused field — pick this right after clicking that field',
  wait: 'The screen is loading or animating; act next step',
  done: 'The goal is already achieved on the current screen',
  blocked:
    'No sequence of available actions can achieve the goal (missing app, permission wall, login required, a value the goal asks to enter that is neither provided nor shown on screen to copy)',
};

// Where can a slider go? Only the positions the goal itself names — an end
// ("minimum"/"maximum") or a percentage. A goal that names no position offers
// no stop at all: the model would be guessing a number the goal never stated.
export function sliderStops(goal) {
  const g = String(goal || '').toLowerCase();
  const stops = [];
  if (/\b(min|minimum|lowest|smallest|bottom)\b/.test(g)) stops.push({ key: 'min', label: 'its minimum', fraction: 0 });
  if (/\b(max|maximum|highest|largest|top|loudest|brightest|full)\b/.test(g)) {
    stops.push({ key: 'max', label: 'its maximum', fraction: 1 });
  }
  if (/\b(half|midway|middle)\b/.test(g)) stops.push({ key: 'mid', label: 'the middle', fraction: 0.5 });
  for (const m of g.matchAll(/(\d{1,3})\s*(?:%|percent)/g)) {
    const pct = Number(m[1]);
    if (pct <= 100 && !stops.some((s) => s.fraction === pct / 100)) {
      stops.push({ key: `p${pct}`, label: `${pct}%`, fraction: pct / 100 });
    }
  }
  return stops;
}

// What an answer_ choice submits, resolved here so a host that executes the
// decision itself does not have to re-derive which text the row held. Pure.
export function answerFor(choice, { table, values = [] }) {
  const single = /^answer_(\d+)$/.exec(choice);
  if (single) {
    const row = table.find((r) => String(r.idx) === single[1]);
    if (!row) throw new DroidJevError(`answer target index ${single[1]} not in current table; nothing submitted`);
    return elementText(row.el);
  }
  const list = /^answer_list_(\d+)$/.exec(choice);
  if (list) {
    const items = listItems(table, list[1]);
    if (items.length === 0) throw new DroidJevError(`answer_list target ${list[1]} holds no items; nothing submitted`);
    return items.map((r) => elementText(r.el)).join(', ');
  }
  const text = /^answer_text_(\d+)$/.exec(choice);
  if (text) {
    const value = values[Number(text[1])];
    if (value === undefined) {
      throw new DroidJevError(`answer_text choice names provided_texts[${text[1]}] which does not exist`);
    }
    return value;
  }
  return null;
}

// One decision per step: the operation question offers click_<idx>/type_<idx>
// for concrete elements alongside the fixed ops. A separate "which element?"
// question let the model point at the right row (e.g. "WLAN" for a Wi-Fi
// goal on a Russian UI) while hedging the operation into endless scrolling —
// merging the choices forces the comparison the model actually understands.
//
// The option set is what the host can execute: a host with no clipboard
// passes clipboard:false, one whose goal is a question passes answers:true.
export function buildQuestions({
  table,
  screen = null,
  texts = [],
  apps = [],
  copied = false,
  clipboardReady = false,
  canScroll = true,
  launcher = false,
  goal = '',
  answers = false,
  clipboard = true,
  recentChoices = [],
  lastUnchanged = null,
  today = null,
}) {
  const criteria = {};
  const stops = sliderStops(goal);
  const wantsClipboard = /\bclipboard\b/i.test(goal);
  const stuck = stuckOps(recentChoices, lastUnchanged);
  for (const r of table) {
    // An editable row is offered as type_<idx>_<k> (tap + type, atomic) rather
    // than click_<idx>: clicking a field on its own only focuses it, which is
    // never the goal's next step — it is the trap the model kept falling into
    // in a date picker, tapping the very field it should have typed into. The
    // exception is paste, which needs the field focused first.
    const needsFocusForPaste = clipboard && (copied || clipboardReady);
    if ((r.el.clickable || r.el.longClickable || r.tappable) && !(isEditable(r.el) && !needsFocusForPaste)) {
      criteria[`click_${r.idx}`] = `Tap ${r.row}`;
    }
    if (r.el.longClickable) criteria[`hold_${r.idx}`] = `Press and hold ${r.row}, opening its context menu`;
    texts.forEach((t, k) => {
      if (isEditable(r.el)) criteria[`type_${r.idx}_${k}`] = `Tap ${r.row}, then type provided_texts[${k}] into it`;
    });
    if (isSlider(r.el)) {
      for (const s of stops) criteria[`set_${r.idx}_${s.key}`] = `Drag the slider ${r.row} to ${s.label}`;
    }
    if (clipboard && r.el.text) criteria[`copy_${r.idx}`] = `Copy the text of ${r.row} to the device clipboard`;
    if (answers && r.el.text) criteria[`answer_${r.idx}`] = `Submit the text of ${r.row} as the answer`;
    if (answers) {
      const items = listItems(table, r.idx);
      if (items.length >= 2) {
        criteria[`answer_list_${r.idx}`] =
          `Submit the ${items.length} items of the list ${r.row}, in order, joined with commas, as the answer`;
      }
    }
  }
  if (clipboard) {
    // A value the goal itself supplies goes onto the clipboard with clip_<k>:
    // no screen holds it, so copy_<idx> (screen → clipboard) cannot reach it.
    // Offered only for a goal that names the clipboard: otherwise the model
    // stages a goal value (a file name) when the goal asked for screen text.
    if (wantsClipboard) {
      texts.forEach((t, k) => {
        criteria[`clip_${k}`] = `Copy provided_texts[${k}] to the device clipboard`;
      });
    }
    // paste rides the CURRENT clipboard, so it is offered only after a copy this
    // run — otherwise a legal model answer could type pre-existing clipboard
    // content the operator never asked to move (see README: --text is the only
    // text the model may type; paste must stay bound to this run's copies). A
    // host that says the clipboard already holds what the goal refers to
    // (clipboardReady) is trusted with it instead.
    if (copied || clipboardReady) criteria.paste = FIXED_OPS.paste;
  }
  if (answers) {
    texts.forEach((t, k) => {
      criteria[`answer_text_${k}`] = `Submit provided_texts[${k}] as the answer, verbatim`;
    });
  }
  // Apps the host can open by name. One step from anywhere, and unambiguous —
  // the launcher may show two icons with the same label.
  apps.forEach((label, k) => {
    criteria[`launch_${k}`] = `Open the ${label} app`;
  });
  // The paste rule tells the truth about where the clipboard content came
  // from: a copy this run, or the text the host says is already there.
  const pasteRule =
    clipboardReady && !copied
      ? 'When the goal asks to paste into a field, click_<idx> the field to focus it and pick paste: the clipboard already holds the text this goal refers to, so nothing needs copying first. If the field already shows that text, choose done — pasting again would duplicate it.'
      : 'When the goal asks to paste copied text into a field, first click_<idx> the field to focus it, then pick paste — paste puts the clipboard content into the focused field. paste is offered only after a copy_<idx> step this run; the goal must name a copy first. If the focused field already shows the copied text, choose done — pasting again would duplicate it. If paste ran with no visible change, the field may not take the paste key: many surfaces offer the clipboard content as a tappable suggestion instead (the Chrome address bar shows it as a suggestion chip, keyboards as a toolbar clip) — click_<idx> the row showing the copied text to insert it.';
  // With launch options on the table the launcher advice has to step aside:
  // walking the drawer is the slow path, and it is also the ambiguous one.
  const launcherRule = apps.length
    ? 'When screen.launcher is true you are on the home screen, and the app drawer behind it lists every installed app. A launch_<k> option that names the app the goal needs opens it in one step — pick that instead of hunting for the icon: it is faster, and two apps can share a label. Only when no launch_ option names it, scroll to open the drawer and click the icon of the app the goal names, scrolling on until it appears (scrolling only pages through icons and never enters an app). A different app that merely seems related is a wrong turn: match the app the goal names. blocked is not offered here: a goal that needs an app is always reachable from the launcher, so if the app is not visible yet, keep scrolling the drawer.'
    : 'When screen.launcher is true you are on the home screen, and the app drawer behind it lists every installed app — so nothing is out of reach from here. Scroll to open the drawer, then click the icon of the app the goal names, scrolling on until it appears (scrolling only pages through icons and never enters an app). A different app that merely seems related is a wrong turn: match the app the goal names. blocked is not offered here: a goal that needs an app is always reachable from the launcher, so if the app is not visible yet, keep scrolling the drawer.';
  const launchRule =
    "launch_<k> opens the app it names in one step, from any screen. When the goal names an app, or asks for something that lives in one, pick that app's launch_ option instead of navigating there by hand; what the goal asks you to DO inside that app still has to be done inside it.";
  const stayRule =
    "An app you have just opened is where the goal's data lives: stay in it and explore — switch views, scroll, use its search. back leaves the app and undoes the opening, so it is the one action that cannot find anything there; only leave when the goal needs a different app.";
  const gridRule =
    'A grid of unlabeled cells (a calendar month, a keypad, a dial pad) is drawn, not described: what each cell holds is invisible to you, so do not tap cells guessing at them. Reach the same data by a text route instead — a view switcher that lists titles/events as text, a date field that opens a picker, or a search box.';
  const todayRule =
    "state.today is the date the host believes this screen lives on (the device clock for `act`; a host may know better). A goal that says today, tomorrow or a weekday refers to that calendar — reach the day it means with the calendar's own navigation: one tap of a next-day arrow for tomorrow, a picker for a named date.";
  const pickerRule =
    'A row flagged date shows a date, and clicking it opens a date picker where the day, month and year can be typed — that is how a date the goal names gets set, in two or three steps. Stepping a calendar one day at a time with its arrows is the slowest route there is: use it only to cross a day or two, never to walk to a date the goal already names. A column of days, months or years (often editable, e.g. "15" "Oct" "2023") is the same picker: click it, type the matching provided_texts value into each field, then click OK.';
  // The rules that only matter for some goals or screens are added only then:
  // a small model pays for every rule it has to hold, and the ones about date
  // pickers or keypads are noise on a Wi-Fi toggle.
  const goalText = String(goal || '').toLowerCase();
  const hasKeypad = table.filter((r) => /^\d$/.test(elementText(r.el))).length >= 6;
  const dateish =
    /\b(date|day|tomorrow|today|week|month|year|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b/.test(
      goalText,
    ) ||
    /\d{4}-\d{1,2}-\d{1,2}/.test(goalText) ||
    table.some((r) => r.row.includes('+date'));
  const timeish =
    /\b(time|hour|minute|second|timer|alarm|clock|am|pm)\b/.test(goalText) || /\d{1,2}:\d{2}/.test(goalText);
  const keypadRule =
    'A keypad (a grid of digit buttons 0-9) fills its display from the right, like a calculator: press the digits of the value in order — 16 minutes 35 seconds is 1, 6, 3, 5 — and press only digits, no separators. The display above the keypad shows what has been entered so far.';
  const formRule =
    'Every value the goal names belongs somewhere in the form. Before saving, check them off: a provided_texts value that no field shows yet still has to be placed — a chip to click, a selector to open (its current value is the row text), or a field to type into. Saving a form with a field still at its default value records the wrong entry, and walking the same form twice records a duplicate.';
  const verbRule =
    "When a visible row or button names the very operation the goal asks for (Delete, Save, Share, Move, Rename, Copy, Add, Send), that row is the goal's next step: click it instead of navigating on. A Delete button on a screen where your items are selected is the deletion the goal asked for.";
  const selectorRule =
    'A row that shows the current value of a field (a category, a unit, a folder, a template, a reminder) is a selector: click it to open the choices and click the value the goal names — or click the choice directly when the choices are listed beside it. Scrolling a selector selects nothing.';
  const allRule =
    'When the goal says all or every, the list has to end up empty: keep deleting until no item is left, and when a selection mode offers Select all, use it — that clears the list in one pass, including the entries it shows as folders.';
  const fileRule =
    'A file name the goal states goes where the dialog asks for a name: when the dialog also shows an extension field (".md"), it already holds the extension — the name field wants the name WITHOUT it, or the file lands as note.md.md. Type the stem into the name field and leave the extension field as it is, unless the goal names a different extension.';
  const searchRule =
    'A search box searches the titles and names the app lists: typing a date, a time or a filter value into it finds nothing, and an empty result is not an answer. When the goal is about a date, navigate to that date; when it is about a property (priority, folder, category), look for a view that groups by it.';
  const canScrollX = !!horizontalAnchor(table, screen);
  Object.assign(criteria, {
    ...(canScroll ? { scroll_down: FIXED_OPS.scroll_down, scroll_up: FIXED_OPS.scroll_up } : {}),
    ...(canScrollX ? { scroll_left: FIXED_OPS.scroll_left, scroll_right: FIXED_OPS.scroll_right } : {}),
    back: FIXED_OPS.back,
    home: FIXED_OPS.home,
    wait: FIXED_OPS.wait,
    done: FIXED_OPS.done,
    // The launcher is never terminal: every installed app is one click away
    // behind it, so a goal that needs an app is reachable from here. Offering
    // blocked on a home screen invites giving up on a goal the model has not
    // looked for yet — observed as the single largest failure mode.
    ...(launcher ? {} : { blocked: FIXED_OPS.blocked }),
  });
  for (const op of stuck) delete criteria[op];
  return {
    operation: {
      type: 'choice',
      instructions: {
        question: 'Pick the single next action that best advances the goal.',
        rules: [
          'click_<idx> options name a visible element — pick one whenever a visible element plausibly leads toward the goal, even if the goal words do not appear in its label. Match by MEANING, not literal text: the goal and the UI may be in different languages (a goal saying "Ajustes" matches "Settings" on an English UI; an English "Wi-Fi" goal matches "WLAN" on a German UI), and a settings page often hides one screen deeper than its title suggests.',
          'A QUESTION goal (it asks for information — a title, a name, a count, a time, a place — rather than asking you to change something) is achieved by answering, not by doing: never submit a message the app itself shows (an empty-list notice like "No items found." is not an answer), and never answer from a screen you have not reached the data on. answer_<idx> submits the text of the row that holds the answer, answer_list_<idx> submits every item of the list at row idx joined with commas (the option is offered only for a list), and answer_text_<k> submits provided_texts[k] verbatim (for answers the goal itself spells out, like True or False). Submit the VALUE, not the label beside it: for "Android version 14" the answer is 14, not Android version. Navigate to the screen that shows the data first, and answer only from what is on screen. done does not answer a question.',
          'When the goal asks to turn something on/off, pick the click option whose row shows check=on/check=off — that is the actual toggle; a same-labeled row without check= is only a container. If the toggle already shows the state the goal needs, choose done instead of tapping it again.',
          'When the goal asks to copy text shown on screen, pick copy_<idx> on the row holding the exact text — the value element (e.g. the number beside "Android version"), not its label. Copies never change the screen: once recent_actions shows the right text copied, choose done. When instead the goal itself spells out the text to copy, pick clip_<k>: that value is not on any screen, so no copy_<idx> can reach it.',
          pasteRule,
          launcherRule,
          launchRule,
          stayRule,
          'If the goal target is a screen inside an app rather than the app itself (e.g. "About phone" inside Settings), first click the app icon, then navigate inside the app.',
          'When hunting a setting inside an app, visit each section at most once: if recent_actions shows you already entered and left a section without finding the target, do not enter it again — its rows were all seen; pick a different section row instead.',
          'A dialog or editor the goal did not ask for (e.g. renaming the device) is a wrong turn — close it with back or its cancel button and continue on the screen beneath.',
          'hold_<idx> presses and holds a row, which is how a list opens the operations it shows no button for (rename, delete, move, share, select). In a file list, a note list or a task list a click OPENS the item instead: when the goal is to delete, move or rename an item that already exists, hold_<idx> it — clicking it opens the wrong screen (a file asks which app to open it with), and only hold_<idx> offers Delete/Move/Rename.',
          'set_<idx>_<k> drags a range row (a slider) to the position the option names — its minimum, its maximum, or a percentage. The slider\'s current value is its own row text (e.g. "197.0"), so judge from it whether the slider already sits where the goal wants. If the goal names a position no offered option matches, this slider is not the way to reach the goal.',
          'Search fields require typing, which is only possible when provided_texts is set; if provided_texts is null, never open a search field to type — scroll lists and read their items instead. Pasting a copied value into a search field is not typing: after a copy_<idx> step, click the field and paste.',
          ...(texts.length ? [formRule] : []),
          searchRule,
          ...(gridNote(table) ? [gridRule] : []),
          verbRule,
          selectorRule,
          ...(/\b(all|every)\b/.test(goalText) ? [allRule] : []),
          ...(texts.some((t) => /\.(txt|md|png|jpg|jpeg|mp4|mp3|csv|json|pdf)$/i.test(t)) ? [fileRule] : []),
          ...(dateish || timeish ? [pickerRule] : []),
          ...(dateish && today ? [todayRule] : []),
          ...(hasKeypad || timeish ? [keypadRule] : []),
          "type_<idx>_<k> types provided_texts[k] and replaces that field's content entirely. When provided_texts has several values, each belongs in a DIFFERENT field — match value to field by meaning (a person's name is not a phone number). If recent_actions already shows the same value typed into a field, it is already there — judge the results on screen instead of typing again.",
          'Every text you may type comes from provided_texts. If the goal asks to enter a specific value (a name, an address) that is neither among provided_texts nor shown on the screen to copy, typing it is impossible — choose blocked right away instead of tapping the field repeatedly or typing a provided value that does not belong in it. A value the goal tells you to copy from the screen never needs provided_texts: copy_<idx> stages it on the clipboard, paste puts it into the focused field.',
          'Never repeat a click, type, or same-direction scroll that recent_actions shows already ran with no visible change — the same input on the same screen will not behave differently. Pick a different element or a different strategy (the other scroll direction, back, home), or conclude done/blocked.',
          'Use scroll_down/scroll_up when the target is likely just off-screen, or when you do not know which list item hides it — scroll and read the item labels and subtitles. If scrolling stops showing new content, check the other direction (scroll_left/scroll_right page a horizontal area) before deciding the target is not on this screen.',
          'Choose done only when the current screen shows the goal is achieved — and never for a question goal, which is achieved by an answer_ option.',
          'If the goal names an app by name (e.g. "open Settings", "open Chrome"), done is valid only when screen.app is that app — settings screens inside a different app do not count.',
          'Choose blocked only when no available action sequence can achieve the goal. A target that is absent from the current screen is not proof of that: an app that is not open yet is reached by opening it, and a list item further down by scrolling — open the app the goal names and search inside it before concluding infeasible.',
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

// A give-up the model is barely sure of is usually a wrong screen, not an
// impossible goal: on a low-confidence blocked, ask once more with blocked off
// the table and act on that answer instead. The model then has to pick
// something that moves, and if it was right the next step concludes blocked
// again — one step later, which the step budget bounds.
export const BLOCKED_RETRY_CONFIDENCE = 0.6;

// A loop the model cannot see it is in: recent_actions lists the same actions
// over and over, but a small model keeps re-picking the same argmax. Withdrawing
// the loop's own options is what breaks it — it has to pick something else.
// Keys, not descriptions: `click_9` repeating is a loop, `click` repeating is
// just how a list is walked.
//
// `lastUnchanged` is the previous choice when it produced no visible change —
// the one fact the model cannot be trusted to act on twice.
export function stuckOps(recentChoices = [], lastUnchanged = null) {
  const ops = recentChoices.map(String);
  const stuck = new Set();
  if (ops.length >= 3 && ops.slice(-3).every((o) => o === 'back')) stuck.add('back');
  const [a, b] = [ops.at(-4), ops.at(-3)];
  if (ops.length >= 4 && a === ops.at(-2) && b === ops.at(-1) && a !== b) {
    stuck.add(a);
    stuck.add(b);
  }
  // A three-step cycle — open, act, retreat (`launch_0, click_1, back`) —
  // repeats just as blindly as a two-step one. Two full turns of the pattern,
  // all distinct, is not how any goal is advanced.
  if (ops.length >= 6) {
    const cycle = ops.slice(-3);
    if (new Set(cycle).size === 3 && ops.slice(-6, -3).every((o, i) => o === cycle[i])) {
      for (const op of cycle) stuck.add(op);
    }
  }
  const last = ops.at(-1);
  if (last && !last.startsWith('scroll_') && ops.slice(-3).every((o) => o === last)) stuck.add(last);
  // Waiting twice is idle time, not patience: the host already waits for the
  // screen to settle, and a budget of ten steps cannot afford a second one.
  if (last === 'wait') stuck.add('wait');
  // A scroll that just changed nothing is at the end of its list, or the
  // content does not move at all: the same direction will do nothing again.
  const unchanged = String(lastUnchanged ?? '');
  if (unchanged.startsWith('scroll_')) stuck.add(unchanged);
  return stuck;
}

export async function askDecision({ state, questions, askFn = ask, log = () => {} }) {
  const first = await askFn({ state, questions });
  const criteria = Object.keys(questions.operation.criteria);
  const operation = validatedChoice(first.answers.operation, criteria, 'operation');
  const goalMet = validatedNoul(first.answers.goal_met, 'goal_met');
  if (operation.choice !== 'blocked' || operation.confidence >= BLOCKED_RETRY_CONFIDENCE) {
    return { operation, goalMet, usage: first.usage, latencyMs: first.latencyMs, retried: false };
  }
  const without = { ...questions.operation.criteria };
  delete without.blocked;
  log(`blocked at confidence ${operation.confidence.toFixed(2)} — asking again without blocked`);
  const again = await askFn({
    state,
    questions: { ...questions, operation: { ...questions.operation, criteria: without } },
  });
  return {
    operation: validatedChoice(again.answers.operation, Object.keys(without), 'operation'),
    goalMet: validatedNoul(again.answers.goal_met, 'goal_met'),
    usage: again.usage,
    latencyMs: first.latencyMs + again.latencyMs,
    retried: true,
  };
}

// --- vision fallback (jev 1.14: images + the box primitive) ---
//
// Runs only when the element table is EMPTY — a screen that exposes no
// accessibility nodes (WebView content, canvas renderers), which otherwise
// ends the run as blocked:blind_screen on arrival. One request asks three
// parallel questions against a screenshot: the operation stays a validated
// choice among host-executable ops, a noul gates the whole step (a box answer
// comes back even when nothing is visible), and the box — the one place the
// model names a position — is strictly validated fractions, never raw
// coordinates, resolved to a tap point in code.

// Box answers below this confidence are not tapped. The docs give no
// recommended threshold; 0.6 is their worked example.
// ponytail: untuned — tune on real blind screens if it withholds good taps.
export const TAP_BOX_MIN_CONFIDENCE = 0.6;

export function buildVisionQuestions(ref) {
  return {
    operation: {
      type: 'choice',
      instructions: {
        question:
          'The element list is empty: this screen exposes no accessibility nodes (a web page or custom renderer), so the screenshot in state.screenshot is the only view of it. Pick the single next action that best advances the goal.',
        rules: [
          'tap_box taps the on-screen element the tap_target answer marks — a link, a button, a field, an image acting as a control. Judge by what is VISIBLE in the screenshot, matching the goal by meaning (the goal and the page may be in different languages).',
          'scroll_down/scroll_up move the page when the target is probably just out of view; back leaves this page for the one before it; home returns to the launcher, whose element list is readable again.',
          'Choose done only when the screenshot itself shows the goal achieved.',
          'Choose blocked only when nothing on this screen can lead to the goal.',
        ],
      },
      criteria: {
        tap_box: 'Tap the element the tap_target box marks — the visible control that best advances the goal',
        scroll_down: 'Scroll down — the target is probably further down the page',
        scroll_up: 'Scroll up — the target is probably higher on the page',
        back: 'Leave this page for the one before it',
        home: 'Return to the home screen',
        wait: 'The page is still loading; act next step',
        done: 'The goal is already achieved, as shown in the screenshot',
        blocked: 'No action on this screen can achieve the goal',
      },
    },
    screen_readable: {
      type: 'noul',
      instructions: `Does the screenshot in state.screenshot show a rendered screen with content (text, controls, images) rather than a blank, black or off display?`,
    },
    // The reference (not a state path) grounds the box precisely; it is the
    // second and last copy of the image in the request.
    tap_target: {
      type: 'box',
      instructions: `The single element in \`${ref}\` to tap next to advance the goal (this answer is used only when the operation choice is tap_box)`,
    },
  };
}

// Consume the three parallel answers: the noul gates, the choice directs,
// the box targets. Pure.
export function visionVerdict({ operation, screenReadable, tapTarget }) {
  if (screenReadable.noul < 0.5) return { kind: 'blocked', reason: 'blind_screen' };
  if (operation.choice === 'tap_box') {
    return tapTarget.confidence === null || tapTarget.confidence >= TAP_BOX_MIN_CONFIDENCE
      ? { kind: 'tap', box: tapTarget.box, confidence: operation.confidence }
      : { kind: 'skip_tap', confidence: tapTarget.confidence };
  }
  return { kind: 'op', choice: operation.choice, confidence: operation.confidence };
}

// Box fractions → a tap point in screen pixels. The screenshot is the full
// screen, so a fraction of the image is a fraction of the screen (±1px from
// downscale rounding).
export function boxCenter(box, screen) {
  const w = screen?.w ?? 1080;
  const h = screen?.h ?? 2340;
  return {
    x: Math.round(((box.left + box.right) / 2) * w),
    y: Math.round(((box.top + box.bottom) / 2) * h),
  };
}

// One blind-screen step. Returns {status, blockedReason?, record, table?,
// rootPkg?, hash?}: table/rootPkg/hash are the post-settle state when a
// gesture ran, null when the screen was not touched. On an untappable
// screenshot pipeline the run degrades to the old blocked:blind_screen
// instead of erroring — vision is a fallback, not a prerequisite.
async function visionStep({
  serial,
  step,
  goal,
  values,
  screen,
  appPkg,
  steps,
  deviceDate,
  pacing,
  animationsOff,
  prevHash,
  log,
  askFn = ask,
}) {
  let shot;
  try {
    shot = await screenshotPng(serial);
  } catch (e) {
    log(`step ${step}: vision fallback unavailable (${e.message})`);
    return {
      status: 'blocked',
      blockedReason: 'blind_screen',
      record: {
        step,
        choice: 'blocked',
        action: 'blocked (screenshot capture failed on a blind screen)',
        changed: null,
        apiMs: 0,
      },
    };
  }
  // A UUID reference cannot collide with goal or row text (docs: references
  // are find-and-replaced as plain substrings).
  const ref = randomUUID();
  const state = {
    goal,
    provided_texts: values.length ? values : null,
    today: deviceDate,
    screen: { app: appPkg, size: screen ? `${screen.w}x${screen.h}` : null },
    elements: [],
    elements_note:
      'the element list is empty: this screen exposes no accessibility nodes (a web page or custom renderer) — the screenshot is the screen',
    screenshot: ref,
    recent_actions: steps
      .slice(-10)
      .map((s) => `${s.step}. ${s.action}${s.changed === false ? ' (no visible change)' : ''}`),
  };
  const questions = buildVisionQuestions(ref);
  const res = await askFn({
    state,
    questions,
    assets: { [ref]: { type: 'image', format: 'png', data_base64: shot.png.toString('base64') } },
    // ~1MB body + image tokens: visibly slower than the text requests the
    // 20s default was tuned for.
    timeoutMs: 45000,
  });
  const apiMs = res.latencyMs ?? 0;
  const operation = validatedChoice(res.answers.operation, Object.keys(questions.operation.criteria), 'operation');
  const screenReadable = validatedNoul(res.answers.screen_readable, 'screen_readable');
  const tapTarget = validatedBox(res.answers.tap_target, 'tap_target');
  const verdict = visionVerdict({ operation, screenReadable, tapTarget });

  if (verdict.kind === 'blocked') {
    return {
      status: 'blocked',
      blockedReason: verdict.reason,
      record: {
        step,
        choice: 'blocked',
        action: `blocked (vision, screen_readable=${screenReadable.noul.toFixed(2)})`,
        changed: null,
        apiMs,
      },
    };
  }
  if (verdict.kind === 'op' && (verdict.choice === 'done' || verdict.choice === 'blocked')) {
    return {
      status: verdict.choice,
      blockedReason: verdict.choice === 'blocked' ? 'model' : null,
      record: {
        step,
        choice: verdict.choice,
        action: `${verdict.choice} (vision, conf=${operation.confidence.toFixed(2)})`,
        changed: null,
        apiMs,
      },
    };
  }
  const choice = verdict.kind === 'tap' ? 'tap_box' : verdict.choice;
  const stepT0 = performance.now();
  let action;
  if (verdict.kind === 'tap') {
    const c = boxCenter(verdict.box, screen);
    log(`step ${step}: vision tap ${c.x},${c.y} (conf ${operation.confidence.toFixed(2)})`);
    await tap(serial, c);
    action = `tap (by screenshot) ${c.x},${c.y}`;
  } else if (verdict.kind === 'skip_tap') {
    log(`step ${step}: vision tap withheld (box confidence ${verdict.confidence.toFixed(2)})`);
    return {
      status: null,
      record: {
        step,
        choice,
        action: `tap withheld (box confidence ${verdict.confidence.toFixed(2)})`,
        changed: false,
        apiMs,
      },
      table: null,
      rootPkg: null,
      hash: null,
    };
  } else if (choice === 'scroll_down' || choice === 'scroll_up') {
    const dir = choice.slice('scroll_'.length);
    pacing.sameDirRun = dir === pacing.lastScrollDir ? pacing.sameDirRun + 1 : 0;
    pacing.lastScrollDir = dir;
    const h = screen?.h ?? 2340;
    const anchor = { x: Math.round((screen?.w ?? 1080) / 2), y: Math.round(h / 2) };
    const dy = scrollDistance(h, pacing.sameDirRun);
    await scrollSwipe(serial, anchor, dy, h, dir);
    log(`step ${step}: ${choice} by screenshot (${Math.round((dy / h) * 100)}% of screen)`);
    action = `${choice} (by screenshot)`;
  } else if (choice === 'back' || choice === 'home') {
    await keyevent(serial, choice === 'back' ? KEYS.back : KEYS.home);
    pacing.lastScrollDir = null;
    action = choice;
  } else if (choice === 'wait') {
    await sleep(700);
    action = 'wait';
  } else {
    throw new DroidJevError(`unhandled vision operation ${choice}`);
  }
  // Same settle contract as a normal step; the settled dump is verification
  // and the next table. ponytail: tableHash cannot tell two different blind
  // screens apart (both hash the empty list) — the action log is the history.
  const settleMs = choice.startsWith('scroll_') ? (animationsOff ? 150 : 250) : animationsOff ? 400 : 600;
  const next = await settleLayout(serial, { maxMs: settleMs });
  const nextTable = buildTable(next.elements, screen);
  return {
    status: null,
    record: {
      step,
      choice,
      action,
      changed: tableHash(nextTable) !== prevHash,
      apiMs,
      actMs: performance.now() - stepT0,
    },
    table: nextTable,
    rootPkg: next.rootPkg,
    hash: tableHash(nextTable),
  };
}

// The device's own date: a goal about "tomorrow" is only decidable against
// it, and the emulator's clock is not the host's.
async function deviceToday(serial) {
  try {
    const r = await shell(serial, ['date', '+%Y-%m-%d'], { timeoutMs: 5000 });
    const m = /(\d{4})-(\d{2})-(\d{2})/.exec(r.stdout || '');
    return m ? `${m[1]}-${m[2]}-${m[3]}` : null;
  } catch {
    return null;
  }
}

export async function runGoal({
  serial,
  goal,
  texts = null,
  apps = null,
  maxSteps = 12,
  animationsOff = false,
  answers = false,
  vision = true,
  log = () => {},
}) {
  if (!goal || typeof goal !== 'string')
    throw new DroidJevError('a goal is required, e.g. droidjev act "open Settings"');
  // Accept one string or an array (repeatable --text). Validate at the trust
  // boundary so an untypable value fails before the first tap, not mid-form.
  const values = Array.isArray(texts) ? texts : texts ? [texts] : [];
  values.forEach((v) => sanitizeInputText(v));
  const t0 = performance.now();
  // The first dump starts the on-device uia2 server (~1s); the key-less
  // connection warm-up rides inside it.
  const [screen, first, deviceDate] = await Promise.all([
    screenSize(serial),
    getLayout(serial),
    prewarm().then(() => deviceToday(serial)),
  ]);
  const { elements } = first;
  // Focused app from the dump's root package — dumpsys would cost an extra
  // adb roundtrip per step for the same answer.
  let rootPkg = first.rootPkg;
  const tokens = { input: 0, output: 0 };

  let table = buildTable(elements, screen);
  // Retry once on an empty dump: entry animations can momentarily hide nodes.
  // Still empty afterwards → the per-step vision fallback takes over (or, with
  // vision off, the model sees an empty list and blocks with blind_screen).
  // ponytail: one retry, 900ms.
  if (table.length === 0) {
    await sleep(900);
    const retry = await getLayout(serial);
    rootPkg = retry.rootPkg;
    table = buildTable(retry.elements, screen);
  }
  let prevHash = tableHash(table);
  const steps = [];
  let copied = false; // a copy_<idx> step ran this run — gates the paste op
  // Consecutive same-direction scrolls pace the distance; a reversal resets
  // it. An object (not two lets) so the vision step can share the state.
  const pacing = { sameDirRun: 0, lastScrollDir: null };
  let status = 'max_steps';
  let blockedReason = null;

  for (let step = 1; step <= maxSteps; step++) {
    const appPkg = rootPkg ?? 'unknown';
    const launcher = /launcher/i.test(appPkg);
    // A blind screen (empty table) goes to the vision fallback: the
    // screenshot becomes the screen. Without it (or --no-vision) the run
    // keeps the old behavior — the model sees an empty list and blocks.
    if (vision && table.length === 0) {
      const v = await visionStep({
        serial,
        step,
        goal,
        values,
        screen,
        appPkg,
        steps,
        deviceDate,
        pacing,
        animationsOff,
        prevHash,
        log,
      });
      if (v.status === 'done' || v.status === 'blocked') {
        if (v.status === 'blocked') blockedReason = v.blockedReason;
        status = v.status;
        steps.push(v.record);
        break;
      }
      steps.push(v.record);
      if (v.table) {
        table = v.table;
        rootPkg = v.rootPkg;
        prevHash = v.hash;
      }
      if (v.record.changed !== null) log(`        → ${v.record.changed ? 'screen changed' : 'no visible change'}`);
      continue;
    }
    const state = {
      goal,
      provided_texts: values.length ? values : null,
      today: deviceDate,
      screen: {
        app: appPkg,
        size: screen ? `${screen.w}x${screen.h}` : null,
        launcher: launcher || undefined,
      },
      elements: serializeRows(table),
      elements_note: elementsNote(table, launcher),
      recent_actions: steps
        .slice(-10)
        .map((s) => `${s.step}. ${s.action}${s.changed === false ? ' (no visible change)' : ''}`),
    };
    const questions = buildQuestions({
      table,
      screen,
      texts: values,
      apps: (apps ?? []).map((a) => a.label),
      copied,
      canScroll: scrollOffered(table, screen),
      launcher,
      goal,
      answers,
      recentChoices: steps.map((s) => s.choice).filter(Boolean),
      // The previous step's own verdict, not a rule the model has to apply:
      // a scroll that changed nothing is withdrawn for the next step.
      lastUnchanged: steps.at(-1)?.changed === false ? steps.at(-1).choice : null,
      today: deviceDate,
    });
    const {
      operation: op,
      goalMet,
      usage,
      latencyMs: apiMs,
    } = await askDecision({
      state,
      questions,
      log,
    });
    if (usage && Number.isFinite(usage.input_tokens)) tokens.input += usage.input_tokens;
    if (usage && Number.isFinite(usage.output_tokens)) tokens.output += usage.output_tokens;

    // done is the model's verdict, accepted as-is — one judgment per step.
    // A second confirm question persistently disagreed with the primary one
    // on already-successful screens (ping-pong to max_steps); the caller
    // remains the source of truth for side-effects. goal_met is logged as a
    // diagnostic only.
    if (op.choice === 'done') {
      status = 'done';
      steps.push({
        step,
        choice: op.choice,
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
        choice: op.choice,
        action: `blocked (goal_met=${goalMet.noul.toFixed(2)}, conf=${op.confidence.toFixed(2)})`,
        changed: null,
        apiMs,
        goalMetNoul: goalMet.noul,
      });
      break;
    }
    // An answer ends the run the way done does — it is the terminal act for a
    // question goal. Resolved here (never by the model) so the submitted text
    // is exactly a row's text or a provided value.
    const answer = answers ? answerFor(op.choice, { table, values }) : null;
    if (answer !== null) {
      status = 'answered';
      steps.push({
        step,
        choice: op.choice,
        action: `answer ${JSON.stringify(answer)} (conf=${op.confidence.toFixed(2)})`,
        changed: null,
        apiMs,
        answer,
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
    const holdM = /^hold_(\d+)$/.exec(op.choice);
    const setM = /^set_(\d+)_(\w+)$/.exec(op.choice);
    const clipM = /^clip_(\d+)$/.exec(op.choice);
    const launchM = /^launch_(\d+)$/.exec(op.choice);
    if (clickM) {
      const row = table.find((r) => String(r.idx) === clickM[1]);
      if (!row) throw new DroidJevError(`click target index ${clickM[1]} not in current table; no action executed`);
      log(`step ${step}: tap ${row.row} (conf ${op.confidence.toFixed(2)})`);
      await tap(serial, row.el.center);
      // History must record the state a tap PRODUCES, not the one it found:
      // "tap check=on" in recent_actions after a flip reads as success when
      // the tap actually turned the toggle off.
      action = row.el.checkable ? `tap ${row.row} (now check=${row.el.checked ? 'off' : 'on'})` : `tap ${row.row}`;
    } else if (holdM) {
      const row = table.find((r) => String(r.idx) === holdM[1]);
      if (!row) throw new DroidJevError(`hold target index ${holdM[1]} not in current table; no action executed`);
      log(`step ${step}: hold ${row.row} (conf ${op.confidence.toFixed(2)})`);
      await longPress(serial, row.el.center);
      action = `hold ${row.row}`;
    } else if (setM) {
      const row = table.find((r) => String(r.idx) === setM[1]);
      const stop = sliderStops(goal).find((s) => s.key === setM[2]);
      if (!row || !stop) {
        throw new DroidJevError(
          `set target ${op.choice} is not a slider stop in the current table; no action executed`,
        );
      }
      log(`step ${step}: drag slider ${row.row} to ${stop.label} (conf ${op.confidence.toFixed(2)})`);
      await slideTo(serial, row.el, stop.fraction);
      action = `drag slider ${row.row} to ${stop.label}`;
    } else if (launchM) {
      const app = (apps ?? [])[Number(launchM[1])];
      if (!app) throw new DroidJevError(`launch choice names an app that was not offered; no action executed`);
      const res = await startApp(serial, app.pkg);
      if (!res.focused) log(`        → ${app.pkg} did not take focus (now ${res.app?.pkg ?? '?'})`);
      log(`step ${step}: open ${app.label} (conf ${op.confidence.toFixed(2)})`);
      action = `open ${app.label}`;
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
        choice: op.choice,
        action: `copy ${shown} from ${row.row}`,
        changed: null,
        apiMs,
        actMs: performance.now() - stepT0,
        confidence: op.confidence,
        stagedText: staged, // raw value a later paste verifies against the screen
      });
      continue;
    } else if (clipM) {
      const value = values[Number(clipM[1])];
      if (value === undefined) {
        throw new DroidJevError(
          `clip choice names provided_texts[${clipM[1]}] which does not exist; no action executed`,
        );
      }
      await setClipboard(serial, value);
      const shown = preview(value);
      log(`step ${step}: copy provided value ${shown} to the clipboard`);
      copied = true;
      steps.push({
        step,
        choice: op.choice,
        action: `copy ${shown} (the value the goal provides) to the clipboard`,
        changed: null,
        apiMs,
        actMs: performance.now() - stepT0,
        confidence: op.confidence,
        stagedText: value,
      });
      continue;
    } else if (op.choice.startsWith('scroll_')) {
      const dir = op.choice.slice('scroll_'.length);
      // Consecutive same-direction scrolls pace the distance (long lists);
      // a reversal resets the pacing.
      pacing.sameDirRun = dir === pacing.lastScrollDir ? pacing.sameDirRun + 1 : 0;
      pacing.lastScrollDir = dir;
      const vertical = dir === 'down' || dir === 'up';
      if (vertical) {
        const anchor = scrollAnchor(table, screen || { w: 1080, h: 2340 });
        const dy = scrollDistance(screen?.h ?? 2340, pacing.sameDirRun);
        await scrollSwipe(serial, anchor, dy, screen?.h ?? 2340, dir);
        log(`step ${step}: ${op.choice} (${Math.round((dy / (screen?.h ?? 2340)) * 100)}% of screen)`);
      } else {
        const w = screen?.w ?? 1080;
        const anchor = horizontalAnchor(table, screen) ?? scrollAnchor(table, screen || { w, h: 2340 });
        const dx = scrollDistance(w, pacing.sameDirRun);
        await scrollSwipeX(serial, anchor, dx, w, dir);
        log(`step ${step}: ${op.choice} (${Math.round((dx / w) * 100)}% of screen)`);
      }
    } else if (op.choice === 'back') {
      await keyevent(serial, KEYS.back);
      pacing.lastScrollDir = null;
    } else if (op.choice === 'home') {
      await keyevent(serial, KEYS.home);
      pacing.lastScrollDir = null;
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
          choice: op.choice,
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

    steps.push({
      step,
      choice: op.choice,
      action,
      changed,
      apiMs,
      actMs: performance.now() - stepT0,
      confidence: op.confidence,
    });
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
    // The submitted text when the goal was a question (status "answered").
    answer: steps.find((s) => s.answer)?.answer ?? null,
    tokens,
    tookMs: performance.now() - t0,
    finalRows: serializeRows(table).slice(0, 15),
    finalApp: rootPkg ?? null,
  };
}
