import test from 'node:test';
import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const run = promisify(execFile);
const cwd = fileURLToPath(new URL('../../web/', import.meta.url));

// Keep the chart/math regressions shared with the Go suite. Child processes
// inherit NODE_V8_COVERAGE; VM source filenames credit the actual web scripts.
for (const script of ['obs_charts_test.js', 'observability_insights_test.js']) {
  test(`statistics kernel regressions: ${script}`, async () => {
    await run(process.execPath, [script], { cwd, timeout: 15_000 });
  });
}
