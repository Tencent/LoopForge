import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('run helpers merge streams and format timing, output, logs, telemetry, preview, and workspace states', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const evaluation = {
    id: 'eval-x', status: 'succeeded', score: 82, passed: true, model: 'model-x',
    policy: { version: 2, threshold: 75, dimensions: [{ id: 'control', name: 'Control', question: 'Healthy?', weight: 100 }] },
    result: { dimensions: { control: 82 }, summary: 'Solid run',
      findings: [{ severity: 'warn', dimension: 'control', message: 'One retry', nodeId: 'b', evidence: 'attempt 2' }],
      recommendations: [{ id: 'rec-1', title: 'Tune node', rationale: 'Reduce retries',
        target: 'workflow', action: 'workflow.update_node', nodeId: 'b', set: { maxAttempts: 2 } }] }
  };
  app.router.get('/api/runs/run-x/eval', evaluation);
  app.router.post('/api/runs/run-x/eval', { ...evaluation, status: 'running' });
  app.router.get('/api/assistant/models', {
    engines: [{id:'codex',name:'Codex',personal:true}],
    models: [{id:'codex:model-x',name:'Model X',engine:'codex'}],
  });
  app.router.post('/api/runs/run-x/eval/apply', {
    ...evaluation, result: { ...evaluation.result,
      recommendations: [{ ...evaluation.result.recommendations[0], status: 'applied', appliedTargetId: 'flow-1' }] }
  });
  app.router.get('/api/workflows', app.evaluate('structuredClone(state.data.workflows)'));
  app.router.get('/api/agents', app.evaluate('structuredClone(state.data.agents)'));
  app.router.get('/api/runs/run-x/artifacts/art-1', '# Report\\n\\n```js\\nconsole.log("ok")\\n```');
  app.router.get('/api/runs/run-x/artifacts/art-zip', 'PK\u0003\u0004\u0000\u0000# not markdown');
  app.router.get('/api/runs/run-x/artifacts/art-ndjson', '{"id":1}\n');
  app.router.get('/api/runs/child-1', {
    id: 'child-1', status: 'waiting', workflowId: 'child-flow',
    workflowSnapshot: { id: 'child-flow', name: 'Child Flow', nodes: [{ id: 'approve', name: 'Approve' }] },
    nodes: { approve: { status: 'waiting_approval' } }
  });
  app.router.get(/^\/api\/runs\/run-live\/logs\?/, {
    logs: [{ sequence: 3, body: 'delta log', context: { nodeId: '' } }]
  });
  app.router.get(/^\/api\/runs\/run-live\/telemetry\?/, {
    events: [{ id: 'delta', sequence: 3, nodeId: '', category: 'output', name: 'answer', message: 'delta' }]
  });
  app.router.get('/api/runs/run-live/logs.txt', 'worker logs');
  app.router.get('/api/runs/run-select', {
    id: 'run-select', status: 'succeeded', workflowId: 'flow-1', createdAt: '2026-02-01',
    workflowSnapshot: { id: 'flow-1', name: 'Selected', nodes: [], edges: [] }, nodes: {}
  });
  app.router.get('/api/runs/run-x/preview/download', () => ({
    status: 200, body: 'zip-data', headers: { 'Content-Type': 'application/zip' }
  }));
  app.router.put('/api/runs/run-x/preview/backend', {
    preview: { status: 'ready', entryFile: 'index.html', fileCount: 4 },
    session: { url: 'https://preview.example/new', backendUrl: 'https://new-api.example' }
  });
  app.router.route('HEAD', '/probe', '', 200);
  const result = structuredClone(app.evaluate(`(() => {
    const run={id:'run-x',status:'failed',createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T00:05:00Z',
      workflowId:'flow-1',workflowSnapshot:{id:'flow-1',name:'Flow',preview:{enabled:true,sourcePath:'dist'},
        nodes:[{id:'a',name:'Build'},{id:'b',name:'Review'}],edges:[{source:'a',target:'b'}]},
      nodes:{a:{status:'succeeded',attempt:1,finishedAt:'2026-01-01T00:03:00Z'},
             b:{status:'failed',attempt:2,error:'gate failed',finishedAt:'2026-01-01T00:05:00Z'}},
      usage:{totalTokens:12500,inputTokens:9000,outputTokens:3500,costUsd:.25},toolCalls:4};
    state.activeRun=run;state.selectedRun='run-x';state.data.runs=[run];
    state.runLogs={runId:'run-x',nodeId:'',items:[{sequence:1,body:'first'}],maxSeq:1,loaded:true};
    state.runTelemetry={runId:'run-x',nodeId:'',adapter:'live',items:[
      {id:'t1',sequence:1,nodeId:'a',category:'reasoning',name:'think',message:'one'}],maxSeq:1,loaded:true};
    const merged=mergeLiveRun('run-x',{...run,status:'running',nodes:{b:{status:'running',activity:'retry'}}},{
      logDelta:[{sequence:2,body:'second',context:{nodeId:''}}],
      telemetryDelta:[{id:'t2',sequence:2,nodeId:'a',category:'output',name:'answer',message:'two'}]});
    const outputs=[
      displayNodeOutput('{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Final answer"}]}}'),
      displayNodeOutput('{"type":"result","result":"Done"}'),
      displayNodeOutput('{"role":"assistant","text":"partial\\\\ntext"'),
      displayNodeOutput('{"x":1}')
    ];
    const logs=[
      formatLogBody({body:'Bash 完成',attributes:{input:{command:'npm test'}}}),
      formatLogBody({body:'Read\\\\npackage main\\\\nfunc main() {}',attributes:{input:{path:'main.go'}}}),
      formatLogBody({level:'error',body:'fatal failure'}),
      formatLogBody({body:'工具完成'})
    ];
    const stages=workflowStages({nodes:[{id:'a'},{id:'b'},{id:'c'}],
      edges:[{source:'a',target:'b'},{source:'a',target:'c'}]}).map(s=>s.map(n=>n.id));
    state.runWorkspace={runId:'run-x',cleaned:false,loading:false,error:'',info:{available:true}};
    const workspace=workspacePaneHTML(run);
    state.runPreview={runId:'run-x',loading:false,error:'',info:{status:'ready',entryFile:'index.html',fileCount:4},
      session:{url:'http://preview.example/x',backendUrl:'https://api.example'}};
    const preview=previewPaneHTML(run);
    const statuses=['pending','failed','skipped','expired','unknown'].map(status=>{
      state.runPreview={runId:'run-x',loading:false,error:'',info:{status,error:'why'},session:null};
      return previewPaneHTML(run);
    });
    const gate=gateResultCardHTML({status:'failed',error:'command failed',
      gateResult:{decision:'pass',reason:'agent done',summary:'summary',findings:[{title:'Tests',evidence:'red'}]}});
    const compact=compactTelemetry([
      {sequence:1,nodeId:'a',category:'reasoning',name:'x',message:'A'},
      {sequence:2,nodeId:'a',category:'reasoning',name:'x',message:'B'},
      {sequence:3,nodeId:'a',category:'tool',name:'y',message:'C'}]);
    return {
      mergedStatus:merged.nodes.b.status,mergedOutput:merged.nodes.b.error,
      logsNow:state.runLogs.items.map(x=>x.sequence),telemetryNow:state.runTelemetry.items.map(x=>x.sequence),
      sorted:sortRunsDescending([{id:'a',createdAt:'2025-01-01'},{id:'b',createdAt:'2026-01-01'}]).map(x=>x.id),
      progress:runProgress(run),stages,timing:runTiming(run),timingTitle:runTimingTitle(run),
      trigger:[taskTriggerInfo(null),taskTriggerInfo({dispatchMode:'rerun'}),
        taskTriggerInfo({dispatchMode:'scheduled',scheduleJobId:'night',triggerOrigin:'timer'})],
      tokens:[formatTokens(12),formatTokens(12500),formatTokens(2500000)],
      outputs,logs,workspace,preview,statuses,gate,compact,
      adapters:[matchTelemetryAdapter({source:{adapter:'agent-hooks-v1'}},'hooks'),
        matchTelemetryAdapter({source:{adapter:'codebuddy-otel-v1'}},'live')],
      urls:[vscodeWorkbenchURL('run x',{embed:true}),vscodeWindowName('run x!'),
        previewCandidateHttpsURL('http://example.com:80/path')],
      clean:[runWorkspaceWasCleaned({workspaceCleanedAt:'1970-01-01'}),runWorkspaceWasCleaned({workspaceCleanedAt:'2026-01-01'})],
      page:[runPageTarget(run,'a'),runDisplayStatus({...run,status:'paused'}),runAwaitsConfirmation({...run,status:'waiting'})]
    };
  })()`));
  assert.equal(result.mergedStatus, 'running');
  assert.equal(result.mergedOutput, 'gate failed');
  assert.deepEqual(result.logsNow, [1,2]);
  assert.deepEqual(result.telemetryNow, [1,2]);
  assert.deepEqual(result.sorted, ['b','a']);
  assert.deepEqual(result.progress, { done: 2, total: 2, pct: 100 });
  assert.deepEqual(result.stages, [['a'],['b','c']]);
  assert.equal(result.timing.execution, 300000);
  assert.match(result.timingTitle, /端到端/);
  assert.deepEqual(result.trigger.map(x => x.label), ['直接运行','手动重跑','定时触发 · night']);
  assert.deepEqual(result.tokens, ['12','13k','2.5M']);
  assert.deepEqual(result.outputs.slice(0,3), ['Final answer','Done','partial\ntext']);
  assert.match(result.outputs[3], /"x": 1/);
  assert.deepEqual(result.logs, ['Bash npm test','Read\\npackage main.go','fatal failure','']);
  assert.match(result.workspace, /vscode\.html/);
  assert.match(result.preview, /index\.html/);
  assert.match(result.statuses[0], /正在生成/);
  assert.match(result.statuses[1], /生成失败/);
  assert.match(result.statuses[2], /已跳过/);
  assert.match(result.statuses[3], /已过期/);
  assert.match(result.statuses[4], /尚未就绪/);
  assert.match(result.gate, /command failed/);
  assert.equal(result.compact[0].message, 'AB');
  assert.deepEqual(result.adapters, [true,false]);
  assert.deepEqual(result.urls, ['/vscode.html?run=run+x&embed=1','agentflow-vscode-runx','https://example.com/path']);
  assert.deepEqual(result.clean, [false,true]);
  assert.equal(result.page[0].runId, 'run-x');
  assert.equal(result.page[1], '已暂停');
  assert.equal(result.page[2], true);
  app.evaluate(`
    state.activeRun={...state.activeRun,status:'failed',artifacts:[
      {id:'art-1',nodeId:'b',name:'report.md',path:'out/report.md',attempt:2,size:1200,sha256:'abcdef',mediaType:'text/markdown'},
      {id:'art-bin',nodeId:'b',name:'model',path:'out/model.bin',attempt:2,size:20971520,sha256:'fedcba',mediaType:'application/octet-stream'},
      {id:'art-image',nodeId:'b',name:'chart',path:'out/chart.png',attempt:2,size:1048576,sha256:'ab',mediaType:'image/png'},
      {id:'art-zip',nodeId:'b',name:'pkg',path:'out/pkg.zip',attempt:2,size:2048,sha256:'aa',mediaType:'text/markdown'},
      {id:'art-ndjson',nodeId:'b',name:'rows',path:'out/rows.ndjson',attempt:2,size:9,sha256:'bb',mediaType:'application/x-ndjson'}]};
    state.selectedRun='run-x';state.currentUser.codeBuddyApiKeyConfigured=true;
    const host=document.querySelector('#runDetail');host.dataset.runId='run-x';
    host.innerHTML='<div data-run-pane="eval"></div>';
  `);
  await app.evaluate(`loadRunEval('run-x',false)`);
  assert.match(app.document.querySelector('[data-run-pane="eval"]').textContent, /Solid run/);
  app.evaluate(`window.afConfirm=async()=>true`);
  await app.evaluate(`applyRunEvalRec(state.activeRun,'rec-1')`);
  const applyCall = app.router.calls.find(c => c.path.endsWith('/eval/apply'));
  assert.deepEqual(applyCall.body.recommendationIds, ['rec-1']);
  await app.evaluate(`previewArtifact(state.activeRun,'art-1')`);
  const artifactModal = app.document.querySelector('#artifactModal');
  assert.equal(artifactModal.open, true);
  assert.match(app.document.querySelector('#artifactPreview').textContent, /Report/);
  Object.defineProperty(app.window, 'isSecureContext', { value: true });
  const copyArtifact = artifactModal.querySelector('[data-copy-artifact]');
  await copyArtifact.onclick();
  assert.match(artifactModal.querySelector('.toast-item-msg').textContent, /产物内容已复制到剪贴板/);
  const writeText = app.window.navigator.clipboard.writeText;
  const execCommand = app.document.execCommand;
  let fallbackText;
  app.window.navigator.clipboard.writeText = async () => { throw new Error('Clipboard unavailable'); };
  app.document.execCommand = command => {
    fallbackText = artifactModal.querySelector('textarea')?.value;
    return command === 'copy';
  };
  copyArtifact.focus();
  try {
    await copyArtifact.onclick();
    assert.match(fallbackText, /# Report/);
    assert.equal(artifactModal.querySelector('textarea'), null);
    assert.equal(app.document.activeElement, copyArtifact);
  } finally {
    app.window.navigator.clipboard.writeText = writeText;
    app.document.execCommand = execCommand;
  }
  app.document.querySelector('#artifactPreview [data-mode="source"]')?.click();
  assert.match(app.document.querySelector('#artifactPreview .artifact-content').textContent, /console\.log/);
  const downloads = [];
  const clickAnchor = app.window.HTMLAnchorElement.prototype.click;
  app.window.HTMLAnchorElement.prototype.click = function () { downloads.push([this.getAttribute('href'), this.download]); };
  const download = artifactModal.querySelector('[data-download-artifact]');
  assert.equal(download.tagName, 'BUTTON');
  download.click();
  assert.deepEqual(downloads.at(-1), ['/api/runs/run-x/artifacts/art-1', 'report.md']);
  artifactModal.close();
  assert.equal(artifactModal.querySelector('.toast-item'), null);
  const artifactCalls = () => app.router.calls.filter(c => c.path.includes('/artifacts/')).length;
  const callsBefore = artifactCalls();
  await app.evaluate(`previewArtifact(state.activeRun,'art-bin')`);
  assert.equal(artifactModal.open, true);
  assert.equal(artifactCalls(), callsBefore, 'an oversized artifact is not read into the page');
  assert.match(app.document.querySelector('#artifactPreview').textContent, /不在页面内预览/);
  assert.match(app.document.querySelector('#artifactPreview').textContent, /超过 5 MiB/);
  artifactModal.querySelector('[data-download-artifact]').click();
  assert.deepEqual(downloads.at(-1), ['/api/runs/run-x/artifacts/art-bin', 'model.bin']);
  artifactModal.close();
  await app.evaluate(`previewArtifact(state.activeRun,'art-image')`);
  assert.equal(artifactCalls(), callsBefore, 'a declared binary artifact is not read into the page');
  artifactModal.querySelector('[data-download-artifact]').click();
  assert.deepEqual(downloads.at(-1), ['/api/runs/run-x/artifacts/art-image', 'chart.png']);
  artifactModal.close();
  await app.evaluate(`previewArtifact(state.activeRun,'art-zip')`);
  assert.match(app.document.querySelector('#artifactPreview').textContent, /不在页面内预览/);
  assert.match(app.document.querySelector('#artifactPreview').textContent, /无法按文本显示/);
  artifactModal.querySelector('[data-download-artifact]').click();
  assert.deepEqual(downloads.at(-1), ['/api/runs/run-x/artifacts/art-zip', 'pkg.zip']);
  artifactModal.close();
  await app.evaluate(`previewArtifact(state.activeRun,'art-ndjson')`);
  assert.match(app.document.querySelector('#artifactPreview .artifact-content').textContent, /"id":1/);
  app.window.HTMLAnchorElement.prototype.click = clickAnchor;
  artifactModal.close();
  app.evaluate(`toast('Outside artifact preview')`);
  assert.match(app.document.querySelector('#toastContainer').textContent, /Outside artifact preview/);
  app.evaluate(`
    const pane=document.createElement('div');
    pane.innerHTML='<button data-preview-download>Download</button><button data-preview-refresh></button><button data-preview-copy></button><button data-preview-open></button><button data-preview-backend-edit></button>';
    document.body.appendChild(pane);state.runPreview={runId:'run-x',loading:false,
      info:{status:'ready'},session:{url:'https://preview.example/x',backendUrl:'https://api.example'}};
    bindPreviewPane(state.activeRun,pane);window.__previewPane=pane;
  `);
  app.document.querySelector('[data-preview-copy]').click();
  app.document.querySelector('[data-preview-open]').click();
  app.document.querySelector('[data-preview-download]').click();
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(app.document.querySelector('[data-preview-download]').disabled, false);
  app.evaluate(`window.afPrompt=async()=> 'https://new-api.example'`);
  await app.evaluate(`editPreviewBackend(state.activeRun)`);
  assert.equal(app.evaluate(`state.runPreview.backendUrl`), 'https://new-api.example');
  assert.equal(await app.evaluate(`resolvePreviewSessionURL('https://preview.example/probe')`), 'https://preview.example/probe');

  app.router.get('/api/runs/run-x', { ...app.evaluate('structuredClone(state.activeRun)'), status: 'cancelled' });
  app.router.post('/api/runs/run-x/cancel', { ok: true });
  await app.evaluate(`runAction('run-x','cancel',{})`);
  assert.equal(app.router.calls.find(c => c.path.endsWith('/cancel')).method, 'POST');
  app.evaluate(`
    state.activeRun={id:'run-live',status:'running',nodes:{},telemetry:[]};
    state.selectedRun='run-live';state.data.runs=[state.activeRun];
    startLiveRun('run-live');
    const es=EventSource.instances.at(-1);
    es.emit({run:{id:'run-live',status:'succeeded',nodes:{},telemetry:[]},
      telemetryDelta:[],logDelta:[]},'run');
  `);
  assert.equal(app.evaluate(`state.activeRun.status`), 'succeeded');
  app.evaluate(`
    const parent={id:'parent-1',status:'waiting',workflowId:'flow-parent',
      workflowSnapshot:{id:'flow-parent',name:'Parent',nodes:[{id:'sub',name:'Nested LOOP'}],edges:[]},
      nodes:{sub:{status:'running',childRunId:'child-1'}}};
    state.activeRun=parent;state.selectedRun='parent-1';state.data.runs=[parent];
    state.childRuns={'child-1':{run:{id:'child-1',status:'waiting',workflowId:'child-flow',
      workflowSnapshot:{name:'Child Flow',nodes:[{id:'approve',name:'Approve'}]},
      nodes:{approve:{status:'waiting_approval'}}},at:Date.now(),loading:false}};
    window.__child={
      focus:childRunFocus(childRunState('child-1')),
      graph:graphNodeView(parent,'sub'),
      panel:childSOPPanelHTML(parent,'sub'),
      callout:childSOPCalloutHTML(parent),
      ids:refreshChildRuns(parent)
    };
  `);
  const child = structuredClone(app.evaluate(`window.__child`));
  assert.equal(child.focus.node.id, 'approve');
  assert.match(child.graph.text, /子 LOOP/);
  assert.match(child.panel, /Child Flow/);
  assert.match(child.callout, /人工审批/);
  assert.deepEqual(child.ids, ['child-1']);
  app.evaluate(`
    state.activeRun={id:'run-live',status:'running',nodes:{},telemetry:[]};state.selectedRun='run-live';state.selectedRunNode='';
    state.runLogs={runId:'run-live',nodeId:'',items:[],maxSeq:0,loaded:true};
    state.runTelemetry={runId:'run-live',nodeId:'',adapter:'live',items:[],maxSeq:0,loaded:true};
  `);
  await app.evaluate(`refreshRunPagesDelta('run-live')`);
  assert.deepEqual(structuredClone(app.evaluate(`state.runLogs.items.map(x=>x.sequence)`)), [3]);
  assert.deepEqual(structuredClone(app.evaluate(`state.runTelemetry.items.map(x=>x.sequence)`)), [3]);
  app.evaluate(`
    state.runWorkspace={runId:'run-live',cleaned:false,info:{available:true},maximized:false};
    onWorkspaceVscodeMessage({origin:location.origin,data:{type:'agentflow-vscode',runId:'run-live',action:'maximize',maximized:true}});
    HTMLAnchorElement.prototype.click=function(){};
  `);
  assert.equal(app.evaluate(`state.runWorkspace.maximized`), true);
  await app.evaluate(`downloadRunLogs('run-live')`);
  assert.equal(app.router.calls.find(c => c.path.endsWith('/logs.txt')).method, 'GET');
  app.evaluate(`
    const evalRun={id:'run-x',status:'failed',workflowId:'flow-1',nodes:{}};
    state.activeRun=evalRun;state.selectedRun='run-x';
    const evalHost=document.querySelector('#runDetail');
    evalHost.innerHTML='<div data-run-pane="eval">'+runEvalPaneHTML(evalRun)+'</div>';
    bindRunEvalPane(evalRun,evalHost);
  `);
  const submittingEvaluation=app.evaluate(`startRunEval(state.activeRun)`);
  app.evaluate(`{
    const host=document.querySelector('#runDetail');
    host.querySelector('[data-run-pane="eval"]').innerHTML=runEvalPaneHTML(state.activeRun);
    bindRunEvalPane(state.activeRun,host);
  }`);
  await submittingEvaluation;
  assert.equal(app.router.calls.find(c => c.method === 'POST' && c.path === '/api/runs/run-x/eval')?.body.model, 'codex:model-x');
  app.evaluate(`stopRunEvalStream()`);
  app.evaluate(`
    state.data.runs=[{id:'run-select',status:'succeeded',workflowId:'flow-1',createdAt:'2026-02-01',nodes:{}}];
    selectRun('run-select');
  `);
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(app.evaluate(`state.activeRun.id`), 'run-select');
});

test('run activation waits for workspace restoration, deduplicates intent, and freezes expected node state', async (t) => {
  let activationCalls = 0;
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/runs/run-1/retry', () => {
    activationCalls++;
    return activationCalls === 1
      ? { workspaceRestore: { status: 'restoring' } }
      : { id: 'run-1', status: 'running' };
  });
  app.router.get('/api/runs/run-1/workspace/restore', { status: 'ready' });
  app.evaluate(`
    const run={id:'run-1',status:'failed',nodes:{'node-1':{status:'failed',attempt:2,reworkCount:1}}};
    state.data.runs=[run];state.activeRun=run;state.selectedRun='run-1';
    const host=document.querySelector('#runDetail');host.dataset.runId='run-1';
    host.innerHTML='<div class="run-detail-header"></div><button data-retry-node>Retry</button>';
  `);
  const values = await app.evaluate(`Promise.all([
    requestRunActivation('run-1','retry',{nodeId:'node-1',reason:'continue'}),
    requestRunActivation('run-1','retry',{nodeId:'node-1',reason:'continue'})
  ])`);
  assert.equal(values[0].status, 'running');
  assert.equal(values[1].status, 'running');
  assert.equal(activationCalls, 2);
  const posts = app.router.calls.filter(c => c.method === 'POST' && c.path.endsWith('/retry'));
  assert.equal(posts[0].body.expected.status, 'failed');
  assert.equal(posts[0].body.expected.attempt, 2);
  assert.equal(posts[0].body.expected.reworkCount, 1);
  assert.equal(app.document.querySelector('[data-run-activation-status]'), null);
  assert.equal(app.document.querySelector('[data-retry-node]').disabled, false);
});

test('run list empty states distinguish search, filters, loading, and true-empty projects', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`
    const pid=state.projectFilter||state.data.projects[0].id;
    state.data.runs=[{id:'run-1',workflowId:'flow-1',status:'succeeded',input:'登录页修复',
      createdAt:new Date().toISOString(),executionContext:{projectId:pid},usage:{totalTokens:100},
      nodes:{a:{status:'succeeded'}},workflowSnapshot:{name:'Delivery Flow'}}];
    state.runFilter='all';state.runQuery='';state.runWorkflowFilter='';renderRuns();
    state.runQuery='不存在的关键词';renderRuns();
    const searched=document.querySelector('#runList .run-list-empty');
    const searchedTitle=searched?.querySelector('b')?.textContent||'';
    const searchedHint=searched?.querySelector('p')?.textContent||'';
    const searchedActions=Boolean(searched?.querySelector('.run-empty-actions'));
    state.runQuery='';state.runWorkflowFilter='flow-9';renderRuns();
    const filteredTitle=document.querySelector('#runList .run-list-empty b')?.textContent||'';
    state.runWorkflowFilter='';state.runQuery='';state.data.runs=null;renderRuns();
    const loadingTitle=document.querySelector('#runList .run-list-empty b')?.textContent||'';
    state.runQuery='';state.runWorkflowFilter='';state.data.runs=[];renderRuns();
    const scopedEmpty=document.querySelector('#runList .run-list-empty')?.textContent||'';
    JSON.stringify({scopedEmpty,searchedTitle,searchedHint,searchedActions,filteredTitle,loadingTitle});
  `));
  assert.ok(out.scopedEmpty.includes('还没有运行'), `true-empty project should guide to SOPs, got: ${out.scopedEmpty}`);
  assert.equal(out.searchedTitle, '未找到与当前关键词匹配的运行');
  assert.ok(out.searchedHint.includes('清除搜索'), `search empty should suggest clearing search, got: ${out.searchedHint}`);
  assert.equal(out.searchedActions, false, 'search empty should not show go-to-SOP actions');
  assert.equal(out.filteredTitle, '当前筛选下没有匹配的运行');
  assert.equal(out.loadingTitle, '正在加载运行记录');
});
