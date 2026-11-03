import test from 'node:test';
import assert from 'node:assert/strict';
import { chooseOption, loadApp, sampleBootstrap, settle } from '../lib/load-app.mjs';

test('list paging, catalog visibility, URL safety, and workflow helpers return concrete domain results', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(() => {
    const page=paginateList('issues',Array.from({length:45},(_,i)=>i+1),20);
    state.listPages.issues=3;
    const last=paginateList('issues',Array.from({length:45},(_,i)=>i+1),20);
    state.projectFilter='project-1';
    state.currentUser={id:1,isGuest:false,isPlatformAdmin:true};
    state.mcpMarket=[
      {id:'market-gh',name:'GitHub',aliases:['github'],description:'Git'},
      {id:'market-gf',name:'工蜂',aliases:['gongfeng'],description:'Git'},
      {id:'market-wiki',name:'Wiki',aliases:['iwiki']}
    ];
    state.data.workflows.push({id:'generic-1',name:'Generic',generic:true,nodes:[]},{id:'shared-1',name:'Shared',projectId:'other',shared:true,nodes:[]});
    const mcp=genericTemplateMCPOptions(['gongfeng','custom']);
    return {
      page:[page.page,page.pages,page.start,page.end,page.items[0]],
      last:[last.page,last.start,last.end,last.items],
      pager:pagerHTML('issues',last),
      labels:[
        clarifyOptionLabel({label:'Choice A'}),
        clarifyOptionLabel('{"text":"Choice B"}'),
        clarifyQuestionOptions({options:'[{"label":"One"},"Two"]'})
      ],
      mcp,checked:genericMCPChecked('market-gf',['gongfeng']),
      workflows:runnableWorkflowOptions().map(x=>x.name),
      visible:[catalogOwned('project-1'),catalogForeign('other'),catalogShared('other',true),catalogVisible('other',true)],
      urls:[safeURL('https://example.com/a'),safeURL('javascript:alert(1)')],
      colors:[safeColor('#abc'),safeColor('red')],
      unconfigured:(state.appDomain='',isOAHost('loopforge.example.com')),
      hosts:[isLoopbackHost('127.0.0.1'),(state.appDomain='loopforge.example.com',isOAHost('loopforge.example.com')),shouldAutoPassportRedirect({isGuest:true,hostname:'loopforge.example.com',bypassAutoAuth:false,authErr:'',loginUrl:'https://login'})]
    };
  })()`);
  assert.equal(JSON.stringify(result.page), JSON.stringify([1, 3, 0, 20, 1]));
  assert.equal(result.last[0], 3);
  assert.equal(result.last[1], 40);
  assert.equal(result.last[2], 45);
  assert.match(result.pager, /第 41–45 条|上一页|disabled/);
  assert.equal(result.labels[0], 'Choice A');
  assert.equal(result.labels[1], 'Choice B');
  assert.equal(JSON.stringify(result.labels[2]), JSON.stringify(['One', 'Two']));
  assert.equal(result.mcp.length, 3);
  assert.equal(JSON.stringify(result.mcp.map((item) => item.id)), JSON.stringify(['github', 'gongfeng', 'custom']));
  assert.equal(result.checked, true);
  assert.match(result.workflows.join(','), /通用 · Generic|Shared/);
  assert.equal(JSON.stringify(result.visible), JSON.stringify([true, true, true, true]));
  assert.equal(result.urls[1], '#');
  assert.equal(JSON.stringify(result.colors), JSON.stringify(['#abc', '#74829a']));
  assert.equal(JSON.stringify(result.hosts), JSON.stringify([true, true, true]));
  assert.equal(result.unconfigured, false, 'no SSO redirect host unless the deployment configures one');
});

test('code editor keyboard controls indent, outdent, auto-indent, save, and close the modal', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`openCodeEditorModal({title:'Python',language:'python',value:'if ready:',onSave:(value,lang)=>window.__editorSaved={value,lang}})`);
  const editor = app.document.querySelector('#codeEditorTextarea');
  editor.selectionStart = editor.selectionEnd = editor.value.length;
  editor.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  assert.equal(editor.value, 'if ready:\n  ');
  editor.value = 'line';
  editor.selectionStart = editor.selectionEnd = 0;
  editor.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
  assert.equal(editor.value, '  line');
  editor.selectionStart = editor.selectionEnd = 2;
  editor.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true }));
  assert.equal(editor.value, 'line');
  editor.value = 'print("saved")';
  editor.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 's', ctrlKey: true, bubbles: true }));
  assert.equal(app.evaluate(`window.__editorSaved.value`), 'print("saved")');
  assert.equal(app.evaluate(`window.__editorSaved.lang`), 'python');
  assert.equal(app.document.querySelector('#codeEditorModal').classList.contains('open'), false);

  app.evaluate(`openCodeEditorModal({title:'Close',value:'x'})`);
  editor.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(app.document.querySelector('#codeEditorModal').classList.contains('open'), false);
});

test('native chat bridge validates messages and updates session, project, and requested views', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`mountNativeChat()`);
  const frame = app.document.querySelector('#chatNextFrame');
  const send = data => app.window.dispatchEvent(new app.window.MessageEvent('message', {
    data, origin: app.window.location.origin, source: frame.contentWindow,
  }));
  send({ type: 'agentflow-chat-session', sessionId: 'session-42' });
  assert.match(app.window.location.search, /chatSession=session-42/);
  send({ type: 'agentflow-chat-session', sessionId: 'not valid!' });
  assert.match(app.window.location.search, /session-42/);
  send({ type: 'agentflow-chat-project', projectId: 'project-1' });
  await settle(3);
  assert.equal(app.evaluate(`state.projectFilter`), 'project-1');
  send({ type: 'agentflow-chat-open-view', view: 'settings' });
  await new Promise(resolve => setTimeout(resolve, 400));
  await settle(3);
  assert.equal(app.evaluate(`state.currentView`), 'settings');
  assert.equal(app.document.querySelector('#pageTitle').textContent, '个人配置');
  send({ type: 'agentflow-chat-open-view', view: 'runs', runId: 'run-1' });
  await settle(3);
  assert.match(app.window.location.hash, /runs\/run-1/);
  assert.equal(app.evaluate(`state.currentView`), 'runs');
});

test('the chat opens personal until a project is picked and follows projects created in the chat', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.window.localStorage.removeItem('agentflow.activeProject');
  assert.equal(app.evaluate(`state.projectFilter`), 'project-1');
  assert.equal(app.evaluate(`nativeChatProjectId()`), '');
  app.window.localStorage.setItem('agentflow.activeProject', 'project-1');
  assert.equal(app.evaluate(`nativeChatProjectId()`), 'project-1');

  app.evaluate(`mountNativeChat()`);
  const frame = app.document.querySelector('#chatNextFrame');
  const send = data => app.window.dispatchEvent(new app.window.MessageEvent('message', {
    data, origin: app.window.location.origin, source: frame.contentWindow,
  }));
  const withProject = (...extra) => () => {
    const shell = sampleBootstrap();
    shell.projects.push(...extra);
    return shell;
  };
  // The chat just created okr-system; the shell has not loaded it yet.
  app.router.get('/api/bootstrap', withProject({ id: 'okr-system', name: 'OKR系统', key: 'OKR' }));
  send({ type: 'agentflow-chat-project', projectId: 'okr-system' });
  await settle(8);
  assert.equal(app.evaluate(`state.projectFilter`), 'okr-system');
  assert.ok([...app.document.querySelectorAll('#projectSwitcher option')].some(o => o.value === 'okr-system'));

  send({ type: 'agentflow-chat-project', projectId: 'missing-project' });
  await settle(8);
  assert.equal(app.evaluate(`state.projectFilter`), 'okr-system');

  app.router.get('/api/bootstrap', withProject({ id: 'okr-system', name: 'OKR系统', key: 'OKR' }, { id: 'crm', name: 'CRM', key: 'CRM' }));
  send({ type: 'agentflow-projects-changed' });
  await settle(8);
  assert.ok(app.evaluate(`state.data.projects.some(p => p.id === 'crm')`));
  assert.ok([...app.document.querySelectorAll('#projectSwitcher option')].some(o => o.value === 'crm'));
});

test('generic workflow hydration, project switching, flow routes, and shell keyboard interactions update state', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/workflows/generic-2/agents', [{ id: 'generic-agent', name: 'Template Developer', projectId: '' }]);
  app.evaluate(`
    state.data.projects.push({id:'project-2',name:'Beta',defaultRepositoryId:'repo-2'});
    state.data.repositories.push({id:'repo-2',projectId:'project-2',name:'beta'});
    state.data.workflows.push(
      {id:'generic-2',name:'Generic Two',generic:true,nodes:[]},
      {id:'flow-2',name:'Beta Flow',projectId:'project-2',nodes:[],edges:[]}
    );
    state.workflow=clone(state.data.workflows.find(x=>x.id==='generic-2'));
  `);
  assert.equal(await app.evaluate(`hydrateGenericWorkflowRoles()`), true);
  assert.equal(app.evaluate(`state.genericTemplateAgents[0].name`), 'Template Developer');
  assert.equal(await app.evaluate(`hydrateGenericWorkflowRoles()`), false);

  const select = app.document.querySelector('#projectSwitcher');
  app.evaluate(`renderProjectSwitcher()`);
  chooseOption(select, 'project-2');
  await settle(6);
  assert.equal(app.evaluate(`state.projectFilter`), 'project-2');
  assert.equal(app.evaluate(`state.workflow.id`), 'flow-2');
  assert.equal(app.window.localStorage.getItem('agentflow.activeProject'), 'project-2');

  app.evaluate(`state.currentView='flow';location.hash='#/flow/flow-2';state.workflow=null`);
  await app.evaluate(`applyFlowRoute()`);
  assert.equal(app.evaluate(`state.workflow.id`), 'flow-2');
  assert.equal(app.window.localStorage.getItem('agentflow.activeWorkflow'), 'flow-2');

  app.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }));
  assert.equal(app.document.querySelector('#commandPalette').hidden, false);
  app.document.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(app.document.querySelector('#commandPalette').hidden, true);

  const modal = app.document.querySelector('#artifactModal');
  modal.classList.add('open');
  modal.dispatchEvent(new app.window.Event('pointerdown', { bubbles: true }));
  assert.equal(modal.classList.contains('open'), false);

  const resize = app.document.querySelector('.sidebar-resize');
  resize.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  assert.ok(Number(app.window.localStorage.getItem('agentflow.sidebarWidth')) >= 220);
  resize.dispatchEvent(new app.window.MouseEvent('dblclick', { bubbles: true }));
  assert.equal(app.window.localStorage.getItem('agentflow.sidebarWidth'), null);
});

test('boot exchanges an authentication callback, cleans the URL, and loads the selected project shell', async (t) => {
  const app = await loadApp({ dispatchReady: false });
  t.after(app.close);
  app.window.history.replaceState({}, '', '/?code=oauth-code&projectId=project-1#/issues');
  app.router.get('/api/auth/exchange?code=oauth-code', {
    user: { id: 9, loginName: 'oauth-user', isGuest: false, isPlatformAdmin: false },
    authRequired: true,
    appDomain: 'loopforge.example.com',
  });
  await app.evaluate(`boot()`);
  const exchange = app.router.calls.find(c => c.path === '/api/auth/exchange');
  assert.equal(exchange.search, '?code=oauth-code');
  assert.equal(app.evaluate(`state.currentUser.loginName`), 'oauth-user');
  assert.equal(app.evaluate(`state.authRequired`), true);
  assert.equal(app.evaluate(`state.projectFilter`), 'project-1');
  assert.doesNotMatch(app.window.location.search, /code=/);
  assert.match(app.window.location.hash, /issues/);
  assert.equal(app.document.querySelector('#pageTitle').textContent, '需求列表');
});
