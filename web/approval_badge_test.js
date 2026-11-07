const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const bundle = fs.readFileSync(process.argv[2], 'utf8');
function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name);
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]/.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}
function badgeContext(api) {
  const badge = {hidden: true, textContent: ''};
  const context = {badge, state: {currentUser: null, data: {runs: []}}, $: sel => sel === '#approvalNavBadge' ? badge : null, scopedRuns: r => r, api};
  vm.createContext(context);
  for (const name of ['approvalItems', 'updateApprovalBadge', 'preloadApprovalBadge']) vm.runInContext(source(name), context);
  return context;
}
const waitingRun = {id: 'r1', status: 'waiting', workflowId: 'wf', nodes: {n: {status: 'waiting_approval'}}};
async function main() {
  const calls = [];
  const pending = badgeContext(async (path, opts) => { calls.push([path, opts]); return [waitingRun, {id: 'r2', status: 'running'}]; });
  await pending.preloadApprovalBadge();
  assert.equal(JSON.stringify(calls), JSON.stringify([['/api/runs', {silent: true}]]));
  assert.equal(pending.state.data.runs.length, 2);
  assert.equal(pending.badge.hidden, false);
  assert.equal(pending.badge.textContent, 1);

  const none = badgeContext(async () => [{id: 'r3', status: 'succeeded'}, {id: 'r4', status: 'waiting', workflowId: 'scheduled-baseline-align'}, {id: 'chat', kind: 'workspace', status: 'waiting'}, {id: 'legacy', status: 'waiting', workflowSnapshot: {collaborationMode: 'group'}, nodes: {n: {status: 'waiting_approval'}}}]);
  none.badge.hidden = false; none.badge.textContent = 3;
  await none.preloadApprovalBadge();
  assert.equal(none.badge.hidden, true);
  assert.equal(none.badge.textContent, '');

  const failed = badgeContext(async () => { throw new Error('network down'); });
  failed.state.data.runs = [waitingRun];
  await failed.preloadApprovalBadge();
  assert.equal(failed.badge.hidden, true, 'failed preload leaves the badge untouched');
  assert.equal(failed.state.data.runs[0], waitingRun, 'failed preload keeps existing runs');

  const metrics = badgeContext(async path => path === '/api/runs' ? [waitingRun] : []);
  Object.assign(metrics, {projectQuery: () => '', renderSOPCatalog: () => {}, toast: m => { throw new Error(m); }});
  vm.runInContext(source('refreshWorkflowMetrics'), metrics);
  await metrics.refreshWorkflowMetrics();
  assert.equal(metrics.badge.hidden, false, 'SOP metrics refresh also refreshes the badge');
  assert.equal(metrics.badge.textContent, 1);
}
main().catch(err => { console.error(err); process.exit(1); });
