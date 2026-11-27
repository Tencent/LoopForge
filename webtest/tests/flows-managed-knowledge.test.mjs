import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

test('flow editor only offers node knowledge bindings to CLI roles', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(()=>{
    switchView('flow');
    state.data.agents=[
      {id:'cli-role',projectId:'project-1',name:'CLI 角色',prompt:'work'},
      {id:'a2a-role',projectId:'project-1',name:'A2A 角色',prompt:'work',executorId:'a2a'},
      {id:'wma-role',projectId:'project-1',name:'WMA 角色',prompt:'work',executorId:'wma'}
    ];
    state.workflow={id:'kb-managed-flow',projectId:'project-1',name:'KB',status:'draft',version:1,
      sandboxProfileId:'sandbox-1',nodes:[
        {id:'n-cli',name:'CLI',type:'agent',agentId:'cli-role',position:{x:20,y:20}},
        {id:'n-a2a',name:'A2A',type:'agent',agentId:'a2a-role',position:{x:220,y:20}},
        {id:'n-wma',name:'WMA',type:'agent',agentId:'wma-role',
         knowledgeRefs:[{connectorId:'kc-legacy',mode:'all'}],position:{x:420,y:20}}
      ],edges:[]};
    state.workflowDirty=false;renderAll();
    const probe=id=>{state.selectedNode=id;renderProperties();
      const host=$('#properties');
      return {button:!!host.querySelector('#nodeKnowledgeRefs'),text:host.textContent||''};};
    return {cli:probe('n-cli'),a2a:probe('n-a2a'),wma:probe('n-wma')};
  })()`);

  assert.equal(result.cli.button, true, 'CLI role keeps the 关联知识库 button');
  assert.equal(result.a2a.button, false, 'A2A managed role must not offer 关联知识库');
  assert.match(result.a2a.text, /托管 Agent（WMA \/ A2A）在服务端检索/);
  assert.equal(result.wma.button, false, 'WMA managed role must not offer 关联知识库');
  assert.match(result.wma.text, /托管 Agent 不会使用节点知识库/);
  assert.match(result.wma.text, /1 个知识库/, 'the stale binding count is surfaced');
});

test('managed node with a stale knowledge binding can clear it in place', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(()=>{
    switchView('flow');
    state.data.agents=[{id:'wma-role',projectId:'project-1',name:'WMA 角色',prompt:'work',executorId:'wma'}];
    state.workflow={id:'kb-clear-flow',projectId:'project-1',name:'KB',status:'draft',version:1,
      sandboxProfileId:'sandbox-1',nodes:[
        {id:'n-wma',name:'WMA',type:'agent',agentId:'wma-role',
         knowledgeRefs:[{connectorId:'kc-a',mode:'all'},{connectorId:'kc-b',mode:'all'}],position:{x:20,y:20}},
        {id:'n-plain',name:'Plain',type:'agent',agentId:'wma-role',position:{x:220,y:20}}
      ],edges:[]};
    state.workflowDirty=false;renderAll();
    state.selectedNode='n-plain';renderProperties();
    const plainButton=!!$('#properties').querySelector('#clearNodeKnowledgeRefs');
    state.selectedNode='n-wma';renderProperties();
    const button=$('#properties').querySelector('#clearNodeKnowledgeRefs');
    const label=button?.textContent||'';
    button?.click();
    const node=state.workflow.nodes.find(n=>n.id==='n-wma');
    const after=$('#properties');
    const cleared={refs:node.knowledgeRefs.length,dirty:state.workflowDirty,
      button:!!after.querySelector('#clearNodeKnowledgeRefs'),text:after.textContent||''};
    flowHistory.undo?.();
    const undone=state.workflow.nodes.find(n=>n.id==='n-wma').knowledgeRefs?.length||0;
    return {plainButton,hasButton:!!button,label,cleared,undone};
  })()`);

  assert.equal(result.plainButton, false, 'no clear button when nothing is bound');
  assert.equal(result.hasButton, true, 'stale binding offers a clear button next to the hint');
  assert.match(result.label, /清空已关联的 2 个知识库/);
  assert.equal(result.cleared.refs, 0, 'clicking clears the node bindings');
  assert.equal(result.cleared.dirty, true, 'clearing marks the flow dirty so it can be saved');
  assert.equal(result.cleared.button, false, 'button disappears once cleared');
  assert.match(result.cleared.text, /托管 Agent（WMA \/ A2A）在服务端检索/);
  assert.equal(result.undone, 2, 'clearing is undoable');
});
