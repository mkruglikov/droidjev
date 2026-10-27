// One-screen capture shared by snapshot/find/act: screen size + focused app +
// layout dump + element table.
import { screenSize, currentApp } from './adb.js';
import { getLayout } from './layout.js';
import { buildTable } from './elements.js';

export async function takeSnapshot(serial) {
  const [screen, app, layout] = await Promise.all([screenSize(serial), currentApp(serial), getLayout(serial)]);
  // The focused app's package rides along: the table builder tells an IME's
  // keys (foreign package) from an in-app dial pad (same package).
  const table = buildTable(layout.elements, { ...screen, app: app?.pkg ?? null });
  return {
    serial,
    screen,
    app,
    tookMs: layout.tookMs,
    count: table.length,
    table,
  };
}
