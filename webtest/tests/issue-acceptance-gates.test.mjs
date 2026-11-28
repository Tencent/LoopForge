import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// The 验收 tab used to print "0 项命令 · " on every node and "0 项命令 ·  · 可打回"
// on the 人工验收 approval node: a separator was emitted even when nothing
// followed it, and the approval node (the human gate itself) was described as
// running zero commands.
test('acceptance gate pills list only the parts that apply and name the approval node as the human gate', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    state.data.agents=[...(state.data.agents||[]),
      {id:'impl',name:'开发实现',verification:[]},
      {id:'mr',name:'发起 MR',verification:[{id:'g1',mode:'command',commands:['git push'],required:true}]},
      {id:'qa',name:'人工复核',verification:[{id:'g2',mode:'manual',required:true},{id:'g3',mode:'command',commands:['make test','make lint'],required:true}]}];
    const wf={id:'flow-1',nodes:[
        {id:'implement',name:'开发实现',type:'agent',agentId:'impl'},
        {id:'qa',name:'人工复核',type:'agent',agentId:'qa',rejectTo:'implement'},
        {id:'accept',name:'人工验收',type:'approval',rejectTo:'implement'},
        {id:'mr',name:'发起 MR',type:'agent',agentId:'mr'}],
      edges:[{source:'implement',target:'qa'},{source:'qa',target:'accept'},{source:'accept',target:'mr'}]};
    const run={id:'run-1',status:'succeeded',workflowId:'flow-1',workflowSnapshot:wf,
      nodes:{implement:{status:'succeeded'},qa:{status:'succeeded'},accept:{status:'succeeded'},mr:{status:'succeeded'}}};
    const issue={...state.data.issues[0],source:'manual'};
    const rows=()=>{const box=document.createElement('div');
      box.innerHTML=acceptancePane(issue,{issue,run,task:null,requirement:null});
      return [...box.querySelectorAll('.node-progress-row')].map(r=>[r.querySelector('.node-name').textContent,r.querySelector('.pill').textContent,r.querySelector('.status').textContent]);};
    const zh=rows();
    const lfT0=window.lfT;
    window.lfT=(zhText,en)=>en;
    try{return {zh,en:rows().map(r=>r[1])}}finally{window.lfT=lfT0}
  })()`));
  assert.deepEqual(result.zh, [
    ['开发实现', '0 项命令', '完成'],
    ['人工复核', '1 项命令 · 需人工 · 可打回', '门禁通过'],
    ['人工验收', '人工门禁 · 可打回', '已批准'],
    ['发起 MR', '1 项命令', '门禁通过'],
  ]);
  for (const [, pill] of result.zh) assert.doesNotMatch(pill, /·\s*$|·\s*·/);
  assert.deepEqual(result.en, [
    '0 commands',
    '1 command · Needs human · Can send back',
    'Human gate · Can send back',
    '1 command',
  ]);
});
