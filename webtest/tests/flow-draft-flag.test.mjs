import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// #1358: the designer header must distinguish an unsaved working copy from the
// published version it is based on. A published v2 flow with unsaved edits
// shows a draft marker instead of blending into the published pill; the save
// button label names its object; a clean published flow shows neither.
test('designer header marks the unsaved working copy separately from the published version', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  app.evaluate(`
    state.projectFilter='project-1';
    switchView('flow');
    state.workflow={id:'published-flow',projectId:'project-1',name:'Published flow',status:'published',version:2,revision:4,publishedRevision:4,
      nodes:[{id:'n1',name:'A',type:'script',position:{x:0,y:0}},{id:'n2',name:'B',type:'approval',position:{x:200,y:0}}],
      edges:[]};
    markWorkflowClean();
  `);
  await settle(2);
  const meta = () => app.document.querySelector('#workflowMeta');

  // Clean published flow: published pill only, no draft flag.
  assert.match(meta().textContent, /已发布/);
  assert.doesNotMatch(meta().textContent, /未保存草稿/);
  assert.doesNotMatch(meta().textContent, /有未保存草稿/);
  assert.equal(app.document.querySelector('#saveStateLabel').textContent, '已保存');
  assert.equal(app.document.querySelector('#saveWorkflow').disabled, true);

  // Six-step unsaved working copy on top of published v2: pill flips to warn
  // tone with the combined label and a separate draft flag naming the object.
  app.evaluate(`
    state.workflow.nodes.push({id:'n3',name:'C',type:'script',position:{x:400,y:0}},
      {id:'n4',name:'D',type:'script',position:{x:600,y:0}},
      {id:'n5',name:'E',type:'script',position:{x:800,y:0}},
      {id:'n6',name:'F',type:'script',position:{x:1000,y:0}});
    state.workflow.description='WIP';
    dirty();
  `);
  const metaDirty = meta();
  const pill = metaDirty.querySelector('.fd-pill');
  assert.ok(pill.classList.contains('is-unsaved'), 'published pill must flip to the unsaved tone');
  assert.match(pill.textContent, /已发布 · 有未保存草稿/);
  assert.match(metaDirty.textContent, /未保存草稿/);
  assert.match(metaDirty.querySelector('.v-step').textContent, /6 步/);
  assert.match(metaDirty.textContent, /v2/);
  assert.equal(app.document.querySelector('#saveStateLabel').textContent, '保存');
  assert.equal(app.document.querySelector('#saveWorkflow').disabled, false);
  assert.equal(app.document.querySelector('#discardWorkflow').hidden, false);

  // A draft (never published) flow keeps its plain draft pill and also gets
  // the draft flag when dirty.
  app.evaluate(`
    state.workflow={id:'draft-flow',projectId:'project-1',name:'Draft flow',status:'draft',version:1,revision:1,
      nodes:[{id:'a',name:'A',type:'script',position:{x:0,y:0}}],edges:[]};
    markWorkflowClean();
  `);
  assert.match(meta().textContent, /草稿/);
  assert.doesNotMatch(meta().textContent, /未保存草稿/);
  app.evaluate(`dirty()`);
  const draftMeta = meta();
  assert.match(draftMeta.textContent, /未保存草稿/);
  assert.ok(draftMeta.querySelector('.fd-pill').classList.contains('is-unsaved'));

  // Read-only shared SOP: no draft flag even though the working copy differs.
  app.evaluate(`
    state.workflow={id:'shared-flow',projectId:'other-project',name:'Shared',status:'published',version:3,revision:2,publishedRevision:2,nodes:[],edges:[]};
    markWorkflowClean();
    state.workflowDirty=true;
    renderFlowHeader();
  `);
  const roMeta = meta();
  assert.doesNotMatch(roMeta.textContent, /未保存草稿/);
  assert.ok(roMeta.querySelector('.fd-pill').classList.contains('ro'));
});
