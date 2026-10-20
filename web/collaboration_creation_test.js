const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('collaboration.js', 'utf8');
const paints = [], requests = [], filters = {
  collabTaskResponsible: {value: 'all'}, collabTaskSearch: {value: 'query'},
  collabTaskFilter: {value: 'waiting'}, collabTaskFilters: {hidden: false},
  collabSearchToggle: {setAttribute() {}},
};
const context = {
  URLSearchParams, taskRefreshGeneration: 0, taskRows: [], taskLimit: 60,
  sessions: [], currentRun: '', active: true, scope: 'p',
  project: () => context.scope, $id: id => filters[id],
  api: path => path.startsWith('/api/runs?') ? new Promise(resolve => requests.push({path, resolve})) : Promise.resolve([]),
  assistant: {refreshShared: async () => {}}, paintSessions() {}, refreshSOPHandoffs: async () => {},
  paintTasks: () => paints.push(context.taskRows.map(r => r.id)),
  error: err => {throw err},
};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('  async function refresh(){'), source.indexOf('  function label(')), context);
vm.runInContext(source.slice(source.indexOf('  function toggleTaskSearch('), source.indexOf("  $id('collabSearchToggle').onclick=")), context);
vm.runInContext(source.split('\n').find(line => line.includes("$id('collabTaskResponsible').onchange=")), context);
async function flush() {for (let i = 0; i < 20; i++) await Promise.resolve()}
async function main() {
  const old = context.refresh();
  assert.equal(new URLSearchParams(requests[0].path.split('?')[1]).has('responsible'), false);
  filters.collabTaskResponsible.value = 'me';
  filters.collabTaskResponsible.onchange();
  assert.equal(context.taskLimit, 30);
  assert.equal(new URLSearchParams(requests[1].path.split('?')[1]).get('responsible'), 'me');
  requests[1].resolve([{id: 'mine', executionContext: {projectId: 'p'}}]); await flush();
  requests[0].resolve([{id: 'other', executionContext: {projectId: 'p'}}]); await old;
  assert.equal(context.taskRows.length, 1); assert.equal(context.taskRows[0].id, 'mine');
  assert(!paints.flat().includes('other'), 'late unfiltered response must never overwrite my tasks');
  assert.equal(filters.collabTaskSearch.value, 'query', 'owner filter keeps keyword');
  assert.equal(filters.collabTaskFilter.value, 'waiting', 'owner filter keeps status');
  context.toggleTaskSearch(false); await flush();
  assert.equal(filters.collabTaskResponsible.value, 'all');
  assert.equal(filters.collabTaskSearch.value, '');
  assert.equal(filters.collabTaskFilter.value, 'all');
  assert.equal(new URLSearchParams(requests[2].path.split('?')[1]).has('responsible'), false);
  requests[2].resolve([]); await flush();
  const previousProject = context.refresh();
  context.scope = 'other';
  requests[3].resolve([{id: 'stale-project', executionContext: {projectId: 'p'}}]); await previousProject;
  assert.equal(context.taskRows.length, 0, 'project change invalidates old response');
}
main().catch(err => {console.error(err); process.exit(1)});
