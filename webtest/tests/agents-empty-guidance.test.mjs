import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// #1370: the roles page must explain what a role is for, where it is used,
// and give a concrete example in the no-role empty state; a search with no
// matches gets its own copy that points at adjusting the search.
test('roles page heading and empty states explain purpose, example, and next steps', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  // Heading subtitle states the purpose and where roles get picked.
  app.evaluate(`switchView('agents')`);
  await settle(2);
  const subtitle = app.document.querySelector('#agentsView .page-heading p');
  assert.match(subtitle.textContent, /角色是供流程节点选用的 AI 岗位/);
  assert.match(subtitle.textContent, /流程模板/);

  // No roles, no search: the empty state carries the work example, both
  // entry points, and the "where to use it" hint.
  app.evaluate(`
    state.projectFilter='project-1';
    state.agentScope='project';
    state.data.agents=[];
    const search=$('#agentSearch');if(search)search.value='';
    renderAgents();
  `);
  const empty = app.document.querySelector('#agentList .catalog-empty');
  assert.ok(empty, 'empty state missing');
  assert.match(empty.textContent, /当前项目还没有角色/);
  assert.match(empty.textContent, /独立评审/);
  assert.match(empty.textContent, /检查实现与风险/);
  assert.match(empty.textContent, /评审报告/);
  assert.match(empty.textContent, /流程模板/);
  const createBtn = empty.querySelector('[data-agent-empty-new]');
  const browseBtn = empty.querySelector('[data-agent-empty-browse]');
  assert.ok(createBtn, 'create entry missing');
  assert.ok(browseBtn, 'browse shared entry missing');
  assert.match(createBtn.textContent, /新建角色/);
  assert.match(browseBtn.textContent, /浏览共享角色/);

  // The browse button switches to the shared scope.
  browseBtn.click();
  assert.equal(app.evaluate(`state.agentScope`), 'shared');
  const sharedEmpty = app.document.querySelector('#agentList .catalog-empty');
  assert.match(sharedEmpty.textContent, /还没有共享给当前项目的角色/);

  // Shared scope, no search: shared-specific empty copy (no example block).
  app.evaluate(`
    state.data.agents=[];
    renderAgents();
  `);
  assert.match(app.document.querySelector('#agentList .catalog-empty').textContent, /还没有共享给当前项目的角色/);

  // Search with no matches but existing agents: independent search-empty copy.
  app.evaluate(`
    state.agentScope='project';
    state.data.agents=[{id:'agent-1',projectId:'project-1',name:'Developer',description:'Builds',version:1}];
    $('#agentSearch').value='不存在的角色';
    renderAgents();
  `);
  const searchEmpty = app.document.querySelector('#agentList .catalog-empty');
  assert.match(searchEmpty.textContent, /没有匹配的角色/);
  assert.match(searchEmpty.textContent, /调整搜索关键词/);
  assert.equal(searchEmpty.querySelector('[data-agent-empty-new]'), null, 'search empty state must not show the create/browse actions');
  assert.doesNotMatch(searchEmpty.textContent, /独立评审/);
});
