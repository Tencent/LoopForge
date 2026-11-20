const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const bundle = fs.readFileSync(process.argv[2], 'utf8');
function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name);
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]|\n\/\/ /.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}
const context = {};
vm.createContext(context);
vm.runInContext(source('agentGatesForSave'), context);
const saved = JSON.parse(JSON.stringify(context.agentGatesForSave([
  {id: 'mr-pushed', name: '改动已提交并推送', mode: 'command', commands: [' git status ', ''], onFailure: 'loop', executionScope: 'run'},
  {id: 'mr-opened', name: 'MR 已创建', mode: 'mr', onFailure: 'human', executionScope: 'repositories', commands: ['ignored']},
  {id: 'empty', name: '', mode: 'command', commands: [''], onFailure: 'loop'},
  {name: 'legacy', commands: ['test -f a.md'], onFailure: 'bogus', executionScope: 'repositories'},
])));
assert.deepEqual(saved, [
  {id: 'mr-pushed', name: '改动已提交并推送', stage: 'after_execution', required: true, onFailure: 'loop', mode: 'command', commands: ['git status'], executionScope: 'run'},
  {id: 'mr-opened', name: 'MR 已创建', stage: 'after_execution', required: true, onFailure: 'human', mode: 'mr'},
  {id: 'gate-4', name: 'legacy', stage: 'after_execution', required: true, onFailure: 'loop', mode: 'command', commands: ['test -f a.md'], executionScope: 'repositories'},
]);
assert.deepEqual(JSON.parse(JSON.stringify(context.agentGatesForSave(undefined))), []);
