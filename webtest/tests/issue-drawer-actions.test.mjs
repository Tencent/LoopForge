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
      clarification:{status:'in_progress',score:65,dimensions:{goal:80,scope:55},
        missing:['scope'],questions:[
          {question:'Browser?',options:['Chrome','Safari'],multiSelect:false},
          {question:'Checks?',options:['Unit','E2E'],multiSelect:true}],
        history:[
          {role:'assistant',content:JSON.stringify({questions:[{question:'Browser?'}]})},
          {role:'user',content:'Safari'}]}};
    const project={...state.data.projects[0],clarificationPolicy:{threshold:75,dimensions:[
      {id:'goal',name:'目标'},{id:'scope',name:'范围'}]}};
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
      score:clarifyScoreHTML(issue,project),
      overview:clarificationOverviewCard(issue),
      operations:operationsPane({...issue,createdAt:'2026-01-01',operations:[
        {at:'2026-01-03',action:'update',actor:'Alice',detail:'changed title'}]}),
      activity:activityPane(issue,state.issueDetailData),
      artifacts:artifactsPane({run:{...state.issueDetailData.run,artifacts:[
        {id:'a1',nodeId:'n1',name:'Report',path:'report.md',attempt:2,size:2048,sha256:'abcdef123456'}]}}),
      delivery:deliveryPane(issue,state.issueDetailData),
      acceptance:acceptancePane(issue,state.issueDetailData),
      dimensions:issueClarifyDimensions({...issue,source:'gongfeng'},project).map(x=>x.id),
      levels:[clarifyScoreLevel(90),clarifyScoreLevel(70),clarifyScoreLevel(20)],
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
  assert.match(result.score, /65/);
  assert.match(result.score, /范围/);
  assert.match(result.overview, /继续/);
  assert.match(result.operations, /编辑需求|changed title/);
  assert.match(result.activity, /Automated note/);
  assert.match(result.activity, /节点失败/);
  assert.match(result.artifacts, /Report/);
  assert.match(result.delivery, /feature\/chart/);
  assert.match(result.acceptance, /门禁失败/);
  assert.deepEqual(result.dimensions, ['phenomenon','impact','expected','acceptance','constraints']);
  assert.deepEqual(result.levels, ['good','fair','weak']);
  assert.deepEqual(result.labels, ['节点失败','运行 · custom','重新打开']);
  assert.equal(result.badTime, '—');
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
    issue: { ...written, clarification: { status: 'ready', score: 88, summary: 'Clarified', writeBackStatus: 'succeeded' } },
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
