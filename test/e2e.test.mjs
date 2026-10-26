// Live-emulator end-to-end scenarios for the act loop. Opt-in: they spend
// real TypeSafe tokens and need a booted emulator (plus Chrome for the paste
// scenario). Run with: DROIDJEV_E2E=1 npm test
import test from 'node:test';
import assert from 'node:assert/strict';
import { runGoal } from '../src/agent.js';
import { ensureDevice } from '../src/boot.js';
import { shell, shellOk } from '../src/adb.js';

const skip =
  process.env.DROIDJEV_E2E === '1' ? false : 'set DROIDJEV_E2E=1 (boots/uses an emulator, spends TypeSafe tokens)';

test('act copies the IMEI from Settings and pastes it into the Chrome search bar', { skip }, async () => {
  const { serial } = await ensureDevice({});
  // A fresh tab keeps the destination field empty, so the paste is exercised
  // even when a previous run left the IMEI in the address bar.
  await shellOk(serial, [
    'am',
    'start',
    '-a',
    'android.intent.action.VIEW',
    '-d',
    'about:blank',
    '-p',
    'com.android.chrome',
  ]);
  await shellOk(serial, ['input', 'keyevent', '3']); // start from home, like a user
  const res = await runGoal({
    serial,
    goal: 'copy the IMEI from settings and paste it into chrome search bar',
    log: (m) => console.error(`# ${m}`),
  });
  console.error(
    `status=${res.status} steps=${res.steps.length} ${(res.tookMs / 1000).toFixed(1)}s app=${res.finalApp ?? '?'}`,
  );
  const trail = () => res.steps.map((s) => s.action).join(' | ');
  const copy = res.steps.find((s) => /^copy "/.test(s.action));
  assert.ok(copy, `no copy step ran: ${trail()}`);
  assert.ok(
    res.steps.some((s) => s.action.startsWith('paste')),
    `no paste step ran: ${trail()}`,
  );
  assert.equal(res.status, 'done', `goal not done (last: ${res.steps.at(-1)?.action})`);
  assert.equal(res.finalApp, 'com.android.chrome', `ended in ${res.finalApp}`);
  const copied = JSON.parse(/^copy (".*") from /.exec(copy.action)[1]);
  assert.ok(
    res.finalRows.some((r) => r.includes(copied)),
    `pasted ${JSON.stringify(copied)} not visible in final rows: ${res.finalRows.join(' | ')}`,
  );
});

test('act fills a multi-field form: create contact Maria +79001234567', { skip }, async () => {
  const { serial } = await ensureDevice({});
  await shellOk(serial, ['input', 'keyevent', '3']);
  const res = await runGoal({
    serial,
    goal: 'open the Contacts app and create a new contact named Maria, phone +79001234567',
    texts: ['Maria', '+79001234567'],
    log: (m) => console.error(`# ${m}`),
  });
  console.error(
    `status=${res.status} steps=${res.steps.length} ${(res.tookMs / 1000).toFixed(1)}s app=${res.finalApp ?? '?'}`,
  );
  const trail = () => res.steps.map((s) => s.action).join(' | ');
  assert.equal(res.status, 'done', `goal not done: ${trail()}`);
  // The reported regression: the phone value must land in the Phone field,
  // not be typed over the First name like the single --text era did.
  const nameStep = res.steps.filter((s) => s.action.startsWith('type "Maria"')).at(-1);
  const phoneStep = res.steps.filter((s) => s.action.startsWith('type "+79001234567"')).at(-1);
  // A contact that already exists with the right data is a valid done: the
  // provider check below is the acceptance gate. When the run did type, the
  // values must have gone into different fields, phone after name.
  if (nameStep || phoneStep) {
    assert.ok(nameStep && phoneStep, `a provided value was typed without the other: ${trail()}`);
    assert.ok(!/first name/i.test(phoneStep.action), `phone typed into the wrong field: ${phoneStep.action}`);
    assert.ok(phoneStep.step > nameStep.step, `phone must be typed after the name: ${trail()}`);
  }
  assert.ok(
    res.finalRows.some((r) => r.includes('Maria')),
    `saved contact not visible: ${res.finalRows.join(' | ')}`,
  );
  // The screen can show a value that landed in the wrong field: check the
  // contact provider itself, so a phone typed over the name fails here.
  const data = await shell(serial, [
    'content',
    'query',
    '--uri',
    'content://com.android.contacts/data',
    '--projection',
    'display_name:mimetype:data1',
  ]);
  const rows = data.stdout.split('\n');
  const digits = (r) => (r.match(/data1=(.*)$/)?.[1] ?? '').replace(/\D/g, '');
  assert.ok(
    rows.some((r) => /display_name=Maria\b/.test(r) && /mimetype=vnd\.android\.cursor\.item\/name\b/.test(r)),
    `Maria was not saved as a contact name: ${data.stdout}`,
  );
  assert.ok(
    rows.some((r) => /mimetype=vnd\.android\.cursor\.item\/phone_v2\b/.test(r) && digits(r).includes('79001234567')),
    `the phone was not saved as a phone number: ${data.stdout}`,
  );
});
