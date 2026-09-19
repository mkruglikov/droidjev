// Live-emulator end-to-end scenarios for the act loop. Opt-in: they spend
// real TypeSafe tokens and need a booted emulator (plus Chrome for the paste
// scenario). Run with: DROIDJEV_E2E=1 npm test
import test from 'node:test';
import assert from 'node:assert/strict';
import { runGoal } from '../src/agent.js';
import { ensureDevice } from '../src/boot.js';
import { shellOk } from '../src/adb.js';

const skip =
  process.env.DROIDJEV_E2E === '1' ? false : 'set DROIDJEV_E2E=1 (boots/uses an emulator, spends TypeSafe tokens)';

test("act copies the Device name in Settings and pastes it into Chrome's search bar", { skip }, async () => {
  const { serial } = await ensureDevice({});
  await shellOk(serial, ['input', 'keyevent', '3']); // start from home, like a user
  const res = await runGoal({
    serial,
    goal: 'Open Settings, open About phone, copy the Device name value to the clipboard, then open Chrome and paste it into the search bar',
    maxSteps: 16,
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

test('act fills a multi-field form: create contact Mary +79001234567', { skip }, async () => {
  const { serial } = await ensureDevice({});
  await shellOk(serial, ['input', 'keyevent', '3']);
  const res = await runGoal({
    serial,
    goal: 'Open Contacts, create a new contact with first name Mary and phone +79001234567, save it',
    texts: ['Mary', '+79001234567'],
    maxSteps: 16,
    log: (m) => console.error(`# ${m}`),
  });
  console.error(
    `status=${res.status} steps=${res.steps.length} ${(res.tookMs / 1000).toFixed(1)}s app=${res.finalApp ?? '?'}`,
  );
  const trail = () => res.steps.map((s) => s.action).join(' | ');
  assert.equal(res.status, 'done', `goal not done: ${trail()}`);
  // The reported regression: the phone value must land in the Phone field,
  // not be typed over the First name like the single --text era did.
  const nameStep = res.steps.filter((s) => s.action.startsWith('type "Mary"')).at(-1);
  const phoneStep = res.steps.filter((s) => s.action.startsWith('type "+79001234567"')).at(-1);
  assert.ok(nameStep && phoneStep, `both values must be typed: ${trail()}`);
  assert.ok(!/first name/i.test(phoneStep.action), `phone typed into the wrong field: ${phoneStep.action}`);
  assert.ok(phoneStep.step > nameStep.step, `phone must be typed after the name: ${trail()}`);
  assert.ok(
    res.finalRows.some((r) => r.includes('Mary')),
    `saved contact not visible: ${res.finalRows.join(' | ')}`,
  );
});
