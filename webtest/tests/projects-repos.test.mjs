import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('project identity, filtering, payload validation, and project form behavior are deterministic', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/projects', (call) => call.body);
  const values = app.evaluate(`({
    identity:generateProjectIdentity('Web Console',[{id:'web-console',key:'WC'}],'seed'),
    chinese:generateProjectIdentity('控制台',[],'a1b2'),
    route:parseProjectAdminRoute('#/projects/project%201/members'),
    hash:projectAdminHash('project 1','repos'),
    people:projectPeopleIds({ownerId:1,members:[{userId:1},{userId:2},{userId:2}]}),
    filtered:filterAdminProjects(
      [{id:'p1',name:'Alpha',ownerId:1,members:[]},{id:'p2',name:'Beta',ownerId:2,members:[]}],
      [{id:'r1',projectId:'p1',name:'frontend'}],'front','all',1).map(p=>p.id)
  })`);
  assert.equal(JSON.stringify(values.identity), JSON.stringify({ id: 'web-console-2', key: 'WCA' }));
  assert.equal(JSON.stringify(values.chinese), JSON.stringify({ id: 'proj-a1b2', key: 'PRJ' }));
  assert.equal(JSON.stringify(values.route), JSON.stringify({ id: 'project 1', tab: 'members' }));
  assert.equal(values.hash, '#/projects/project%201/repos');
  assert.equal(JSON.stringify(values.people), JSON.stringify([1, 2]));
  assert.equal(JSON.stringify(values.filtered), JSON.stringify(['p1']));

  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[state.currentUser,{id:2,loginName:'member'}];
    state.data.workflows.push({id:'generic-backend-delivery',name:'Generic',generic:true});
    state.projectAdminDetailId='new';state.projectAdminTab='basic';state.projectDraft=null;
    renderProjectDetail({force:true});
  `);
  const form = app.document.querySelector('#projectAdminForm');
  form.elements.name.value = 'Payments API';
  form.elements.name.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(form.elements.id.value, 'payments-api');
  assert.equal(form.elements.key.value, 'PA');
  form.querySelector('[data-pa-color="#10b981"]').click();
  assert.equal(app.evaluate(`state.projectDraft.color`), '#10b981');
  assert.equal(app.evaluate(`setProjectAdminTab('repos')`), false);
  const payloadResult = app.evaluate(`
    (() => {
      state.data.repositories=[{id:'repo-x',projectId:'payments-api'}];
      const payload=buildProjectPayload({id:'payments-api',name:' Payments ',key:'pay',ownerId:1,
        workflowId:'generic-backend-delivery',members:[{userId:1},{userId:2}],defaultRepositoryId:'repo-x'});
      const valid=validateProjectPayload(payload,{creating:true,projects:[],workflows:state.data.workflows});
      const duplicate=validateProjectPayload({...payload,members:[{userId:2},{userId:2}]},
        {creating:true,projects:[],workflows:state.data.workflows});
      // The id is a path segment of /api/projects/:id: a slash or an over-long
      // id would create a project nobody can open or delete.
      const slash=validateProjectPayload({...payload,id:'wa bad/id?x'},{creating:true,projects:[],workflows:state.data.workflows});
      const long=validateProjectPayload({...payload,id:'p'.repeat(65)},{creating:true,projects:[],workflows:state.data.workflows});
      const legacy=validateProjectPayload({...payload,id:'old id'},{creating:false,saved:true,projects:[],workflows:state.data.workflows});
      const longName=generateProjectIdentity('Alpha '.repeat(20),[],'seed').id;
      return {payload,valid,duplicate,slash,long,legacy,longName};
    })()
  `);
  assert.equal(payloadResult.payload.name, 'Payments');
  assert.equal(payloadResult.payload.key, 'PAY');
  assert.equal(payloadResult.payload.members.length, 1);
  assert.equal(payloadResult.valid, null);
  assert.match(payloadResult.duplicate.message, /不能重复/);
  assert.equal(payloadResult.slash.field, 'id');
  assert.match(payloadResult.slash.message, /不能包含空格/);
  assert.match(payloadResult.long.message, /64/);
  assert.equal(payloadResult.legacy, null);
  assert.ok(payloadResult.longName.length <= 56 && !payloadResult.longName.endsWith('-'), payloadResult.longName);

  app.evaluate(`
    resetProjectAdminDetail();
    state.data.projects[0].ownerId=1;state.data.projects[0].key='ATL';state.data.projects[0].workflowId='flow-1';
    state.projectAdminDetailId='project-1';state.projectAdminTab='basic';
    renderProjectDetail({force:true});
  `);
  const edit = app.document.querySelector('#projectAdminForm');
  edit.querySelector('[data-pa-ident-edit]').click();
  assert.equal(app.evaluate(`state.projectIdentCustom`), true);
  edit.querySelector('[data-pa-ident-collapse]').click();
  edit.elements.description.value = 'Updated project scope';
  edit.elements.description.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  edit.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const update = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/projects');
  assert.equal(update.body.description, 'Updated project scope');
  assert.equal(app.evaluate(`state.data.projects[0].description`), 'Updated project scope');
});

test('repository drawer detects Git SSH URLs and persists a default repository', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/repositories', (call) => ({ ...call.body, id: 'repo-new' }));
  app.router.put('/api/projects', (call) => call.body);
  app.router.delete('/api/repositories', { ok: true });
  app.evaluate(`
    state.projectAdminDetailId='project-1';
    state.projectDraftFor='project-1';
    state.projectDraft=projectDraftFrom(state.data.projects[0]);
    state.projectDraftBase=clone(state.projectDraft);
    state.data.repositories=[];
    openRepoDrawer('');
  `);
  const form = app.document.querySelector('#repoDrawerForm');
  form.elements.repoUrl.value = 'git@gitlab.example.org:platform/payments.git';
  form.elements.repoUrl.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  await settle(6);
  assert.equal(app.document.querySelector('#repoDrawerSSHMissing').hidden, false);
  assert.match(app.document.querySelector('#repoDrawerSSHMissing').textContent, /可稍后/);
  assert.equal(app.document.querySelector('#repoDrawerSave').disabled, false, 'a missing key is advisory and does not block saving');
  assert.equal(app.document.querySelector('#repoDrawerDetect').hidden, false);
  assert.equal(form.elements.name.value, 'payments');
  assert.equal(app.document.querySelector('#repoDrawerSSH').hidden, false);
  form.elements.webUrl.value = 'https://gitlab.example.org/platform/payments';
  form.elements.defaultBranch.value = 'main';
  form.elements.integrateBranch.value = 'release';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const create = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/repositories');
  assert.equal(create.body.provider, 'ssh');
  assert.equal(create.body.gongfengProjectId, '');
  assert.equal(create.body.cloneUrl, 'git@gitlab.example.org:platform/payments.git');
  assert.equal(create.body.webUrl, 'https://gitlab.example.org/platform/payments');
  assert.equal(create.body.defaultBranch, 'main');
  assert.equal(app.evaluate(`state.data.repositories[0].id`), 'repo-new');
  assert.equal(app.evaluate(`state.data.projects[0].defaultRepositoryId`), 'repo-new');
  assert.equal(app.document.querySelector('#repoDrawer').classList.contains('open'), false);
  app.evaluate(`window.__deleteRepo=deleteProjectRepository('repo-new')`);
  await settle(2);
  app.document.querySelector('#confirmOkBtn').click();
  await app.evaluate(`window.__deleteRepo`);
  assert.equal(app.evaluate(`state.data.repositories.length`), 0);
  assert.ok(app.router.calls.some(c => c.method === 'DELETE' && c.path === '/api/repositories'));
});

test('repository drawer saves AI and prefix naming settings', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/repositories', (call) => call.body);
  app.router.get('/api/executor-models', {
    codebuddy: { configured: true, models: [{ id: 'hy4std', name: 'Hy4' }] },
    codex: { configured: true, models: [{ id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol' }] },
    claude: { models: [] },
  });
  app.evaluate(`
    state.projectAdminDetailId='project-1';
    state.projectDraftFor='project-1';
    state.data.executors=[{id:'codebuddy',kind:'codebuddy',name:'CodeBuddy'},{id:'codex',kind:'codex',name:'Codex'}];
    state.data.repositories=[{
      id:'branch-repo', projectId:'project-1', provider:'github',
      name:'payments', cloneUrl:'https://github.com/acme/payments.git', defaultBranch:'main',
      workBranchNaming:{mode:'ai',executorId:'codebuddy',model:'hy4std',prompt:'Use the team namespace'}
    }];
    state.data.projects[0].defaultRepositoryId='branch-repo';
    state.projectDraft=projectDraftFrom(state.data.projects[0]);
    state.projectDraftBase=clone(state.projectDraft);
    openRepoDrawer('branch-repo');
  `);
  const form = app.document.querySelector('#repoDrawerForm');
  const fields = form.elements;
  await settle(6);
  assert.equal(fields.workBranchNamingMode.value, 'ai');
  assert.equal(fields.workBranchExecutorId.value, 'codebuddy');
  assert.equal(fields.workBranchModelChoice.value, 'hy4std');
  assert.equal(fields.workBranchPrompt.value, 'Use the team namespace');
  assert.equal(form.querySelector('[data-work-branch-prefix]').hidden, true);
  assert.equal(form.querySelector('[data-work-branch-prompt]').hidden, false);
  fields.workBranchExecutorId.value = 'codex';
  fields.workBranchExecutorId.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.equal(fields.workBranchModelChoice.value, '', 'changing CLI requires a fresh model selection');
  assert.ok(Array.from(fields.workBranchModelChoice.options).some(option => option.value === 'gpt-5.6-sol'));
  fields.workBranchModelChoice.value = '__custom__';
  fields.workBranchModelChoice.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  fields.workBranchCustomModel.value = '  team/payment-model  ';
  fields.workBranchPrompt.value = '  Use TASK- followed by the issue number  ';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const updates = () => app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/repositories');
  assert.deepEqual(updates().at(-1).body.workBranchNaming, {
    mode: 'ai', executorId: 'codex', model: 'team/payment-model', prompt: 'Use TASK- followed by the issue number',
  });

  app.evaluate(`openRepoDrawer('branch-repo')`);
  assert.equal(fields.workBranchExecutorId.value, 'codex');
  assert.equal(fields.workBranchCustomModel.value, 'team/payment-model');
  fields.workBranchNamingMode.value = 'prefix';
  fields.workBranchNamingMode.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  assert.equal(form.querySelector('[data-work-branch-prefix]').hidden, false);
  assert.equal(form.querySelector('[data-work-branch-prompt]').hidden, true);
  assert.deepEqual(Array.from(fields.workBranchPrefix.options, option => [option.value, option.text]), [
    ['feature/', 'feature/'], ['custom', '自定义'], ['', '无前缀'],
  ]);
  assert.equal(fields.workBranchPrefix.value, 'feature/');
  fields.workBranchPrefix.value = '';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  assert.deepEqual(updates().at(-1).body.workBranchNaming, { mode: 'prefix', prefix: '' });

  app.evaluate(`openRepoDrawer('branch-repo')`);
  assert.equal(fields.workBranchNamingMode.value, 'prefix');
  assert.equal(fields.workBranchPrefix.value, '');

  for (const prefix of ['feat/', 'fix/', 'bugfix/']) {
    app.evaluate(`
      state.data.repositories[0].workBranchNaming={mode:'prefix',prefix:${JSON.stringify(prefix)}};
      openRepoDrawer('branch-repo');
    `);
    assert.equal(fields.workBranchPrefix.value, 'custom');
    assert.equal(fields.workBranchCustomPrefix.value, prefix);
    assert.equal(form.querySelector('[data-work-branch-custom-prefix]').hidden, false);
  }
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  assert.deepEqual(updates().at(-1).body.workBranchNaming, { mode: 'prefix', prefix: 'bugfix/' });
});

test('AI branch naming offers only CLIs configured for the user', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/repositories', (call) => call.body);
  app.router.get('/api/executor-models', {
    codebuddy: { configured: true, models: [{ id: 'hy4std', name: 'Hy4' }] },
    codex: { configured: false, models: [{ id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol' }] },
    claude: { configured: false, models: [] },
  });
  app.evaluate(`
    state.projectAdminDetailId='project-1';
    state.projectDraftFor='project-1';
    state.data.executors=[{id:'codebuddy',kind:'codebuddy',name:'CodeBuddy'},{id:'codex',kind:'codex',name:'Codex'},{id:'claude',kind:'claude-code',name:'Claude Code'}];
    state.data.repositories=[{
      id:'branch-repo', projectId:'project-1', provider:'github',
      name:'payments', cloneUrl:'https://github.com/acme/payments.git', defaultBranch:'main',
      workBranchNaming:{mode:'prefix',prefix:'feature/'}
    }];
    state.data.projects[0].defaultRepositoryId='branch-repo';
    state.projectDraft=projectDraftFrom(state.data.projects[0]);
    state.projectDraftBase=clone(state.projectDraft);
    openRepoDrawer('branch-repo');
  `);
  const form = app.document.querySelector('#repoDrawerForm');
  const fields = form.elements;
  const options = () => Array.from(fields.workBranchExecutorId.options, option => [option.value, option.text, option.disabled]);
  const updates = () => app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/repositories');
  fields.workBranchNamingMode.value = 'ai';
  fields.workBranchNamingMode.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(6);
  assert.deepEqual(options(), [['', '选择 CLI', false], ['codebuddy', 'CodeBuddy', false]],
    'CLIs the user cannot run are not offered, as in the role editor');

  // A CLI that is not usable cannot be saved as a new choice.
  fields.workBranchExecutorId.add(new app.window.Option('Codex', 'codex'));
  fields.workBranchExecutorId.value = 'codex';
  fields.workBranchExecutorId.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  fields.workBranchModelChoice.value = 'gpt-5.6-sol';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  assert.equal(updates().length, 0, 'an unconfigured CLI is rejected before saving');
  assert.match(app.document.body.textContent, /请选用已配置的 CLI/);

  // A stored CLI the current editor has not configured stays visible but
  // disabled, and saving other fields keeps it.
  app.evaluate(`
    state.data.repositories[0].workBranchNaming={mode:'ai',executorId:'codex',model:'gpt-5.6-sol',prompt:''};
    openRepoDrawer('branch-repo');
  `);
  await settle(6);
  assert.deepEqual(options(), [['', '选择 CLI', false], ['codex', 'Codex（未配置）', true], ['codebuddy', 'CodeBuddy', false]]);
  assert.equal(fields.workBranchExecutorId.value, 'codex');
  fields.defaultBranch.value = 'trunk';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  assert.equal(updates().length, 1);
  assert.equal(updates().at(-1).body.defaultBranch, 'trunk');
  assert.deepEqual(updates().at(-1).body.workBranchNaming, { mode: 'ai', executorId: 'codex', model: 'gpt-5.6-sol', prompt: '' });
});

test('AI branch naming says the CLI list is loading instead of not configured', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let release;
  app.router.get('/api/executor-models', () => new Promise(resolve => {
    release = () => resolve({ codebuddy: { configured: true, models: [{ id: 'hy4std', name: 'Hy4' }] } });
  }));
  app.evaluate(`
    state.projectAdminDetailId='project-1';
    state.projectDraftFor='project-1';
    state.data.executors=[{id:'codebuddy',kind:'codebuddy',name:'CodeBuddy'},{id:'codex',kind:'codex',name:'Codex'}];
    state.data.repositories=[{
      id:'saved-ai', projectId:'project-1', provider:'github',
      name:'payments', cloneUrl:'https://github.com/acme/payments.git', defaultBranch:'main',
      workBranchNaming:{mode:'ai',executorId:'codebuddy',model:'hy4std',prompt:''}
    },{
      id:'prefix-repo', projectId:'project-1', provider:'github',
      name:'ledger', cloneUrl:'https://github.com/acme/ledger.git', defaultBranch:'main',
      workBranchNaming:{mode:'prefix',prefix:'feature/'}
    }];
    state.projectDraft=projectDraftFrom(state.data.projects[0]);
    state.projectDraftBase=clone(state.projectDraft);
    openRepoDrawer('saved-ai');
  `);
  const fields = app.document.querySelector('#repoDrawerForm').elements;
  const options = () => Array.from(fields.workBranchExecutorId.options, option => [option.value, option.text, option.disabled]);
  await settle(2);
  assert.deepEqual(options(), [['', '选择 CLI', false], ['codebuddy', 'CodeBuddy（检查配置中）', true]],
    'a saved CLI is not reported as unconfigured before the catalog answers');
  assert.equal(fields.workBranchExecutorId.value, 'codebuddy');
  release();
  await settle(6);
  assert.deepEqual(options(), [['', '选择 CLI', false], ['codebuddy', 'CodeBuddy', false]]);
  assert.equal(fields.workBranchExecutorId.value, 'codebuddy');

  // Switching a prefix repository to AI while the catalog reloads shows the
  // checking placeholder rather than "no configured CLI".
  app.evaluate(`closeRepoDrawer({restoreFocus:false});openRepoDrawer('prefix-repo');`);
  fields.workBranchNamingMode.value = 'ai';
  fields.workBranchNamingMode.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await settle(2);
  assert.deepEqual(options(), [['', '检查配置中…', false]]);
  release();
  await settle(6);
  assert.deepEqual(options(), [['', '选择 CLI', false], ['codebuddy', 'CodeBuddy', false]]);
});

test('project owner deletes a project from its danger zone and the list moves on', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.delete('/api/projects/okr-system', (call) => ({ id: 'okr-system', archivedAt: '2026-11-01T00:00:00Z' }));
  app.evaluate(`
    afConfirm=async()=>true;
    state.authRequired=true;
    state.currentUser={id:7,loginName:'owner'};
    state.data.users=[state.currentUser];
    state.data.projects=[...state.data.projects,{id:'okr-system',name:'OKR系统',ownerId:7,members:[]}];
    state.projectFilter='okr-system';
  `);
  const values = app.evaluate(`({
    owner:canDeleteProject({id:'okr-system',ownerId:7}),
    other:canDeleteProject({id:'x',ownerId:8}),
    unsaved:canDeleteProject(null),
    row:projectDeleteRow({id:'okr-system',ownerId:7}).includes('data-project-delete="okr-system"'),
    hidden:projectDeleteRow({id:'x',ownerId:8})
  })`);
  assert.deepEqual({ ...values }, { owner: true, other: false, unsaved: false, row: true, hidden: '' });
  await app.evaluate(`deleteProjectAdmin('okr-system')`);
  await settle(4);
  assert.ok(app.router.calls.some(c => c.method === 'DELETE' && c.path === '/api/projects/okr-system'));
  assert.equal(app.evaluate(`state.data.projects.some(p=>p.id==='okr-system')`), false);
  assert.notEqual(app.evaluate(`state.projectFilter`), 'okr-system');
  assert.equal(app.evaluate(`localStorage.getItem('agentflow.activeProject')`), app.evaluate(`state.projectFilter`));
  assert.equal(app.evaluate(`state.projectAdminDetailId||''`), '');
});
