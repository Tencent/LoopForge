import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// #1356: a project with a chosen default SOP (project.workflowId) used to see
// "当前项目还没有 SOP" on the templates page, even though new requirements
// dispatch through that default. The project-tab empty state must (a) show the
// default SOP with its source and a view entry, and (b) qualify the empty copy
// to self-created or copied SOPs only.
test('project default SOP banner renders above the qualified project empty state', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  // Project selected, default SOP points at a generic template, project owns no SOP.
  app.evaluate(`
    state.projectFilter='project-1';
    state.sopScope='project';
    state.data.projects=[{id:'project-1',name:'Atlas',workflowId:'generic-backend-delivery'}];
    state.data.workflows=[{id:'generic-backend-delivery',name:'通用后端交付 SOP',generic:true,status:'published',version:2,nodes:[]}];
    renderSOPCatalog();
  `);
  await settle(2);
  const banner = app.document.querySelector('#sopCatalog .sop-default-banner');
  assert.ok(banner, 'default SOP banner missing');
  assert.match(banner.textContent, /项目默认 SOP/);
  assert.match(banner.textContent, /通用 · 通用后端交付 SOP/);
  assert.match(banner.textContent, /内置 SOP/);
  assert.match(banner.textContent, /新需求使用默认流程，已关联需求保留原绑定/);
  const viewGeneric = banner.querySelector('[data-adopt-generic="generic-backend-delivery"]');
  assert.ok(viewGeneric, 'generic default SOP must offer a template view entry');

  const empty = app.document.querySelector('#sopCatalog .sop-empty');
  assert.ok(empty, 'empty state missing');
  assert.match(empty.textContent, /还没有自建或复制的 LOOP/);
  assert.doesNotMatch(empty.textContent, /当前项目还没有 LOOP。/);

  // A shared SOP as the project default names its source project.
  app.evaluate(`
    state.data.projects=[{id:'project-1',name:'Atlas',workflowId:'flow-2'}];
    state.data.workflows=[{id:'flow-2',name:'Shared flow',projectId:'project-2',shared:true,status:'published',version:1,nodes:[]},
      {id:'project-2',name:'Other',workflowId:''}];
    state.data.projects.push({id:'project-2',name:'Other'});
    renderSOPCatalog();
  `);
  const sharedBanner = app.document.querySelector('#sopCatalog .sop-default-banner');
  assert.ok(sharedBanner);
  assert.match(sharedBanner.textContent, /团队共享 · 来自 Other/);
  const viewShared = sharedBanner.querySelector('[data-open-sop="flow-2"]');
  assert.ok(viewShared, 'shared default SOP must offer a view entry');

  // A project-owned default (usual case): the card itself already represents
  // the default in the project list, so the catalog renders the card and no
  // banner is needed.
  app.evaluate(`
    state.data.projects=[{id:'project-1',name:'Atlas',workflowId:'flow-9'}];
    state.data.workflows=[{id:'flow-9',name:'Owned flow',projectId:'project-1',status:'draft',version:1,nodes:[]}];
    renderSOPCatalog();
  `);
  const ownedBanner = app.document.querySelector('#sopCatalog .sop-default-banner');
  assert.equal(ownedBanner, null, 'an owned default SOP renders as its own card');
  assert.ok(app.document.querySelector('#sopCatalog .sop-card'));
});

test('the banner disappears once the project has its own SOP cards and on other scopes', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  // Project owns a SOP card: the catalog renders cards, no banner.
  app.evaluate(`
    state.projectFilter='project-1';
    state.sopScope='project';
    state.data.projects=[{id:'project-1',name:'Atlas',workflowId:'flow-1'}];
    state.data.workflows=[{id:'flow-1',name:'Delivery Flow',projectId:'project-1',status:'published',version:1,nodes:[]}];
    renderSOPCatalog();
  `);
  await settle(2);
  assert.equal(app.document.querySelector('#sopCatalog .sop-default-banner'), null);
  assert.ok(app.document.querySelector('#sopCatalog .sop-card'));

  // Shared / generic scopes keep their original empty copy without the banner.
  app.evaluate(`
    state.sopScope='shared';
    state.data.workflows=[];
    renderSOPCatalog();
  `);
  assert.equal(app.document.querySelector('#sopCatalog .sop-default-banner'), null);
  assert.match(app.document.querySelector('#sopCatalog .sop-empty').textContent, /还没有共享给当前项目的 LOOP/);

  // No project selected: no banner even when the list is empty.
  app.evaluate(`
    state.sopScope='project';
    state.projectFilter='';
    state.data.projects=[{id:'project-1',name:'Atlas',workflowId:'flow-1'}];
    renderSOPCatalog();
  `);
  assert.equal(app.document.querySelector('#sopCatalog .sop-default-banner'), null);
});
