// Deterministic label search in scrollable content: snapshot → match →
// (tap | scroll) → repeat. No model calls — a long alphabetized list is a
// code problem, not a judgment problem.
import { takeSnapshot } from './snapshot.js';
import { tableHash, scrollAnchor } from './elements.js';
import { settleLayout } from './layout.js';
import { tap, scrollSwipe } from './act.js';
import { wakeAndUnlock } from './boot.js';
import { DroidJevError } from './util.js';

const MAX_ITERATIONS = 20;

// Rank matches: exact quoted label beats substring, clickable beats not,
// topmost (smallest index) wins ties.
export function matchRows(table, query) {
  const q = String(query ?? '')
    .trim()
    .toLowerCase();
  if (!q) throw new DroidJevError('find needs a non-empty label');
  const exact = [];
  const partial = [];
  for (const r of table) {
    const label = (r.row.match(/"([^"]*)"/)?.[1] ?? '').toLowerCase();
    if (label === q) exact.push(r);
    else if (r.row.toLowerCase().includes(q)) partial.push(r);
  }
  const pool = exact.length ? exact : partial;
  return [...pool].sort((a, b) => Number(b.el.clickable) - Number(a.el.clickable) || a.idx - b.idx);
}

export async function findLabel({ serial, label, tapIt = false, maxIterations = MAX_ITERATIONS, log = () => {} }) {
  let prevHash = null;
  let stalled = false; // one hash-repeat triggers a wake-and-retry (doze eats gestures silently)
  for (let i = 1; i <= maxIterations; i++) {
    const s = await takeSnapshot(serial);
    const matches = matchRows(s.table, label);
    if (matches.length) {
      const best = matches[0];
      // A coordinate tap on the label hits whatever clickable element
      // contains it, so tapping a text row works for standard list rows.
      if (tapIt) {
        await tap(serial, best.el.center);
        log(`iteration ${i}: tapped ${best.row}`);
      } else {
        log(`iteration ${i}: found ${best.row}`);
      }
      return {
        found: true,
        iterations: i,
        match: best.row,
        center: best.el.center,
        matches: matches.map((m) => m.row),
        tapped: tapIt,
      };
    }
    // No scrollable container on screen: scrolling cannot reveal anything,
    // so stop instead of swiping into overscroll until max_iterations.
    if (!s.table.some((r) => r.el.scrollable)) {
      return { found: false, iterations: i, match: null, matches: [], tapped: false, reason: 'end_of_list' };
    }
    const h = tableHash(s.table);
    if (h === prevHash) {
      if (stalled) {
        return { found: false, iterations: i, match: null, matches: [], tapped: false, reason: 'end_of_list' };
      }
      stalled = true;
      log(`iteration ${i}: screen unchanged — waking display and retrying once`);
      await wakeAndUnlock(serial);
    } else {
      stalled = false;
    }
    prevHash = h;
    const screen = s.screen || { w: 1080, h: 2340 };
    const anchor = scrollAnchor(s.table, screen);
    await scrollSwipe(serial, anchor, Math.round(screen.h * 0.6), screen.h, 'down');
    log(`iteration ${i}: "${label}" not visible — scrolled`);
    // Outlast the fling's overscroll bounce: while it plays, rows flicker
    // across the clip edge and every dump hashes differently, faking progress
    // all the way to max_iterations. Polling ends as soon as the hierarchy
    // stops moving — a settled swipe exits in ~2 dumps, a momentum fling at
    // the cap (no worse than the fixed sleep it replaced).
    await settleLayout(serial, { maxMs: 600 });
  }
  return { found: false, iterations: maxIterations, match: null, matches: [], tapped: false, reason: 'max_iterations' };
}
