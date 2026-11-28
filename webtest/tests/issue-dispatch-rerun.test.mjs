import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// 抽屉里的「启动 LOOP」曾按「这条需求有没有跑过任何 LOOP」决定 rerun，
// 但 rerun 是按 LOOP 粒度判定的：需求跑过 LOOP A（例如失败）后再选一条从未跑过的
// LOOP B，前端仍发 rerun:true，服务端回 400「该需求尚未运行这条 LOOP」，
// 用户在界面上再也启动不了这条 LOOP。这里固定按目标 LOOP 判定。
test('dispatch rerun is decided per LOOP, not by any earlier task', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(() => {
    const issue={id:'i-1',projectId:'project-1',status:'blocked',workflowId:'flow-b'};
    state.data.issues=[issue];
    state.data.workflows=[{id:'flow-a',name:'A',projectId:'project-1'},{id:'flow-b',name:'B',projectId:'project-1'}];
    state.data.tasks=[
      {id:'t-a',issueId:'i-1',workflowId:'flow-a',status:'failed',createdAt:'2026-01-01T00:00:00Z'},
      {id:'t-b',issueId:'i-1',workflowId:'flow-b',status:'succeeded',createdAt:'2026-01-02T00:00:00Z'},
      {id:'t-other',issueId:'i-2',workflowId:'flow-b',status:'running',createdAt:'2026-01-03T00:00:00Z'},
    ];
    return {
      other: issueTaskForWorkflow(issue,'flow-a')?.id || '',
      same: issueTaskForWorkflow(issue,'flow-b')?.id || '',
      none: issueTaskForWorkflow(issue,'flow-c')?.id || '',
      blank: issueTaskForWorkflow({...issue,workflowId:''},'')?.id || '',
      missingIssue: issueTaskForWorkflow({id:'nope'},'flow-a')?.id || '',
    };
  })()`);
  assert.equal(result.other, 't-a');
  assert.equal(result.same, 't-b');
  assert.equal(result.none, '', 'a LOOP that never ran must not report an earlier task');
  assert.equal(result.blank, 't-b', 'without a bound LOOP keep the latest task fallback');
  assert.equal(result.missingIssue, '');
});

test('drawer dispatch sends rerun=false when only another LOOP ran', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/issues', (call) => ({ ...call.body, id: call.body.id || 'issue-1' }));
  app.evaluate(`(() => {
    const issue={...state.data.issues[0],id:'issue-1',status:'blocked',workflowId:'flow-1'};
    state.data.issues=[issue];
    state.data.workflows=[
      {id:'flow-1',name:'Bound',projectId:'project-1',nodes:[]},
      {id:'flow-2',name:'Other',projectId:'project-1',nodes:[]},
    ];
    state.data.tasks=[{id:'t-old',issueId:'issue-1',workflowId:'flow-2',status:'failed',createdAt:'2026-01-01T00:00:00Z'}];
    state.confirmCalls=0;
    afConfirm=async()=>{state.confirmCalls++;return true};
    openIssue(issue);
  })()`);
  await settle();
  const pending = app.evaluate(`dispatchIssue()`);
  await settle(8);
  await pending;
  const bodies = app.router.calls.filter((c) => c.method === 'POST' && c.path === '/api/issues/issue-1/dispatch');
  assert.equal(bodies.length, 1);
  assert.equal(bodies[0].body.rerun, false, 'another LOOP task must not force a rerun');
  assert.equal(app.evaluate('state.confirmCalls'), 0, 'no rerun confirmation for a first run of this LOOP');
});

test('drawer dispatch confirms and sends rerun=true when this very LOOP already ran', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/issues', (call) => ({ ...call.body, id: call.body.id || 'issue-1' }));
  app.evaluate(`(() => {
    const issue={...state.data.issues[0],id:'issue-1',status:'blocked',workflowId:'flow-1'};
    state.data.issues=[issue];
    state.data.workflows=[
      {id:'flow-1',name:'Bound',projectId:'project-1',nodes:[]},
      {id:'flow-2',name:'Other',projectId:'project-1',nodes:[]},
    ];
    state.data.tasks=[
      {id:'t-own',issueId:'issue-1',workflowId:'flow-1',status:'failed',createdAt:'2026-01-01T00:00:00Z'},
      {id:'t-newer',issueId:'issue-1',workflowId:'flow-2',status:'succeeded',createdAt:'2026-01-03T00:00:00Z'},
    ];
    state.confirmCalls=0;
    afConfirm=async()=>{state.confirmCalls++;return true};
    openIssue(issue);
  })()`);
  await settle();
  const pending = app.evaluate(`dispatchIssue()`);
  await settle(8);
  await pending;
  const bodies = app.router.calls.filter((c) => c.method === 'POST' && c.path === '/api/issues/issue-1/dispatch');
  assert.equal(bodies.length, 1);
  assert.equal(bodies[0].body.rerun, true);
  assert.equal(app.evaluate('state.confirmCalls'), 1);
});
