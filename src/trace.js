// Episode tracing: every step's exact question inputs, the questions sent,
// the effective answers, and the executed record, one JSON line each under
// ~/.cache/droidjev/runs/. Opt-in on purpose — a trace contains the goal,
// screen text, typed values and answers. The file is created 0o600 and is
// never written inside the repo; it is a temporary diagnostic, not an
// artifact to commit.
import { appendFileSync, mkdirSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { redact } from './util.js';

const RUNS = join(homedir(), '.cache', 'droidjev', 'runs');

const slug = (goal) =>
  String(goal || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 40) || 'goal';

// Millisecond stamp plus a random suffix: two runs of the same goal in the
// same second (parallel benchmark workers) must not share a file.
const stamp = () =>
  new Date().toISOString().replace(/[:.]/g, '-').replace('T', '_').slice(0, 23) + '-' + randomBytes(2).toString('hex');

const pick = (obj, keys) => {
  const out = {};
  for (const k of keys) if (obj?.[k] !== undefined) out[k] = obj[k];
  return out;
};

export function openTrace({ enabled, goal, serial, dir = null }) {
  if (!enabled) return null;
  const file = join(dir ?? RUNS, `${stamp()}-${String(serial).replace(/[^\w.-]/g, '_')}-${slug(goal)}.jsonl`);
  let disabled = false;
  let noticed = false;
  const write = (obj) => {
    if (disabled) return;
    try {
      // redact() runs on the serialized line: the JSON stays valid (the
      // pattern stops at a quote), and no bearer material can reach disk.
      const line = redact(JSON.stringify(obj));
      mkdirSync(dir ?? RUNS, { recursive: true });
      appendFileSync(file, line + '\n', { mode: 0o600 });
    } catch (e) {
      // A full disk or an unwritable cache directory must never end an
      // episode that is otherwise succeeding: say so once, then go quiet.
      disabled = true;
      if (!noticed) {
        noticed = true;
        console.error(`# trace disabled (${e.message})`);
      }
    }
  };
  write({ v: 1, type: 'header', goal, serial, startedAt: new Date().toISOString() });
  return {
    file,
    recordStep: ({ step, questionInputs, questions, answers, criteria, record, hash, kind }) =>
      write(
        kind === 'vision'
          ? { type: 'step', step, kind: 'vision', record }
          : { type: 'step', step, questionInputs, questions, answers, criteria, record, hash },
      ),
    recordResult: (result) =>
      write({
        type: 'result',
        ...pick(result, ['status', 'blockedReason', 'tookMs', 'tokens']),
      }),
    close: () => {},
  };
}
