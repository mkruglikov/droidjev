// Element table: filter the normalized layout to the elements the model should
// see, give them stable indices, and serialize compact rows. The model only
// ever sees rows like `[12] click "About phone"` — never coordinates.
import { createHash } from 'node:crypto';

const MAX_ROWS = 80;
// Top fraction of the screen where the platform's chrome package is a status
// bar rather than content.
const STATUS_BAR_FRACTION = 0.06;
const CHROME_PKGS = new Set(['com.android.systemui']);

const classShort = (cls) => (cls || '').split('.').pop() || '';
const resourceIdTail = (id) => (id || '').split('/').pop() || id || '';

export const isEditable = (el) => /EditText|Editable|AutoCompleteTextView|TextInput/.test(el.class || '');

// A slider is a track with a thumb: the value follows the touch position, so
// the only way to set it is a drag. Android's SeekBar and Material's Slider
// both report the value as the element's own text, which is what lets the
// model read the current position off the row.
export const isSlider = (el) => /SeekBar|RangeSlider|\.Slider$|(^|\.)Slider\b/.test(el.class || '');

// A row whose text is a date: a calendar header, a task's due chip. Flagged
// because clicking it opens a date picker — the fast route to a date the goal
// names, next to a calendar's day-by-day arrows.
const DATE_LABEL = new RegExp(
  '^(?:\\d{4}-\\d{1,2}-\\d{1,2}' +
    '|\\d{1,2}/\\d{1,2}/\\d{2,4}' +
    '|(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\\.?\\s+\\d{1,2}(?:st|nd|rd|th)?(?:,?\\s+\\d{4})?)' +
    '(?:\\s|$)',
);
export const isDateLabel = (text) => DATE_LABEL.test(String(text || '').trim());

function cleanLabel(s) {
  const t = String(s || '')
    .replace(/"/g, "'")
    .replace(/\s+/g, ' ')
    .trim();
  return t.length > 90 ? t.slice(0, 87) + '…' : t;
}

function onScreen(el, screen) {
  if (!el.bounds) return false;
  const { l, t, r, b } = el.bounds;
  if (r <= l || b <= t) return false; // zero/negative area
  if (el.offScreen) return false;
  if (!screen) return true;
  return r > 0 && b > 0 && l < screen.w && t < screen.h; // intersects viewport
}

// Geometry-based label adoption: an unlabeled interactive element takes the
// text of a labeled element geometrically contained in it. Works for both
// providers (no tree needed); launcher icon = clickable FrameLayout containing
// the icon label TextView.
function adoptLabel(el, labeled) {
  let best = null;
  const elArea = (el.bounds.r - el.bounds.l) * (el.bounds.b - el.bounds.t);
  for (const c of labeled) {
    if (c === el || !c.bounds || !el.bounds) continue;
    const inside =
      c.bounds.l >= el.bounds.l - 2 &&
      c.bounds.t >= el.bounds.t - 2 &&
      c.bounds.r <= el.bounds.r + 2 &&
      c.bounds.b <= el.bounds.b + 2;
    if (!inside) continue;
    const area = (c.bounds.r - c.bounds.l) * (c.bounds.b - c.bounds.t);
    // A label must be a substantial part of the container; otherwise a huge
    // wrapper (scrollable workspace) "adopts" some tiny unrelated child.
    // 3%: real row titles observed at 4.7-29% of their container; a
    // workspace's stray child is far below 1%.
    if (area < elArea * 0.03) continue;
    const label = cleanLabel(c.text) || cleanLabel(c.contentDesc);
    if (!label) continue;
    // Titles and subtitles both sit inside the row; the title is usually the
    // smaller box, so smallest-area wins (short badges lose to it).
    if (!best || area < best.area) best = { label, area };
  }
  return best?.label || null;
}

// Flat providers (android CLI) expose preference rows without hierarchy: an
// unlabeled switch/checkbox sits beside its title, never inside it. Adopt the
// nearest label that lies entirely to the left of the control with its
// vertical center inside the control's band — i.e. the same row.
function rowLabel(el, labeled) {
  if (!el.checkable || !el.bounds) return null;
  let best = null;
  for (const c of labeled) {
    if (c === el || !c.bounds || c.bounds.r > el.bounds.l) continue;
    const mid = (c.bounds.t + c.bounds.b) / 2;
    if (mid < el.bounds.t || mid > el.bounds.b) continue;
    const label = cleanLabel(c.text) || cleanLabel(c.contentDesc);
    if (!label) continue;
    const dist = el.bounds.l - c.bounds.r;
    if (!best || dist < best.dist) best = { label, dist };
  }
  return best?.label ?? null;
}

// Automation companions droidjev itself installs (the io.appium.settings
// clipboard helper, the uiautomator2 server) show up on the launcher as
// ordinary app icons — a goal saying "open Settings" then matches "Appium
// Settings" literally. They are never a legitimate target for any command.
const HELPER_PKGS = new Set([
  'io.appium.settings',
  'io.appium.uiautomator2.server',
  'io.appium.uiautomator2.server.test',
]);
const isHelperEl = (el) =>
  HELPER_PKGS.has(el.pkg) ||
  cleanLabel(el.text) === 'Appium Settings' ||
  cleanLabel(el.contentDesc) === 'Appium Settings';

// The largest group of same-sized, unlabeled, interactive elements laid out in
// rows and columns — a drawn grid (a calendar month, a canvas, a dial pad with
// drawn digits). Returns the group so callers can both drop it from the table
// and describe what was dropped.
function drawnGrid(elements) {
  const groups = new Map();
  for (const el of elements) {
    if (!el.bounds || cleanLabel(el.text) || cleanLabel(el.contentDesc)) continue;
    if (!(el.clickable || el.longClickable || el.checkable || isEditable(el))) continue;
    const key = `${el.bounds.r - el.bounds.l}x${el.bounds.b - el.bounds.t}`;
    groups.set(key, [...(groups.get(key) || []), el]);
  }
  let best = null;
  for (const [size, cells] of groups) {
    if (cells.length < 6 || new Set(cells.map((c) => c.bounds.l)).size < 3) continue;
    if (!best || cells.length > best.cells.length) best = { size, cells };
  }
  return best;
}

export function buildTable(elements, screen) {
  // A helper's launcher icon is its clickable container (which carries no
  // label of its own) — drop the container with the helper node (its direct
  // parent), or an anonymous clickable row survives to be tapped blindly.
  // A scrollable parent is kept: all-apps grids sit icons directly in the
  // RecyclerView, and losing it would break scrolling for the whole drawer.
  const hidden = new Set();
  elements.forEach((el, i) => {
    if (isHelperEl(el)) {
      hidden.add(i);
      const p = el.parentIdx !== null && el.parentIdx !== undefined ? elements[el.parentIdx] : null;
      if (p && !p.scrollable) hidden.add(el.parentIdx);
    }
  });
  // The status bar is chrome, not the app: its clock, battery and notification
  // icons belong to another package and sit in the top band. A clock reading
  // "15:34" looks exactly like a time field waiting to be filled in — observed
  // as a step spent tapping it in an event editor — and an answer picked from
  // it is wrong. Dialogs from other packages (the brightness slider) are not in
  // that band, so they stay.
  // Only the platform's own chrome package, and only in the top band: an app's
  // rows are never dropped, whatever the dump's ordering.
  const isStatusBar = (el) =>
    !!screen && CHROME_PKGS.has(el.pkg) && el.bounds && el.bounds.b <= screen.h * STATUS_BAR_FRACTION;
  // An open IME floods the table with its keys — 40 single-character rows
  // crowding out the app's own. Typing is atomic here (type_, paste, digits),
  // so the keys are never the right target. Ten or more clickable
  // single-character labels from ANOTHER package is a keyboard, and it goes —
  // same-package single chars are an in-app dial pad (the clock timer),
  // which stays: its digits are exactly what a keypad goal must press.
  const appPkg = screen?.app;
  const imeKeys = new Set(
    elements.filter(
      (el) =>
        el.clickable &&
        el.pkg &&
        (/inputmethod|keyboard/i.test(el.pkg) ||
          (appPkg &&
            el.pkg !== appPkg &&
            /^[\w,.'"!?-]$/.test(cleanLabel(el.text) || cleanLabel(el.contentDesc) || ''))),
    ),
  );
  if (imeKeys.size < 10) imeKeys.clear();
  const visible = elements.filter(
    (el, i) => !hidden.has(i) && !isStatusBar(el) && !imeKeys.has(el) && onScreen(el, screen),
  );
  // A grid of same-sized unlabeled cells is drawn, not described: a calendar's
  // month, a canvas. The model cannot tell one cell from another, so tapping
  // them is guessing — and 40 of them crowd out the rows that do carry a label.
  // They are dropped from the table and reported as a note instead, which is
  // what points the model at the text route (a view switcher, a picker).
  const grid = drawnGrid(visible);
  const shown = grid ? visible.filter((el) => !grid.cells.includes(el)) : visible;
  const labeled = shown.filter((el) => cleanLabel(el.text) || cleanLabel(el.contentDesc));

  const entry = (el) => {
    const interactive = el.clickable || el.longClickable || el.scrollable || el.checkable || isEditable(el);
    const own = cleanLabel(el.text) || cleanLabel(el.contentDesc) || cleanLabel(el.hint);
    if (!interactive && !own) return null; // decorative container
    const label = own || (interactive ? adoptLabel(el, labeled) : '') || rowLabel(el, labeled) || '';
    const flags = [];
    if (el.clickable) flags.push('click');
    if (el.longClickable) flags.push('longclick');
    if (el.scrollable) flags.push('scroll');
    if (isEditable(el)) flags.push('edit');
    if (el.checkable) flags.push(el.checked ? 'check=on' : 'check=off');
    if (isSlider(el)) flags.push('range');
    if (isDateLabel(label)) flags.push('date');
    if (el.selected) flags.push('selected');
    if (el.focused) flags.push('focused');
    let row = `[${rows.length}] ${flags.join('+') || 'text'}`;
    if (label) row += ` "${label}"`;
    if (el.hint && !own) row += ` hint="${cleanLabel(el.hint)}"`;
    if (!label) row += ` ${classShort(el.class)}`;
    if (!label && el.resourceId) row += ` id=${resourceIdTail(el.resourceId)}`;
    // A labeled non-interactive element is a tap candidate: providers may not
    // flag its row container as clickable (android CLI drops some Settings
    // rows), but tapping the label's center reaches the container's handler.
    const tappable = !interactive && !!own;
    return { interactive: interactive || tappable, tappable, row, el };
  };

  // Interactive elements win the cap: a dropped clickable target can never be
  // recovered by scrolling blind. Context (text-only) rows fill the remainder.
  const rows = [];
  const entries = shown.map(entry).filter(Boolean);
  for (const e of entries) {
    if (!e.interactive) continue;
    e.idx = rows.length;
    e.row = e.row.replace(/^\[\d+\]/, `[${e.idx}]`);
    rows.push(e);
    if (rows.length >= MAX_ROWS) break;
  }
  let truncated = entries.length > rows.length;
  for (const e of entries) {
    if (e.interactive || rows.length >= MAX_ROWS) continue;
    e.idx = rows.length;
    e.row = e.row.replace(/^\[\d+\]/, `[${e.idx}]`);
    rows.push(e);
  }
  truncated = truncated || entries.length > rows.length;
  // A container that holds several labeled rows says which ones, so the model
  // can reason about a list or a selector without the hierarchy. A title and
  // subtitle inside a preference row is two, not a list: three items, or a
  // scrollable container with two.
  for (const r of rows) {
    if (!r.el.bounds) continue;
    const items = listItems(rows, r.idx);
    if (items.length >= 3 || (r.el.scrollable && items.length >= 2)) {
      r.row += ` holds [${items.map((i) => i.idx).join(',')}]`;
    }
  }
  Object.defineProperty(rows, 'truncated', { value: truncated, enumerable: false });
  Object.defineProperty(rows, 'grid', { value: grid, enumerable: false });
  return rows;
}

// Model-facing view: rows only, no coordinates, no resource internals.
export function serializeRows(table) {
  return table.map((r) => r.row);
}

// Scroll anchor: the scrollable that holds the focus wins (a number picker or
// a small inner list is the thing a swipe must move, and a swipe anywhere else
// scrolls the wrong container — or, with the keyboard up, glides over its
// keys). The innermost holder wins: a page contains the picker contains the
// focused cell, and the picker is what has to move. Otherwise the largest
// scrollable's center, else the screen middle.
// The scrollable a gesture must move: the innermost one holding the focus,
// else the largest. Exposed as the ROW so a host can size its swipe from the
// element's own bounds; scrollAnchor wraps it as a point.
export function scrollAnchorRow(table) {
  const focused = table.find((r) => r.el.focused && r.el.bounds);
  const scrollables = table.filter((r) => r.el.scrollable && r.el.bounds);
  const holders = focused ? scrollables.filter((r) => contains(r.el.bounds, focused.el.bounds)) : [];
  const area = (r) => (r.el.bounds.r - r.el.bounds.l) * (r.el.bounds.b - r.el.bounds.t);
  const smallest = holders.reduce((a, b) => (a === null || area(b) < area(a) ? b : a), null);
  const largest = scrollables.reduce((a, b) => (a === null || area(b) > area(a) ? b : a), null);
  return smallest ?? largest ?? null;
}

export function scrollAnchor(table, screen) {
  const chosen = scrollAnchorRow(table);
  return chosen ? centerOf(chosen.el) : { x: Math.round(screen.w / 2), y: Math.round(screen.h / 2) };
}

const contains = (outer, inner) =>
  inner.l >= outer.l - 2 && inner.t >= outer.t - 2 && inner.r <= outer.r + 2 && inner.b <= outer.b + 2;

// Providers set center alongside bounds; a host-supplied table may not, and a
// gesture needs a point either way.
const centerOf = (el) =>
  el.center ?? { x: Math.round((el.bounds.l + el.bounds.r) / 2), y: Math.round((el.bounds.t + el.bounds.b) / 2) };

// The scrollable a horizontal gesture must start on, or null when no dump
// signal says the axis is horizontal: a class that names it, or a short wide
// container whose clickable children sit side by side (a row of cards, a
// pager's pages). The rest of the time the axis is unknowable from the tree —
// an off-screen page is simply not in it — so vertical stays the only offered
// direction.
export function horizontalAnchor(table, screen) {
  const h = screen?.h ?? 2340;
  const named = table.find(
    (r) =>
      r.el.scrollable &&
      r.el.bounds &&
      /HorizontalScrollView|ViewPager|Pager|Carousel|Gallery|TabLayout/i.test(r.el.class || ''),
  );
  if (named?.el.bounds) return centerOf(named.el);
  for (const r of table) {
    const el = r.el;
    if (!el.scrollable || !el.bounds) continue;
    const { l, t, r: right, b } = el.bounds;
    const tall = b - t;
    if (tall < 1 || tall > h * 0.4 || right - l < tall * 2) continue;
    const band = (x) => x.t >= t - 2 && x.b <= b + 2;
    const inside = table.filter(
      (x) => x.el !== el && x.el.bounds && band(x.el.bounds) && contains(el.bounds, x.el.bounds),
    );
    if (new Set(inside.map((x) => x.el.bounds.l)).size >= 2) return centerOf(el);
  }
  return null;
}

// The rows a container holds: geometry is the only parent link both providers
// (and a host-supplied table) are guaranteed to have. Used to read a list's
// items off the screen in order.
//
// A row and its own label are both in the table (the container row carries the
// adopted label, the child TextView its own text), so containment drops the
// inner duplicate: one item, one entry. Order is visual (top, then left) —
// table order is interactive-first, which is not how the list reads.
export function listItems(table, idx) {
  const container = table.find((r) => String(r.idx) === String(idx));
  if (!container?.el.bounds) return [];
  const inside = table.filter(
    (r) =>
      r !== container && r.el.bounds && contains(container.el.bounds, r.el.bounds) && (r.el.text || r.el.contentDesc),
  );
  const outermost = inside.filter((r) => !inside.some((o) => o !== r && contains(o.el.bounds, r.el.bounds)));
  const seen = new Set();
  return outermost
    .sort((a, b) => a.el.bounds.t - b.el.bounds.t || a.el.bounds.l - b.el.bounds.l)
    .filter((r) => {
      const text = elementText(r.el);
      if (seen.has(text)) return false;
      seen.add(text);
      return true;
    });
}

// The text a row contributes to the clipboard or to an answer: the element's
// own text, single-spaced.
export const elementText = (el) =>
  String(el.text || el.contentDesc || '')
    .replace(/\s+/g, ' ')
    .trim();

// A grid of same-sized, unlabeled, clickable cells is drawn rather than
// described: a calendar's month, a drawing canvas, a dial pad. The model sees
// the cells and not what is on them, so tapping them is guessing — the note
// says so, which is what sends it looking for a text route instead.
export function gridNote(table) {
  const grid = table.grid;
  if (!grid) return null;
  return (
    `a ${grid.size} grid of ${grid.cells.length} unlabeled cells was left out of the list: what each cell` +
    ' holds is drawn, not text, so it cannot be told from its neighbours — reach that data by a text route instead'
  );
}

// Change detection between consecutive tables: content only (labels, states,
// indices), NOT positions. A scroll that re-reveals the same apps at different
// offsets is not progress. focused/selected flags are stripped too — they flap
// on scrolls without any content change and would make wandering look
// productive.
export function tableHash(table) {
  const parts = table.map(({ row }) => row.replace(/\+(focused|selected)/g, '')).sort();
  return createHash('sha256').update(parts.join('\n')).digest('hex').slice(0, 16);
}
