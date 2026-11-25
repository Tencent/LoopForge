const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const code = fs.readFileSync(require('node:path').join(__dirname, 'app/runners.js'), 'utf8');
function extract(name) {
  const start = code.indexOf(`function ${name}(`);
  assert.ok(start >= 0, name);
  return code.slice(start, code.indexOf('\n}', start) + 2);
}

const context = {state: {minRunnerVersion: '0.8.0'}};
vm.createContext(context);
vm.runInContext(extract('runnerVersionAtLeast') + extract('runnerDefaultBlocker'), context);
const blocker = r => context.runnerDefaultBlocker(r);

assert.equal(blocker({status: 'online', runnerVersion: '0.8.0'}), '');
assert.equal(blocker({status: 'online', runnerVersion: 'v0.9.2'}), '');
assert.equal(blocker({status: 'offline', runnerVersion: '0.9.2'}), '机器离线');
assert.match(blocker({status: 'online', runnerVersion: '0.1.0'}), /0\.1\.0 过低，需升级至 0\.8\.0/);
assert.match(blocker({status: 'online', runnerVersion: ''}), /未知 过低/);
assert.match(blocker({status: 'online', runnerVersion: 'dev'}), /过低/);
// Before GET /api/runners/default answers, only the offline rule applies.
context.state.minRunnerVersion = '';
assert.equal(blocker({status: 'online', runnerVersion: '0.1.0'}), '');

assert.ok(code.includes('${defaultBlocker?`disabled title='), 'set-default button must use runnerDefaultBlocker');
assert.ok(code.includes("Runner ${escapeHTML(r.runnerVersion||'未知')}"), 'card shows the runner version');
console.log('runner default rule ok');
