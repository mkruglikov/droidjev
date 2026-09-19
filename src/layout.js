// Layout: one provider — the resident appium-uiautomator2-server (uia2.js),
// a persistent on-device instrumentation holding the accessibility
// connection. A dump is a single adb-forwarded HTTP call (~20-50ms); the
// server is installed on first use and kept resident across commands.
import * as uia2 from './uia2.js';
import { createHash } from 'node:crypto';
import { DroidJevError, sleep } from './util.js';

function decodeEntities(s) {
  return s
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, d) => {
      const cp = Number(d);
      return cp <= 0x10ffff ? (cp >= 0xd800 && cp <= 0xdfff ? '' : String.fromCodePoint(cp)) : '';
    })
    .replace(/&amp;/g, '&');
}

function parseBounds(s) {
  const m = /\[(\d+),(\d+)\]\[(\d+),(\d+)\]/.exec(s || '');
  if (!m) return null;
  const l = +m[1],
    t = +m[2],
    r = +m[3],
    b = +m[4];
  if (!(r >= l && b >= t)) return null;
  return { l, t, r, b, cx: Math.round((l + r) / 2), cy: Math.round((t + b) / 2) };
}

const BOOL_ATTRS = [
  'clickable',
  'long-clickable',
  'longClickable',
  'scrollable',
  'checkable',
  'checked',
  'focusable',
  'focused',
  'selected',
  'password',
];

// Minimal tokenizer for the server's machine-generated XML: attribute-only
// tags, no text nodes. uia2 v10+ emits class-named nested tags
// (<android.widget.TextView .../>) instead of uiautomator's <node>, with the
// same attribute vocabulary (optional attrs omitted when empty) plus
// displayed (→ offScreen). Tag scanning respects quotes so an escaped `>`
// inside an attribute cannot truncate the tag; nesting is tracked through
// close tags so labels can be aggregated.
export function parseUia2Xml(xml) {
  const start = xml.indexOf('<hierarchy');
  if (start === -1) throw new DroidJevError('uia2 source contained no <hierarchy> (screen may be off)');
  const body = xml.slice(start);
  const out = [];
  const stack = []; // element indices; -1 = the <hierarchy> root itself
  let i = 0;
  while (i < body.length) {
    const lt = body.indexOf('<', i);
    if (lt === -1) break;
    if (body[lt + 1] === '/') {
      // any closing tag pops one nesting level
      stack.pop();
      const gt = body.indexOf('>', lt);
      if (gt === -1) break;
      i = gt + 1;
      continue;
    }
    // scan to the tag's real end, ignoring '>' inside quoted attribute values
    let j = lt + 1;
    let quote = null;
    for (; j < body.length; j++) {
      const ch = body[j];
      if (quote) {
        if (ch === quote) quote = null;
      } else if (ch === '"' || ch === "'") quote = ch;
      else if (ch === '>') break;
    }
    if (j >= body.length) break;
    const selfClosing = body[j - 1] === '/';
    const name = body.slice(lt + 1).match(/^[\w.$]+/)?.[0] || '';
    const rawTag = body.slice(lt, j + 1);
    const attrs = {};
    let am;
    const attrRe = /([\w-]+)="([^"]*)"/g;
    while ((am = attrRe.exec(rawTag)) !== null) attrs[am[1]] = decodeEntities(am[2]);
    if (name === 'hierarchy') {
      if (!selfClosing) stack.push(-1);
    } else {
      const b = parseBounds(attrs.bounds);
      const top = stack[stack.length - 1];
      const parentIdx = top === undefined || top === -1 ? null : top;
      const el = {
        class: attrs.class || name,
        pkg: attrs.package || null,
        text: attrs.text || '',
        resourceId: attrs['resource-id'] || '',
        contentDesc: attrs['content-desc'] || '',
        hint: '',
        bounds: b ? { l: b.l, t: b.t, r: b.r, b: b.b } : null,
        center: b ? { x: b.cx, y: b.cy } : null,
        offScreen: attrs.displayed === 'false',
        parentIdx,
        children: [],
      };
      for (const k of BOOL_ATTRS) el[k] = attrs[k] === 'true';
      el.longClickable = el['long-clickable'] || el.longClickable;
      if (parentIdx !== null) out[parentIdx].children.push(out.length);
      out.push(el);
      if (!selfClosing) stack.push(out.length - 1);
    }
    i = j + 1;
  }
  if (out.length === 0) throw new DroidJevError('uia2 XML parsed to zero nodes');
  return out;
}

export async function getLayout(serial) {
  const t0 = performance.now();
  const xml = await uia2.source(serial);
  const elements = parseUia2Xml(xml);
  // The root element's package is the focused window's package — the same
  // answer `dumpsys window` gives, without an extra adb roundtrip per step.
  return { elements, tookMs: performance.now() - t0, xml, rootPkg: elements[0]?.pkg ?? null };
}

const xmlHash = (xml) => createHash('sha256').update(xml).digest('hex').slice(0, 16);

// Wait out screen motion by polling the dump: two consecutive identical
// hierarchy hashes mean nothing (elements, states, bounds) is changing
// anymore. Returns the final layout so the caller reuses it as its dump
// instead of sleeping a fixed time and dumping once more.
export async function settleLayout(serial, { maxMs = 900 } = {}) {
  const deadline = Date.now() + maxMs;
  let prev = await getLayout(serial);
  let prevH = xmlHash(prev.xml);
  for (;;) {
    await sleep(80);
    if (Date.now() >= deadline) return prev;
    const next = await getLayout(serial);
    if (xmlHash(next.xml) === prevH) return next;
    prev = next;
    prevH = xmlHash(next.xml);
  }
}
