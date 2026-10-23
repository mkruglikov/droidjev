import test from 'node:test';
import assert from 'node:assert/strict';
import { parseUia2Xml } from '../src/layout.js';
import {
  buildTable,
  tableHash,
  scrollAnchor,
  serializeRows,
  listItems,
  elementText,
  isDateLabel,
} from '../src/elements.js';
import { validatedChoice, validatedNoul, validatedBox } from '../src/typesafe.js';
import { parseScreencap, scaleTarget, downscaleRgba, encodePng } from '../src/shot.js';
import { inflateSync } from 'node:zlib';
import { run } from '../src/util.js';
import { sanitizeInputText, slideGesture } from '../src/act.js';
import { redact } from '../src/util.js';
import { parseArgs, parseApp } from '../src/cli.js';
import { matchRows } from '../src/find.js';
import { parseResolveActivity } from '../src/start.js';
import {
  scrollDistance,
  buildQuestions,
  scrollOffered,
  pasteOutcome,
  sliderStops,
  answerFor,
  stuckOps,
  buildVisionQuestions,
  visionVerdict,
  boxCenter,
  TAP_BOX_MIN_CONFIDENCE,
} from '../src/agent.js';
import { buildDecideRequest, decide, serveDecide } from '../src/decide.js';

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
  const launcherKeys = Object.keys(buildQuestions({ goal: 'g', table, launcher: true }).operation.criteria);
  assert.ok(!launcherKeys.includes('blocked'), `no blocked offered from the launcher: ${launcherKeys}`);
  assert.ok(launcherKeys.includes('done'), 'done stays available on the launcher');
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

test('stuckOps withdraws a second wait and a scroll that changed nothing', () => {
  assert.ok(stuckOps(['click_1', 'wait']).has('wait'), 'a wait after a wait is idle time');
  assert.ok(!stuckOps(['click_1']).has('wait'), 'the first wait stays available');
  assert.ok(stuckOps(['scroll_down'], 'scroll_down').has('scroll_down'), 'a scroll that moved nothing is withdrawn');
  assert.ok(!stuckOps(['scroll_down']).has('scroll_down'), 'a scroll that moved something stays');
  assert.ok(!stuckOps(['click_1'], 'click_2').has('click_2'), 'only scrolls are withdrawn on no-change');
});

test('buildQuestions drops the options a stuck loop or an idle step rules out', () => {
  const table = buildTable(parseUia2Xml(SAMPLE_XML), SCREEN);
  const idle = buildQuestions({ table, texts: [], recentChoices: ['wait'], lastUnchanged: null });
  const fresh = buildQuestions({ table, texts: [], recentChoices: [], lastUnchanged: null });
  assert.ok(!('wait' in idle.operation.criteria), 'wait is gone once a wait changed nothing');
  assert.ok('wait' in fresh.operation.criteria, 'wait is offered while it is still useful');
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

// --- decide: the host-facing single step -----------------------------------

const decideElements = () => [
  {
    pkg: 'com.android.settings',
    class: 'android.widget.TextView',
    text: 'Wi-Fi',
    bounds: { l: 0, t: 100, r: 500, b: 200 },
    center: { x: 250, y: 150 },
    clickable: true,
  },
  {
    pkg: 'com.android.settings',
    class: 'android.widget.Switch',
    checkable: true,
    checked: true,
    clickable: true,
    bounds: { l: 800, t: 120, r: 1000, b: 180 },
    center: { x: 900, y: 150 },
  },
  {
    pkg: 'com.android.settings',
    class: 'android.widget.EditText',
    hint: 'Search',
    bounds: { l: 0, t: 300, r: 1000, b: 400 },
    center: { x: 500, y: 350 },
  },
  {
    pkg: 'com.android.settings',
    class: 'android.widget.TextView',
    text: '15',
    bounds: { l: 0, t: 500, r: 200, b: 600 },
    center: { x: 100, y: 550 },
  },
];

const decideRequest = (over = {}) => ({
  goal: 'turn Wi-Fi off',
  screen: { app: 'com.android.settings', size: '1080x2400', w: 1080, h: 2400 },
  elements: decideElements(),
  provided_texts: ['hello', 'world'],
  can_scroll: false,
  clipboard: false,
  ...over,
});

test('buildDecideRequest offers the act-loop operations for a host table', () => {
  const { state, questions } = buildDecideRequest(decideRequest());
  const keys = Object.keys(questions.operation.criteria);
  assert.ok(keys.includes('click_0'), 'clickable labeled row is offered');
  assert.ok(
    keys.includes('type_2_0') && keys.includes('type_2_1'),
    'each provided text gets a type option on editable rows',
  );
  assert.deepEqual(
    keys.filter((k) => ['back', 'home', 'wait', 'done', 'blocked'].includes(k)).sort(),
    ['back', 'blocked', 'done', 'home', 'wait'],
    'fixed operations are always offered',
  );
  assert.ok(!keys.includes('scroll_down'), 'scroll is withheld when can_scroll is false');
  assert.ok(!keys.some((k) => k.startsWith('copy_') || k === 'paste'), 'no clipboard ops when the host cannot copy');
  assert.deepEqual(
    state.elements,
    serializeRows(buildTable(decideElements(), { w: 1080, h: 2400 })),
    'state carries the serialized rows',
  );
  assert.equal(state.provided_texts.length, 2);
  assert.equal(state.screen.app, 'com.android.settings');
  assert.equal(state.elements_note, null);
});

test('buildDecideRequest keeps clipboard ops, screen notes and history bounded', () => {
  const { state, questions } = buildDecideRequest(
    decideRequest({
      clipboard: true,
      copied: true,
      can_scroll: true,
      screen: { app: 'com.android.launcher3', size: '1080x2400', w: 1080, h: 2400 },
      recent_actions: Array.from({ length: 14 }, (_, i) => `${i}. click_${i}`),
    }),
  );
  const keys = Object.keys(questions.operation.criteria);
  assert.ok(keys.includes('copy_3'), 'a row with text can be copied');
  assert.ok(keys.includes('paste'), 'paste appears once a copy ran');
  assert.ok(keys.includes('scroll_down') && keys.includes('scroll_up'), 'scroll offered when the host allows it');
  assert.match(state.elements_note, /home screen \(launcher\)/);
  assert.equal(state.recent_actions.length, 10, 'only the last 10 actions are sent');
  assert.equal(state.recent_actions[9], '13. click_13');
});

test('decide validates the answer against the offered options', async () => {
  const { questions } = buildDecideRequest(decideRequest());
  const ids = Object.keys(questions.operation.criteria);
  const answers = (choice, probabilities) => ({ operation: { choice, probabilities }, goal_met: { noul: 0.2 } });
  const all = Object.fromEntries(ids.map((k) => [k, 0]));
  all.click_0 = 1;

  const res = await decide(decideRequest(), {
    askFn: async () => ({ answers: answers('click_0', all), usage: { input_tokens: 5 }, latencyMs: 7 }),
  });
  assert.equal(res.operation.choice, 'click_0');
  assert.equal(res.operation.confidence, 1);
  assert.equal(res.goal_met.noul, 0.2);
  assert.equal(res.app, 'com.android.settings');
  assert.equal(res.usage.input_tokens, 5);

  // Probabilities that do not cover exactly the offered options, or a choice
  // that is not the argmax, must never become an action.
  const missing = { ...all };
  delete missing.wait;
  await assert.rejects(
    () => decide(decideRequest(), { askFn: async () => ({ answers: answers('click_0', missing) }) }),
    /do not cover exactly the offered options/,
  );
  const notArgmax = { ...all, click_0: 0.4, done: 0.6 };
  await assert.rejects(
    () => decide(decideRequest(), { askFn: async () => ({ answers: answers('click_0', notArgmax) }) }),
    /not the probability argmax/,
  );
  const unoffered = { ...all, copy_3: 0.5 };
  await assert.rejects(
    () => decide(decideRequest(), { askFn: async () => ({ answers: answers('done', unoffered) }) }),
    /options that were not offered/,
  );
});

test('buildDecideRequest rejects a request without a goal or elements', () => {
  assert.throws(() => buildDecideRequest({ elements: [] }), /goal string/);
  assert.throws(() => buildDecideRequest({ goal: 'g' }), /elements array/);
});

test('decide reports which caller element each numbered row came from', async () => {
  const offScreen = {
    pkg: 'com.android.settings',
    class: 'android.widget.FrameLayout',
    bounds: { l: 2000, t: 3000, r: 2100, b: 3100 }, // outside the 1080x2400 viewport
    center: { x: 2050, y: 3050 },
  };
  const elements = [offScreen, ...decideElements()];
  const request = decideRequest({ elements });
  const { sources, centers } = buildDecideRequest(request);
  assert.equal(sources['0'], 1, 'row 0 is the first surviving caller element, not element 0');
  assert.equal(sources['1'], 2);
  assert.deepEqual(centers['0'], { x: 250, y: 150 }, 'each row carries its tap point');

  const { questions } = buildDecideRequest(request);
  const ids = Object.keys(questions.operation.criteria);
  const probabilities = Object.fromEntries(ids.map((k) => [k, 0]));
  probabilities.click_0 = 1;
  const res = await decide(request, {
    askFn: async () => ({
      answers: { operation: { choice: 'click_0', probabilities }, goal_met: { noul: 0 } },
      latencyMs: 1,
    }),
  });
  assert.equal(res.sources['0'], 1, 'the host can map the chosen row back to its own element');
  assert.deepEqual(res.centers['0'], { x: 250, y: 150 }, 'and tap it without re-resolving indices');
});

test('serveDecide answers one JSON line per request on one process', async () => {
  const input = (async function* () {
    yield JSON.stringify({ goal: 'turn wifi off', elements: [] }) + '\n';
    yield JSON.stringify({ goal: 'open settings', elements: [] }) + '\n';
  })();
  const written = [];
  const output = { write: (chunk) => written.push(chunk) };
  const seen = [];
  const askFn = async ({ state, questions }) => {
    seen.push(state.goal);
    const ids = Object.keys(questions.operation.criteria);
    const probabilities = Object.fromEntries(ids.map((k) => [k, k === 'wait' ? 1 : 0]));
    return { answers: { operation: { choice: 'wait', probabilities }, goal_met: { noul: 0 } }, latencyMs: 1 };
  };

  await serveDecide(input, output, askFn);

  assert.equal(written.length, 2, 'one answer line per request line');
  const first = JSON.parse(written[0]);
  assert.equal(first.ok, true);
  assert.equal(first.operation.choice, 'wait');
  assert.deepEqual(seen, ['turn wifi off', 'open settings']);
});

test('serveDecide reports a bad request without dropping the session', async () => {
  const input = (async function* () {
    yield 'not json\n';
    yield JSON.stringify({ goal: 'g', elements: [] }) + '\n';
  })();
  const written = [];
  const askFn = async ({ questions }) => {
    const ids = Object.keys(questions.operation.criteria);
    const probabilities = Object.fromEntries(ids.map((k) => [k, k === 'done' ? 1 : 0]));
    return { answers: { operation: { choice: 'done', probabilities }, goal_met: { noul: 1 } }, latencyMs: 1 };
  };

  await serveDecide(input, { write: (c) => written.push(c) }, askFn);

  assert.equal(JSON.parse(written[0]).ok, false);
  assert.equal(JSON.parse(written[1]).ok, true, 'the next request is still served');
});

// --- sliders, long press, answers, clipboard, horizontal scroll --------------

test('sliderStops offers only the positions the goal names', () => {
  assert.deepEqual(
    sliderStops('Turn brightness to the max value.').map((s) => s.key),
    ['max'],
  );
  assert.deepEqual(
    sliderStops('Set the volume to minimum').map((s) => s.key),
    ['min'],
  );
  assert.deepEqual(
    sliderStops('set it to 30%').map((s) => s.fraction),
    [0.3],
  );
  assert.deepEqual(sliderStops('Open the Settings app'), [], 'no position named, no stop offered');
});

test('slider stops are offered only on range rows', () => {
  const table = [
    { idx: 0, row: '[0] click "Brightness level"', el: { clickable: true, text: 'Brightness level' } },
    { idx: 1, row: '[1] range "197.0"', el: { class: 'android.widget.SeekBar', text: '197.0' } },
  ];
  const keys = Object.keys(buildQuestions({ goal: 'set brightness to maximum', table }).operation.criteria);
  assert.ok(keys.includes('set_1_max'), `a named position lands on the slider: ${keys}`);
  assert.ok(!keys.some((k) => k.startsWith('set_0')), 'no stop on a row that is not a slider');
  const none = Object.keys(buildQuestions({ goal: 'open Settings', table }).operation.criteria);
  assert.ok(!none.some((k) => k.startsWith('set_')), `a goal naming no position offers no stop: ${none}`);
});

test('slideGesture overshoots the track so the ends clamp', () => {
  const el = { bounds: { l: 100, t: 200, r: 900, b: 260 } };
  assert.deepEqual(slideGesture(el, 0), { x1: 500, y1: 230, x2: 60, y2: 230, durationMs: 400 });
  assert.deepEqual(slideGesture(el, 1), { x1: 500, y1: 230, x2: 940, y2: 230, durationMs: 400 });
  assert.equal(slideGesture(el, 0.5).x2, 500, 'a middle stop drags to the middle of the track');
});

test('long press is offered only where a row has a context menu', () => {
  const table = [
    { idx: 0, row: '[0] click+longclick "Note"', el: { clickable: true, longClickable: true } },
    { idx: 1, row: '[1] click "Other"', el: { clickable: true } },
  ];
  const keys = Object.keys(buildQuestions({ goal: 'delete the note', table }).operation.criteria);
  assert.ok(keys.includes('hold_0') && !keys.includes('hold_1'), `hold only on long-clickable rows: ${keys}`);
});

test('answer options appear only when the host can submit them', () => {
  const table = [
    { idx: 0, row: '[0] text "Android version"', el: { text: 'Android version' } },
    { idx: 1, row: '[1] text "14"', el: { clickable: false, text: '14' } },
  ];
  const off = Object.keys(buildQuestions({ goal: 'what is the Android version', table }).operation.criteria);
  assert.ok(!off.some((k) => k.startsWith('answer')), `no answers unless asked for: ${off}`);
  const on = Object.keys(
    buildQuestions({ goal: 'what is the Android version', table, answers: true }).operation.criteria,
  );
  assert.ok(on.includes('answer_0') && on.includes('answer_1'), `every text row is a candidate: ${on}`);
  const withTexts = Object.keys(
    buildQuestions({ goal: 'is it a todo', table, answers: true, texts: ['True', 'False'] }).operation.criteria,
  );
  assert.ok(
    withTexts.includes('answer_text_0') && withTexts.includes('answer_text_1'),
    `stated literals are offered verbatim: ${withTexts}`,
  );
});

test('answer_list is offered for a container holding several items', () => {
  const table = [
    { idx: 0, row: '[0] scroll "Tasks"', el: { scrollable: true, bounds: { l: 0, t: 0, r: 1080, b: 2000 } } },
    { idx: 1, row: '[1] text "Buy milk"', el: { text: 'Buy milk', bounds: { l: 20, t: 100, r: 400, b: 160 } } },
    { idx: 2, row: '[2] text "Call mom"', el: { text: 'Call mom', bounds: { l: 20, t: 200, r: 400, b: 260 } } },
  ];
  const keys = Object.keys(
    buildQuestions({ goal: 'which tasks are due today', table, answers: true }).operation.criteria,
  );
  assert.ok(keys.includes('answer_list_0'), `the list is one candidate answer: ${keys}`);
  assert.ok(!keys.includes('answer_list_1'), 'a row holding no items is no container');
});

test('answerFor resolves a row, a container and a goal-supplied value', () => {
  const table = [
    { idx: 0, row: '[0] scroll "Tasks"', el: { scrollable: true, bounds: { l: 0, t: 0, r: 1080, b: 2000 } } },
    { idx: 1, row: '[1] text "Buy milk"', el: { text: 'Buy milk', bounds: { l: 20, t: 100, r: 400, b: 160 } } },
    { idx: 2, row: '[2] text "Call mom"', el: { text: 'Call mom', bounds: { l: 20, t: 200, r: 400, b: 260 } } },
  ];
  assert.equal(answerFor('answer_1', { table }), 'Buy milk');
  assert.equal(answerFor('answer_list_0', { table }), 'Buy milk, Call mom');
  assert.equal(answerFor('answer_text_1', { table, values: ['True', 'False'] }), 'False');
  assert.equal(answerFor('click_1', { table }), null, 'a non-answer choice submits nothing');
});

test('clipboard ops are what the host says it can execute', () => {
  const table = [{ idx: 0, row: '[0] edit "Search"', el: { class: 'android.widget.EditText', text: 'x' } }];
  const on = Object.keys(
    buildQuestions({ goal: 'copy the text to the clipboard', table, texts: ['abc'] }).operation.criteria,
  );
  assert.ok(on.includes('clip_0'), `the goal-supplied value can go to the clipboard: ${on}`);
  const elsewhere = Object.keys(
    buildQuestions({ goal: 'add a note named a.txt', table, texts: ['a.txt'] }).operation.criteria,
  );
  assert.ok(
    !elsewhere.some((k) => k.startsWith('clip_')),
    `no clip option for a goal that never says clipboard: ${elsewhere}`,
  );
  const off = Object.keys(
    buildQuestions({ goal: 'copy the text to the clipboard', table, texts: ['abc'], clipboard: false }).operation
      .criteria,
  );
  assert.ok(
    !off.some((k) => k.startsWith('clip_') || k.startsWith('copy_') || k === 'paste'),
    `no clipboard ops for a host without one: ${off}`,
  );
});

test('horizontal scroll is offered only when the dump shows a horizontal area', () => {
  const vertical = [
    { idx: 0, row: '[0] scroll "List"', el: { scrollable: true, bounds: { l: 0, t: 0, r: 1080, b: 2000 } } },
  ];
  const vKeys = Object.keys(buildQuestions({ goal: 'g', table: vertical }).operation.criteria);
  assert.ok(!vKeys.includes('scroll_right'), `a tall list is not horizontal: ${vKeys}`);
  const pager = [
    {
      idx: 0,
      row: '[0] scroll "Pages"',
      el: {
        scrollable: true,
        class: 'androidx.viewpager2.widget.ViewPager2',
        bounds: { l: 0, t: 500, r: 1080, b: 1500 },
      },
    },
  ];
  const hKeys = Object.keys(buildQuestions({ goal: 'g', table: pager }).operation.criteria);
  assert.ok(hKeys.includes('scroll_left') && hKeys.includes('scroll_right'), `a pager pages sideways: ${hKeys}`);
});

test('listItems drops the label inside its own row and reads in screen order', () => {
  const table = [
    { idx: 0, row: '[0] scroll "List"', el: { scrollable: true, bounds: { l: 0, t: 0, r: 1080, b: 2000 } } },
    {
      idx: 1,
      row: '[1] click "Check the tires"',
      el: { clickable: true, text: 'Check the tires', bounds: { l: 20, t: 400, r: 900, b: 500 } },
    },
    {
      idx: 2,
      row: '[2] text "Check the tires"',
      el: { text: 'Check the tires', bounds: { l: 40, t: 420, r: 400, b: 480 } },
    },
    {
      idx: 3,
      row: '[3] click "Book a flight"',
      el: { clickable: true, text: 'Book a flight', bounds: { l: 20, t: 300, r: 900, b: 400 } },
    },
  ];
  assert.deepEqual(
    listItems(table, 0).map((r) => elementText(r.el)),
    ['Book a flight', 'Check the tires'],
  );
});

test('scrollAnchor moves the scrollable that holds the focus', () => {
  const table = [
    {
      idx: 0,
      row: '[0] scroll "Page"',
      el: { scrollable: true, bounds: { l: 0, t: 0, r: 1080, b: 2400 }, center: { x: 540, y: 1200 } },
    },
    {
      idx: 1,
      row: '[1] scroll "Hours"',
      el: { scrollable: true, bounds: { l: 700, t: 800, r: 900, b: 1100 }, center: { x: 800, y: 950 } },
    },
    {
      idx: 2,
      row: '[2] text "10"',
      el: { focused: true, text: '10', bounds: { l: 720, t: 850, r: 880, b: 950 }, center: { x: 800, y: 900 } },
    },
  ];
  assert.deepEqual(scrollAnchor(table, { w: 1080, h: 2400 }), { x: 800, y: 950 });
});

test('app candidates become one-step launch options', () => {
  const table = [{ idx: 0, row: '[0] click "Chrome"', el: { clickable: true, text: 'Chrome' } }];
  const keys = Object.keys(
    buildQuestions({ goal: 'open Markor', table, apps: ['markor', 'simple calendar pro'] }).operation.criteria,
  );
  assert.ok(keys.includes('launch_0') && keys.includes('launch_1'), `one option per offered app: ${keys}`);
  const none = Object.keys(buildQuestions({ goal: 'open Markor', table }).operation.criteria);
  assert.ok(!none.some((k) => k.startsWith('launch_')), `no launch options without candidates: ${none}`);
});

test('parseApp takes "Label=package" or a bare package', () => {
  assert.deepEqual(parseApp('Settings=com.android.settings'), { label: 'Settings', pkg: 'com.android.settings' });
  assert.deepEqual(parseApp('com.android.settings'), { label: 'settings', pkg: 'com.android.settings' });
  assert.throws(() => parseApp('=com.android.settings'), /needs "Label=package"/);
  assert.throws(() => parseApp('Settings='), /needs "Label=package"/);
});

test('a paste-only clipboard is offered when the host says it is filled', () => {
  const table = [{ idx: 0, row: '[0] edit "Body"', el: { class: 'android.widget.EditText' } }];
  const cold = Object.keys(buildQuestions({ goal: 'paste it', table }).operation.criteria);
  assert.ok(!cold.includes('paste'), 'no paste before a copy this run');
  const warm = Object.keys(buildQuestions({ goal: 'paste it', table, clipboardReady: true }).operation.criteria);
  assert.ok(warm.includes('paste'), `a host-filled clipboard offers paste: ${warm}`);
});

test('a date row is flagged so the picker route is visible', () => {
  assert.ok(isDateLabel('October 15 (Sun)') && isDateLabel('Oct 8') && isDateLabel('2023-10-26'));
  assert.ok(!isDateLabel('15:34'), 'the status-bar clock is not a date row');
  const els = [
    {
      class: 'android.widget.TextView',
      text: 'October 15 (Sun)',
      clickable: true,
      bounds: { l: 0, t: 0, r: 400, b: 60 },
    },
  ];
  const rows = buildTable(els, { w: 1080, h: 2400 });
  assert.ok(rows[0].row.includes('date'), `the row carries the flag: ${rows[0].row}`);
});

test('foreign status-bar chrome is dropped from the table', () => {
  const els = [
    {
      pkg: 'com.simplemobiletools.calendar.pro',
      class: 'android.widget.TextView',
      text: 'October 15 (Sun)',
      bounds: { l: 0, t: 300, r: 400, b: 360 },
    },
    {
      pkg: 'com.simplemobiletools.calendar.pro',
      class: 'android.widget.TextView',
      text: 'Search',
      clickable: true,
      bounds: { l: 0, t: 80, r: 200, b: 120 },
    },
    {
      pkg: 'com.android.systemui',
      class: 'android.widget.TextView',
      text: '15:34',
      clickable: true,
      bounds: { l: 700, t: 0, r: 800, b: 60 },
    },
    {
      pkg: 'com.android.systemui',
      class: 'android.widget.SeekBar',
      text: 'Display brightness',
      bounds: { l: 42, t: 149, r: 1038, b: 275 },
    },
  ];
  const rows = serializeRows(buildTable(els, { w: 1080, h: 2400 }));
  assert.ok(
    rows.some((r) => r.includes('October 15')),
    `the app's own rows stay: ${rows}`,
  );
  assert.ok(!rows.some((r) => r.includes('15:34')), `the clock is chrome: ${rows}`);
  assert.ok(
    rows.some((r) => r.includes('Display brightness')),
    `a foreign dialog below the status bar stays: ${rows}`,
  );
  assert.ok(
    rows.some((r) => r.includes('Search')),
    `an app row in the top band is never chrome: ${rows}`,
  );
});

test('an editable row is typed into, not clicked', () => {
  const table = [{ idx: 0, row: '[0] edit "Search"', el: { clickable: true, class: 'android.widget.EditText' } }];
  const keys = Object.keys(buildQuestions({ goal: 'g', table, texts: ['abc'] }).operation.criteria);
  assert.ok(keys.includes('type_0_0'), `the field can be typed into: ${keys}`);
  assert.ok(!keys.includes('click_0'), `a bare click on a field is the trap, not an option: ${keys}`);
  const withPaste = Object.keys(
    buildQuestions({ goal: 'paste it', table, texts: ['abc'], clipboardReady: true }).operation.criteria,
  );
  assert.ok(withPaste.includes('click_0'), `paste needs the field focusable by click: ${withPaste}`);
});

test('a split name/extension dialog is explained when the goal names a file', () => {
  const table = [{ idx: 0, row: '[0] edit "my_note"', el: { clickable: true, class: 'android.widget.EditText' } }];
  const withFile = buildQuestions({ goal: 'create a note named a.txt', table, texts: ['a.txt', 'a'] });
  assert.match(withFile.operation.instructions.rules.join('\n'), /extension field/);
  const noFile = buildQuestions({ goal: 'turn off wifi', table, texts: ['5'] });
  assert.ok(!/extension field/.test(noFile.operation.instructions.rules.join('\n')), 'and stays out of other goals');
});

// ---- vision fallback (screenshot → image asset → box answer) ----

test('run(binary) keeps stdout bytes untouched', async () => {
  const r = await run(process.execPath, ['-e', 'process.stdout.write(Buffer.from([0x89,0x50,0x00,0xff,0xfe]))'], {
    binary: true,
  });
  assert.ok(Buffer.isBuffer(r.stdout), 'stdout stays a Buffer');
  assert.deepEqual([...r.stdout], [0x89, 0x50, 0x00, 0xff, 0xfe]);
});

test('parseScreencap detects 16- and 12-byte headers and rejects bad payloads', () => {
  const px = Buffer.alloc(2 * 2 * 4, 0xaa);
  const h16 = Buffer.alloc(16);
  h16.writeUInt32LE(2, 0);
  h16.writeUInt32LE(2, 4);
  h16.writeUInt32LE(1, 8);
  h16.writeUInt32LE(1, 12);
  assert.equal(parseScreencap(Buffer.concat([h16, px])).offset, 16);
  const h12 = h16.subarray(0, 12);
  assert.equal(parseScreencap(Buffer.concat([h12, px])).offset, 12);
  assert.throws(() => parseScreencap(Buffer.concat([h16, px.subarray(0, 4)])), /payload/);
  const bad = Buffer.alloc(12);
  bad.writeUInt32LE(2, 0);
  bad.writeUInt32LE(2, 4);
  bad.writeUInt32LE(99, 8);
  assert.throws(() => parseScreencap(Buffer.concat([bad, px])), /header/);
});

test('scaleTarget fits the TypeSafe image limits from both directions', () => {
  const t = scaleTarget(1080, 2340);
  assert.ok(Math.max(t.w, t.h) <= 1568, `longest side ≤1568: ${t.w}x${t.h}`);
  assert.ok(t.w * t.h <= 2458624 && t.w * t.h >= 78400, `pixel count in range: ${t.w * t.h}`);
  assert.deepEqual(scaleTarget(240, 320), { w: 280, h: 373 }, 'a tiny screen is upscaled to the 280px floor');
  const same = scaleTarget(720, 1280);
  assert.deepEqual(same, { w: 720, h: 1280 }, 'an in-range screen passes through 1:1');
});

test('downscaleRgba averages the covered source rectangle', () => {
  const src = { w: 2, h: 2, format: 1, bpp: 4, offset: 0 };
  const buf = Buffer.from([255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 255, 255, 255, 255]);
  assert.deepEqual([...downscaleRgba(buf, src, { w: 1, h: 1 })], [128, 128, 128]);
});

test('encodePng emits a structurally valid minimal PNG', () => {
  const rgb = Uint8Array.from([255, 0, 0, 0, 255, 0, 0, 0, 255, 1, 2, 3]);
  const png = encodePng(rgb, 2, 2);
  assert.deepEqual([...png.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  // Reference CRC32 to check every chunk independently of the encoder's table.
  const crcOf = (buf) => {
    let c = 0xffffffff;
    for (const b of buf) {
      c ^= b;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunks = [];
  let off = 8;
  while (off < png.length) {
    const len = png.readUInt32BE(off);
    const type = png.subarray(off + 4, off + 8).toString('ascii');
    const data = png.subarray(off + 8, off + 8 + len);
    assert.equal(png.readUInt32BE(off + 8 + len), crcOf(png.subarray(off + 4, off + 8 + len)), `${type} CRC`);
    chunks.push([type, data]);
    off += 12 + len;
  }
  assert.deepEqual(
    chunks.map(([t]) => t),
    ['IHDR', 'IDAT', 'IEND'],
  );
  const [, ihdr] = chunks[0];
  assert.equal(ihdr.readUInt32BE(0), 2);
  assert.equal(ihdr.readUInt32BE(4), 2);
  assert.equal(ihdr[8], 8);
  assert.equal(ihdr[9], 2);
  const scan = inflateSync(chunks[1][1]);
  // (stride 6 + filter byte) × 2 rows = 14 bytes: filter-0 per scanline.
  assert.deepEqual([...scan], [0, 255, 0, 0, 0, 255, 0, 0, 0, 0, 255, 1, 2, 3]);
});

test('validatedBox accepts bounded rectangles, nullable confidence included', () => {
  const good = validatedBox(
    { type: 'box', box: { top: 0.25, bottom: 0.82, left: 0.44, right: 0.84 }, confidence: 0.92 },
    'tap_target',
  );
  assert.equal(good.confidence, 0.92);
  assert.equal(validatedBox({ box: { top: 0, bottom: 1, left: 0, right: 1 }, confidence: null }, 't').confidence, null);
  assert.throws(() => validatedBox({ box: { top: 0.5, bottom: 0.4, left: 0, right: 1 } }, 't'), /rectangle/);
  assert.throws(() => validatedBox({ box: { top: -0.1, bottom: 1, left: 0, right: 1 } }, 't'), /out of range/);
  assert.throws(() => validatedBox({ box: { top: 0, bottom: 2, left: 0, right: 1 } }, 't'), /out of range/);
  assert.throws(() => validatedBox({ confidence: 0.5 }, 't'), /missing box/);
  assert.throws(
    () => validatedBox({ box: { top: 0, bottom: 1, left: 0, right: 1 }, confidence: 1.5 }, 't'),
    /confidence/,
  );
  assert.throws(() => validatedBox('x', 't'), /missing answer/);
});

test('buildVisionQuestions offers the vision op set and embeds the image reference', () => {
  const ref = 'b04c0ed6-refs';
  const q = buildVisionQuestions(ref);
  assert.deepEqual(Object.keys(q.operation.criteria), [
    'tap_box',
    'scroll_down',
    'scroll_up',
    'back',
    'home',
    'wait',
    'done',
    'blocked',
  ]);
  assert.equal(q.screen_readable.type, 'noul');
  assert.equal(q.tap_target.type, 'box');
  assert.ok(q.tap_target.instructions.includes(`\`${ref}\``), 'box grounds on the asset reference');
  assert.ok(!JSON.stringify(q.operation).includes(ref), 'the choice text carries no second image copy');
});

test('visionVerdict gates on the noul and the box confidence', () => {
  const op = (choice) => ({ choice, confidence: 0.8 });
  assert.deepEqual(
    visionVerdict({ operation: op('tap_box'), screenReadable: { noul: 0.3 }, tapTarget: { box: {}, confidence: 0.9 } }),
    { kind: 'blocked', reason: 'blind_screen' },
    'an unreadable screen blocks regardless of the choice',
  );
  const box = { top: 0, bottom: 1, left: 0, right: 1 };
  const tap = visionVerdict({
    operation: op('tap_box'),
    screenReadable: { noul: 0.9 },
    tapTarget: { box, confidence: null },
  });
  assert.equal(tap.kind, 'tap');
  assert.deepEqual(tap.box, box, 'null confidence does not withhold the tap');
  assert.equal(
    visionVerdict({
      operation: op('tap_box'),
      screenReadable: { noul: 0.9 },
      tapTarget: { box, confidence: TAP_BOX_MIN_CONFIDENCE - 0.01 },
    }).kind,
    'skip_tap',
  );
  assert.deepEqual(
    visionVerdict({
      operation: op('scroll_down'),
      screenReadable: { noul: 0.9 },
      tapTarget: { box, confidence: null },
    }),
    { kind: 'op', choice: 'scroll_down', confidence: 0.8 },
  );
});

test('boxCenter maps fractions to an integer screen point', () => {
  assert.deepEqual(boxCenter({ left: 0.25, right: 0.75, top: 0.4, bottom: 0.6 }, { w: 1000, h: 2000 }), {
    x: 500,
    y: 1000,
  });
  const c = boxCenter({ left: 0.2, right: 0.3, top: 0.7, bottom: 0.71 }, { w: 1080, h: 2340 });
  assert.ok(Number.isInteger(c.x) && Number.isInteger(c.y));
});
