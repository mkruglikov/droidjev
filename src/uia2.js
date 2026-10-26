// appium-uiautomator2-server as a layout provider: a resident on-device
// instrumentation holding the accessibility connection and serving hierarchy
// dumps over adb-forwarded HTTP (~50ms per dump vs ~1.1s for the android CLI).
// It also sets the device clipboard (setClipboard): Android 10+ silently denies
// clipboard writes from unfocused processes, so the write rides a broadcast
// into the io.appium.settings companion APK, installed alongside the server.
//
// UiAutomation is exclusive device-wide. Entering this provider force-stops
// the android CLI's resident instrumentation (com.android.cli.interact.
// instrumentation) or the server cannot connect. The server is kept resident
// ACROSS droidjev invocations (~1.2s to boot is too costly per command);
// while it lives, the android CLI's own layout commands stay displaced —
// stop() (also used internally for restarts) hands UiAutomation back.
import { spawn } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { shell } from './adb.js';
import { DroidJevError, run, sleep } from './util.js';

const PKG = 'io.appium.uiautomator2.server';
const TEST_PKG = `${PKG}.test`;
const RUNNER = 'androidx.test.runner.AndroidJUnitRunner';
// The on-device server always listens here; only the LOCAL side of the adb
// forward is per-serial, so two devices in one process cannot collide.
const DEVICE_PORT = 6790;
const sessions = new Map();
const ensured = new Set(); // serials whose forward + server are known-good
const CACHE = join(homedir(), '.cache', 'droidjev', 'uia2');
const SETTINGS_PKG = 'io.appium.settings';
const CLI_INSTRUMENTATION = 'com.android.cli.interact.instrumentation';

// The first serial keeps the default port (every existing setup is unchanged);
// later ones get a stable hash-derived port in 6790-6853, skipping anything in
// use. Per-process only: two separate CLI processes can still pick the same
// local port, which is harmless while each drives one device.
export function pickPort(used, serial) {
  if (!used.length) return DEVICE_PORT;
  const hash = createHash('sha256').update(String(serial)).digest('hex').slice(0, 8);
  const start = DEVICE_PORT + (parseInt(hash, 16) % 64);
  for (let i = 0; i < 64; i++) {
    const port = DEVICE_PORT + ((start - DEVICE_PORT + i) % 64);
    if (!used.includes(port)) return port;
  }
  return DEVICE_PORT;
}

export function sessionFor(serial) {
  let s = sessions.get(serial);
  if (!s) {
    s = {
      port: pickPort(
        [...sessions.values()].map((v) => v.port),
        serial,
      ),
      sessionId: null,
    };
    sessions.set(serial, s);
  }
  return s;
}

const baseOf = (serial) => `http://127.0.0.1:${sessionFor(serial).port}`;

export async function httpJson(serial, path, init = {}, timeoutMs = 15000) {
  const res = await fetch(baseOf(serial) + path, { ...init, signal: AbortSignal.timeout(timeoutMs) });
  if (!res.ok) throw new DroidJevError(`uia2 HTTP ${res.status} on ${path}`);
  return res.json();
}

// Pinned helper APKs: exact release assets + SHA-256, verified before anything
// reaches a device. Bump deliberately (edit url and sha together). `latest`
// is not a version, and a cached or tampered file must never install silently.
export const PINNED = {
  tag: { server: 'v10.6.6', settings: 'v8.2.1' },
  assets: {
    server: {
      url: 'https://github.com/appium/appium-uiautomator2-server/releases/download/v10.6.6/appium-uiautomator2-server-v10.6.6.apk',
      sha256: '8ff760a2a86b487f53090fbdcd5b0360e67d02bb811887d527a9557b0d59c80d',
    },
    test: {
      url: 'https://github.com/appium/appium-uiautomator2-server/releases/download/v10.6.6/appium-uiautomator2-server-debug-androidTest.apk',
      sha256: 'e6f729287d72351388fd66597c35b69988b9091a7011954bf92a55d416cb1e1c',
    },
    settings: {
      url: 'https://github.com/appium/io.appium.settings/releases/download/v8.2.1/settings_apk-debug.apk',
      sha256: 'e4024b3ee348929252bcaf5eb422bcab9e4ace96703499a84f3332281c03375f',
    },
  },
};

export function sha256Matches(buf, hex) {
  return createHash('sha256').update(buf).digest('hex') === String(hex).toLowerCase();
}

const fetchDefault = (url) => fetch(url, { signal: AbortSignal.timeout(180000) });
const adbInstallDefault = (args) => run('adb', args, { timeoutMs: 180000 });

// Download (or reuse) one pinned asset, verify its hash, install it. The cache
// key carries the sha, so a new pin can never pick up a stale file; a corrupt
// cache entry is re-downloaded instead of failing the run.
export async function installApk(serial, asset, deps = {}) {
  const fetchImpl = deps.fetchImpl ?? fetchDefault;
  const cacheDir = deps.cacheDir ?? CACHE;
  const adbInstall = deps.adbInstall ?? adbInstallDefault;
  const base = asset.url
    .split('/')
    .pop()
    .replace(/\.apk$/i, '');
  const file = join(cacheDir, `${base}-${asset.sha256.slice(0, 8)}.apk`);
  let buf = null;
  try {
    buf = await readFile(file);
    if (!sha256Matches(buf, asset.sha256)) buf = null;
  } catch {
    /* not cached */
  }
  if (!buf) {
    const res = await fetchImpl(asset.url);
    if (!res.ok) throw new DroidJevError(`download of ${base}.apk failed: HTTP ${res.status}`);
    buf = Buffer.from(await res.arrayBuffer());
    const got = createHash('sha256').update(buf).digest('hex');
    if (!sha256Matches(buf, asset.sha256)) {
      throw new DroidJevError(
        `download of ${base}.apk failed sha256: expected ${asset.sha256.slice(0, 12)}, got ${got.slice(0, 12)}`,
      );
    }
    await mkdir(cacheDir, { recursive: true });
    await writeFile(file, buf);
  }
  const r = await adbInstall(['-s', serial, 'install', '-r', '-t', file]);
  if (!/Success/.test(r.stdout + r.stderr)) {
    throw new DroidJevError(`adb install of ${base}.apk failed: ${(r.stderr || r.stdout).trim().slice(0, 200)}`);
  }
  return file;
}

async function install(serial) {
  console.error(
    `# uia2: installing pinned helper APKs (uia2 ${PINNED.tag.server}, settings ${PINNED.tag.settings}; one-time, ~21MB)`,
  );
  for (const kind of ['server', 'test', 'settings']) await installApk(serial, PINNED.assets[kind]);
}

async function installed(serial) {
  for (const pkg of [PKG, SETTINGS_PKG]) {
    const r = await shell(serial, ['pm', 'path', pkg], { timeoutMs: 8000 });
    if (!(r.code === 0 && r.stdout.includes('package:'))) return false;
  }
  return true;
}

function startInstrument(serial) {
  // `am instrument -w` blocks for the server's lifetime; detached keeps the
  // server resident after this process exits (it is force-stopped in stop()).
  const p = spawn(
    'adb',
    ['-s', serial, 'shell', 'am', 'instrument', '-w', '-e', 'disableAnalytics', 'true', `${TEST_PKG}/${RUNNER}`],
    { stdio: 'ignore', detached: true },
  );
  // run() can't be used here (detached + unref'd), so cover its one gap
  // directly: an unhandled 'error' event (adb vanished since preflight)
  // would crash the process instead of letting waitReady() report it.
  p.on('error', () => {});
  p.unref();
}

async function waitReady(serial, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  const port = sessionFor(serial).port;
  for (;;) {
    try {
      await httpJson(serial, '/wd/hub/status', {}, 1500);
      return;
    } catch {
      if (Date.now() > deadline) {
        throw new DroidJevError(`uia2 server did not become ready on port ${port} within 20s`);
      }
      await sleep(250);
    }
  }
}

export async function ensure(serial) {
  if (ensured.has(serial)) return;
  const port = sessionFor(serial).port;
  // The local side is per-serial; the device side is always 6790.
  await run('adb', ['-s', serial, 'forward', `tcp:${port}`, `tcp:${DEVICE_PORT}`], { timeoutMs: 8000 });
  try {
    await httpJson(serial, '/wd/hub/status', {}, 1500);
    ensured.add(serial);
    return; // already resident from a previous step/run
  } catch {
    /* not up yet */
  }
  if (!(await installed(serial))) await install(serial);
  // The CLI's resident instrumentation owns UiAutomation; it must be gone
  // before the server starts, or the server binds a dead connection.
  try {
    await shell(serial, ['am', 'force-stop', CLI_INSTRUMENTATION], { timeoutMs: 8000 });
  } catch {}
  startInstrument(serial);
  await waitReady(serial);
  ensured.add(serial);
}

async function ensureSession(serial) {
  const s = sessionFor(serial);
  if (s.sessionId) return s.sessionId;
  const j = await httpJson(serial, '/wd/hub/session', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      capabilities: { alwaysMatch: { platformName: 'android', 'appium:automationName': 'UiAutomator2' } },
    }),
  });
  s.sessionId = j?.sessionId || j?.value?.sessionId || null;
  if (!s.sessionId)
    throw new DroidJevError(`uia2 session not created: ${(j?.value?.message || 'no session id').slice(0, 200)}`);
  return s.sessionId;
}

// Fetch the hierarchy XML. One full restart on failure: the server or session
// may have died mid-run (or another tool stole UiAutomation).
export async function source(serial) {
  for (let attempt = 0; ; attempt++) {
    await ensure(serial);
    try {
      const sid = await ensureSession(serial);
      const xml = (await httpJson(serial, `/wd/hub/session/${sid}/source`, {}, 20000))?.value;
      if (typeof xml !== 'string' || !xml.includes('<hierarchy')) {
        throw new DroidJevError('uia2 source returned no hierarchy xml');
      }
      return xml;
    } catch (e) {
      sessionFor(serial).sessionId = null;
      if (attempt >= 1) throw e;
      await stop(serial);
    }
  }
}

// Any UTF-8 text survives this path — base64 over HTTP, no shell, no charset
// limits. The server "succeeds" even when the companion APK is missing, so the
// caller cannot detect a lost write; installed() therefore requires both APKs.
export async function setClipboard(serial, text) {
  for (let attempt = 0; ; attempt++) {
    await ensure(serial);
    try {
      const sid = await ensureSession(serial);
      const r = await httpJson(
        serial,
        `/wd/hub/session/${sid}/appium/device/set_clipboard`,
        {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            content: Buffer.from(String(text), 'utf8').toString('base64'),
            contentType: 'plaintext',
          }),
        },
        20000,
      );
      if (r?.value?.error)
        throw new DroidJevError(`uia2 set_clipboard failed: ${(r.value.message || r.value.error).slice(0, 200)}`);
      return;
    } catch (e) {
      sessionFor(serial).sessionId = null;
      if (attempt >= 1) throw e;
      await stop(serial);
    }
  }
}

export async function stop(serial) {
  sessions.delete(serial);
  ensured.delete(serial);
  for (const pkg of [PKG, CLI_INSTRUMENTATION]) {
    try {
      await shell(serial, ['am', 'force-stop', pkg], { timeoutMs: 8000 });
    } catch {}
  }
}
