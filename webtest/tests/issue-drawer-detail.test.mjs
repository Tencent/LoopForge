import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('issue drawer loads every tab and preserves clarification answers', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const issue = {
    ...app.evaluate('structuredClone(state.data.issues[0])'),
    source: 'tapd', externalId: 'TAPD-42',
    externalRef: { externalUrl: 'https://tapd.example/42' },
    clarification: {
      status: 'questions', summary: 'Needs browser details',
      dimensions: { goal: 80, scope: 60, acceptance: 40 },
      questions: [
        { question: 'Target browser?', options: ['Chrome', 'Firefox'], multiSelect: false },
        { question: 'Required checks?', options: ['Unit', 'E2E'], multiSelect: true },
      ],
      history: [{ role: 'assistant', content: 'Please clarify.' }],
    },
    tasks: [{ id: 'task-1', runId: 'run-1', workflowId: 'flow-1', status: 'waiting' }],
    operations: [{ action: 'updated', userName: 'Tester', createdAt: '2026-10-21T10:00:00Z' }],
  };
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'waiting',
    workflowSnapshot: app.evaluate('structuredClone(state.data.workflows[0])'),
    nodes: { 'node-1': { status: 'succeeded', output: 'Implemented' }, 'node-2': { status: 'waiting_approval' } },
    artifacts: [{ id: 'artifact-1', name: 'Report', path: 'report.md' }],
  };
  app.router.get('/api/issues/issue-1', {
    issue, comments: [{ id: 'comment-1', authorName: 'Tester', content: 'Context', createdAt: '2026-10-21T10:00:00Z' }],
  });
  app.router.get('/api/issues/issue-1/requirement', {
    title: issue.title, description: issue.description, acceptanceCriteria: ['Charts render'],
  });
  app.router.get('/api/runs/run-1', run);
  app.router.get(/^\/api\/assistant\/sessions\?/, [{
    id: 'session-issue', projectId: 'project-1', runId: 'run-1', title: 'Issue discussion',
    messages: [], updatedAt: '2026-10-21T10:00:00Z',
  }]);
  await app.evaluate(`openIssueDetail('issue-1','clarify')`);
  await settle(5);
  const body = app.document.querySelector('#issueDetailBody');
  const first = body.querySelector('.issue-clarify-option');
  first?.click();
  const other = body.querySelector('.issue-clarify-other');
  if (other) other.value = 'Safari';
  app.evaluate(`window.__drawerDraft=captureIssueClarifyDraft(document.querySelector('#issueDetailBody'))`);
  for (const tab of ['overview', 'clarify', 'sop', 'artifacts', 'delivery', 'acceptance', 'activity', 'operations', 'conversations']) {
    app.evaluate(`state.issueDetailTab=${JSON.stringify(tab)};renderIssueDetailTab()`);
    await settle(3);
  }
  app.evaluate(`state.issueDetailTab='clarify';renderIssueDetailTab();restoreIssueClarifyDraft(document.querySelector('#issueDetailBody'),window.__drawerDraft)`);
  assert.match(app.document.querySelector('#issueDetailDrawer').textContent, /58\/100|Improve dashboard/);
});

test('issue clarification helpers handle thinking, protocol recovery, and action controls', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    const issue={...state.data.issues[0],clarification:{status:'analyzing',questions:[],history:[]}};
    state.issueDetailData={issue,comments:[],run:null,task:null,requirement:null};
    state.issueDetailTab='clarify';state.drawerOpen=true;renderIssueDetailTab();
  `);
  app.evaluate(`
    issueClarifyThinking({kind:'thinking',text:'Inspecting requirement'},'issue-1');
    issueClarifyThinking({kind:'tool',tool:'search',text:'Searching'},'issue-1');
    issueClarifyThinking({kind:'answer',text:'Draft answer'},'issue-1');
    setIssueClarifyLastError('issue-1','Temporary failure');
  `);
  const recovered = app.evaluate(`
    [
      recoverClarifyView('prefix {"summary":"Clear","questions":[],"score":88} suffix'),
      clarifyExtractProtocol('text {"result":{"summary":"Done"}}'),
      clarifyVisibleSummary('Visible summary'),
      clarifyThoughtShouldHide('submit_clarification_result payload')
    ]
  `);
  assert.ok(recovered.length === 4);
  app.evaluate(`finishIssueClarifyJob('issue-1',true);closeIssueDrawer()`);
});
