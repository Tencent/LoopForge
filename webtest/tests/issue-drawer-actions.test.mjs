import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('drawer clarification, activity, delivery, and operation helpers render meaningful details', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    const issue={...state.data.issues[0],source:'tapd',status:'backlog',priority:'high',
      externalRef:{externalId:'T-1',externalUrl:'https://tapd.example/T-1'},
      repositoryIds:['repo-1'],baseBranch:'feature/chart',
      clarification:{status:'in_progress',questions:[
          {question:'Browser?',options:['Chrome','Safari'],multiSelect:false},
          {question:'Checks?',options:['Unit','E2E'],multiSelect:true}],
        history:[
          {role:'assistant',content:JSON.stringify({questions:[{question:'Browser?'}]})},
          {role:'user',content:'Safari'}]}};
    const project={...state.data.projects[0]};
    state.data.projects=[project];
    state.issueDetailData={issue,comments:[
      {authorType:'agent',body:'Automated note',createdAt:'2026-01-01T01:02:03Z'}],
      task:{id:'task-x',status:'running',createdAt:'2026-01-01T00:00:00Z'},
      run:{id:'run-x',status:'failed',workflowId:'flow-1',input:'Build charts',
        executionContext:{traceId:'trace-123456789012345'},
        repositories:[{repositoryId:'repo-1',name:'atlas-web',baseBranch:'main',workBranch:'feature/chart'}],
        workflowSnapshot:{id:'flow-1',nodes:[{id:'n1',name:'Build',type:'agent'}]},
        nodes:{n1:{status:'failed',error:'tests failed'}},
        events:[{type:'node.failed',message:'A'.repeat(190),timestamp:'2026-01-01T03:00:00Z'}]},
      requirement:null};
    const c=issue.clarification;
    const protocol='prefix '+JSON.stringify({summary:'Useful summary',questions:[{question:'OS?',options:['Linux']}],mcpCalls:[]});
    return {
      questionKey:clarifyQuestionKey(c.questions),
      cards:clarifyQuestionCardsHTML(c.questions,{questions:[{selected:[false,true],other:'Other'}]}),
      reply:clarifySubmittedHTML('Safari'),
      collectedHistory:clarifyHistoryHTML(c),
      protocol:clarifyExtractProtocol(protocol),
      visible:clarifyVisibleSummary(protocol),
      hidden:clarifyThoughtShouldHide(protocol),
      recovered:recoverClarifyView({status:'in_progress',summary:protocol,questions:[]}),
      pane:clarifyPane(issue,project),
      overview:clarificationOverviewCard(issue),
      operations:operationsPane({...issue,createdAt:'2026-01-01',operations:[
        {at:'2026-01-03',action:'update',actor:'Alice',detail:'changed title'}]}),
      activity:activityPane(issue,state.issueDetailData),
      artifacts:artifactsPane({run:{...state.issueDetailData.run,artifacts:[
        {id:'a1',nodeId:'n1',name:'Report',path:'report.md',attempt:2,size:2048,sha256:'abcdef123456'}]}}),
      delivery:deliveryPane(issue,state.issueDetailData),
      acceptance:acceptancePane(issue,state.issueDetailData),
      labels:[activityEventLabel('node.failed'),activityEventLabel('run.custom'),issueOperationLabel('reopen')],
      badTime:formatActivityTime('not-a-date')
    };
  })()`));
  assert.match(result.questionKey, /Browser\?/);
  assert.match(result.cards, /selected/);
  assert.match(result.cards, /Other/);
  assert.match(result.reply, /Safari/);
  assert.match(result.collectedHistory, /Browser\?/);
  assert.equal(result.protocol.summary, 'Useful summary');
  assert.equal(result.visible, 'prefix');
  assert.equal(result.hidden, true);
  assert.equal(result.recovered.questions[0].question, 'OS?');
  assert.doesNotMatch(result.pane, /信息完整度|准入线|clarify-score/);
  assert.match(result.pane, /只围绕这张单本身/);
  assert.match(result.overview, /继续/);
  assert.match(result.operations, /编辑需求|changed title/);
  assert.match(result.activity, /Automated note/);
  assert.match(result.activity, /节点失败/);
  assert.match(result.artifacts, /Report/);
  assert.match(result.delivery, /feature\/chart/);
  assert.match(result.acceptance, /门禁失败/);
  assert.deepEqual(result.labels, ['节点失败','运行 · custom','重新打开']);
  assert.equal(result.badTime, '—');
});

test('acceptance gates follow the SOP edges, so a node inserted by a SOP update shows where it runs', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    // SOP v2 inserted "preview" between "test" and "accept"; the editor appended
    // it to the nodes array, and refresh-sop keeps that array order.
    const wf={id:'flow-1',
      nodes:[{id:'design',name:'方案设计',type:'agent'},{id:'test',name:'测试验证',type:'agent'},
        {id:'accept',name:'人工验收',type:'approval'},{id:'summary',name:'交付汇总',type:'agent'},
        {id:'preview',name:'构建静态站点',type:'agent'}],
      edges:[{source:'design',target:'test'},{source:'test',target:'preview'},
        {source:'preview',target:'accept'},{source:'accept',target:'summary'}]};
    const run={id:'run-v2',status:'waiting',workflowId:'flow-1',workflowSnapshot:wf,
      nodes:{design:{status:'succeeded'},test:{status:'succeeded'},preview:{status:'succeeded'},
        accept:{status:'waiting_approval'},summary:{status:'pending'}}};
    const issue={...state.data.issues[0],source:'manual'};
    const html=acceptancePane(issue,{issue,run,task:null,requirement:null});
    const names=html.split('<span class="node-name">').slice(1).map(part=>part.split('<')[0]);
    const cyclic={...wf,edges:[...wf.edges,{source:'summary',target:'design'}]};
    return {names,
      unordered:nodesInExecutionOrder({nodes:wf.nodes}).map(n=>n.id),
      cyclic:nodesInExecutionOrder(cyclic).map(n=>n.id)};
  })()`));
  assert.deepEqual(result.names, ['方案设计','测试验证','构建静态站点','人工验收','交付汇总']);
  assert.deepEqual(result.unordered, ['design','test','accept','summary','preview']);
  assert.equal(result.cyclic.length, 5);
  assert.deepEqual([...result.cyclic].sort(), ['accept','design','preview','summary','test']);
});

test('acceptance gate ordering ignores self-loops and edges to deleted nodes, and keeps degenerate SOPs as stored', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    const ids=wf=>nodesInExecutionOrder(wf).map(n=>n.id);
    const nodes=[{id:'c'},{id:'a'},{id:'b'}];
    return {
      // A self-loop and an edge from/to a node removed in a later SOP version
      // must not block the real order a -> b -> c.
      dangling:ids({nodes,edges:[{source:'a',target:'b'},{source:'b',target:'c'},
        {source:'a',target:'a'},{source:'gone',target:'a'},{source:'a',target:'gone'}]}),
      // Only ignorable edges: stable array order.
      onlyIgnored:ids({nodes,edges:[{source:'b',target:'b'},{source:'gone',target:'c'}]}),
      // Diamond: ties are broken by array order.
      diamond:ids({nodes:[{id:'end'},{id:'right'},{id:'left'},{id:'start'}],
        edges:[{source:'start',target:'left'},{source:'start',target:'right'},
          {source:'left',target:'end'},{source:'right',target:'end'}]}),
      single:ids({nodes:[{id:'only'}],edges:[{source:'x',target:'only'}]}),
      missingID:ids({nodes:[{id:'b'},{name:'no id'},{id:'a'}],edges:[{source:'a',target:'b'}]}).map(id=>id??null),
      empty:ids(undefined),
      noNodes:ids({edges:[{source:'a',target:'b'}]}),
    };
  })()`));
  assert.deepEqual(result.dangling, ['a', 'b', 'c']);
  assert.deepEqual(result.onlyIgnored, ['c', 'a', 'b']);
  assert.deepEqual(result.diamond, ['start', 'right', 'left', 'end']);
  assert.deepEqual(result.single, ['only']);
  assert.deepEqual(result.missingID, ['b', null, 'a']);
  assert.deepEqual(result.empty, []);
  assert.deepEqual(result.noNodes, []);
});

test('drawer actions write back clarification and resync TAPD with API payloads', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const issue = { ...app.evaluate('structuredClone(state.data.issues[0])'), source: 'tapd',
    externalRef: { externalId: 'T-1', externalUrl: 'https://tapd.example/T-1' },
    clarification: { status: 'ready', writeBackStatus: 'pending' } };
  const written = { ...issue, clarification: { status: 'ready', writeBackStatus: 'succeeded' } };
  const synced = { ...written, title: 'Synced from TAPD' };
  app.router.post('/api/issues/issue-1/clarification', written);
  app.router.post('/api/issues/issue-1/resync-tapd', synced);
  app.router.post('/api/issues/issue-1/chat-clarify', {
    issue: { ...written, clarification: { status: 'ready', summary: 'Clarified', writeBackStatus: 'succeeded' } },
    ready: true
  });
  app.router.get('/api/issues/issue-1/chat-clarify', {}, 404);
  app.router.post('/api/issues/issue-1/chat-clarify/cancel', { ok: true });
  app.evaluate(`state.data.issues=[${JSON.stringify(issue)}];
    state.issueDetailData={issue:state.data.issues[0],comments:[],run:null,task:null,requirement:null};
    state.issueDetailTab='clarify';state.drawerOpen=true;renderIssueDetailTab()`);
  await app.evaluate(`retryIssueClarificationWriteBack()`);
  await settle(3);
  assert.equal(app.evaluate(`state.issueDetailData.issue.clarification.writeBackStatus`), 'succeeded');
  await app.evaluate(`resyncTAPDIssueById('issue-1')`);
  await settle(3);
  assert.equal(app.evaluate(`state.data.issues[0].title`), 'Synced from TAPD');
  app.evaluate(`
    state.issueDetailData.issue.clarification={status:'in_progress',questions:[
      {question:'Browser?',options:['Chrome','Safari'],multiSelect:false}],history:[]};
    state.issueDetailTab='clarify';renderIssueDetailTab();
    document.querySelector('.issue-clarify-option')?.click();
    const input=document.querySelector('#issueClarifyInput');if(input)input.value='Safari details';
  `);
  assert.match(app.evaluate(`collectIssueClarifyReply()`), /Chrome|Safari details/);
  await app.evaluate(`sendIssueClarification('Safari is required')`);
  assert.equal(app.evaluate(`state.issueDetailData.issue.clarification.status`), 'ready');
  await app.evaluate(`attachIssueClarifyStream('issue-1')`);
  app.evaluate(`
    openIssueClarification(false,false);
    const box=document.querySelector('#issueClarifyThinking')||document.body.appendChild(Object.assign(document.createElement('div'),{id:'issueClarifyThinking'}));
    appendIssueClarifyThought(box,{kind:'status',text:'Checking repository'});
    appendIssueClarifyThought(box,{kind:'tool',tool:'search',detail:'issues'});
  `);
  assert.match(app.document.querySelector('#issueClarifyThinking').textContent, /Checking repository/);
  app.evaluate(`stopIssueClarification('issue-1')`);
  await settle(2);
  const calls = app.router.calls.filter(c => c.method === 'POST');
  assert.equal(calls.find(c => c.path.endsWith('/clarification')).body.retry, true);
  assert.equal(calls.find(c => c.path.endsWith('/resync-tapd')).path, '/api/issues/issue-1/resync-tapd');
  assert.equal(calls.find(c => c.path.endsWith('/chat-clarify')).body.message, 'Safari is required');
});
