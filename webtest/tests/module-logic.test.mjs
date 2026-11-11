import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('issue drawer panes render clarification, SOP, delivery, and activity data', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const rendered = app.evaluate(`
    (() => {
      const issue={...state.data.issues[0], status:'completed', source:'tapd', externalId:'TAPD-9',
        clarification:{status:'completed',summary:'Clear objective',questions:[
          {id:'q1',question:'Which browser?',options:['Chrome','Firefox'],required:true}
        ],history:[{role:'assistant',content:'Please clarify'},{role:'user',content:'Chrome'}]},
        operations:[{action:'closed',createdAt:'2026-10-21T10:00:00Z',userName:'Tester'}]};
      const run={...state.data.runs[0],status:'succeeded',artifacts:[{name:'report',path:'report.md'}]};
      const project=state.data.projects[0],wf=state.data.workflows[0];
      const data={issue,run,task:state.data.tasks[0],workflow:wf,repositories:state.data.repositories,
        activities:[{type:'run_succeeded',createdAt:'2026-10-21T10:00:00Z'}],artifacts:run.artifacts};
      state.issueDetailData=data;
      return [
        clarificationOverviewCard(issue), clarifyQuestionCardsHTML(issue.clarification.questions,{}),
        clarifyHistoryHTML(issue.clarification),
        clarifyPane(issue,project), sopPane(issue,data), artifactsPane(data), deliveryPane(issue,data),
        acceptancePane(issue,data), operationsPane(issue), activityPane(issue,data),
        issueProgressActionsHTML(issue,run,{includeIssueActions:true})
      ].join('');
    })()
  `);
  assert.match(rendered, /Clear objective|Chrome/);
  assert.match(rendered, /report|完成/);
});

test('orchestration parsing and merge helpers preserve a valid workflow', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`
    (() => {
      const raw={name:'Generated delivery',nodes:[
        {id:'a',name:'Plan',type:'agent',position:{x:0,y:0}},
        {id:'b',name:'Review',type:'approval',position:{x:0,y:0}}
      ],edges:[{source:'a',target:'b'}]};
      const normalized=normalizeAIWorkflow(raw);
      const parsed=parseStreamingWorkflow('{"name":"Generated delivery","nodes":[{"id":"a","name":"Plan","type":"agent"}],"edges":[]}');
      const shell=shellForAIWorkflow(raw);
      const compact=compactWorkflowForAI(state.data.workflows[0]);
      const merged=mergeAIEdit(state.data.workflows[0],raw,'replace implementation',[]);
      return {normalized,parsed,shell,compact,merged,prompt:aiOrchestratePrompt('add review',compact,[])};
    })()
  `);
  assert.equal(result.normalized.nodes.length, 2);
  assert.match(result.prompt, /add review/);
  assert.ok(result.merged.nodes.length >= 2);
  await settle();
});

test('asset editors render the selected asset of every asset type', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  for (const [type, name] of [['rules', 'Code style'], ['skills', 'Testing'], ['dockerfiles', 'Node image'],
    ['environments', 'Node setup'], ['sandboxes', 'Restricted']]) {
    app.evaluate(`switchView(${JSON.stringify(type)});state.assetType=${JSON.stringify(type)};
      state.selectedAsset=state.data[${JSON.stringify(type)}][0].id;renderAssets();renderAssetDetail({force:true})`);
    await settle(2);
    assert.match(app.document.querySelector('#assetsView').textContent, new RegExp(name), type);
  }
});

test('node properties panel shows the selected agent node and its bound role', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`switchView('flow');state.selectedNode='node-1';renderProperties()`);
  await settle(2);
  const text = app.document.querySelector('#properties').textContent;
  assert.match(text, /Agent 步骤 · node-1/);
  assert.match(text, /Developer/);
  assert.match(text, /这个节点没有声明需要校验的产物/);
});
