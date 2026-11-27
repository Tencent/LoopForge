// ⌘K says "换一个标题、编号或页面名称试试": requirements must be found by the
// key the list shows (LED-2, #12 for Gongfeng, the TAPD ID), not only by title.
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert/strict');
function localBundle() {
  const page = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const modules = [...page.matchAll(/<script src="\/(app\.js|app\/[^"?]+\.js)(?:\?[^"]*)?"><\/script>/g)];
  return modules.map(([, file]) => fs.readFileSync(path.join(__dirname, file), 'utf8')).join('\n');
}
const bundle = process.argv[2] ? fs.readFileSync(process.argv[2], 'utf8') : localBundle();
function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name);
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]|\n\/\/ /.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}
const elements = { '#commandSearch': { value: '' }, '#commandResults': { innerHTML: '' } };
const context = vm.createContext({
  state: { data: {
    projects: [{ id: 'ledger', key: 'LED' }],
    issues: [
      { id: 'a', projectId: 'ledger', number: 1, title: 'split_evenly 平分金额时丢分', priority: 'medium', status: 'done' },
      { id: 'b', projectId: 'ledger', number: 2, title: '按分类汇总支出', priority: 'high', status: 'in_progress' },
      { id: 'c', projectId: 'ledger', number: 3, title: '工蜂导入', source: 'gongfeng', externalRef: { externalIid: 12 }, status: 'backlog' },
    ],
    agents: [],
  } },
  $: sel => elements[sel], escapeHTML: s => String(s), sopRuns: () => [], catalogWorkflows: () => [],
  openIssueDetail: () => {}, openRunFromId: () => {}, switchView: () => {}, openSOPWizard: () => {},
});
vm.runInContext('let commandMatches=[],commandSelectedIndex=0;', context);
const helpers = ['commandCatalog', 'renderCommandResults', 'issueKeyLabel', 'issuePriorityLabel', 'issueStatusLabel'];
// issueKeyLabel may delegate to the shared key rule issueKeyFor (#1517); load it when the bundle has it.
if (/^function issueKeyFor\(/m.test(bundle)) helpers.push('issueKeyFor');
for (const name of helpers) vm.runInContext(source(name), context);
function search(query) {
  elements['#commandSearch'].value = query;
  context.renderCommandResults();
  return [...vm.runInContext('commandMatches', context)].filter(item => item.kind === '需求').map(item => `${item.title} | ${item.meta}`);
}
assert.deepEqual(search('LED-2'), ['按分类汇总支出 | LED-2 · 高 · 进行中']);
assert.deepEqual(search('led-1'), ['split_evenly 平分金额时丢分 | LED-1 · 中 · 已完成']);
assert.deepEqual(search('#12'), ['工蜂导入 | #12 · — · 待规划']);
// Titles still match, and the raw priority enum no longer leaks into the row.
assert.deepEqual(search('平分'), ['split_evenly 平分金额时丢分 | LED-1 · 中 · 已完成']);
assert.ok(!search('').some(row => /medium|high/.test(row)));
