import test from 'node:test';
import assert from 'node:assert/strict';
import {loadApp,settle} from '../lib/load-app.mjs';

function attachRun(app){
  const host=app.document.createElement('div');
  host.innerHTML='<div class="run-header-actions"></div><div class="run-header-meta"></div>';
  app.document.body.appendChild(host);app.window.__feedbackHost=host;
  app.evaluate(`state.projectFilter='project-1';state.selectedRunNode='code';evoDecorateRunDetail(window.__feedbackHost,{id:'feedback-run',status:'succeeded',executionContext:{projectId:'project-1'}})`);
  return host;
}
function submit(app,text){
  app.document.querySelector('#confirmPromptMultiline').value=text;
  app.document.querySelector('#confirmOkBtn').click();
}

test('feedback keeps its selected node and links to its actual problem without a separate check',async t=>{
  const detail='预期有 reason 字段，实际产物缺少 reason。';
  const finding={id:'feedback-finding',projectId:'project-1',assetKind:'agent',assetId:'role',assetName:'日志分析',status:'open',sampleRunIds:['feedback-run'],triage:{verdict:'watch'},clusters:[{id:'feedback-cluster',kind:'user_feedback',label:detail,runs:1}]};
  const app=await loadApp({routes:[
    {method:'POST',path:'/api/runs/feedback-run/feedback',body:{projectId:'project-1',findingId:finding.id,clusterId:'feedback-cluster',message:'反馈已加入RSI工作台。'}},
    {method:'GET',path:'/api/evolution/overview?projectId=project-1',body:{health:[],queue:[]}},
    {method:'GET',path:'/api/evolution/findings?projectId=project-1',body:[finding]},
    {method:'GET',path:'/api/evolution/findings/feedback-finding/runs?cluster=feedback-cluster',body:{items:[],complete:true,total:0}},
  ]});t.after(app.close);
  const host=attachRun(app);
  host.querySelector('[data-evo-feedback]').click();await settle(2);
  app.evaluate(`state.selectedRunNode='different-node'`);
  submit(app,detail);await settle(5);
  const request=app.router.calls.find(c=>c.path==='/api/runs/feedback-run/feedback');
  assert.deepEqual(request.body,{nodeId:'code',detail});
  assert.match(host.querySelector('.evo-feedback-result').textContent,/已加入RSI工作台/);
  host.querySelector('.evo-feedback-result button').click();await settle(8);
  assert.equal(app.window.location.hash,'#/evolution/findings/feedback-finding/feedback-cluster');
  assert.match(app.document.querySelector('#evoFindingTitle').textContent,/缺少 reason/);
  app.document.querySelector('[data-evo-source-example="user_feedback"]').click();
  assert.match(app.document.querySelector('#evoStorySourceExample').textContent,/缺少 reason/);
  assert.equal(app.router.calls.filter(c=>c.path==='/api/evolution/diagnose').length,0,'opening the saved feedback is a read-only action');
});

test('recorded-only feedback explains why instead of promising a generated problem',async t=>{
  const app=await loadApp({routes:[{method:'POST',path:'/api/runs/feedback-run/feedback',body:{projectId:'project-1',message:'反馈已保存。该节点未绑定已保存角色。<img src=x>'}}]});t.after(app.close);
  const host=attachRun(app);
  host.querySelector('[data-evo-feedback]').click();await settle(2);submit(app,'实际输出不符合预期');await settle(5);
  const result=host.querySelector('.evo-feedback-result');
  assert.match(result.textContent,/未绑定已保存角色/);
  assert.equal(result.querySelector('img'),null);
  assert.equal(result.querySelector('button'),null);
});
