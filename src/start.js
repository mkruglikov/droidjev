// Deterministic app launch by package: resolve the launcher activity, start
// it; fall back to monkey. Launching an app is not a judgment problem either.
import { shell, currentApp } from './adb.js';
import { DroidJevError, sleep } from './util.js';

// `cmd package resolve-activity --brief pkg` may print an apk path line
// before the component; the component is the last line containing '/'.
export function parseResolveActivity(out) {
  const lines = String(out || '')
    .trim()
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.includes('/') && /^\S+\/\S+$/.test(l));
  return lines.length ? lines[lines.length - 1] : null;
}

export async function startApp(serial, pkg) {
  if (!/^[a-zA-Z][\w.]*(\.[\w]+)+$/.test(pkg ?? '')) {
    throw new DroidJevError(`"${pkg}" does not look like an Android package name (e.g. com.google.android.deskclock)`);
  }
  const r = await shell(serial, ['cmd', 'package', 'resolve-activity', '--brief', pkg], { timeoutMs: 10000 });
  const comp = r.code === 0 ? parseResolveActivity(r.stdout) : null;
  let via;
  if (comp && comp.startsWith(pkg + '/')) {
    const am = await shell(serial, ['am', 'start', '-n', comp], { timeoutMs: 15000 });
    if (am.code !== 0) throw new DroidJevError(`am start ${comp} exited ${am.code}: ${am.stderr.trim().slice(0, 200)}`);
    via = `am start -n ${comp}`;
  } else {
    const mk = await shell(serial, ['monkey', '-p', pkg, '-c', 'android.intent.category.LAUNCHER', '1'], {
      timeoutMs: 15000,
    });
    if (mk.code !== 0) {
      throw new DroidJevError(
        `could not launch ${pkg}: resolve-activity found no launcher component and monkey exited ${mk.code} (${mk.stderr.trim().slice(0, 200)})`,
      );
    }
    via = 'monkey';
  }
  // Wait briefly for the window to take focus so callers can trust the result.
  for (let i = 0; i < 6; i++) {
    await sleep(i === 0 ? 500 : 400);
    const app = await currentApp(serial);
    if (app?.pkg === pkg) return { pkg, component: comp ?? null, via, app, focused: true };
  }
  return { pkg, component: comp ?? null, via, app: await currentApp(serial), focused: false };
}
