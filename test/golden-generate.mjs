// Generates the byte-exact prompt goldens from the Node implementation.
//
// The Go rewrite (docs/superpowers/plans/2026-11-13-go-rewrite.md) must
// reproduce every model-facing prompt byte-for-byte. These goldens are the
// parity gate, so they are captured by DRIVING the real functions in src/ —
// never hand-written. Run while the Node implementation still exists:
//
//   node test/golden-generate.mjs
//
// Inputs:  testdata/golden-fixtures.json
// Outputs: testdata/golden/<kind>-<name>.json (JSON.stringify(output, null, 2))
//
// The fixture's tableTruncated/tableGrid fields are cross-checked against what
// buildTable really derives from the elements, so a fixture can never silently
// drift from the table it describes.
import { readFileSync, writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildTable } from '../src/elements.js';
import { buildQuestions, buildVisionQuestions } from '../src/agent.js';
import { buildDecideRequest } from '../src/decide.js';
import { buildCheckQuestions } from '../src/check.js';

// `node --test` discovers every file under test/ and runs it in a child
// process, marking that child with NODE_TEST_CONTEXT. Regenerating testdata is
// not something `npm test` may do as a side effect, so the runner gets a no-op.
if (process.env.NODE_TEST_CONTEXT) process.exit(0);

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..');
const fixturesPath = join(root, 'testdata', 'golden-fixtures.json');
const outDir = join(root, 'testdata', 'golden');

const fixtures = JSON.parse(readFileSync(fixturesPath, 'utf8'));
if (!Array.isArray(fixtures) || fixtures.length === 0) {
  throw new Error(`${fixturesPath} must hold a non-empty array of cases`);
}

const questionsOutput = (input) => {
  const { elements, screen, tableTruncated, tableGrid, ...rest } = input;
  const table = buildTable(elements, screen);
  const truncated = !!table.truncated;
  if (truncated !== !!tableTruncated) {
    throw new Error(`fixture tableTruncated=${tableTruncated} but buildTable derived ${truncated}`);
  }
  const grid = table.grid ? { size: table.grid.size, count: table.grid.cells.length } : null;
  if (JSON.stringify(grid) !== JSON.stringify(tableGrid)) {
    throw new Error(`fixture tableGrid=${JSON.stringify(tableGrid)} but buildTable derived ${JSON.stringify(grid)}`);
  }
  return buildQuestions({ table, screen, ...rest });
};

const decideOutput = (request) => {
  const { state, questions, sources, centers } = buildDecideRequest(request);
  return { state, questions, sources, centers };
};

const checkOutput = ({ ref, goal, values }) => buildCheckQuestions(ref, { goal, values });
const visionOutput = ({ ref, hybrid, guidance }) => buildVisionQuestions(ref, { hybrid, guidance });

const outputs = {
  questions: questionsOutput,
  decide: decideOutput,
  check: checkOutput,
  vision: visionOutput,
};

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const written = [];
const seen = new Set();
for (const fixture of fixtures) {
  const { kind, name } = fixture;
  const make = outputs[kind];
  if (!make) throw new Error(`unknown fixture kind "${kind}" (${name})`);
  const key = `${kind}-${name}`;
  if (seen.has(key)) throw new Error(`duplicate fixture ${key}`);
  seen.add(key);
  const output = make(fixture.input ?? fixture.request ?? fixture);
  const file = join(outDir, `${key}.json`);
  writeFileSync(file, JSON.stringify(output, null, 2));
  written.push(`${key}.json`);
}

console.log(`wrote ${written.length} goldens to testdata/golden/`);
for (const f of written) console.log(`  ${f}`);
