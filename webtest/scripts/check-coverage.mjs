import { readFile } from 'node:fs/promises';

const threshold = JSON.parse(
  await readFile(new URL('../coverage-threshold.json', import.meta.url), 'utf8'),
).lines;
const summary = JSON.parse(
  await readFile(new URL('../coverage/coverage-summary.json', import.meta.url), 'utf8'),
);
const actual = summary.total.lines.pct;

console.log(`Line coverage: ${actual}% (required: ${threshold}%)`);
if (actual < threshold) {
  console.error(`Line coverage ${actual}% is below the required ${threshold}%`);
  process.exitCode = 1;
}
