const fs = require('fs');
const vm = require('vm');
const assert = require('assert/strict');
const source = fs.readFileSync(process.argv[2], 'utf8');
const issue = (id, extra = {}) => ({id, title: `需求 ${id}`, deletionAllowed: true, ...extra});
const ids = items => Array.from(items, i => typeof i === 'string' ? i : i.id);
const escapeHTML = value => String(value).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

function setup(items, options = {}) {
  const calls = [], notices = [], buttons = {};
  const host = {innerHTML:'', querySelector: s => buttons[s] ||= {}};
  const root = {inputs: [], pages: [], labels: [], querySelectorAll(s) {
    if(s === '[data-issue-select]') return this.inputs;
    if(s === '[data-issue-select-page]') return this.pages;
    return this.labels;
  }};
  const state = {data:{issues:items, tasks:items.map(i => ({id:`task-${i.id}`, issueId:i.id}))},
    issueSelectionMode:false, issueSelection:new Set(), issueBulkDeleting:false, issueView:'table'};
  const c = {state, Set, escapeHTML, $:s => s === '#issueBulkActions' ? host : s === '#startIssueSelection' ? (buttons[s] ||= {}) : root,
    issueCanDelete:i => i?.deletionAllowed === true && i.clarification?.status !== 'analyzing',
    issueDeletionBlock:i => i.deletionBlock || {reason:'暂时无法确认删除条件，请刷新后重试'},
    issueDeletionContextHTML:() => '', bindIssueDeletionContext:() => {},
    filteredIssues:() => state.data.issues.filter(i => !state.projectFilter || i.projectId === state.projectFilter),
    toast:(...args) => notices.push(args),
    afConfirm:async opts => {calls.push(['confirm',opts]);return options.confirm !== false;},
    api:async (path,opts) => {calls.push([path,opts.method]);if(options.fail?.(path)) throw new Error('<conflict>');},
    refreshRuns:async () => calls.push(['refresh']), closeIssueDrawer:() => calls.push(['close'])};
  vm.createContext(c);
  vm.runInContext(source,c);
  c.renderIssues = () => {c.syncIssueSelection(c.filteredIssues()); c.renderIssueBulkActions(c.filteredIssues());};
  c.renderIssues();
  return {c,state,calls,notices,host,root,buttons};
}

function select(f, ...selected) {if(!f.state.issueSelectionMode)f.c.setIssueSelectionMode(true);selected.forEach(id => f.state.issueSelection.add(id));}
const deletes = f => f.calls.filter(call => call[1] === 'DELETE');

(async () => {
  // Page selection excludes protected rows, preserves other pages, and has a mixed state.
  let f = setup([issue('a'),issue('blocked',{deletionAllowed:false}),issue('b')]);
  assert.equal(f.state.issueSelectionMode,false);
  assert.equal(f.host.hidden,true, 'batch controls are hidden during normal browsing');
  assert.equal(f.c.issueSelectionCheckbox(issue('a')),'');
  assert.equal(f.c.issueSelectAllCheckbox('全选'),'');
  await f.c.deleteSelectedIssues();
  assert.equal(f.calls.length,0, 'normal browsing cannot submit a batch');
  f.buttons['#startIssueSelection'].onclick();
  assert.equal(f.state.issueSelectionMode,true);
  assert.equal(f.host.hidden,false);
  assert.equal(f.buttons['#startIssueSelection'].hidden,true);
  select(f,'a');
  f.buttons['[data-issue-cancel-selection]'].onclick();
  assert.equal(f.state.issueSelectionMode,false);
  assert.equal(f.state.issueSelection.size,0);
  assert.equal(f.host.hidden,true);
  assert.equal(f.buttons['#startIssueSelection'].hidden,false);
  assert.equal(f.calls.length,0, 'exiting multi-select never deletes records');
  f.c.setIssueSelectionMode(true);
  f.root.pages = [{}];
  f.root.labels = [{}];
  f.root.inputs = f.state.data.issues.map(i => ({dataset:{issueSelect:i.id},closest:() => null}));
  f.c.bindIssueSelection(f.state.data.issues.slice(0,2));
  assert.equal(f.root.inputs[1].disabled,true);
  assert.equal(f.root.pages[0].disabled,false);
  let stopped = false;
  f.root.labels[0].onclick({stopPropagation:() => {stopped=true;}});
  assert.equal(stopped,true, 'selecting must not open the issue drawer');
  f.root.pages[0].checked = true;
  f.root.pages[0].onchange();
  assert.deepEqual(ids(f.state.issueSelection),['a']);
  f.c.bindIssueSelection([f.state.data.issues[2]]);
  f.root.pages[0].checked = true;
  f.root.pages[0].onchange();
  assert.deepEqual(ids(f.state.issueSelection),['a','b']);
  f.root.inputs[2].checked = false;
  f.root.inputs[2].onchange();
  f.c.bindIssueSelection(f.state.data.issues);
  assert.equal(f.root.pages[0].indeterminate,true);
  f.state.issueView = 'kanban';
  f.c.syncIssueSelection(f.state.data.issues);
  assert.deepEqual(ids(f.state.issueSelection),['a'], 'view changes preserve selections');
  f.state.issueQuery = 'new filter';
  f.c.syncIssueSelection(f.state.data.issues);
  assert.equal(f.state.issueSelection.size,0, 'filter changes clear hidden selections');
  select(f,'a','missing','blocked');
  f.state.data.issues[0].deletionAllowed = false;
  f.c.syncIssueSelection(f.state.data.issues);
  assert.equal(f.state.issueSelection.size,0, 'stale and newly protected rows cannot remain selected');

  // Cancellation preserves all records and selection.
  f = setup([issue('a')],{confirm:false});select(f,'a');
  await f.c.deleteSelectedIssues();
  assert.equal(deletes(f).length,0);
  assert.deepEqual(ids(f.state.data.issues),['a']);
  assert.deepEqual(ids(f.state.issueSelection),['a']);
  assert.equal(f.state.issueBulkDeleting,false);
  assert.equal(f.state.issueSelectionMode,true, 'cancelling confirmation preserves multi-select');

  // One confirmation deletes all selected items, encodes IDs, and refreshes history once.
  f = setup([issue('a/1',{source:'tapd'}),issue('b'),issue('keep')]);select(f,'a/1','b');
  f.state.issueDetailData={issue:f.state.data.issues[0]};f.state.selectedIssue='a/1';
  await f.c.deleteSelectedIssues();
  assert.deepEqual(deletes(f),[['/api/issues/a%2F1','DELETE'],['/api/issues/b','DELETE']]);
  assert.equal(f.calls.filter(c => c[0] === 'confirm').length,1);
  assert.match(f.calls[0][1].message,/后续同步可能重新导入/);
  assert.match(f.calls[0][1].confirmText,/2 条需求/);
  assert.equal(f.calls.filter(c => c[0] === 'refresh').length,1);
  assert.equal(f.calls.filter(c => c[0] === 'close').length,1);
  assert.equal(f.state.issueDetailData,null);
  assert.equal(f.state.selectedIssue,null);
  assert.deepEqual(ids(f.state.data.issues),['keep']);
  assert.deepEqual(ids(f.state.data.tasks),['task-keep']);
  assert.equal(f.state.issueSelection.size,0);
  assert.equal(f.state.issueBulkDeleting,false);
  assert.equal(f.state.issueBulkDeleteProgress,null);
  assert.equal(f.state.issueSelectionMode,false, 'a successful batch returns to normal browsing');
  assert.equal(f.host.hidden,true);

  // Server conflicts do not abort later items; failed selections can be retried.
  f = setup([issue('a',{title:'<unsafe>'}),issue('b'),issue('keep')],{fail:path => path.endsWith('/a')});
  select(f,'a','b');await f.c.deleteSelectedIssues();
  assert.equal(deletes(f).length,2);
  assert.deepEqual(ids(f.state.data.issues),['a','keep']);
  assert.deepEqual(ids(f.state.issueSelection),['a']);
  assert.equal(f.state.issueBulkDeleteResult.deleted,1);
  assert.equal(f.state.issueSelectionMode,true, 'partial failures stay available for retry');
  assert.equal(f.state.issueBulkDeleteResult.failures[0].id,'a');
  assert.match(f.host.innerHTML,/&lt;unsafe&gt;/);
  assert.match(f.host.innerHTML,/&lt;conflict&gt;/);
  assert.doesNotMatch(f.host.innerHTML,/<unsafe>|<conflict>/);
  f.c.api=async (path,opts) => f.calls.push([path,opts.method]);
  await f.c.deleteSelectedIssues();
  assert.deepEqual(ids(f.state.data.issues),['keep']);
  assert.equal(f.state.issueBulkDeleteResult.failures.length,0);

  f = setup([issue('a'),issue('b')],{fail:() => true});select(f,'a','b');
  await f.c.deleteSelectedIssues();
  assert.deepEqual(ids(f.state.data.issues),['a','b']);
  assert.deepEqual(ids(f.state.issueSelection),['a','b']);
  assert.equal(f.state.issueBulkDeleteResult.deleted,0);
  assert.equal(f.state.issueBulkDeleteResult.failures.length,2);
  assert.equal(f.calls.filter(c => c[0] === 'refresh').length,1, 'refresh permissions even when every item fails');

  // Lock submission during confirmation; a changed scope requires a fresh selection.
  f = setup([issue('a')]);select(f,'a');
  let resolveConfirm;
  f.c.afConfirm = () => new Promise(resolve => {resolveConfirm=resolve;});
  const pending = f.c.deleteSelectedIssues();
  assert.equal(f.state.issueBulkDeleting,true);
  f.c.setIssueSelectionMode(false);
  assert.equal(f.state.issueSelectionMode,true, 'multi-select cannot exit during a pending batch');
  assert.equal(f.buttons['[data-issue-bulk-delete]'].onclick,f.c.deleteSelectedIssues);
  await f.c.deleteSelectedIssues();
  assert.equal(deletes(f).length,0);
  f.state.projectFilter = 'other';resolveConfirm(true);await pending;
  assert.equal(deletes(f).length,0);
  assert.equal(f.state.issueBulkDeleting,false);
  assert.match(f.notices[0][0],/筛选条件已变化/);

  f = setup([issue('protected',{deletionAllowed:false})]);select(f,'protected');
  await f.c.deleteSelectedIssues();
  assert.equal(f.calls.length,0, 'protected items never reach confirmation or deletion');
  console.log('bulk delete interactions passed');
})().catch(err => {console.error(err);process.exit(1);});
