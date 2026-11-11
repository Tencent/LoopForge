import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('issue model derives lanes, health, stages, outcomes, and action markup', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    state.data.workflows=[
      {id:'wf-product',name:'产品交付',sopLane:'product',nodes:[{id:'p1',name:'定义'}],edges:[]},
      {id:'wf-back',name:'后端交付',tags:['backend'],nodes:[{id:'b1',name:'实现'}],edges:[]},
      {id:'wf-fix',name:'缺陷修复',nodes:[{id:'f1',name:'修复'}],edges:[]}
    ];
    state.data.projects=[{id:'p1',name:'项目',workflowId:'wf-back',
      sopBindings:[{workflowId:'wf-product',order:2},{workflowId:'wf-back',order:1}]}];
    const base={id:'i1',projectId:'p1',kind:'requirement',source:'local',status:'backlog',
      title:'需求',workflowId:'wf-product',assigneeUserId:1,
      sopBindings:[{id:'s1',workflowId:'wf-product',order:1,assigneeUserId:1},
                   {id:'s2',workflowId:'wf-back',order:2,assigneeUserId:2}]};
    state.data.issues=[base];
    state.data.tasks=[];
    state.data.runs=[];
    const laneNames=clientIssueLanes(base).map(x=>[x.lane,x.status,x.label]);
    const inferred=['产品','generic-frontend delivery','设计交付','requirement-development','issue-fix','misc']
      .map((name,i)=>inferSOPLane({id:'x'+i,name}));
    const playbooks=normalizePlaybookList([
      {id:'one',name:'套餐一',default:true,bindings:[{workflowId:'wf-product'}]},
      {id:'one',name:'重复',default:true,bindings:[]},
      {id:'two',name:'套餐二',default:true,bindings:[{workflowId:'wf-back',assigneeUserId:'2'}]}
    ]);
    const running={id:'r1',workflowId:'wf-product',status:'running',createdAt:'2026-01-01',
      executionContext:{issueId:'i1'},workflowSnapshot:state.data.workflows[0],
      nodes:{p1:{status:'running',attempt:1}}};
    state.data.runs=[running];
    const activeStage=issueCurrentStage(base);
    const activeActor=issueActor(base);
    const activeOutcome=issueOutcome(base);
    const failed={...running,id:'r2',status:'failed',createdAt:'2026-02-01',
      nodes:{p1:{status:'failed',error:'tests failed'}}};
    state.data.runs=[failed];
    const failedStage=issueCurrentStage(base);
    const failedOutcome=issueOutcome(base);
    const waiting={...running,id:'r3',status:'waiting',createdAt:'2026-03-01',
      nodes:{p1:{status:'waiting_approval'}}};
    state.data.runs=[waiting];
    const waitingStage=issueCurrentStage(base);
    const mrIssue={...base,deliveryState:'merged',deliveryMrState:'merged',
      deliveryMrs:[{url:'https://git.example.com/team/repo/-/merge_requests/17',state:'merged',repository:'repo'}]};
    const closed={...base,status:'closed',closeSource:'manual'};
    return {
      normalized:['产品','front','后端','uiux','bugfix','strange'].map(normalizeSOPLane),
      inferred,laneNames,playbooks,
      bindingOrder:projectSOPBindings(state.data.projects[0]).map(x=>x.workflowId),
      names:playbookNames(state.data.projects[0]),
      quick:issueSOPQuickAdds(base,new Set(['wf-product'])).map(x=>x.workflowId),
      activeStage,activeActor,activeOutcome,failedStage,failedOutcome,waitingStage,
      mrList:deliveryMRList(mrIssue),mrLabel:deliveryLockedLabel(mrIssue),
      mrButtons:deliveryMRButtons(mrIssue),block:deliveryBlockReason(mrIssue),
      closedOutcome:issueOutcome(closed),reopen:reopenDeliveredButton(closed),
      triggerClosed:triggerSOPButton(closed),retrigger:canRetriggerIssue(mrIssue,failed),
      retriggerHTML:retriggerButton(mrIssue,failed),
      chips:[laneStatusLabel('attention'),sopLaneLabel('repair'),issueOwnerSummary(base)]
    };
  })()`));
  assert.deepEqual(result.normalized, ['product','frontend','backend','design','repair','other']);
  assert.deepEqual(result.inferred, ['other','frontend','design','backend','repair','other']);
  assert.deepEqual(result.bindingOrder, ['wf-back','wf-product']);
  assert.equal(result.playbooks.length, 2);
  assert.equal(result.playbooks.filter(x => x.default).length, 1);
  assert.deepEqual(result.laneNames, [['product','not_started','产品交付'],['backend','not_started','后端交付']]);
  assert.deepEqual(result.quick, ['wf-back','wf-fix']);
  assert.equal(result.activeStage.label, '定义');
  assert.equal(result.activeActor.title, 'Agent 执行中');
  assert.equal(result.activeOutcome.key, 'active');
  assert.match(result.failedStage.label, /失败/);
  assert.equal(result.failedOutcome.title, '运行失败');
  assert.equal(result.waitingStage.key, 'waiting');
  assert.equal(result.mrList[0].iid, 17);
  assert.equal(result.mrLabel, 'MR 已合入');
  assert.match(result.mrButtons, /merge_requests\/17/);
  assert.match(result.block, /MR 已合入/);
  assert.equal(result.closedOutcome.title, '需求已关闭');
  assert.match(result.reopen, /重新打开/);
  assert.match(result.triggerClosed, /关闭/);
  assert.equal(result.retrigger, true);
  assert.match(result.retriggerHTML, /重新触发/);
  assert.deepEqual(result.chips, ['需要介入','修复','未指定 · +1']);
  const edge = structuredClone(app.evaluate(`(() => {
    const base={id:'edge',projectId:'p1',title:'Edge',source:'local',kind:'requirement',
      status:'backlog',workflowId:'wf-product',clarification:{status:'ready'}};
    state.data.issues=[base];state.data.tasks=[];state.data.runs=[];
    const stages=[];
    stages.push(issueCurrentStage({...base,status:'closed',closeSource:'manual'}).label);
    stages.push(issueCurrentStage({...base,status:'done'}).label);
    stages.push(issueCurrentStage(base).label);
    state.data.tasks=[{id:'t-edge',issueId:'edge',status:'queued'}];
    stages.push(issueCurrentStage(base).label);
    state.data.tasks=[];state.data.runs=[{id:'success',workflowId:'wf-product',status:'succeeded',
      executionContext:{issueId:'edge'},nodes:{p1:{status:'succeeded'}}}];
    stages.push(issueCurrentStage(base).label);
    state.data.runs=[{id:'cancel',workflowId:'wf-product',status:'cancelled',
      executionContext:{issueId:'edge'},nodes:{p1:{status:'cancelled'}}}];
    stages.push(issueCurrentStage(base).label);
    state.data.tasks=[];state.data.runs=[];
    const asked={...base,clarification:{status:'in_progress',questions:[{id:'q1',question:'Q?',options:['A','B']}]}};
    stages.push(issueCurrentStage(asked).label,issueCurrentStage(asked).key,issueRunHealth(asked));
    stages.push(issueCurrentStage({...base,clarification:{status:'in_progress',questions:[]}}).label);
    const askedTrigger=triggerSOPButton(asked);
    const actionKinds=[
      issueActionButtons({...base,status:'closed',closeSource:'manual'}),
      issueActionButtons({...base,clarification:{status:'analyzing'}}),
      issueActionButtons({...base,clarification:{status:'needs_user',gated:true}})
    ];
    const triggers=[
      triggerSOPButton({...base,clarification:{status:'ready',writeBackStatus:'pending'},source:'tapd',
        externalRef:{externalId:'T-1'}}),
      triggerSOPButton({...base,clarification:{status:'needs_user',gated:true}})
    ];
    return {stages,actionKinds,triggers,askedTrigger,externals:[
      issueExternalState({externalRef:{externalState:' 已关闭 '}}),
      issueExternallyDone({externalRef:{externalState:'已完成'}}),
      issueInPlanning({externalRef:{externalState:'planning'}})
    ]};
  })()`));
  assert.deepEqual(edge.stages, ['已关闭','已完成','澄清已完成','排队中','交付闭环','已取消 · 定义','待澄清','clarifying','not_started','等待执行']);
  assert.match(edge.askedTrigger, /data-dispatch-issue/);
  assert.match(edge.actionKinds.join(' '), /更多操作/);
  assert.match(edge.triggers[0], /确认结论/);
  assert.match(edge.triggers[1], /触发/);
  assert.deepEqual(edge.externals, ['已关闭',true,true]);
  app.evaluate(`
    state.data.issues=[{id:'menu-edge',projectId:'p1',title:'Menu issue',status:'backlog',
      priority:'medium',closureAllowed:true,deletionAllowed:true,workflowId:'wf-product'}];
    state.projectFilter='p1';renderIssues();
    const button=document.querySelector('[data-issue-menu]');
    showIssueActionMenu({preventDefault(){},stopPropagation(){},currentTarget:button},'menu-edge');
  `);
  assert.match(app.document.querySelector('#issueActionPopover').textContent, /编辑需求/);
  assert.match(app.document.querySelector('#issueActionPopover').textContent, /关闭需求/);
});
