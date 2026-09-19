#!/usr/bin/env node
// droidjev — fast jev-powered Android emulator clicker.
import { adbDevices, avdName, isEmulatorSerial } from './adb.js';
import { ensureDevice, wakeAndUnlock, withAnimationsOff } from './boot.js';
import { serializeRows } from './elements.js';
import { runGoal } from './agent.js';
import { takeSnapshot } from './snapshot.js';
import { findLabel } from './find.js';
import { startApp } from './start.js';
import { ensure as ensureUia2 } from './uia2.js';
import { requireApiKey } from './typesafe.js';
import { run, DroidJevError, redact } from './util.js';
import { readFileSync, realpathSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, join } from 'node:path';

const VERSION = JSON.parse(
  readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', 'package.json'), 'utf8'),
).version;

const USAGE = `droidjev ${VERSION} — fast jev-powered Android emulator clicker (no screenshots)

usage: droidjev <command> [args] [flags]

commands:
  devices                    list adb devices with AVD names (fast: raw adb)
  boot [avd]                 ensure an emulator is booted; auto-boots when none is online
  start <package>            launch an app by package (deterministic, no model)
  snapshot [flags]           dump the current screen as an indexed element table
  find "<label>" [flags]     deterministically search scrollable content for a
                             label (snap → match → scroll), optionally tapping it
  act "<goal>" [flags]       run the agent loop until the goal is achieved

flags:
  --device SERIAL|AVD        target device (serial or AVD name; default: sole online
                             emulator, else auto-boot the first AVD)
  --text "..."               value to type when the goal needs typing (never
                             guessed); repeat for multi-field forms — one --text
                             per value, the model matches each to its field
  --max-steps N              action budget for act (default 12)
  --max-iterations N         (find) scroll budget (default 20)
  --tap                      (find) tap the best match instead of just reporting it
  --grep "..."               (snapshot) print only rows containing the substring
  --json                     machine-readable output
  --no-animations            disable window/transition animations for the run
                             (restored after) — screens settle in ~1 frame, so
                             steps verify sooner

env: TYPESAFE_API_KEY (required for act)

exit codes: 0 done, 2 blocked, 3 error`;

const VALUE_FLAGS = new Set(['--device', '--text', '--max-steps', '--grep', '--max-iterations']);

export function parseArgs(argv) {
  const flags = { _: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (VALUE_FLAGS.has(a)) {
      const v = argv[++i];
      if (v === undefined || v.startsWith('--')) {
        throw new DroidJevError(`flag ${a} needs a value (got ${v === undefined ? 'nothing' : `"${v}"`})`);
      }
      if (a === '--device') flags.device = v;
      else if (a === '--text') (flags.texts ??= []).push(v);
      else if (a === '--grep') flags.grep = v;
      else if (a === '--max-iterations') {
        const n = Number(v);
        if (!Number.isInteger(n) || n < 1 || n > 50) {
          throw new DroidJevError(`--max-iterations must be an integer 1..50 (got "${v}")`);
        }
        flags.maxIterations = n;
      } else {
        const n = Number(v);
        if (!Number.isInteger(n) || n < 1 || n > 100) {
          throw new DroidJevError(`--max-steps must be an integer 1..100 (got "${v}")`);
        }
        flags.maxSteps = n;
      }
    } else if (a === '--json') flags.json = true;
    else if (a === '--tap') flags.tap = true;
    else if (a === '--no-animations') flags.noAnimations = true;
    else if (a === '--help' || a === '-h') flags.help = true;
    else if (a.startsWith('--')) throw new DroidJevError(`unknown flag: ${a}`);
    else flags._.push(a);
  }
  return flags;
}

// The uia2 server is kept resident across droidjev invocations: booting it
// costs ~1.2s, so every command ensures it as its first device-side step and
// none of them stop it. UiAutomation stays held device-wide meanwhile —
// `adb shell am force-stop io.appium.uiautomator2.server` hands it back to
// other tooling.
async function withUia2(serial, fn) {
  await ensureUia2(serial);
  return fn();
}

async function cmdDevices(flags) {
  const devs = await adbDevices();
  const rows = await Promise.all(
    devs.map(async (d) => ({
      serial: d.serial,
      state: d.state,
      model: d.model,
      avd: isEmulatorSerial(d.serial) ? await avdName(d.serial) : null,
    })),
  );
  if (flags.json) {
    console.log(JSON.stringify({ devices: rows }, null, 2));
    return 0;
  }
  if (rows.length === 0) console.log('no devices online (see: droidjev boot)');
  for (const r of rows) {
    console.log(`${r.serial}  ${r.state}  ${r.avd ? `avd=${r.avd}  ` : ''}${r.model ? `model=${r.model}` : ''}`);
  }
  return 0;
}

async function cmdBoot(flags) {
  const avd = flags._[1]; // optional positional
  const res = await ensureDevice({ device: avd ?? flags.device });
  for (const n of res.notices) console.error(`# ${n}`);
  if (flags.json) {
    const avd2 = isEmulatorSerial(res.serial) ? await avdName(res.serial) : null;
    console.log(JSON.stringify({ serial: res.serial, avd: avd2, booted: res.booted }));
  } else {
    console.log(`${res.serial} ready${res.booted ? ' (freshly booted)' : ''}`);
  }
  return 0;
}

async function cmdSnapshot(flags) {
  const { serial } = await ensureDevice({ device: flags.device });
  const s = await withUia2(serial, () => takeSnapshot(serial));
  const table = flags.grep ? s.table.filter((r) => r.row.toLowerCase().includes(flags.grep.toLowerCase())) : s.table;
  if (flags.json) {
    console.log(
      JSON.stringify(
        {
          serial: s.serial,
          app: s.app?.pkg ?? null,
          screen: s.screen,
          tookMs: s.tookMs,
          rows: serializeRows(table),
          centers: Object.fromEntries(table.map(({ idx, el }) => [idx, el.center])),
        },
        null,
        2,
      ),
    );
  } else {
    const flt = flags.grep ? ` matching="${flags.grep}"` : '';
    console.error(`# ${s.serial} app=${s.app?.pkg ?? '?'} dump=${Math.round(s.tookMs)}ms rows=${table.length}${flt}`);
    for (const r of serializeRows(table)) console.log(r);
  }
  return 0;
}

async function cmdFind(flags) {
  const label = flags._[1];
  if (!label) throw new DroidJevError('find needs a label, e.g. droidjev find "About phone"');
  const { serial, notices } = await ensureDevice({ device: flags.device });
  for (const n of notices) console.error(`# ${n}`);
  await wakeAndUnlock(serial);
  const res = await withUia2(serial, () =>
    findLabel({
      serial,
      label,
      tapIt: !!flags.tap,
      maxIterations: flags.maxIterations || 20,
      log: (m) => console.error(`# ${m}`),
    }),
  );
  if (flags.json) {
    console.log(JSON.stringify(res, null, 2));
  } else if (res.found) {
    console.log(
      `${res.tapped ? 'tapped' : 'found'} ${res.match} center=${res.center.x},${res.center.y} iterations=${res.iterations}`,
    );
    for (const m of res.matches.slice(1)) console.log(`  also matched: ${m}`);
  } else {
    console.log(`not found (${res.reason}) after ${res.iterations} snapshots`);
  }
  return res.found ? 0 : 2;
}

async function cmdStart(flags) {
  const pkg = flags._[1];
  if (!pkg) throw new DroidJevError('start needs a package, e.g. droidjev start com.android.settings');
  const { serial, notices } = await ensureDevice({ device: flags.device });
  for (const n of notices) console.error(`# ${n}`);
  await wakeAndUnlock(serial);
  const res = await startApp(serial, pkg);
  if (flags.json) {
    console.log(JSON.stringify(res, null, 2));
  } else {
    console.log(`started ${res.pkg} via ${res.via} (focused=${res.focused}${res.app ? `, app=${res.app.pkg}` : ''})`);
  }
  return res.focused ? 0 : 2;
}

async function cmdAct(flags) {
  const goal = flags._[1];
  if (!goal) throw new DroidJevError('act needs a goal, e.g. droidjev act "open Settings"');
  const { serial, notices } = await ensureDevice({ device: flags.device });
  for (const n of notices) console.error(`# ${n}`);
  await wakeAndUnlock(serial); // display may have dozed since boot
  // Animations run by default; --no-animations zeroes them for the run and
  // restores them after, so settled screens arrive ~1 frame after an action.
  const runIt = () =>
    runGoal({
      serial,
      goal,
      texts: flags.texts ?? null,
      maxSteps: flags.maxSteps || 12,
      animationsOff: !!flags.noAnimations,
      log: (m) => console.error(`# ${m}`),
    });
  const res = await withUia2(serial, () => (flags.noAnimations ? withAnimationsOff(serial, runIt) : runIt()));
  if (flags.json) {
    console.log(JSON.stringify(res, null, 2));
  } else {
    for (const s of res.steps)
      console.log(`${s.action}${s.changed === true ? ' ✓' : s.changed === false ? ' ✗(no change)' : ''}`);
    console.log(
      `status=${res.status} steps=${res.steps.length} ${(res.tookMs / 1000).toFixed(1)}s tokens=${res.tokens.input}/${res.tokens.output} app=${res.finalApp ?? '?'}`,
    );
  }
  return res.status === 'done' ? 0 : 2;
}

// Fail fast on missing prerequisites instead of failing mid-run: every device
// command needs adb; act additionally needs the Typesafe key BEFORE we spend
// a minute booting an emulator.
async function preflight(cmd) {
  const r = await run('adb', ['version'], { timeoutMs: 8000 });
  if (r.code !== 0 && !r.stdout.includes('Android Debug Bridge')) {
    throw new DroidJevError('"adb" is not usable — install Android platform-tools and ensure adb is in PATH');
  }
  if (cmd === 'act') {
    await requireApiKey(); // throws a clear message when TYPESAFE_API_KEY is missing
  }
}

async function main() {
  const flags = parseArgs(process.argv.slice(2));
  const cmd = flags._[0];
  if (flags.help || !cmd) {
    console.log(USAGE);
    return flags.help ? 0 : 3;
  }
  switch (cmd) {
    case 'version':
      console.log(VERSION);
      return 0;
    case 'help':
      console.log(USAGE);
      return 0;
    default:
      await preflight(cmd);
  }
  switch (cmd) {
    case 'devices':
      return await cmdDevices(flags);
    case 'boot':
      return await cmdBoot(flags);
    case 'snapshot':
      return await cmdSnapshot(flags);
    case 'find':
      return await cmdFind(flags);
    case 'start':
      return await cmdStart(flags);
    case 'act':
      return await cmdAct(flags);
    default:
      throw new DroidJevError(`unknown command: ${cmd}\n\n${USAGE}`);
  }
}

// Sets process.exitCode instead of calling process.exit(): writes to a piped
// stdout/stderr are asynchronous, and process.exit() would drop any that have
// not flushed yet.
export function bootstrap() {
  main().then(
    (code) => {
      process.exitCode = code ?? 0;
    },
    (err) => {
      // Every path is redacted: DroidJevError messages carry subprocess stderr that
      // could quote request material; stacks could quote fetch header errors.
      const msg = redact(err instanceof DroidJevError ? err.message : err?.stack || String(err));
      console.error(`droidjev: ${msg}`);
      process.exitCode = err instanceof DroidJevError ? err.exitCode : 3;
    },
  );
}

// package.json bin points here; run directly, this file is the CLI. Node
// resolves module URLs to realpaths but leaves process.argv[1] as given
// (npm's bin symlink, macOS /var→/private/var), so realpath argv[1] before
// comparing — the bare import.meta.url === argv[1] idiom silently never
// fires through an npm install. Plain imports (tests use parseArgs) never
// trigger main().
if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) bootstrap();
