const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const flows = fs.readFileSync(path.join(__dirname, 'app/flows.js'), 'utf8');
function extract(code, name) {
  const start = code.indexOf(`function ${name}(`);
  assert.ok(start >= 0, name);
  return code.slice(start, code.indexOf('\n}', start) + 2);
}

const context = {};
vm.createContext(context);
vm.runInContext(extract(flows, 'applyGateField'), context);
const field = (gate, key, value) => context.applyGateField(gate, {dataset: {gk: key}, value});

// Clearing the threshold input drops the field (default 80) instead of
// writing 0, which would make the gate pass every change.
const gate = {mode: 'diff_coverage', coverage: {reportPath: 'coverage.out', thresholdPct: 85, minChangedLines: 3}};
field(gate, 'coverage.thresholdPct', '');
assert.equal('thresholdPct' in gate.coverage, false, 'empty threshold must fall back to the default');
field(gate, 'coverage.thresholdPct', '  ');
assert.equal('thresholdPct' in gate.coverage, false, 'blank threshold must fall back to the default');
field(gate, 'coverage.thresholdPct', '90');
assert.equal(gate.coverage.thresholdPct, 90);
field(gate, 'coverage.thresholdPct', '0');
assert.equal(gate.coverage.thresholdPct, 0, 'an explicit 0 is still allowed');
field(gate, 'coverage.thresholdPct', '150');
assert.equal(gate.coverage.thresholdPct, 100);
field(gate, 'coverage.minChangedLines', '');
assert.equal('minChangedLines' in gate.coverage, false);

// Saving a role from the UI keeps API-only coverage settings.
const agents = fs.readFileSync(path.join(__dirname, 'app/agents.js'), 'utf8');
const start = agents.indexOf('...(coverage?{coverage:{');
assert.ok(start >= 0, 'coverage mapping not found');
const mapping = agents.slice(start + '...(coverage?'.length, agents.indexOf('}}:{})', start) + 2);
const build = new Function('coverage', `return (${mapping});`);
const saved = build({reportPath: ' coverage.out ', reportFormat: 'go_coverprofile', include: ['internal/**'], exclude: ['internal/gen/**'], baseBranch: 'release/2.x', thresholdPct: '', minChangedLines: 'x'}).coverage;
assert.deepEqual(saved.include, ['internal/**']);
assert.deepEqual(saved.exclude, ['internal/gen/**']);
assert.equal(saved.baseBranch, 'release/2.x');
assert.equal(saved.reportPath, 'coverage.out');
assert.equal('thresholdPct' in saved, false, 'invalid threshold must not leak through the spread');
assert.equal('minChangedLines' in saved, false, 'invalid min lines must not leak through the spread');
assert.equal(build({reportPath: 'c', reportFormat: 'lcov', thresholdPct: 70, minChangedLines: 2}).coverage.thresholdPct, 70);
console.log('diff coverage gate ui ok');
