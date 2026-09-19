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
import { access, mkdir, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { shell } from './adb.js';
import { DroidJevError, run, sleep } from './util.js';

const PKG = 'io.appium.uiautomator2.server';
const TEST_PKG = `${PKG}.test`;
const RUNNER = 'androidx.test.runner.AndroidJUnitRunner';
const PORT = 6790;
const BASE = `http://127.0.0.1:${PORT}`;
const CACHE = join(homedir(), '.cache', 'droidjev', 'uia2');
const RELEASES = 'https://api.github.com/repos/appium/appium-uiautomator2-server/releases/latest';
const SETTINGS_RELEASES = 'https://api.github.com/repos/appium/io.appium.settings/releases/latest';
const SETTINGS_PKG = 'io.appium.settings';
const CLI_INSTRUMENTATION = 'com.android.cli.interact.instrumentation';

let sessionId;
const ensured = new Set(); // serials whose forward + server are known-good

async function httpJson(path, init = {}, timeoutMs = 15000) {
  const res = await fetch(BASE + path, { ...init, signal: AbortSignal.timeout(timeoutMs) });
  if (!res.ok) throw new DroidJevError(`uia2 HTTP ${res.status} on ${path}`);
  return res.json();
}

async function installed(serial) {
  for (const pkg of [PKG, SETTINGS_PKG]) {
    const r = await shell(serial, ['pm', 'path', pkg], { timeoutMs: 8000 });
    if (!(r.code === 0 && r.stdout.includes('package:'))) return false;
  }
  return true;
}

async function installApk(serial, url) {
  const file = join(CACHE, url.split('/').pop());
  try {
    await access(file);
  } catch {
    const res = await fetch(url, { signal: AbortSignal.timeout(180000) });
    // The cache key is the filename: an error page saved as <name>.apk would
    // poison every later run's adb install.
    if (!res.ok) throw new DroidJevError(`download of ${url.split('/').pop()} failed: HTTP ${res.status}`);
    const buf = Buffer.from(await res.arrayBuffer());
    await writeFile(file, buf);
  }
  const r = await run('adb', ['-s', serial, 'install', '-r', '-t', file], { timeoutMs: 180000 });
  if (!/Success/.test(r.stdout + r.stderr)) {
    throw new DroidJevError(
      `adb install of ${url.split('/').pop()} failed: ${(r.stderr || r.stdout).trim().slice(0, 200)}`,
    );
  }
}

async function install(serial) {
  console.error('# uia2: fetching appium-uiautomator2-server release (one-time, ~18MB)');
  const rel = await fetch(RELEASES, { headers: { 'user-agent': 'droidjev' }, signal: AbortSignal.timeout(30000) });
  if (!rel.ok) throw new DroidJevError(`github releases HTTP ${rel.status} for uia2 server`);
  const urls = {};
  for (const a of (await rel.json()).assets || []) {
    if (a.name.endsWith('.apk')) urls[a.name.includes('androidTest') ? 'test' : 'server'] = a.browser_download_url;
  }
  if (!urls.server || !urls.test) throw new DroidJevError('latest uia2 release has no server+test apk assets');
  await mkdir(CACHE, { recursive: true });
  for (const kind of ['server', 'test']) await installApk(serial, urls[kind]);
  console.error('# uia2: fetching io.appium.settings release (one-time, ~3MB, clipboard companion)');
  const srel = await fetch(SETTINGS_RELEASES, {
    headers: { 'user-agent': 'droidjev' },
    signal: AbortSignal.timeout(30000),
  });
  if (!srel.ok) throw new DroidJevError(`github releases HTTP ${srel.status} for io.appium.settings`);
  const asset = ((await srel.json()).assets || []).find((a) => a.name.endsWith('.apk'));
  if (!asset) throw new DroidJevError('latest io.appium.settings release has no apk asset');
  await installApk(serial, asset.browser_download_url);
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

async function waitReady(timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      await httpJson('/wd/hub/status', {}, 1500);
      return;
    } catch {
      if (Date.now() > deadline) throw new DroidJevError('uia2 server did not become ready on port 6790 within 20s');
      await sleep(250);
    }
  }
}

export async function ensure(serial) {
  if (ensured.has(serial)) return;
  await run('adb', ['-s', serial, 'forward', `tcp:${PORT}`, `tcp:${PORT}`], { timeoutMs: 8000 });
  try {
    await httpJson('/wd/hub/status', {}, 1500);
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
  await waitReady();
  ensured.add(serial);
}

async function ensureSession() {
  if (sessionId) return sessionId;
  const j = await httpJson('/wd/hub/session', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      capabilities: { alwaysMatch: { platformName: 'android', 'appium:automationName': 'UiAutomator2' } },
    }),
  });
  sessionId = j?.sessionId || j?.value?.sessionId || null;
  if (!sessionId)
    throw new DroidJevError(`uia2 session not created: ${(j?.value?.message || 'no session id').slice(0, 200)}`);
  return sessionId;
}

// Fetch the hierarchy XML. One full restart on failure: the server or session
// may have died mid-run (or another tool stole UiAutomation).
export async function source(serial) {
  for (let attempt = 0; ; attempt++) {
    await ensure(serial);
    try {
      const sid = await ensureSession();
      const xml = (await httpJson(`/wd/hub/session/${sid}/source`, {}, 20000))?.value;
      if (typeof xml !== 'string' || !xml.includes('<hierarchy')) {
        throw new DroidJevError('uia2 source returned no hierarchy xml');
      }
      return xml;
    } catch (e) {
      sessionId = null;
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
      const sid = await ensureSession();
      const r = await httpJson(
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
      sessionId = null;
      if (attempt >= 1) throw e;
      await stop(serial);
    }
  }
}

export async function stop(serial) {
  sessionId = null;
  ensured.delete(serial);
  for (const pkg of [PKG, CLI_INSTRUMENTATION]) {
    try {
      await shell(serial, ['am', 'force-stop', pkg], { timeoutMs: 8000 });
    } catch {}
  }
}
