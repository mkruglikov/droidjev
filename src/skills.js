// Goal-specific macro skills: composite actions the host can execute in one
// step for goals that state their own data (a keypad sequence, a table of
// rows, a labeled form). They live outside the generic loop so buildQuestions
// stays generic, each macro is testable alone, and a host (the decide
// protocol) can later share the same definitions.
//
// apply(qi) returns the criteria mutations it wants, or null:
//   { options?, delete?: string[], deletePrefixes?: string[], deleteScrolls? }
// `anchor` is the emission position, because criteria key order is prompt
// order: 'assign' folds options in before the fixed ops (digits_0), 'post'
// runs after them in registry order, 'postStuck' after the loop withdrawals.
import { elementText, isEditable } from './elements.js';

// --- the goal parsers (pure; re-exported by agent.js for hosts/tests) -------

// A keypad's digit sequence, when the goal states the value in mechanical
// form: "0 hours, 16 minutes, and 35 seconds" → 1635, "3 hours" → 30000
// (the odometer reads 03h00m00s), "14:35" → 1435. Any subset of units works.
// The odometer entry is one atomic action when the host can send keycodes —
// digit-by-digit asks the model to count, and it loses count.
export function keypadSequence(goal) {
  const g = String(goal || '')
    .toLowerCase()
    .replace(/,/g, ' ');
  const unit = (name) => Number(new RegExp(`(\\d+)\\s*${name}\\b`).exec(g)?.[1] ?? 0);
  const h = unit('hours?');
  const m = unit('minutes?');
  const s = unit('seconds?');
  if (h || m || s) {
    const total = h * 10000 + m * 100 + s;
    return String(total).replace(/^0+(?=\d)/, '');
  }
  const clock = /\b(\d{1,2}):(\d{2})\b/.exec(g);
  if (clock) return `${Number(clock[1])}${clock[2]}`;
  return null;
}

// A goal that states its data as a pipe-delimited table:
//   name|amount_dollars|category_name|note
//   Stationery|$352.34|Others|A need
// → header names the columns (not data), each following line is one row.
// Multi-entry goals are generated this way; parsing it host-side is what lets
// a row be entered atomically instead of trusting a small model to track
// which values belong to which entry.
export function goalTable(goal) {
  const lines = String(goal || '').split('\n');
  const headerWords = /^(name|amount\w*|category\w*|note|description|title|date|time|priority|quantity|unit)$/i;
  const rows = [];
  let header = null;
  for (const line of lines) {
    if (line.split('|').length < 3) continue;
    const cells = line
      .split('|')
      .map((c) => c.trim())
      .filter(Boolean);
    if (!cells.length) continue;
    if (cells.every((c) => headerWords.test(c))) {
      header = cells;
      continue;
    }
    rows.push(cells);
  }
  return { header, rows };
}

// Labeled values the goal states in quotes — `with the title 'X' and the
// description 'Y'` — as {label, value} pairs. The label names the form field
// the value belongs in; filling a form field-by-field is where the model
// wanders (it scrolls a form whose fields are all visible).
export function goalFields(goal) {
  const text = String(goal || '');
  const fields = [];
  const labelled =
    /(titles?|titled|description|location|name[sd]?|subject|body|note|label)\s*(?:is|:)?\s*['“]([^'”]{1,120})['”]/gi;
  for (const m of text.matchAll(labelled)) {
    const label = m[1]
      .toLowerCase()
      .replace(/^(titled|named|names|titles)$/, (w) => (w.startsWith('titl') ? 'title' : 'name'));
    fields.push({ label, value: m[2] });
  }
  return fields;
}

// Values the goal itself supplies for typing. The goal is operator input, so
// these are as trusted as --text: `droidjev act "<full flow>"` can fill the
// fields a goal already names. Only patterns that unambiguously denote a value
// are read; the rest of the goal stays prose.
export function goalTexts(goal) {
  const text = String(goal || '');
  const values = [];
  const add = (v) => {
    const s = String(v ?? '')
      .trim()
      .replace(/\s+/g, ' ');
    if (!s || s.length > 120 || /[\n\r]/.test(s)) return;
    if (!values.some((x) => x.toLowerCase() === s.toLowerCase())) values.push(s);
  };
  // Quoted values: the operator spelled the exact text.
  for (const m of text.matchAll(/['“"]([^'”"]{1,120})['”"]/g)) add(m[1]);
  // Route endpoints only in a route goal: "build a route from A to B". Commas
  // stay inside a value — "moscow, ID" and "saint-petersburg, russia" are
  // qualified places, not two values each. "scroll from top to bottom" has no
  // route word and must not become two typed values.
  if (/\b(route|directions|navigat|drive|walk|transit|cycl)\w*/i.test(text)) {
    const route = /\bfrom\s+(.+?)\s+to\s+(.+?)(?=[.!?]|,\s+(?:and|then)\b|$)/i.exec(text);
    if (route) {
      add(route[1]);
      add(route[2]);
    }
  }
  // A person's name: "named Maria" / "called Bob" — not the stopwords that
  // follow "called" in ordinary prose ("called to order").
  for (const m of text.matchAll(/\b(?:named|called)\s+([A-Za-z][\w'’-]{0,40})/gi)) {
    if (!/^(?:to|from|the|a|an|and|with|for|at|in|on|by)$/i.test(m[1])) add(m[1]);
  }
  // A phone/email only when the value has that shape: "phone is available"
  // must not become a typed value.
  for (const m of text.matchAll(/\bphone(?:\s+number)?\s*[:=]?\s*(\+?\d[\d\s().-]{5,})/gi)) {
    add(m[1].replace(/[.\s]+$/, ''));
  }
  for (const m of text.matchAll(/\bemail(?:\s+address)?\s*[:=]?\s*([^\s,;]+@[^\s,;]+)/gi)) {
    add(m[1].replace(/[.,;]+$/, ''));
  }
  return values;
}

export const hasKeypad = (table) => table.filter((r) => /^\d$/.test(elementText(r.el))).length >= 6;

// --- the registry -----------------------------------------------------------

export const SKILLS = [
  {
    // The keypad's digit sequence, straight from the goal: one atomic action
    // beats six counted presses (the model loses count mid-sequence).
    id: 'digits',
    anchor: 'assign',
    apply({ table, goal }) {
      const seq = hasKeypad(table) ? keypadSequence(goal) : null;
      return seq
        ? {
            options: {
              digits_0: `Type the goal's digit sequence "${seq}" into the keypad in one action — the host sends every digit; the display fills to the goal's value`,
            },
          }
        : null;
    },
  },
  {
    // On the launcher, a launch_<k> that names the goal's app is one step and
    // the drawer hunt is many: the model scrolled the home screen for a whole
    // 10-step budget with launch_0 offered (calendar query, observed). With a
    // launch option on the table, the scrolls only page icons.
    id: 'launcherScrolls',
    anchor: 'post',
    apply({ launcher, apps }) {
      return launcher && apps.length ? { deleteScrolls: true } : null;
    },
  },
  {
    // A goal that states its data as a table (pipe-separated rows) is entered
    // row by row into the visible form. Each row is ONE atomic option the host
    // fills and saves; rows already used this run are withdrawn, so the same
    // entry is never saved twice (observed: 4 entries for a 3-entry goal).
    id: 'addRow',
    anchor: 'post',
    apply({ goal, recentChoices }) {
      const rows = goalTable(goal).rows;
      if (!rows.length) return null;
      const used = new Set(
        recentChoices
          .map((c) => /^add_row_(\d+)$/.exec(String(c))?.[1])
          .filter((k) => k !== undefined)
          .map(Number),
      );
      const options = {};
      rows.forEach((row, k) => {
        if (!used.has(k)) {
          options[`add_row_${k}`] =
            `Add entry ${k + 1} of the goal's table (${row.join(' · ')}) — the host opens the form if needed, enters every column in its right field, picks the category and saves`;
        }
      });
      return { options };
    },
  },
  {
    // A grid game driven by Up/Down/Left/Right buttons: the host walks the
    // piece to the goal cell, using the piece's own position as feedback (a
    // move that changes its bounds is progress; a wall does not). A small model
    // cannot plan a maze blind, but the host can walk it deterministically.
    // Hand-pressing directions is how the piece wanders; the host walk is the
    // only route while it is offered. Idle and chrome clicks are noise on a
    // one-screen game (the model picks them over a novel op).
    id: 'walk',
    anchor: 'post',
    apply({ table, recentChoices }) {
      const dirLabels = ['up', 'down', 'left', 'right'].filter((d) =>
        table.some((r) => elementText(r.el).toLowerCase() === d),
      );
      if (dirLabels.length !== 4 || recentChoices.includes('walk_0')) return null;
      return {
        options: {
          walk_0:
            'Walk the piece (the X) to the goal cell with the Up/Down/Left/Right buttons — the host presses them and follows the piece until it arrives',
        },
        // With the host walk available, hand-driven navigation is the wrong route.
        delete: ['wait', 'back', 'home'],
        deletePrefixes: ['click_', 'scroll_'],
      };
    },
  },
  {
    // "Click the button N times, remember the numbers, enter their product":
    // the displayed numbers are readable, so the host presses, multiplies and
    // submits — a small model cannot reliably hold five numbers and multiply.
    // Re-offered until the game's button is gone: a blocked first attempt (a
    // consent dialog over the page) leaves the game untouched, so the op must
    // stay available after it. Idle ops are noise on a one-screen game and the
    // model is *confidently* idle (wait at 0.74) rather than novel; with those
    // gone it clicked unlabeled browser chrome instead — so every click is
    // withdrawn too. done/blocked remain, and done is checked.
    id: 'multiply',
    anchor: 'post',
    apply({ goal, table }) {
      if (!/\bproduct\b/i.test(goal) || !table.some((r) => /click me/i.test(elementText(r.el)))) return null;
      return {
        options: {
          multiply_0:
            'Play the memory game: press the button five times, read each number shown, enter their product in the form and submit — done in this one action',
        },
        delete: ['wait', 'back', 'home'],
        deletePrefixes: ['click_', 'scroll_'],
      };
    },
  },
  {
    // Labeled goal values into the visible form: the goal names the field each
    // value belongs in ("the title 'X'"), so the host types each into the field
    // whose label matches. A small model scrolls a form whose fields are all
    // visible and never types anything (calendar editor, observed 4×).
    // Scrolling reveals nothing on a form whose fields are all in the table.
    id: 'fill',
    anchor: 'post',
    apply({ goal, table, recentChoices }) {
      const fieldsOfGoal = goalFields(goal);
      const editableLabels = table.filter((r) => isEditable(r.el)).map((r) => elementText(r.el).toLowerCase());
      if (
        fieldsOfGoal.length < 2 ||
        !fieldsOfGoal.some((f) => editableLabels.some((l) => l.includes(f.label))) ||
        recentChoices.includes('fill_0')
      ) {
        return null;
      }
      return {
        options: {
          fill_0: `Fill the form: type each labeled value into the field named like it (${fieldsOfGoal
            .map((f) => `${f.label} = "${f.value}"`)
            .join('; ')}) — the host types them all in one action`,
        },
        deleteScrolls: true,
      };
    },
  },
  {
    // Resend: the last message's text, sent again. The host reads the bubble
    // and re-sends it — copy/paste across two screens is where the model left
    // the conversation and never came back.
    id: 'resend',
    anchor: 'post',
    apply({ goal, table, recentChoices }) {
      if (!/\bresend\b/i.test(goal) || !table.some((r) => isEditable(r.el)) || recentChoices.includes('resend_0')) {
        return null;
      }
      return {
        options: {
          resend_0:
            'Resend the last message: its text is sent again exactly as shown (the host reads the bubble, types it and presses send)',
        },
      };
    },
  },
  {
    // A recording is complete after its start press and its stop press; a
    // third press of the same control starts ANOTHER recording and ruins a goal
    // that asked for one. Prose rules did not stop the model from pressing
    // again, so after two recorded presses the control is withdrawn outright.
    id: 'shutter',
    anchor: 'postStuck',
    apply({ goal, table, recentActions }) {
      if (!/\b(video|record)\b/i.test(goal)) return null;
      const presses = recentActions.filter((a) => /click \[\d+\].*(shutter|record)/i.test(String(a))).length;
      if (presses < 2) return null;
      const del = table.filter((r) => /shutter|record/i.test(elementText(r.el) || '')).map((r) => `click_${r.idx}`);
      return del.length ? { delete: del } : null;
    },
  },
];

export const skillsAt = (anchor) => SKILLS.filter((s) => s.anchor === anchor);

export function applyTo(criteria, skill, qi) {
  const r = skill.apply(qi);
  if (!r) return;
  if (r.options) Object.assign(criteria, r.options);
  for (const k of r.delete ?? []) delete criteria[k];
  if (r.deleteScrolls) {
    delete criteria.scroll_down;
    delete criteria.scroll_up;
  }
  for (const p of r.deletePrefixes ?? []) {
    for (const k of Object.keys(criteria)) if (k.startsWith(p)) delete criteria[k];
  }
}
