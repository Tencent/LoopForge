import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('approval helpers explain gates, failures, pauses, and contextual payloads', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`
    (() => {
      const run={id:'approval-run',status:'waiting',workflowId:'flow-1',pauseRequested:false,
        workflowSnapshot:{id:'flow-1',name:'交付 LOOP',version:1,revision:1,nodes:[
          {id:'build',name:'构建',type:'script'},
          {id:'review',name:'人工验收',type:'approval',rejectTo:'build',taskPrompt:'检查测试结果'},
          {id:'ship',name:'发布',type:'agent'}],
          edges:[{source:'build',target:'review'},{source:'review',target:'ship'}]},
        nodes:{review:{status:'waiting_approval',gateResult:{decision:'human',actionRequired:'检查截图',
          reason:'视觉差异',summary:'需要确认',findings:[{title:'按钮颜色',evidence:'截图 1'}]}}}};
      state.data.runs=[run];state.activeRun=run;
      return {
        waiting:waitingNodeId(run),
        reason:approvalWaitReason(run,'review'),
        sections:humanGateSectionsHTML(run.nodes.review,run,run.workflowSnapshot.nodes[1]),
        buttons:waitingActionButtons(run.id,'review',run.nodes.review,run.workflowSnapshot.nodes[1],run.workflowSnapshot.nodes),
        context:withHumanContext({nodeId:'review',reason:'人工确认',context:'请保留日志'}),
        paused:pausedRunPanelHTML({...run,pauseRequested:true,nodes:{review:{status:'waiting_approval',activity:'用户暂停'}}}),
        recovery:failedNodeRecoveryCalloutHTML(run.id,{id:'build',name:'构建',type:'script'},{error:'exit 1'}),
        agentRecovery:failedNodeRecoveryCalloutHTML(run.id,{id:'ship',name:'发布',type:'agent'},
          {error:'CodeBuddy API Key invalid',gateResult:{decision:'pass',summary:'已实现',findings:[{title:'校验',evidence:'失败'}]}}),
        callout:approvalCalloutHTML(run.workflowSnapshot.nodes[1],run.nodes.review,run.workflowSnapshot.nodes),
        retryPanel:failedResumePanelHTML({...run,status:'failed',nodes:{build:{status:'failed'}},
          workflowSnapshot:{...run.workflowSnapshot,nodes:[{id:'build',name:'构建',type:'script'}]}}),
        pausedNodes:pausedWorkflowNodes({...run,pauseRequested:true,nodes:{review:{status:'waiting_approval',activity:'用户暂停'}}}).map(n=>n.id),
        refresh:refreshRunSOPButtonHTML({...run,status:'waiting'})
      };
    })()
  `);
  assert.equal(result.waiting, 'review');
  assert.match(result.reason, /「构建」已完成/);
  assert.match(result.reason, /确认后继续「发布」/);
  assert.match(result.sections, /检查截图|视觉差异|按钮颜色|截图 1/);
  assert.match(result.buttons, /退回上游「构建」/);
  assert.match(result.context.reason, /人工补充上下文/);
  assert.match(result.paused, /运行已暂停/);
  assert.match(result.recovery, /手动通过，继续下游/);
  assert.match(result.agentRecovery, /补充信息，重跑当前节点/);
  assert.match(result.agentRecovery, /去配置 CodeBuddy API Key/);
  assert.match(result.callout, /等待人工确认/);
  assert.match(result.retryPanel, /重试失败节点/);
  assert.equal(JSON.stringify(result.pausedNodes), JSON.stringify(['review']));
});

test('approval list sends context and removes the approved run after refreshing', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = {
    id: 'approval-run', projectId: 'project-1', status: 'waiting', workflowId: 'flow-approval', createdAt: '2026-10-20T10:00:00Z',
    executionContext: { responsibleUserId: 7, projectId: 'project-1' },
    workflowSnapshot: {
      id: 'flow-approval', name: '交付 LOOP', version: 1, revision: 1,
      nodes: [{ id: 'review', name: '验收', type: 'approval', taskPrompt: '确认结果' }], edges: [],
    },
    nodes: { review: { status: 'waiting_approval' } },
  };
  app.router.get('/api/runs/approval-run', run);
  app.router.post('/api/runs/approval-run/approve', { ok: true });
  app.router.get('/api/runs', [{ ...run, status: 'running' }]);
  app.router.get('/api/tasks', []);
  app.router.get('/api/issues', []);
  app.router.post('/api/runs/approval-run/retry', { ...run, status: 'running' });
  app.router.post('/api/runs/approval-run/resume', { ...run, status: 'running' });
  app.router.post('/api/runs/approval-run/pause', { ok: true });
  app.evaluate(`
    state.currentUser={id:7,loginName:'reviewer'};
    state.data.runs=${JSON.stringify([run])};
    state.data.tasks=[];state.data.issues=[];
  `);
  await app.evaluate(`renderApprovals(true)`);
  const card = app.document.querySelector('#approvalList .ap-card');
  assert.match(card.textContent, /交付 LOOP/);
  assert.match(card.textContent, /为什么需要人工确认/);
  card.querySelector('[data-human-context]').value = '验收通过，保留截图';
  card.querySelector('[data-approve]').click();
  await settle(6);
  const call = app.router.calls.find(c => c.path === '/api/runs/approval-run/approve');
  assert.equal(call.method, 'POST');
  assert.equal(call.body.context, '验收通过，保留截图');
  assert.equal(app.document.querySelector('#approvalList .ap-card'), null);
  assert.match(app.document.querySelector('#approvalList').textContent, /没有需要你拍板的需求/);

  app.evaluate(`
    state.data.runs=[${JSON.stringify(run)}];
    state.activeRun=state.data.runs[0];
    const root=document.createElement('div');root.className='human-decision';
    root.innerHTML='<textarea data-human-context>补充测试日志</textarea><button id="retry-source"></button>';
    document.body.append(root);
  `);
  await app.evaluate(`retryWaitingNode('approval-run','review',document.querySelector('#retry-source'))`);
  assert.equal(app.router.calls.find(c => c.path === '/api/runs/approval-run/retry').body.context, '补充测试日志');
  await app.evaluate(`resumePausedRun('approval-run',document.querySelector('#retry-source'))`);
  assert.equal(app.router.calls.find(c => c.path === '/api/runs/approval-run/resume').body.context, '补充测试日志');
  const patched = app.evaluate(`patchCachedRun('approval-run',{pauseRequested:true});state.activeRun.pauseRequested`);
  assert.equal(patched, true);
  app.evaluate(`window.__pauseRun=pauseRun('approval-run')`);
  await settle(2);
  app.document.querySelector('#confirmOkBtn').click();
  await app.evaluate(`window.__pauseRun`);
  assert.ok(app.router.calls.some(c => c.method === 'POST' && c.path === '/api/runs/approval-run/pause'));
});

test('skip reasons keep route labels verbatim while naming upstream nodes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const texts = app.evaluate(`
    (() => {
      const wf={nodes:[{id:'classify',name:'Classify'},{id:'small',name:'Small branch'},{id:'large',name:'Large branch'},
        {id:'classification',name:'Another node'},{id:'size',name:'Size node'}]};
      const numeric={nodes:[{id:'0',name:'零点节点'},{id:'small',name:'Small branch'}]};
      const punctuation={nodes:[...numeric.nodes,{id:'build；check',name:'Build Check'}]};
      const nestedGraph={nodes:[{id:'a',name:'First'},{id:'b',name:'Second'},{id:'a 已跳过（b',name:'Unrelated'}],
        edges:[{source:'a',target:'join'},{source:'b',target:'a'}]};
      const joinedReason='a 已跳过；b 已跳过';
      const ambiguousRoute='a 路由 [small] 未选择此分支；b 路由 [large] 未选择此分支';
      const joinedGraph={nodes:[...nestedGraph.nodes,{id:'a 已跳过；b',name:'Combined'}],
        edges:[{source:'a',target:'join'},{source:'b',target:'join'},{source:'a 已跳过；b',target:'join'}]};
      const quotedArtifact='a 的产物 p 字段 /x 未匹配条件 "x 未匹配条件 true；b 路由 [y"；c 路由 [z] 未选择此分支';
      const quotedGraph={nodes:[...joinedGraph.nodes,{id:'c',name:'Third'}],
        edges:['a','b','c'].map(source=>({source,target:'join'}))};
      const join={id:'join',name:'Join'};
      return {
        route: skipReasonText({skipReason:'classify 路由 [small] 未选择此分支'},wf,{name:'Large branch'}),
        artifact: skipReasonText({skipReason:'classify 的产物 classification 字段 /size 未匹配条件 "small"'},wf,{name:'Large branch'}),
        plain: skipReasonText({skipReason:'classify 已跳过'},wf,{name:'Large branch'}),
        nested: skipReasonText({skipReason:'small 已跳过（classify 的产物 classification 字段 /size 未匹配条件 "small；large"）；large 已命中其他条件，未选择默认分支'},wf,{name:'Join'}),
        numeric: skipReasonText({skipReason:'0 路由 [small] 未选择此分支'},numeric,{name:'Small branch'}),
        graphNested: skipReasonText({skipReason:'a 已跳过（b 路由 [small] 未选择此分支）'},nestedGraph,join),
        ambiguousJoin: skipReasonText({skipReason:joinedReason},joinedGraph,join),
        ambiguousRoute: skipReasonText({skipReason:ambiguousRoute},joinedGraph,join),
        ambiguousQuotedArtifact: skipReasonText({skipReason:quotedArtifact},quotedGraph,join),
        overlappingMarkers: skipReasonText({skipReason:quotedArtifact.replace('x 未匹配条件 true','x 未匹配条件 未匹配条件 true')},quotedGraph,join),
        uniqueJoin: skipReasonText({skipReason:joinedReason},{...joinedGraph,edges:joinedGraph.edges.slice(0,2)},join),
        uniqueNested: skipReasonText({skipReason:'a 已跳过（b 路由 [small] 未选择此分支）'},
          {...nestedGraph,edges:[...nestedGraph.edges,{source:'a 已跳过（b',target:'join'}]},join),
        punctuationId: skipReasonText({skipReason:'build；check 已跳过'},punctuation,{name:'Join'}),
        punctuationPointer: skipReasonText({skipReason:'build；check 的产物 classification 字段 /field；0 已跳过 未匹配条件 true；0 已跳过'},punctuation,{name:'Join'}),
      };
    })()
  `);
  // The upstream node keeps its display name; the reported route label does not.
  assert.equal(texts.route, '上游Classify 路由 [small] 未选择此分支，因此跳过 Large branch');
  assert.equal(texts.artifact, '上游Classify 的产物 classification 字段 /size 未匹配条件 "small"，因此跳过 Large branch');
  assert.equal(texts.plain, '上游Classify 已跳过，因此跳过 Large branch');
  assert.equal(texts.nested, '上游Small branch 已跳过（Classify 的产物 classification 字段 /size 未匹配条件 "small；large"）；Large branch 已命中其他条件，未选择默认分支，因此跳过 Join');
  // A numeric node id must not corrupt the protected route label.
  assert.equal(texts.numeric, '上游零点节点 路由 [small] 未选择此分支，因此跳过 Small branch');
  assert.equal(texts.graphNested, '上游First 已跳过（Second 路由 [small] 未选择此分支），因此跳过 Join');
  assert.equal(texts.ambiguousJoin, 'a 已跳过；b 已跳过');
  assert.equal(texts.ambiguousRoute, 'a 路由 [small] 未选择此分支；b 路由 [large] 未选择此分支');
  assert.equal(texts.ambiguousQuotedArtifact, 'a 的产物 p 字段 /x 未匹配条件 "x 未匹配条件 true；b 路由 [y"；c 路由 [z] 未选择此分支');
  assert.equal(texts.overlappingMarkers, 'a 的产物 p 字段 /x 未匹配条件 "x 未匹配条件 未匹配条件 true；b 路由 [y"；c 路由 [z] 未选择此分支');
  assert.equal(texts.uniqueJoin, '上游First 已跳过；Second 已跳过，因此跳过 Join');
  assert.equal(texts.uniqueNested, texts.graphNested);
  assert.equal(texts.punctuationId, '上游Build Check 已跳过，因此跳过 Join');
  assert.equal(texts.punctuationPointer, '上游Build Check 的产物 classification 字段 /field；0 已跳过 未匹配条件 true；零点节点 已跳过，因此跳过 Join');
});
