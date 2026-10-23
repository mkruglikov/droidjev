// One decision, no device.
//
// The act loop owns the device: dump → ask → tap. A host that already has its
// own observation and action pipeline (a test harness, a web driver) only
// needs the decision, so this module exposes exactly that — the same table
// build, the same questions, the same strict validation — as one JSON request
// in and one JSON answer out. Nothing here touches adb: the caller supplies the
// element list and executes whatever action comes back.
import { buildTable, serializeRows, elementText, isSlider } from './elements.js';
import { buildQuestions, elementsNote, scrollOffered, answerFor, sliderStops, askDecision } from './agent.js';
import { slideGesture } from './act.js';
import { ask } from './typesafe.js';
import { DroidJevError } from './util.js';

// Assemble the request without sending it: pure, so callers (and tests) can
// inspect exactly what the model would be asked.
export function buildDecideRequest({
  goal,
  screen = null,
  elements,
  recent_actions = [],
  provided_texts = null,
  apps = null,
  can_scroll = null,
  copied = false,
  clipboard = true,
  clipboard_ready = false,
  answers = false,
  recent_choices = [],
  last_unchanged = null,
  today = null,
}) {
  if (!goal || typeof goal !== 'string') {
    throw new DroidJevError('decide needs a goal string');
  }
  if (!Array.isArray(elements)) {
    throw new DroidJevError("decide needs an elements array (the host's UI dump)");
  }
  const texts = Array.isArray(provided_texts) ? provided_texts : provided_texts ? [provided_texts] : [];
  const labels = Array.isArray(apps) ? apps : apps ? [apps] : [];
  const table = buildTable(elements, screen);
  const canScroll = can_scroll === null ? scrollOffered(table, screen) : !!can_scroll;
  const app = screen?.app ?? 'unknown';
  const launcher = /launcher/i.test(app);
  const questions = buildQuestions({
    table,
    screen,
    texts,
    apps: labels,
    copied: copied && clipboard,
    clipboardReady: clipboard_ready,
    canScroll,
    launcher,
    goal,
    answers,
    clipboard,
    recentChoices: recent_choices,
    lastUnchanged: last_unchanged,
    today,
  });
  if (!clipboard) {
    // The host has no clipboard operation: offering copy_/paste would invite a
    // decision it cannot execute. The rules describing them stay verbatim, so
    // the prompt is still the one droidjev ships.
    for (const key of Object.keys(questions.operation.criteria)) {
      if (key.startsWith('copy_') || key === 'paste') delete questions.operation.criteria[key];
    }
  }
  const state = {
    goal,
    provided_texts: texts.length ? texts : null,
    // The device's own date, when the host knows it: "tomorrow" and "today"
    // are only decidable against it.
    today: today || undefined,
    screen: { app, size: screen?.size ?? null, launcher: launcher || undefined },
    elements: serializeRows(table),
    elements_note: elementsNote(table, launcher),
    recent_actions: Array.isArray(recent_actions) ? recent_actions.slice(-10) : [],
  };
  // buildTable drops elements and renumbers the rest, so `click_3` means row 3
  // of the table, not element 3 of the caller's list. Row objects keep the
  // caller's element by reference, so map identity back to its position, and
  // hand back each row's tap point: a host that re-resolves indices against a
  // fresh dump can land on a shorter list than the one it decided from.
  const sourceOf = new Map(elements.map((el, i) => [el, i]));
  const sources = Object.fromEntries(table.map((r) => [r.idx, sourceOf.get(r.el) ?? null]));
  const centers = Object.fromEntries(table.map((r) => [r.idx, r.el.center ?? null]));
  return { state, questions, table, sources, centers };
}

// askFn is injectable so tests can drive the whole path without the network.
export async function decide(request, { askFn = ask, log = () => {} } = {}) {
  const { state, questions, table, sources, centers } = buildDecideRequest(request);
  const { operation, goalMet, usage, latencyMs, retried } = await askDecision({ state, questions, askFn, log });
  const values = Array.isArray(request.provided_texts)
    ? request.provided_texts
    : request.provided_texts
      ? [request.provided_texts]
      : [];
  // A slider stop is a drag, and a drag is not a click: hand the host the four
  // numbers of the gesture it must replay, computed from the same dump the
  // decision was made on.
  const drags = {};
  for (const r of table) {
    if (!isSlider(r.el) || !r.el.bounds) continue;
    for (const s of sliderStops(request.goal)) drags[`set_${r.idx}_${s.key}`] = slideGesture(r.el, s.fraction);
  }
  return {
    operation,
    goal_met: goalMet,
    criteria: Object.keys(questions.operation.criteria),
    rows: serializeRows(table),
    sources,
    centers,
    drags,
    // What a copy_/answer_ choice acts on, resolved here: a host that executes
    // the decision itself should not have to re-derive which text the row held
    // (the serialized row is a display form, not the text).
    texts: Object.fromEntries(table.map((r) => [r.idx, elementText(r.el)])),
    answer: request.answers ? answerFor(operation.choice, { table, values }) : null,
    truncated: !!table.truncated,
    app: state.screen.app,
    retried,
    latencyMs,
    usage,
  };
}

// Long-lived mode: one JSON request per line on stdin, one JSON answer per line
// on stdout. A host that decides every step should not fork a process per step —
// forking a threaded process repeatedly is what deadlocks (the child can inherit
// a held lock and never reach exec, leaving the parent blocked in Popen), so
// `decide --serve` pays the startup cost once.
export async function serveDecide(input = process.stdin, output = process.stdout, askFn = ask) {
  let buffer = '';
  for await (const chunk of input) {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (!line) continue;
      let reply;
      try {
        reply = { ok: true, ...(await decide(JSON.parse(line), { askFn })) };
      } catch (error) {
        reply = { ok: false, error: error?.message || String(error) };
      }
      output.write(`${JSON.stringify(reply)}\n`);
    }
  }
}
