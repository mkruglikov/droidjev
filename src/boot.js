// Ensure a booted emulator exists: resolve --device (serial or AVD name),
// auto-boot the first AVD when nothing is online. The `android` CLI is fine
// here (cold path); the per-step hot loop stays adb-only.
import { run, sleep, DroidJevError, redact } from './util.js';
import { adbDevices, avdName, isEmulatorSerial, shell, bootCompleted } from './adb.js';

const ANDROID = 'android';
const BOOT_TIMEOUT_MS = 240_000;

export async function listAvds() {
  const r = await run(ANDROID, ['emulator', 'list'], { timeoutMs: 20000 });
  if (r.code !== 0) {
    throw new DroidJevError(`android emulator list failed: ${redact((r.stderr || r.stdout).trim()).slice(0, 300)}`);
  }
  return r.stdout
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith('Usage'));
}

async function waitBoot(serial, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await bootCompleted(serial)) return true;
    await sleep(1500);
  }
  return false;
}

// ponytail: wake + MENU key only dismisses a non-secure keyguard; a locked
// emulator with a PIN stays blocked. Upgrade path: check
// `dumpsys window` keyguard state and report blocked_locked explicitly.
export async function wakeAndUnlock(serial) {
  const pre = await shell(serial, ['dumpsys', 'power'], { timeoutMs: 8000 });
  if (/mWakefulness=Awake/.test(pre.stdout)) return; // already awake — no keys, no settle delay
  for (let attempt = 0; attempt < 2; attempt++) {
    await shell(serial, ['input', 'keyevent', '224']); // KEYCODE_WAKE_UP
    await sleep(300);
    const r = await shell(serial, ['dumpsys', 'power'], { timeoutMs: 8000 });
    if (/mWakefulness=Awake/.test(r.stdout)) break;
  }
  await shell(serial, ['input', 'keyevent', '82']); // MENU dismisses simple keyguard
  await sleep(250);
}

// Disable window/transition/animator scales for the duration of fn and
// restore them after — the standard UI-automation speedup (Espresso asks for
// the same): transitions complete in ~1 frame instead of ~300ms, so the dump
// after an action sees the settled screen sooner. `null` (never set) is
// restored by deleting the key; the framework default is 1.0.
// ponytail: no restore if the process is SIGKILLed — an emulator left with
// animations off is cosmetic; `settings put global *_scale 1` fixes it.
const ANIM_SCALES = ['window_animation_scale', 'transition_animation_scale', 'animator_duration_scale'];

export async function withAnimationsOff(serial, fn) {
  const orig = {};
  const get = (k) => shell(serial, ['settings', 'get', 'global', k], { timeoutMs: 5000 });
  const put = (k, v) => shell(serial, ['settings', 'put', 'global', k, v], { timeoutMs: 5000 });
  const del = (k) => shell(serial, ['settings', 'delete', 'global', k], { timeoutMs: 5000 });
  const restore = async () => {
    for (const k of ANIM_SCALES) {
      if (orig[k] === undefined) continue;
      try {
        if (orig[k] === 'null' || orig[k] === '') await del(k);
        else if (orig[k] !== '0') await put(k, orig[k]);
      } catch {
        /* best effort */
      }
    }
  };
  try {
    for (const k of ANIM_SCALES) orig[k] = (await get(k)).stdout.trim();
    for (const k of ANIM_SCALES) if (orig[k] !== '0') await put(k, '0');
  } catch {
    // best effort — running with animations on is only slower, not wrong
  }
  // Ctrl-C SIGINTs our adb children too, so fn() rejects and the finally
  // below restores — but only if Node survives the signal: the first SIGINT
  // is swallowed to let that cleanup run, a second exits at once (130).
  let sigints = 0;
  const onInt = () => {
    if (++sigints > 1) process.exit(130);
    process.exitCode = 130;
  };
  process.on('SIGINT', onInt);
  try {
    return await fn();
  } finally {
    process.removeListener('SIGINT', onInt);
    await restore();
  }
}

async function bootAvd(avd, notices) {
  // Already starting under this AVD? Attach to it instead of double-booting.
  for (const d of await adbDevices()) {
    if (d.state !== 'device' && d.state !== 'offline') continue;
    if (isEmulatorSerial(d.serial) && (await avdName(d.serial))?.toLowerCase() === avd.toLowerCase()) {
      notices?.push(`emulator for ${avd} already starting (${d.serial}); waiting for boot`);
      if (!(await waitBoot(d.serial))) {
        throw new DroidJevError(`emulator ${avd} (${d.serial}) did not finish booting`);
      }
      await wakeAndUnlock(d.serial);
      return { serial: d.serial, booted: false };
    }
  }

  notices?.push(`booting AVD ${avd} (android emulator start; this can take ~30-60s)`);
  const preSerials = new Set((await adbDevices()).map((d) => d.serial));
  const r = await run(ANDROID, ['emulator', 'start', avd], { timeoutMs: BOOT_TIMEOUT_MS });
  if (r.code !== 0) {
    throw new DroidJevError(
      `android emulator start ${avd} failed: ${redact((r.stderr || r.stdout).trim()).slice(0, 400)}`,
    );
  }
  // `android emulator start` returns when fully booted, but the new serial may
  // take a moment to appear in `adb devices`. Only bind to an emulator that
  // was NOT online before we started — otherwise we'd tap a different AVD.
  let serial = null;
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline && !serial) {
    const online = (await adbDevices()).filter(
      (d) => d.state === 'device' && isEmulatorSerial(d.serial) && !preSerials.has(d.serial),
    );
    for (const d of online) {
      const avd2 = await avdName(d.serial);
      if (!avd2 || avd2.toLowerCase() === avd.toLowerCase()) {
        serial = d.serial; // exact match, or the only new emulator in town
        break;
      }
    }
    if (!serial) await sleep(1500);
  }
  if (!serial) {
    throw new DroidJevError(
      `emulator ${avd} started but its serial never appeared in adb devices (new emulators seen: none)`,
    );
  }
  if (!(await waitBoot(serial))) {
    throw new DroidJevError(`emulator ${avd} (${serial}) did not reach sys.boot_completed=1`);
  }
  await wakeAndUnlock(serial);
  return { serial, booted: true };
}

// Resolve which emulator to use. Returns {serial, booted, notices}.
export async function ensureDevice({ device } = {}) {
  const notices = [];
  const devs = await adbDevices();
  const online = devs.filter((d) => d.state === 'device');

  if (device) {
    const bySerial = online.find((d) => d.serial === device);
    if (bySerial) {
      await wakeAndUnlock(bySerial.serial); // online but possibly dozing — dumps need the screen on
      return { serial: bySerial.serial, booted: false, notices };
    }
    for (const d of online) {
      if (isEmulatorSerial(d.serial) && (await avdName(d.serial))?.toLowerCase() === device.toLowerCase()) {
        await wakeAndUnlock(d.serial);
        return { serial: d.serial, booted: false, notices };
      }
    }
    const avds = await listAvds();
    const match = avds.find((a) => a.toLowerCase() === device.toLowerCase());
    if (!match) {
      throw new DroidJevError(
        `"${device}" is neither an online device serial nor an AVD name.\n` +
          `online: ${online.map((d) => d.serial).join(', ') || '(none)'}\n` +
          `avds:   ${avds.join(', ') || '(none)'}`,
      );
    }
    const res = await bootAvd(match, notices);
    return { ...res, notices };
  }

  // A device that is present but not ready (offline/booting) is waited for,
  // never raced with a second emulator boot.
  const pending = devs.filter((d) => d.state !== 'device');
  if (pending.length) {
    const serial = pending[0].serial;
    notices.push(`${serial} present as "${pending[0].state}" — waiting for it to come online`);
    const deadline = Date.now() + 120_000;
    while (Date.now() < deadline) {
      const now2 = await adbDevices();
      const me = now2.find((d) => d.serial === serial);
      if (!me) break; // vanished; fall through to the boot path
      if (me.state === 'unauthorized') {
        throw new DroidJevError(
          `${serial} is unauthorized — accept the USB debugging dialog on the device, or use --device to pick another`,
        );
      }
      if (me.state === 'device' && (await bootCompleted(serial))) {
        await wakeAndUnlock(serial);
        return { serial, booted: false, notices };
      }
      await sleep(2000);
    }
  }

  // Default target is emulators only; a physical phone requires explicit --device.
  const onlineEmulators = online.filter((d) => isEmulatorSerial(d.serial));
  if (onlineEmulators.length === 1) {
    await wakeAndUnlock(onlineEmulators[0].serial); // online but possibly dozing
    return { serial: onlineEmulators[0].serial, booted: false, notices };
  }
  if (onlineEmulators.length > 1) {
    const lines = await Promise.all(
      onlineEmulators.map(async (d) => `  --device ${d.serial}   (avd ${await avdName(d.serial)})`),
    );
    throw new DroidJevError(`multiple emulators online; pick one:\n${lines.join('\n')}`);
  }
  if (online.length > 0) {
    throw new DroidJevError(
      `only physical devices are online (${online.map((d) => d.serial).join(', ')}); droidjev drives emulators by default — pass --device <serial> to target one explicitly`,
    );
  }

  const avds = await listAvds();
  if (avds.length === 0) {
    throw new DroidJevError('no devices online and no AVDs found (`android emulator list`)');
  }
  notices.push(`no device online; using first AVD "${avds[0]}"`);
  const res = await bootAvd(avds[0], notices);
  return { ...res, notices };
}
