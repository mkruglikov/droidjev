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

// Scroll gesture that stays inside the screen: symmetric around the anchor,
// both endpoints clamped. input swipe silently no-ops on negative coords.
export async function scrollSwipe(serial, anchor, dy, screenH, direction = 'down', durationMs = 300) {
  const margin = 40;
  const half = Math.round(Math.abs(dy) / 2);
  const y1 = direction === 'down' ? Math.min(anchor.y + half, screenH - margin) : Math.max(anchor.y - half, margin);
  const y2 = direction === 'down' ? Math.max(anchor.y - half, margin) : Math.min(anchor.y + half, screenH - margin);
  await shellOk(
    serial,
    [...AWAKE, 'input', 'swipe', String(anchor.x), String(y1), String(anchor.x), String(y2), String(durationMs)],
    { timeoutMs: 8000 },
  );
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
