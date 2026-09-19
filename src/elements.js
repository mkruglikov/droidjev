// Element table: filter the normalized layout to the elements the model should
// see, give them stable indices, and serialize compact rows. The model only
// ever sees rows like `[12] click "About phone"` — never coordinates.
import { createHash } from 'node:crypto';

const MAX_ROWS = 80;

const classShort = (cls) => (cls || '').split('.').pop() || '';
const resourceIdTail = (id) => (id || '').split('/').pop() || id || '';

export const isEditable = (el) => /EditText|Editable|AutoCompleteTextView|TextInput/.test(el.class || '');

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
  const visible = elements.filter((el, i) => !hidden.has(i) && onScreen(el, screen));
  const labeled = visible.filter((el) => cleanLabel(el.text) || cleanLabel(el.contentDesc));

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
  const entries = visible.map(entry).filter(Boolean);
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
  Object.defineProperty(rows, 'truncated', { value: truncated, enumerable: false });
  return rows;
}

// Model-facing view: rows only, no coordinates, no resource internals.
export function serializeRows(table) {
  return table.map((r) => r.row);
}

// Scroll anchor: largest scrollable element's center, else screen middle.
export function scrollAnchor(table, screen) {
  let best = null;
  for (const { el } of table) {
    if (!el.scrollable || !el.bounds) continue;
    const area = (el.bounds.r - el.bounds.l) * (el.bounds.b - el.bounds.t);
    if (!best || area > best.area) best = { area, center: el.center };
  }
  if (best?.center) return best.center;
  return { x: Math.round(screen.w / 2), y: Math.round(screen.h / 2) };
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
