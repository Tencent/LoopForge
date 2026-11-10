import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('issue list helpers filter, format, parse repositories, and persist edits', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const saved = { id: 'issue-1', projectId: 'project-1', title: 'Saved title', status: 'todo', source: 'tapd',
    externalRef: { externalId: 'T-9', externalUrl: 'https://tapd.example/T-9' } };
  app.router.put('/api/issues', saved);
  app.router.put('/api/issues/issue-1/sops', { ...saved, sopBindings: [{ workflowId: 'flow-1', order: 1 }] });
  app.router.post('/api/issues/import-tapd', {
    id: 'tapd-new', projectId: 'project-1', title: 'Imported TAPD', source: 'tapd',
    status: 'backlog', repositoryIds: ['repo-1'], clarification: { status: 'ready' }
  });
  const result = structuredClone(await app.evaluate(`(async () => {
    state.data.projects[0].key='ATL';
    state.data.issues=[
      {id:'a',projectId:'project-1',number:12,title:'【Bug】 Login: fails!',description:'Safari only',
       source:'local',status:'todo',priority:'urgent',assigneeUserId:'user-1'},
      {id:'b',projectId:'project-1',number:2,title:'Dashboard metrics',description:'graphs',
       source:'tapd',status:'backlog',priority:'low',externalRef:{externalId:'TAPD-77'}},
      {id:'c',projectId:'other',number:1,title:'Other',status:'done',priority:'medium'}
    ];
    state.projectFilter='project-1';
    state.sourceFilter='';state.issueStatusFilter='';state.issueAssigneeFilter='';
    state.issueRunFilter='';state.issueQuery='login fails';
    const queried=filteredIssues().map(x=>x.id);
    state.issueQuery='TAPD-77';
    const external=filteredIssues().map(x=>x.id);
    state.issueQuery='';state.issueAssigneeFilter='unassigned';
    const unassigned=filteredIssues().map(x=>x.id);
    const parsed=[
      parseGongfengRepoURL('git clone git@git.example.com:team/service.git'),
      parseGongfengRepoURL('https://git.example.com/team/service/-/merge_requests/9'),
      parseGongfengRepoURL('team/sub/repo'),
      parseGongfengRepoURL('https://evil.example/team/repo'),
      parseGongfengRepoURL('../secret')
    ];
    const fill={name:''};const filled=fillRepositoryFromLink('ssh://git@git.example.com/team/api.git',fill);
    const title=issueTitleMarkup('[Bug] 【前端】 broken <button>');
    const keys=[issueKeyColumnWidth([{id:'x',projectId:'project-1',number:1}]),
      issueKeyColumnWidth([{source:'tapd',externalRef:{externalId:'1234567890'}}])];
    const stored={...state.data.issues[1],repositoryIds:['repo-1'],baseBranch:'main'};
    const workBranch=storedIssueWorkBranch(stored);
    const edited=await persistIssue({id:'issue-1',title:'Saved title',status:'todo',
      source:'local',externalRef:{externalId:'T-9',externalUrl:'https://tapd.example/T-9'}});
    state.data.issues=[edited];
    await saveIssueSOPS('issue-1',[{workflowId:'flow-1',order:1}]);
    return {queried,external,unassigned,parsed,fill,filled,title,keys,workBranch,
      status:[issueStatusLabel('in_review'),issueStatusIcon('blocked'),issuePriorityLabel('urgent')],
      normalized:normalizeIssueTitleSearch(' Ｌｏｇｉｎ：Fails! '),edited,issues:state.data.issues};
  })()`));
  assert.deepEqual(result.queried, ['a']);
  assert.deepEqual(result.external, ['b']);
  assert.deepEqual(result.unassigned, ['b']);
  assert.equal(result.parsed[0].cloneUrl, 'git@git.example.com:team/service.git');
  assert.equal(result.parsed[1].project, 'team/service');
  assert.equal(result.parsed[2].name, 'repo');
  assert.equal(result.parsed[3], null);
  assert.equal(result.parsed[4], null);
  assert.equal(result.filled, true);
  assert.deepEqual(result.fill, {
    name: 'api', gongfengProjectId: 'team/api', webUrl: 'https://git.example.com/team/api',
    cloneUrl: 'git@git.example.com:team/api.git'
  });
  assert.match(result.title, /tone-danger/);
  assert.match(result.title, /tone-cyan/);
  assert.match(result.title, /&lt;button&gt;/);
  assert.deepEqual(result.keys, ['76px','120px']);
  assert.equal(result.workBranch, '');
  assert.deepEqual(result.status, ['评审中','alert','紧急']);
  assert.equal(result.normalized, 'loginfails');
  assert.equal(result.edited.source, 'tapd');
  assert.equal(result.issues[0].sopBindings[0].workflowId, 'flow-1');
  const put = app.router.calls.filter(c => c.method === 'PUT');
  assert.equal(put[0].path, '/api/issues');
  assert.equal(put[0].body.externalRef.externalId, 'T-9');
  assert.equal(put[1].path, '/api/issues/issue-1/sops');
  assert.deepEqual(put[1].body.bindings, [{ workflowId: 'flow-1', order: 1 }]);
  app.evaluate(`
    state.projectFilter='project-1';
    state.data.issues=[{id:'edit-me',projectId:'project-1',number:5,title:'Edit me',description:'desc',
      status:'todo',priority:'high',source:'local',repositoryIds:['repo-1'],baseBranch:'feature/old'}];
    openIssue(state.data.issues[0]);
  `);
  assert.equal(app.document.querySelector('#issueModal').classList.contains('open'), true);
  assert.equal(app.document.querySelector('#issueForm [name="title"]').value, 'Edit me');
  assert.match(app.document.querySelector('#issueForm [name="baseBranch"]').placeholder, /main/);
  app.evaluate(`openImportTAPDModal();setImportTAPDRepositoryIds(['repo-1']);
    document.querySelector('#importTAPDURL').value='https://tapd.example/story/88'`);
  await app.evaluate(`importTAPDStory({preventDefault(){}})`);
  await settle(2);
  const imported = app.router.calls.find(c => c.path === '/api/issues/import-tapd');
  assert.deepEqual(imported.body.repositoryIds, ['repo-1']);
  assert.equal(imported.body.url, 'https://tapd.example/story/88');
  app.evaluate(`
    state.data.issues=[{id:'kanban-1',projectId:'project-1',number:9,title:'Move card',
      status:'backlog',priority:'medium',source:'local',workflowId:'flow-1'}];
    state.issueView='kanban';renderIssues();
    const card=document.querySelector('.kanban-card');
    const transfer={value:'',setData(_t,v){this.value=v},getData(){return this.value},effectAllowed:'',dropEffect:''};
    card.ondragstart({dataTransfer:transfer});card.ondrag();
    card.ondragend();window.__kanbanTransfer=transfer;
  `);
  const doneColumn = [...app.document.querySelectorAll('.kanban-column')].find(x => x.dataset.status === 'done');
  await app.evaluate(`document.querySelector('.kanban-column[data-status="done"]').ondrop({
    preventDefault(){},dataTransfer:window.__kanbanTransfer})`);
  assert.equal(app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/issues').at(-1).body.status, 'done');
  await settle(1);
});

test('issue row actions close and delete eligible issues through named controls', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const issue = { ...app.evaluate('structuredClone(state.data.issues[0])'), status: 'backlog',
    closureAllowed: true, deletionAllowed: true };
  app.router.post('/api/issues/issue-1/close', { ...issue, status: 'closed', closeSource: 'manual' });
  app.router.delete('/api/issues/issue-1', { ok: true });
  app.evaluate(`state.data.issues=[${JSON.stringify(issue)}];state.projectFilter='project-1';
    window.afConfirm=async()=>true;renderIssues()`);
  app.evaluate(`showIssueActionMenu({preventDefault(){},stopPropagation(){},currentTarget:document.querySelector('[data-issue-menu]')},'issue-1')`);
  app.document.querySelector('#issueActionPopover [data-action="close"]').click();
  await settle(4);
  assert.equal(app.evaluate(`state.data.issues[0].status`), 'closed');
  assert.equal(app.router.calls.find(c => c.path.endsWith('/close'))?.method, 'POST');
  app.evaluate(`state.data.issues[0].status='backlog';state.data.issues[0].closeSource='';renderIssues();
    showIssueActionMenu({preventDefault(){},stopPropagation(){},currentTarget:document.querySelector('[data-issue-menu]')},'issue-1')`);
  app.document.querySelector('#issueActionPopover [data-action="delete"]').click();
  await settle(5);
  assert.equal(app.router.calls.find(c => c.method === 'DELETE')?.path, '/api/issues/issue-1');
});

test('rerun-from-node dialog returns the selected node and human context', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = {
    id: 'rerun-1', status: 'failed', workflowSnapshot: {
      id: 'flow-r', nodes: [{ id: 'done', name: 'Done', type: 'script' }, { id: 'fix', name: 'Fix', type: 'agent' }],
      edges: [{ source: 'done', target: 'fix' }]
    }, nodes: { done: { status: 'succeeded' }, fix: { status: 'failed', attempt: 2 } }
  };
  app.router.put('/api/issues/issue-1/sops', call => ({
    ...app.evaluate('structuredClone(state.data.issues[0])'), sopBindings: call.body.bindings
  }));
  app.router.post('/api/issues/issue-1/reopen', {
    ...app.evaluate('structuredClone(state.data.issues[0])'), status: 'backlog', deliveryState: 'reopened'
  });
  app.router.post('/api/runs/rerun-1/retry', { id: 'rerun-1', status: 'running', nodes: run.nodes });
  app.router.get('/api/runs/rerun-1', { ...run, status: 'running' });
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}]`);
  const promise = app.evaluate(`afRerunFromNode(state.data.runs[0],'fix')`);
  assert.equal(app.document.querySelector('#confirmTitle').textContent, '从这里重跑');
  assert.match(app.document.querySelector('#confirmMessage').textContent, /Fix/);
  app.document.querySelector('#rerunFromNodeContext').value = 'retry failing unit test';
  app.document.querySelector('#confirmOkBtn').click();
  const choice = structuredClone(await promise);
  assert.deepEqual(choice, { nodeId: 'fix', context: 'retry failing unit test' });
  const choices = structuredClone(app.evaluate(`runNodeChoices(state.data.runs[0])`));
  assert.deepEqual(choices.map(x => x.id), ['done','fix']);
  assert.equal(app.evaluate(`canRerunFromHere(state.data.runs[0],'fix')`), true);
  app.evaluate(`
    state.data.issues=[{...state.data.issues[0],id:'issue-1',workflowId:'flow-1',
      sopBindings:[{id:'one',workflowId:'flow-1',order:1}],deliveryState:''}];
    state.data.runs[0].executionContext={issueId:'issue-1'};
  `);
  await app.evaluate(`addIssueSOP('issue-1','second-flow')`);
  assert.equal(app.router.calls.filter(c => c.path.endsWith('/sops')).at(-1).body.bindings.length, 2);
  await app.evaluate(`moveIssueSOP('issue-1','second-flow',-1)`);
  await app.evaluate(`setIssueSOPOwner('issue-1','flow-1',7)`);
  await app.evaluate(`setIssueSOPHandoff('issue-1','flow-1','frontend，backend')`);
  await app.evaluate(`removeIssueSOP('issue-1','second-flow')`);
  app.evaluate(`window.afPrompt=async()=> 'focus failed test'`);
  await app.evaluate(`resumeRun('rerun-1',null)`);
  const retry = app.router.calls.find(c => c.path === '/api/runs/rerun-1/retry');
  assert.equal(retry.body.context, 'focus failed test');
  app.evaluate(`state.data.issues[0].deliveryState='merged';state.data.issues[0].deliveryMrState='merged'`);
  await app.evaluate(`reopenDeliveredIssueSilent('issue-1')`);
  assert.equal(app.router.calls.find(c => c.path.endsWith('/reopen')).method, 'POST');
});
