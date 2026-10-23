// Executor: turns a validated decision into adb input. All taps come from
// coordinates computed in code from our own element table — the model never
// produces coordinates, selectors, or commands.
import { shellOk } from './adb.js';
import { DroidJevError } from './util.js';

// Emulators intermittently report "Awake" while ignoring input (doze/AOD
// edge states); WAKE_UP before each gesture is idempotent. It rides in the
// same adb invocation as the gesture — the `;` separates commands in the
// device shell, and every other token is code-generated (integers), so the
// model can never reach this string.
const AWAKE = ['input', 'keyevent', '224', ';'];

export async function tap(serial, center) {
  if (!center || !Number.isInteger(center.x) || !Number.isInteger(center.y)) {
    throw new DroidJevError('internal: tap called without integer center');
  }
  await shellOk(serial, [...AWAKE, 'input', 'tap', String(center.x), String(center.y)], { timeoutMs: 8000 });
}

// One gesture, clamped inside the screen: `input swipe` silently no-ops on
// negative coordinates.
async function swipe(serial, { x1, y1, x2, y2, durationMs }) {
  await shellOk(
    serial,
    [...AWAKE, 'input', 'swipe', String(x1), String(y1), String(x2), String(y2), String(durationMs)],
    { timeoutMs: 8000 },
  );
}

// Scroll gesture that stays inside the screen: symmetric around the anchor,
// both endpoints clamped.
export async function scrollSwipe(serial, anchor, dy, screenH, direction = 'down', durationMs = 300) {
  const margin = 40;
  const half = Math.round(Math.abs(dy) / 2);
  const y1 = direction === 'down' ? Math.min(anchor.y + half, screenH - margin) : Math.max(anchor.y - half, margin);
  const y2 = direction === 'down' ? Math.max(anchor.y - half, margin) : Math.min(anchor.y + half, screenH - margin);
  await swipe(serial, { x1: anchor.x, y1, x2: anchor.x, y2, durationMs });
}

// The same, on the other axis: the finger travels toward the content the goal
// is looking for, so `right` reveals what is off to the right.
export async function scrollSwipeX(serial, anchor, dx, screenW, direction = 'right', durationMs = 300) {
  const margin = 40;
  const half = Math.round(Math.abs(dx) / 2);
  const x1 = direction === 'right' ? Math.min(anchor.x + half, screenW - margin) : Math.max(anchor.x - half, margin);
  const x2 = direction === 'right' ? Math.max(anchor.x - half, margin) : Math.min(anchor.x + half, screenW - margin);
  await swipe(serial, { x1, y1: anchor.y, x2, y2: anchor.y, durationMs });
}

// A slider's value follows the touch, so a tap lands where the finger did —
// and near the ends it lands short of them (a tap 3px from the right edge of
// the brightness slider left 251/255). Press the track to grab the thumb
// (Android's SeekBar jumps it to the press point), drag on to the target, and
// for the extremes overshoot off the track so the value clamps to min/max.
// The numbers are returned on their own so a host that executes the decision
// itself sends the very same gesture.
export function slideGesture(el, fraction) {
  const { l, t, r, b } = el.bounds;
  const cy = Math.round((t + b) / 2);
  const margin = 40;
  const x2 = fraction <= 0 ? l - margin : fraction >= 1 ? r + margin : Math.round(l + fraction * (r - l));
  return { x1: Math.round((l + r) / 2), y1: cy, x2, y2: cy, durationMs: 400 };
}

export async function slideTo(serial, el, fraction) {
  await swipe(serial, slideGesture(el, fraction));
}

// Press and hold: identical endpoints with a long duration is the only
// press-and-hold `input` can send, and a context menu (rename, delete, move,
// share) is where most lists keep the operations that have no visible button.
export async function longPress(serial, center, holdMs = 800) {
  if (!center || !Number.isInteger(center.x) || !Number.isInteger(center.y)) {
    throw new DroidJevError('internal: longPress called without integer center');
  }
  await swipe(serial, { x1: center.x, y1: center.y, x2: center.x, y2: center.y, durationMs: holdMs });
}

export async function keyevent(serial, code) {
  await shellOk(serial, ['input', 'keyevent', String(code)], { timeoutMs: 8000 });
}

// `input text` reaches the device shell, so the string must be reduced to a
// charset that is inert there (no ; & | < > ( ) $ ` " ' \ * ? [ ] { } ~ !);
// space maps to %s (the only escape input text supports). Anything outside
// the safe set is rejected with the offending characters named, never mangled.
// ponytail: no unicode/emoji typing; upgrade path is a base64 insertion bridge.
const SAFE_TEXT_RE = /^[A-Za-z0-9 .,:+=/@%^_-]*$/;

export function sanitizeInputText(text) {
  const t = String(text ?? '');
  if (!t.trim()) throw new DroidJevError('--text is empty');
  if (t.length > 500) throw new DroidJevError('--text longer than 500 chars');
  const bad = [...new Set([...t].filter((ch) => !SAFE_TEXT_RE.test(ch)))].join(' ');
  if (bad) throw new DroidJevError(`--text contains characters that cannot be typed safely: ${JSON.stringify(bad)}`);
  // %s from the user stays %s on device (becomes a space) — documented ceiling.
  return t.replace(/ /g, '%s');
}

export async function typeText(serial, text) {
  const safe = sanitizeInputText(text);
  // "Set" semantics, not "append": select-all (CTRL+A) + delete first, so a
  // repeated type — the model retrying into the same field — is idempotent
  // instead of doubling the content ("bluetoothbluetooth"). Older devices
  // without `input keycombination` fall back to appending, as before.
  try {
    await shellOk(serial, ['input', 'keycombination', '113', '29'], { timeoutMs: 5000 });
    await shellOk(serial, ['input', 'keyevent', '67'], { timeoutMs: 5000 });
  } catch {
    // no keycombination support; append remains usable
  }
  await shellOk(serial, ['input', 'text', safe], { timeoutMs: 10000 });
}

// paste (279) rides the focused field; callers must focus one first (tap).
export const KEYS = { back: 4, home: 3, enter: 66, wake: 224, paste: 279 };
