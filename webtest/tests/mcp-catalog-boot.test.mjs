import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// The MCP market page is gone, but its catalog still drives generic-template
// role MCP options, market-ID/alias mapping and the SOP dependency precheck.
// These tests never assign state.mcpMarket by hand: the catalog must arrive
// through the real boot path.
const catalog = {
  items: [
    { id: 'github', name: 'GitHub', aliases: ['github'], description: 'GitHub remote MCP', url: 'https://api.githubcopilot.com/mcp/' },
    // Market IDs that do not mention the service: only the alias identifies them.
    { id: 'mkt-7f3a', name: 'Code Review', aliases: ['gongfeng'], url: 'https://mcp.example.test/cr' },
    { id: 'mkt-91c2', name: 'Story Sync', aliases: ['tapd'], url: 'https://mcp.example.test/story' },
  ],
};

test('boot loads the MCP catalog for generic-template role options without visiting the MCP page', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/mcp-market', body: catalog }] });
  t.after(app.close);
  await settle(6);
  assert.ok(app.router.calls.some((call) => call.method === 'GET' && call.path === '/api/mcp-market'), 'boot requests the catalog');
  assert.equal(app.evaluate('state.mcpMarket.length'), 3);
  const options = app.evaluate('genericTemplateMCPOptions([])');
  assert.equal(JSON.stringify(options.map((o) => [o.id, o.name])), JSON.stringify([['github', 'GitHub']]), 'an unbound role can still pick GitHub');
  assert.equal(app.evaluate(`genericMCPChecked('github',['github'])`), true);
  assert.equal(app.evaluate(`genericMCPChecked('mkt-7f3a',['gongfeng'])`), true, 'aliases keep bound IDs checked');
});

test('generic-template role options keep GitHub when the catalog request fails', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/mcp-market', body: { body: { error: 'down' }, status: 500 } }] });
  t.after(app.close);
  await settle(6);
  assert.equal(app.evaluate('state.mcpMarket.length'), 0);
  assert.equal(JSON.stringify(app.evaluate('genericTemplateMCPOptions([])').map((o) => o.id)), '["github"]');
  app.router.get('/api/mcp-market', catalog);
  await app.evaluate('ensureMCPMarketCatalog()');
  assert.equal(app.evaluate('state.mcpMarket.length'), 3, 'a failed load is retried');
});

test('SOP precheck recognises market MCPs whose market ID does not name the service', async (t) => {
  const app = await loadApp({ routes: [{ path: '/api/mcp-market', body: catalog }] });
  t.after(app.close);
  await settle(6);
  const result = app.evaluate(`(() => {
    state.projectFilter='project-1';
    state.data.mcpServers=[
      {id:'a',scope:'project',projectId:'project-1',name:'Code Review',marketId:'mkt-7f3a'},
      {id:'b',scope:'project',projectId:'other',name:'Story Sync',marketId:'mkt-91c2'}
    ];
    const before={gongfeng:gongfengMCPInstalled(),tapd:tapdMCPInstalled()};
    state.data.mcpServers.push({id:'c',scope:'project',projectId:'project-1',name:'Story Sync',marketId:'tapd'});
    return {before,afterAlias:tapdMCPInstalled()};
  })()`);
  assert.equal(result.before.gongfeng, true, 'market ID mkt-7f3a maps to gongfeng through the catalog');
  assert.equal(result.before.tapd, false, 'an install in another project does not count');
  assert.equal(result.afterAlias, true, 'a row stored under the alias also matches');
});

test('MCP page offers uninstalled catalog entries and installs them into the project', async (t) => {
  const installed = { id: 'mcp-github-project-project-1', scope: 'project', projectId: 'project-1', marketId: 'github', name: 'GitHub' };
  const app = await loadApp({ routes: [
    { path: '/api/mcp-market', body: catalog },
    { path: '/api/mcp-servers', body: [] },
    { method: 'POST', path: '/api/mcp-market/install', body: installed },
  ] });
  t.after(app.close);
  app.evaluate(`state.projectFilter='project-1';state.mcpScope='project';switchView('mcp')`);
  await settle(8);
  const grid = app.document.querySelector('#mcpMarketGrid');
  assert.ok(grid.querySelector('.mcp-catalog-suggest'), 'catalog strip is shown');
  assert.equal(app.document.querySelector('[data-mcp-tab]'), null, 'no market tab comes back');
  grid.querySelector('[data-mcp-install="github"]').click();
  await settle(8);
  const call = app.router.calls.find((c) => c.method === 'POST' && c.path === '/api/mcp-market/install');
  assert.equal(JSON.stringify(call.body), JSON.stringify({ marketId: 'github', scope: 'project', projectId: 'project-1' }));
  assert.equal(grid.querySelector('[data-mcp-install="github"]'), null, 'an added entry leaves the strip');
  assert.match(grid.textContent, /GitHub/);

  app.evaluate(`state.mcpScope='personal';renderMCPManagement()`);
  assert.equal(grid.querySelector('.mcp-catalog-suggest'), null, 'personal scope only manages custom MCPs');
});
