import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const reason = '已有成功执行记录，当前不支持删除';
const blockedIssue = {
  id: 'delete-feedback', projectId: 'project-1', title: '国际站套餐支持升级',
  status: 'closed', closeSource: 'manual', source: 'local', priority: 'medium',
  deletionAllowed: false, closureAllowed: true,
  deletionBlock: { code: 'succeeded', reason, runId: 'old-success' },
};

function showMenu(app, issue) {
  app.evaluate(`{
    state.data.issues=[${JSON.stringify(issue)}];
    state.data.tasks=[];state.data.runs=[];
    const trigger=document.createElement('button');
    document.body.appendChild(trigger);
    showIssueActionMenu({preventDefault(){},stopPropagation(){},currentTarget:trigger},'${issue.id}');
  }`);
  return app.document.querySelector('#issueActionPopover');
}

test('closed issue keeps a disabled delete entry, visible reason, and the exact historical run link', async t => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`openRunFromId=(id,opts)=>window.deletionDestination={id,opts}`);
  app.evaluate(`state.issueDetailData={issue:${JSON.stringify({ ...blockedIssue, deletionBlock: { code:'active', reason:'旧抽屉数据', runId:'stale-run' } })}}`);
  const menu = showMenu(app, blockedIssue);
  const deletion = menu.querySelector('[data-action="delete"]');
  assert.equal(deletion.disabled, true);
  assert.equal(app.document.getElementById(deletion.getAttribute('aria-describedby')).textContent, reason);
  assert.match(menu.textContent, /重新打开/);
  assert.match(menu.textContent, /查看相关运行/);
  assert.equal(app.document.activeElement, menu.querySelector('[data-action="edit"]'));
  app.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'End', bubbles: true }));
  assert.equal(app.document.activeElement, menu.querySelector('[data-issue-deletion-context]'));
  menu.querySelector('[data-issue-deletion-context]').click();
  assert.deepEqual(structuredClone(app.evaluate('window.deletionDestination')), {
    id: 'old-success', opts: { forceRuns: true },
  });
  assert.equal(menu.hidden, true);
  assert.equal(app.router.calls.some(c => c.method === 'DELETE'), false);
});

test('allowed, live clarification, completed, and unknown deletion states have honest feedback', async t => {
  const app = await loadApp();
  t.after(app.close);
  let menu = showMenu(app, { ...blockedIssue, deletionAllowed: true, deletionBlock: undefined });
  assert.equal(menu.querySelector('[data-action="delete"]').disabled, false);
  assert.equal(menu.querySelector('.issue-delete-reason'), null);
  menu = showMenu(app, { ...blockedIssue, deletionAllowed: true, clarification: { status: 'analyzing' } });
  assert.equal(menu.querySelector('[data-action="delete"]').disabled, true);
  assert.match(menu.querySelector('.issue-delete-reason').textContent, /正在澄清/);
  assert.match(menu.querySelector('[data-issue-deletion-context]').textContent, /查看澄清/);
  menu = showMenu(app, { ...blockedIssue, status: 'done', deletionBlock: {code: 'completed', reason: '需求已完成，当前不支持删除'} });
  assert.match(menu.textContent, /需求已完成/);
  menu = showMenu(app, { ...blockedIssue, deletionAllowed: undefined, deletionBlock: undefined });
  assert.match(menu.querySelector('.issue-delete-reason').textContent, /刷新后重试/);
  assert.doesNotMatch(menu.querySelector('.issue-delete-reason').textContent, /成功/);
});

test('drawer uses fresh server restrictions and displays the task that prevents deletion', async t => {
  const app = await loadApp();
  t.after(app.close);
  const issue = { ...blockedIssue, deletionBlock: { code: 'succeeded', reason, taskId: 'historical-task' } };
  app.router.get('/api/issues/delete-feedback', { issue, comments: [] });
  app.router.get('/api/issues/delete-feedback/requirement', {});
  app.router.get('/api/tasks/historical-task', {
    id: 'historical-task', issueId: issue.id, status: 'completed', createdAt: '2026-01-01T00:00:00Z',
  });
  app.evaluate(`state.data.issues=[${JSON.stringify({ ...issue, deletionAllowed: true, deletionBlock: undefined })}];state.data.tasks=[];state.data.runs=[]`);
  await app.evaluate(`openIssueDetail('delete-feedback','overview')`);
  assert.equal(app.evaluate('state.issueDetailData.issue.deletionAllowed'), false);
  assert.equal(app.evaluate('state.data.issues[0].deletionAllowed'), false);
  const drawer = app.document.querySelector('#issueDetailBody');
  assert.equal(drawer.querySelector('[data-delete-issue]').disabled, true);
  assert.equal(drawer.querySelector('.issue-delete-reason').textContent, reason);
  drawer.querySelector('[data-issue-deletion-context]').click();
  await settle(4);
  assert.equal(app.evaluate('state.issueDetailTab'), 'activity');
  assert.match(drawer.textContent, /阻止删除的任务/);
  assert.match(drawer.textContent, /historical-task/);
  assert.match(drawer.textContent, /已完成/);
  await app.evaluate(`deleteIssueById('delete-feedback')`);
  assert.equal(app.router.calls.some(c => c.method === 'DELETE'), false);

  app.router.get('/api/issues/delete-feedback', { issue: { ...issue, deletionAllowed: true, deletionBlock: undefined }, comments: [] });
  await app.evaluate(`openIssueDetail('delete-feedback','overview')`);
  assert.equal(app.evaluate('state.issueDetailData.issue.deletionAllowed'), true);
  assert.equal(drawer.querySelector('[data-delete-issue]').disabled, false);
  assert.equal(drawer.querySelector('.issue-delete-reason'), null);
});

test('bulk selection explains protected rows and escapes server-provided reasons', async t => {
  const app = await loadApp();
  t.after(app.close);
  const malicious = '<img src=x onerror=alert(1)>';
  const issue = { ...blockedIssue, title: malicious, deletionBlock: { code: 'active', reason: malicious } };
  app.evaluate(`
    state.data.issues=[${JSON.stringify(issue)},${JSON.stringify({ ...blockedIssue, id: 'allowed', deletionAllowed: true, deletionBlock: undefined })}];
    state.projectFilter='';state.issueQuery='';state.issueRunFilter='';state.issueStatusFilter='';state.issueAssigneeFilter='';
    state.data.tasks=[];state.data.runs=[];state.issueView='table';
    setIssueSelectionMode(true);
  `);
  const host = app.document.querySelector('#issueBulkActions');
  const blocked = app.document.querySelector('[data-issue-select="delete-feedback"]');
  assert.equal(blocked.disabled, true);
  assert.match(blocked.getAttribute('aria-label'), /onerror/);
  assert.match(host.querySelector('.issue-bulk-blocked summary').textContent, /1 项不可删除/);
  assert.equal(host.querySelector('.issue-bulk-blocked p').textContent, malicious);
  assert.equal(host.querySelector('img'), null);
  assert.equal(app.document.querySelector('[data-issue-select="allowed"]').disabled, false);
  host.querySelector('.issue-bulk-blocked summary').click();
  assert.equal(host.querySelector('.issue-bulk-blocked').open, true);
  app.document.querySelector('[data-issue-select="allowed"]').click();
  assert.equal(host.querySelector('.issue-bulk-blocked').open, true, 'selection changes preserve expanded reasons');
  app.evaluate(`openIssueDetail=(id,tab)=>window.deletionDestination={id,tab}`);
  host.querySelector('[data-issue-deletion-context]').click();
  assert.deepEqual(structuredClone(app.evaluate('window.deletionDestination')), { id: issue.id, tab: 'activity' });
});
