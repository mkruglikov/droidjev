import test from 'node:test';
import assert from 'node:assert/strict';
import { parseUia2Xml } from '../src/layout.js';
import { buildTable, tableHash, scrollAnchor } from '../src/elements.js';
import { validatedChoice, validatedNoul } from '../src/typesafe.js';
import { sanitizeInputText } from '../src/act.js';
import { redact } from '../src/util.js';
import { parseArgs } from '../src/cli.js';
import { matchRows } from '../src/find.js';
import { parseResolveActivity } from '../src/start.js';
import { scrollDistance, buildQuestions, scrollOffered, pasteOutcome } from '../src/agent.js';

const mkRow = (idx, row, clickable) => ({
  idx,
  row,
  el: { clickable, center: { x: 10, y: 20 }, bounds: { l: 0, t: 0, r: 5, b: 5 } },
});

test('matchRows prefers exact label, then clickable, then topmost', () => {
  const table = [
    mkRow(0, '[0] text "Idioma del dispositivo"', false),
    mkRow(1, '[1] click "Español (España)"', true),
    mkRow(2, '[2] click "Español"', false),
    mkRow(3, '[3] text "Español (Latinoamérica)"', false),
  ];
  const exact = matchRows(table, 'español'); // exact match, case-insensitive non-ASCII
  assert.equal(exact[0].row, '[2] click "Español"');
  const partial = matchRows(table, 'español (españa)');
  assert.equal(partial[0].row, '[1] click "Español (España)"'); // clickable beats text
  const many = matchRows(table, 'esp');
  assert.equal(many.length, 3); // "Idioma del dispositivo" does not contain the substring
  assert.throws(() => matchRows(table, '  '), /non-empty/);
});

test('scrollDistance grows with consecutive same-direction scrolls and caps', () => {
  assert.equal(scrollDistance(2000, 0), 800); // 40%
  assert.ok(scrollDistance(2000, 1) > 800);
  const capped = scrollDistance(2000, 10);
  assert.equal(capped, 1200); // 60% of screen — long flings skip pages
  assert.ok(scrollDistance(2000, 10) <= 1200);
});

test('buildQuestions merges element choices into the operation question', () => {
  const table = [
    { idx: 0, row: '[0] click "Ajustes"', el: { clickable: true } },
    { idx: 1, row: '[1] text "Titel"', tappable: true, el: { clickable: false, text: 'Titel' } },
    { idx: 2, row: '[2] edit "Buscar"', el: { clickable: true, class: 'android.widget.EditText' } },
    { idx: 3, row: '[3] text "15"', el: { clickable: false, text: '15' } },
  ];
  const q = buildQuestions({ goal: 'g', table, text: null });
  const keys = Object.keys(q.operation.criteria);
  assert.ok(keys.includes('click_0') && keys.includes('click_1'), `click options: ${keys}`);
  assert.ok(!keys.some((k) => k.startsWith('type_')), 'no type options without --text');
  assert.ok(keys.includes('copy_3') && keys.includes('copy_1'), `copy options for text rows: ${keys}`);
  assert.ok(!keys.includes('copy_0'), 'no copy option for a text-less row');
  assert.ok(['scroll_down', 'done', 'blocked'].every((k) => keys.includes(k)));
  assert.ok(!keys.includes('paste'), 'no paste before any copy this run');
  assert.ok(
    Object.keys(buildQuestions({ goal: 'g', table, copied: true }).operation.criteria).includes('paste'),
    'paste offered after a copy step',
  );
  const qText = buildQuestions({ goal: 'g', table, texts: ['Mary', '+79001234567'] });
  const textKeys = Object.keys(qText.operation.criteria);
  assert.ok(
    textKeys.includes('type_2_0') && textKeys.includes('type_2_1'),
    `one type option per --text value: ${textKeys}`,
  );
  assert.ok(!textKeys.some((k) => /^type_2$/.test(k)), 'type options carry their value index');
  assert.equal(q.goal_met.type, 'noul');
  const noScrollKeys = Object.keys(buildQuestions({ goal: 'g', table, canScroll: false }).operation.criteria);
  assert.ok(
    !noScrollKeys.some((k) => k.startsWith('scroll_')),
    `no scroll options without a usable scrollable: ${noScrollKeys}`,
  );
  assert.ok(
    ['done', 'blocked', 'back'].every((k) => noScrollKeys.includes(k)),
    'fixed ops survive scroll removal',
  );
});

test('scrollOffered requires a screen-high scrollable, not a dialog scrollview', () => {
  const row = (t, b, scrollable) => ({ el: { scrollable, bounds: { l: 0, t, r: 100, b } } });
  assert.equal(scrollOffered([row(100, 2600, true)], SCREEN), true, 'full-screen list scrolls');
  assert.equal(scrollOffered([row(900, 1400, true)], SCREEN), false, 'tiny dialog scrollview does not');
  assert.equal(scrollOffered([row(100, 2600, false)], SCREEN), false, 'no scrollable at all');
  assert.equal(scrollOffered([], null), false, 'empty table / unknown screen size');
});

test('pasteOutcome lands on an edit row, falls back to a suggestion chip, or reports a miss', () => {
  const IMEI = '867400022047199';
  const edit = {
    row: '[6] click+longclick+edit+focused "x"',
    el: { clickable: true, class: 'android.widget.EditText' },
  };
  const chip = { row: '[1] click "x"', tappable: true, el: { clickable: false } };
  const editWith = (t) => ({
    row: `[6] edit "${t}"`,
    el: { class: 'android.widget.EditText' },
  });
  const chipWith = (t) => ({ row: `[1] click "${t}"`, tappable: true, el: { clickable: false } });
  assert.equal(pasteOutcome([editWith(IMEI)], IMEI).landed, true, 'staged text in an edit field = landed');
  assert.equal(pasteOutcome([editWith(IMEI)], IMEI).chip, null, 'no chip needed once landed');
  const fallback = pasteOutcome([chipWith(IMEI)], IMEI);
  assert.equal(fallback.landed, false, 'chip alone means the paste key missed');
  assert.equal(fallback.chip.row, `[1] click "${IMEI}"`, 'chip row found for the fallback tap');
  assert.deepEqual(pasteOutcome([edit, chip], IMEI), { landed: false, chip: null }, 'nowhere = honest miss');
});

test('parseResolveActivity takes the pkg/activity line, not apk paths', () => {
  const out = '/data/app/com.example-Yq==/base.apk\ncom.example/.MainActivity';
  assert.equal(parseResolveActivity(out), 'com.example/.MainActivity');
  assert.equal(parseResolveActivity('No activity found'), null);
  assert.equal(parseResolveActivity(''), null);
});

const SCREEN = { w: 1344, h: 2992 };

// ---- uia2 server XML parsing (v10 class-named nested tags) ----

const SAMPLE_XML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy index="0" class="hierarchy" rotation="0" width="1344" height="2992">
  <android.widget.FrameLayout index="0" package="com.example" class="android.widget.FrameLayout" text="" bounds="[0,0][1344,2992]" displayed="true">
    <androidx.recyclerview.widget.RecyclerView index="0" package="com.example" class="androidx.recyclerview.widget.RecyclerView" resource-id="com.example:id/list" scrollable="true" bounds="[0,200][1344,2800]" displayed="true">
      <android.widget.TextView index="0" package="com.example" class="android.widget.TextView" text="Wi&#45;Fi &amp; Network" bounds="[100,300][1200,400]" displayed="true"/>
      <android.widget.LinearLayout index="1" package="com.example" class="android.widget.LinearLayout" resource-id="com.example:id/row" clickable="true" bounds="[100,300][1244,500]" displayed="true">
        <android.widget.TextView index="0" package="com.example" class="android.widget.TextView" text="About &lt;phone&gt;" bounds="[100,320][1200,380]" displayed="true"/>
      </android.widget.LinearLayout>
    </androidx.recyclerview.widget.RecyclerView>
  </android.widget.FrameLayout>
</hierarchy>`;

test('parses nesting, entities, and flags from uia2 xml', () => {
  const els = parseUia2Xml(SAMPLE_XML);
  assert.equal(els.length, 5);
  const row = els.find((e) => e.resourceId === 'com.example:id/row');
  assert.equal(row.clickable, true);
  assert.equal(row.parentIdx, 1);
  const wifi = els.find((e) => e.text.includes('Wi'));
  assert.equal(wifi.text, 'Wi-Fi & Network'); // entity decoded
  const about = els.find((e) => e.text.startsWith('About'));
  assert.equal(about.text, 'About <phone>');
});

test('buildTable adopts contained labels and flags interactions', () => {
  const els = parseUia2Xml(SAMPLE_XML);
  const table = buildTable(els, SCREEN);
  const rows = table.map((r) => r.row);
  assert.ok(
    rows.some((r) => r.includes('click') && r.includes('About')),
    `row adopts child label: ${rows}`,
  );
  assert.ok(
    rows.some((r) => r.startsWith('[0] scroll')),
    `scrollable listed without tiny-child label: ${rows}`,
  );
  assert.ok(rows.some((r) => r.includes('"Wi-Fi & Network"')));
});

test('buildTable drops decorative containers and off-screen elements', () => {
  const els = parseUia2Xml(SAMPLE_XML);
  const table = buildTable(els, SCREEN);
  assert.ok(!table.some((r) => r.row.includes('FrameLayout')), 'non-interactive unlabeled root dropped');
  const offscreen = [{ ...els[0], bounds: { l: 0, t: 3100, r: 100, b: 3200 }, center: { x: 50, y: 3150 } }];
  assert.equal(buildTable(offscreen, SCREEN).length, 0);
});

test('buildTable adopts row titles and sibling switch labels, never a wrapper stray child', () => {
  const mk = (cls, l, t, r, b, extra = {}) => ({
    class: cls,
    text: '',
    resourceId: '',
    contentDesc: '',
    hint: '',
    bounds: { l, t, r, b },
    center: { x: (l + r) / 2, y: (t + b) / 2 },
    clickable: false,
    longClickable: false,
    scrollable: false,
    checkable: false,
    focused: false,
    selected: false,
    password: false,
    offScreen: false,
    ...extra,
  });
  // real geometry from a Settings preference list (emulator-5554):
  const els = [
    // full-width preference row, title ~6% of container area
    mk('LinearLayout', 0, 347, 1344, 518, { clickable: true }),
    mk('TextView', 252, 375, 463, 440, { text: 'WLAN' }),
    // airplane-mode switch beside its title on the same row band
    mk('TextView', 252, 850, 576, 915, { text: 'Flugmodus' }),
    mk('Switch', 1092, 811, 1248, 955, { checkable: true, clickable: true }),
    // full-screen scrollable wrapper with a tiny stray child (~0.07%)
    mk('ScrollView', 0, 0, 1344, 2992, { scrollable: true }),
    mk('TextView', 600, 100, 700, 130, { text: 'Suche' }),
  ];
  const rows = buildTable(els, SCREEN).map((r) => r.row);
  assert.ok(
    rows.some((r) => r.includes('click') && r.includes('WLAN')),
    `row adopts title: ${rows}`,
  );
  assert.ok(
    rows.some((r) => r.includes('check') && r.includes('Flugmodus')),
    `switch adopts row label: ${rows}`,
  );
  assert.ok(
    rows.some((r) => r.startsWith('[') && r.includes('scroll') && !r.includes('"')),
    `wrapper stays unlabeled: ${rows}`,
  );
});

test('buildTable hides automation helper apps and their icon containers', () => {
  // Launcher shapes: home-screen icon = clickable container + label child;
  // all-apps icon = clickable view with its own text inside the scrollable
  // grid. Both helper flavors must vanish without taking the grid with them.
  const xml = `<hierarchy rotation="0">
  <node package="com.android.launcher3" class="android.widget.FrameLayout" bounds="[0,0][1344,2992]">
    <node package="com.android.launcher3" class="android.widget.RecyclerView" scrollable="true" bounds="[0,100][1344,2800]">
      <node package="com.android.launcher3" class="android.widget.FrameLayout" clickable="true" bounds="[100,300][400,500]">
        <node package="com.android.launcher3" class="android.widget.TextView" text="Appium Settings" bounds="[100,400][400,460]"/>
      </node>
      <node package="com.android.launcher3" class="android.widget.TextView" text="Settings" clickable="true" bounds="[500,300][800,460]"/>
      <node package="io.appium.settings" class="android.widget.TextView" text="whatever" clickable="true" bounds="[500,600][800,700]"/>
    </node>
  </node>
</hierarchy>`;
  const rows = buildTable(parseUia2Xml(xml), SCREEN).map((r) => r.row);
  assert.ok(!rows.some((r) => /Appium|whatever/.test(r)), `helper icons hidden: ${rows}`);
  assert.ok(!rows.some((r) => /^\[\d+\] click(?! ")/.test(r)), `no anonymous icon container left: ${rows}`);
  assert.ok(
    rows.some((r) => r.includes('click') && r.includes('"Settings"')),
    `real Settings kept: ${rows}`,
  );
  assert.ok(
    rows.some((r) => r.includes('scroll')),
    `scrollable drawer survives: ${rows}`,
  );
});

test('operation rules cover untypable goal text and no-change repeats', () => {
  const joined = buildQuestions({ table: [], texts: [] }).operation.instructions.rules.join('\n');
  assert.ok(joined.includes('choose blocked right away'), 'missing goal text → blocked guidance');
  assert.ok(joined.includes('no visible change'), 'no-repeat-on-no-change guidance');
  assert.ok(joined.includes('would duplicate it'), 'paste-into-already-filled field → done guidance');
  assert.ok(joined.includes('do not enter it again'), 'abandoned-section guidance');
});

test('tableHash ignores position-only shifts, detects content/state changes', () => {
  const els = parseUia2Xml(SAMPLE_XML);
  const t1 = buildTable(els, SCREEN);
  assert.equal(tableHash(t1), tableHash(buildTable(els, SCREEN)));
  // pure scroll: same rows visible at different offsets → same content hash
  const shifted = els.map((e) =>
    e.bounds
      ? {
          ...e,
          bounds: { ...e.bounds, t: e.bounds.t - 200, b: e.bounds.b - 200, center: undefined },
          center: { x: e.center.x, y: e.center.y - 200 },
        }
      : e,
  );
  assert.equal(tableHash(t1), tableHash(buildTable(shifted, SCREEN)));
  const otherApp = els.map((e) => ({ ...e, text: e.text ? 'Different' : '' }));
  assert.notEqual(tableHash(t1), tableHash(buildTable(otherApp, SCREEN)));
});

test('scrollAnchor prefers the largest scrollable', () => {
  const els = parseUia2Xml(SAMPLE_XML);
  const anchor = scrollAnchor(buildTable(els, SCREEN), SCREEN);
  assert.deepEqual(anchor, { x: 672, y: 1500 });
});

// ---- typesafe answer validation ----

const IDS = ['click', 'scroll_down', 'done'];

test('validatedChoice accepts a sound answer', () => {
  const a = { choice: 'click', confidence: 0.9, probabilities: { click: 0.8, scroll_down: 0.15, done: 0.05 } };
  const v = validatedChoice(a, IDS, 'operation');
  assert.equal(v.choice, 'click');
});

test('validatedChoice rejects injection / malformed answers', () => {
  assert.throws(
    () => validatedChoice({ choice: 'rm -rf /', probabilities: {} }, IDS, 'operation'),
    /not in offered options/,
  );
  assert.throws(
    () =>
      validatedChoice(
        { choice: 'click', probabilities: { click: 0.5, scroll_down: 0.2, done: 0.1 } },
        IDS,
        'operation',
      ),
    /sum/,
  );
  assert.throws(
    () =>
      validatedChoice(
        { choice: 'scroll_down', probabilities: { click: 0.9, scroll_down: 0.05, done: 0.05 } },
        IDS,
        'operation',
      ),
    /argmax/,
  );
  assert.throws(
    () => validatedChoice({ choice: 'click', probabilities: { click: 0.8, scroll_down: 0.2 } }, IDS, 'operation'),
    /cover/,
  );
  assert.throws(
    () =>
      validatedChoice(
        { choice: 'click', probabilities: { click: 1.5, scroll_down: -0.3, done: -0.2 } },
        IDS,
        'operation',
      ),
    /range/,
  );
});

test('validatedNoul bounds-checks', () => {
  assert.equal(validatedNoul({ noul: 0.42 }, 'goal_met').noul, 0.42);
  assert.throws(() => validatedNoul({ noul: 1.2 }, 'goal_met'));
  assert.throws(() => validatedNoul({ noul: 'yes' }, 'goal_met'));
});

test('validatedChoice rejects extra probability keys and NaN confidence falls back', () => {
  assert.throws(
    () =>
      validatedChoice(
        { choice: 'click', probabilities: { click: 0.8, scroll_down: 0.15, done: 0.05, 'rm -rf /': 5 } },
        IDS,
        'operation',
      ),
    /not offered/,
  );
  const nanConf = validatedChoice(
    { choice: 'click', confidence: 'garbage', probabilities: { click: 0.8, scroll_down: 0.15, done: 0.05 } },
    IDS,
    'operation',
  );
  assert.equal(nanConf.confidence, 0.8); // falls back to validated p(choice), never NaN
});

test('buildTable keeps interactive rows when the cap truncates', () => {
  const els = [];
  for (let i = 0; i < 90; i++) {
    els.push({
      class: 'android.widget.Button',
      text: '',
      resourceId: '',
      contentDesc: '',
      hint: '',
      bounds: { l: 0, t: i * 10, r: 100, b: i * 10 + 10 },
      center: { x: 50, y: i * 10 + 5 },
      clickable: true,
      scrollable: false,
      checkable: false,
      longClickable: false,
      focusable: true,
      focused: false,
      selected: false,
      offScreen: false,
    });
  }
  for (let i = 0; i < 5; i++) {
    els.push({
      class: 'android.widget.TextView',
      text: `deco ${i}`,
      resourceId: '',
      contentDesc: '',
      hint: '',
      bounds: { l: 0, t: 2000 + i * 10, r: 100, b: 2010 + i * 10 },
      center: { x: 50, y: 2005 + i * 10 },
      clickable: false,
      scrollable: false,
      checkable: false,
      longClickable: false,
      focusable: false,
      focused: false,
      selected: false,
      offScreen: false,
    });
  }
  const table = buildTable(els, SCREEN);
  assert.equal(table.length, 80);
  assert.equal(table.truncated, true);
  assert.ok(
    table.every((r) => r.el.clickable),
    'interactive rows survive the cap',
  );
});

test('xml tokenizer survives > inside quoted attributes', () => {
  const xml = `<hierarchy rotation="0"><node index="0" text="a &gt; b" resource-id="" class="android.widget.Button" content-desc="" clickable="true" bounds="[0,0][100,100]"/></hierarchy>`;
  const els = parseUia2Xml(xml);
  assert.equal(els.length, 1);
  assert.equal(els[0].text, 'a > b');
  assert.equal(els[0].clickable, true);
  assert.deepEqual(els[0].bounds, { l: 0, t: 0, r: 100, b: 100 });
});

test('out-of-range numeric entities decode to empty, not a crash', () => {
  const xml = `<hierarchy rotation="0"><node index="0" text="&#1114112;x" resource-id="" class="X" content-desc="" bounds="[0,0][100,100]"/></hierarchy>`;
  assert.doesNotThrow(() => parseUia2Xml(xml));
});

// ---- appium uiautomator2-server v10 XML (class-named nested tags) ----

const UIA2_XML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy index="0" class="hierarchy" rotation="0" width="1344" height="2992">
  <android.widget.FrameLayout index="0" package="com.example" class="android.widget.FrameLayout" text="" bounds="[0,0][1344,2992]" displayed="true">
    <androidx.recyclerview.widget.RecyclerView index="0" package="com.example" class="androidx.recyclerview.widget.RecyclerView" resource-id="com.example:id/list" scrollable="true" bounds="[0,200][1344,2800]" displayed="true"></androidx.recyclerview.widget.RecyclerView>
    <android.widget.LinearLayout index="1" package="com.example" class="android.widget.LinearLayout" clickable="true" bounds="[100,300][1244,500]" displayed="true">
      <android.widget.TextView index="0" package="com.example" class="android.widget.TextView" text="Wi&#45;Fi &amp; Network" content-desc="Wi-Fi" bounds="[100,320][1200,380]" displayed="true"/>
    </android.widget.LinearLayout>
    <android.widget.Switch index="2" package="com.example" class="android.widget.Switch" checkable="true" checked="true" clickable="true" bounds="[1092,811][1248,955]" displayed="false"/>
  </android.widget.FrameLayout>
</hierarchy>`;

test('parseUia2Xml handles class-named tags, nesting, and v10 attrs', () => {
  const els = parseUia2Xml(UIA2_XML);
  assert.equal(els.length, 5); // hierarchy root is not an element
  const list = els.find((e) => e.resourceId === 'com.example:id/list');
  assert.equal(list.scrollable, true);
  assert.equal(list.parentIdx, 0);
  assert.deepEqual(els[0].children, [1, 2, 4]); // nesting tracked through close tags
  const label = els.find((e) => e.text.includes('Wi'));
  assert.equal(label.text, 'Wi-Fi & Network'); // entity decoded
  assert.equal(label.contentDesc, 'Wi-Fi');
  const sw = els.find((e) => e.class === 'android.widget.Switch');
  assert.equal(sw.checked, true);
  assert.ok(sw.offScreen, 'displayed=false maps to offScreen');
  assert.throws(() => parseUia2Xml('<no-hierarchy/>'), /no <hierarchy>/);
});

test('redact covers single- and multi-line bearer values', () => {
  assert.ok(!redact('Headers.append: "Bearer line1\nSECRET123" is invalid').includes('SECRET123'));
  assert.ok(redact('Bearer abc123 tail').includes('[REDACTED]'));
});

test('parseArgs rejects value-less flags and bad values', () => {
  assert.throws(() => parseArgs(['act', 'goal', '--device', '--json']), /needs a value/);
  assert.throws(() => parseArgs(['act', 'goal', '--text']), /needs a value/);
  assert.throws(() => parseArgs(['act', 'goal', '--max-steps', '-5']), /1\.\.100/);
  assert.throws(() => parseArgs(['act', 'goal', '--max-steps', 'abc']), /1\.\.100/);
  assert.throws(() => parseArgs(['act', 'goal', '--provider', 'appium']), /unknown flag/);
  assert.equal(parseArgs(['act', 'goal', '--no-animations']).noAnimations, true);
  assert.deepEqual(parseArgs(['act', 'open x', '--text', 'hi there', '--max-steps', '5']), {
    _: ['act', 'open x'],
    texts: ['hi there'],
    maxSteps: 5,
  });
});

// ---- input text sanitizer (device-shell injection boundary) ----

test('sanitizeInputText maps spaces and rejects shell metacharacters', () => {
  assert.equal(sanitizeInputText('hello world'), 'hello%sworld');
  assert.equal(sanitizeInputText('a.b,c:d/e+f@g%h^i'), 'a.b,c:d/e+f@g%h^i');
  for (const evil of [
    'a;b',
    'a&b',
    'a|b',
    '$(x)',
    'a`b`',
    'a"b',
    "a'b",
    'a\\b',
    'a<b',
    'rm -rf /tmp/x*',
    'a{b}',
    'café',
    '',
  ]) {
    assert.throws(() => sanitizeInputText(evil), /cannot be typed safely|empty/i, `must reject: ${evil}`);
  }
});
