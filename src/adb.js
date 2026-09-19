// Thin adb wrapper. Everything here is argument-array spawns through util.run.
// The hot loop must never shell out to the `android` CLI (~670ms startup tax);
// adb is ~14ms per call.
import { run, DroidJevError } from './util.js';

const ADB = 'adb';

// Parse `adb devices -l` into [{serial, state, model}].
export async function adbDevices() {
  const r = await run(ADB, ['devices', '-l'], { timeoutMs: 10000 });
  if (r.code !== 0) throw new DroidJevError(`adb devices failed: ${r.stderr.trim().slice(0, 300)}`);
  const devices = [];
  for (const line of r.stdout.split('\n').slice(1)) {
    const m = line.match(/^(\S+)\s+(device|offline|unauthorized|booting|recovery|no permissions.*|.*device)\s+(.*)$/);
    if (!m) continue;
    const model = m[3].match(/model:([^ ]+)/)?.[1] || null;
    devices.push({ serial: m[1], state: m[2] === 'device' ? 'device' : m[2], model });
  }
  return devices;
}

export const isEmulatorSerial = (s) => /^emulator-\d+$/.test(s);

// AVD display name for a running emulator; null for physical devices / failures.
export async function avdName(serial) {
  const r = await run(ADB, ['-s', serial, 'emu', 'avd', 'name'], { timeoutMs: 6000 });
  if (r.code !== 0) return null;
  const name = r.stdout.split('\n')[0]?.trim();
  return name && name !== 'OK' ? name : null;
}

// `adb -s S shell ...` — args array, e.g. shell(serial, ['input', 'tap', '10', '20']).
export function shell(serial, args, opts) {
  return run(ADB, ['-s', serial, 'shell', ...args], { timeoutMs: 15000, ...opts });
}

export async function shellOk(serial, args, opts) {
  const r = await shell(serial, args, opts);
  if (r.code !== 0) {
    throw new DroidJevError(`adb shell ${args.join(' ')} exited ${r.code}: ${r.stderr.trim().slice(0, 300)}`);
  }
  return r;
}

export async function getprop(serial, prop) {
  const r = await shell(serial, ['getprop', prop], { timeoutMs: 8000 });
  return r.stdout.trim();
}

export async function bootCompleted(serial) {
  return (await getprop(serial, 'sys.boot_completed')) === '1';
}

// Screen size in px: {w, h}. Override (active display resolution) wins over
// Physical — dump coordinates are in override space when one is set.
export async function screenSize(serial) {
  const r = await shell(serial, ['wm', 'size'], { timeoutMs: 8000 });
  const ovr = r.stdout.match(/Override size:\s*(\d+)x(\d+)/);
  const phys = r.stdout.match(/Physical size:\s*(\d+)x(\d+)/);
  const m = ovr || phys;
  if (!m) return null;
  return { w: +m[1], h: +m[2] };
}

// Current focused window, e.g. "com.android.settings/com.android.settings.Settings".
// Format: `mCurrentFocus=Window{88d6767 u0 pkg/activity}` (no spaces around '=').
export async function currentApp(serial) {
  const r = await shell(serial, ['dumpsys', 'window'], { timeoutMs: 10000 });
  const line = r.stdout.match(/mCurrentFocus\s*=\s*(.+)/)?.[1] || '';
  const comp = line.match(/([a-zA-Z][\w.]*)\/([a-zA-Z][\w.$]*)/);
  if (!comp) return null;
  return { pkg: comp[1], activity: comp[2] };
}
