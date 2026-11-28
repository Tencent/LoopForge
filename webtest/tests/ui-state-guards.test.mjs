import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, settle } from '../lib/load-app.mjs';

// Regressions for UI state bugs found in the full sweep: LOOP designer edits
// dropped on open, a stale run-list LOOP filter, Escape wiping the composer,
// a stale issue drawer response, and AI clarification text being translated.

const loops = `[
  {id:'loop-a',projectId:'project-1',name:'LOOP A',nodes:[{id:'n1',type:'agent',name:'A1',position:{x:0,y:0}}],edges:[]},
  {id:'loop-b',projectId:'project-1',name:'LOOP B',nodes:[{id:'n2',type:'agent',name:'B1',position:{x:0,y:0}}],edges:[]}
]`;

async function withLoops(t) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.workflows=${loops};
    state.workflow=clone(state.data.workflows[0]);
    state.workflowSaved=clone(state.workflow);
    state.workflow.name='LOOP A (edited)';
    state.workflowDirty=true;
    window.__confirms=[];window.__answer=false;
    afConfirm=(o)=>{window.__confirms.push(o);return Promise.resolve(window.__answer)};
  `);
  return app;
}

test('opening another LOOP from the catalog asks before dropping unsaved edits', async (t) => {
  const app = await withLoops(t);
  await app.evaluate(`openSOPDesigner('loop-b')`);
  assert.equal(app.evaluate('window.__confirms.length'), 1, 'must confirm before discarding edits');
  assert.equal(app.evaluate('state.workflow.id'), 'loop-a', 'cancel keeps the edited LOOP open');
  assert.equal(app.evaluate('state.workflow.name'), 'LOOP A (edited)');

  app.evaluate('window.__answer=true');
  await app.evaluate(`openSOPDesigner('loop-b')`);
  assert.equal(app.evaluate('state.workflow.id'), 'loop-b');
  assert.equal(app.evaluate('state.workflowDirty'), false, 'the newly opened LOOP is clean');
  assert.equal(app.evaluate('state.workflowSaved.id'), 'loop-b', 'discard must restore the LOOP on screen, not the previous one');
});

test('command palette LOOP entries use the same guarded opener', async (t) => {
  const app = await withLoops(t);
  const action = app.evaluate(`commandCatalog().find(x=>x.kind==='LOOP'&&x.title==='LOOP B').action`);
  await action();
  assert.equal(app.evaluate('window.__confirms.length'), 1);
  assert.equal(app.evaluate('state.workflow.id'), 'loop-a');
});

test('reopening the LOOP already on screen keeps its unsaved edits', async (t) => {
  const app = await withLoops(t);
  await app.evaluate(`openSOPDesigner('loop-a')`);
  assert.equal(app.evaluate('window.__confirms.length'), 0);
  assert.equal(app.evaluate('state.workflow.name'), 'LOOP A (edited)');
  assert.equal(app.evaluate('state.workflowDirty'), true);
});

test('run list drops a LOOP filter the dropdown can no longer show', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.runWorkflowFilter='deleted-loop';renderRuns()`);
  assert.equal(app.evaluate('state.runWorkflowFilter'), '');
  app.evaluate(`state.runWorkflowFilter='flow-1';renderRuns()`);
  assert.equal(app.evaluate('state.runWorkflowFilter'), 'flow-1', 'a LOOP still in the list stays selected');
});

test('Escape in the requirement composer confirms before discarding a typed description', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`window.__confirms=[];afConfirm=(o)=>{window.__confirms.push(o);return Promise.resolve(false)};openRequirementWizard()`);
  await settle();
  app.evaluate(`document.getElementById('chatInput').value='导出订单报表'`);
  app.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  await settle();
  assert.equal(app.evaluate('window.__confirms.length'), 1);
  assert.equal(app.evaluate(`document.getElementById('requirementModal').classList.contains('open')`), true);
  assert.equal(app.evaluate(`document.getElementById('chatInput').value`), '导出订单报表');
});

test('the issue drawer ignores a slower response for an issue the user already left', async (t) => {
  let releaseFirst;
  const firstGate = new Promise((resolve) => { releaseFirst = resolve; });
  const app = await loadApp({ routes: [
    { method: 'GET', path: '/api/issues/slow-issue', body: async () => { await firstGate; return { issue: { id: 'slow-issue', projectId: 'project-1', title: 'Slow' }, comments: [] }; } },
    { method: 'GET', path: '/api/issues/fast-issue', body: { issue: { id: 'fast-issue', projectId: 'project-1', title: 'Fast' }, comments: [] } },
  ] });
  t.after(app.close);
  const first = app.evaluate(`openIssueDetail('slow-issue')`);
  await settle();
  await app.evaluate(`openIssueDetail('fast-issue')`);
  releaseFirst();
  await first;
  await settle();
  assert.equal(app.evaluate('state.selectedIssue'), 'fast-issue');
  assert.equal(app.evaluate('state.issueDetailData?.issue?.id'), 'fast-issue');
});

test('clarification questions and options are rendered as untranslatable data', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const html = app.evaluate(`clarifyQuestionCardsHTML([{question:'是否需要 测试 覆盖？',options:['前端','后端']}],null)`);
  const host = app.document.createElement('div');
  host.innerHTML = html;
  assert.ok(host.querySelector('b').hasAttribute('data-i18n-skip'));
  for (const option of host.querySelectorAll('.issue-clarify-option')) assert.ok(option.hasAttribute('data-i18n-skip'));
});

test('project switcher options and project card names are marked as user data', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate('renderProjectSwitcher()');
  const option = app.document.querySelector('#projectSwitcher option[value="project-1"]');
  assert.ok(option.hasAttribute('data-i18n-data'));
});

test('switching projects preserves rejected edits and opens the target after confirmation', async (t) => {
  const app = await withLoops(t);
  app.evaluate(`state.projectFilter='project-1';
    state.data.projects.push({id:'project-2',name:'Beta',key:'BETA'});
    state.data.workflows.push({id:'beta-flow',name:'Beta flow',projectId:'project-2',nodes:[],edges:[]});
    renderProjectSwitcher();`);
  const picker = app.document.querySelector('#projectSwitcher');
  chooseOption(picker, 'project-2');
  await settle();
  assert.equal(picker.value, 'project-1');
  assert.equal(app.evaluate('state.workflow.name'), 'LOOP A (edited)');
  assert.equal(app.evaluate('state.workflowDirty'), true);
  assert.equal(app.evaluate('window.__confirms.length'), 1);

  app.evaluate('window.__answer=true');
  chooseOption(picker, 'project-2');
  await settle();
  assert.equal(app.evaluate('state.projectFilter'), 'project-2');
  assert.equal(app.evaluate('state.workflow.id'), 'beta-flow');
  assert.equal(app.evaluate('state.workflowDirty'), false);
});

test('switching projects keeps an edited shared LOOP visible in the target project', async (t) => {
  const app = await withLoops(t);
  app.evaluate(`state.projectFilter='project-1';state.workflow.shared=true;
    state.data.projects.push({id:'project-2',name:'Beta',key:'BETA'});renderProjectSwitcher();`);
  assert.equal(await app.evaluate("selectProject('project-2')"), true);
  assert.equal(app.evaluate('state.workflow.name'), 'LOOP A (edited)');
  assert.equal(app.evaluate('state.workflowDirty'), true);
  assert.equal(app.evaluate('window.__confirms.length'), 0);
});
