import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('business report helpers sort, fill date gaps, and render delivery charts', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const values = app.evaluate(`
    (() => {
      businessState.from='2026-10-01';businessState.to='2026-10-03';businessState.sort='completed';
      businessState.report={grain:'day'};
      const points=businessChartPoints([
        {date:'2026-10-01',completedIssues:2,mergedMrs:1,runs:3,cycleSamples:1,averageCycleDurationMs:3600000},
        {date:'2026-10-03',completedIssues:1,mergedMrs:0,runs:1,cycleSamples:1,averageCycleDurationMs:7200000}
      ]);
      return {
        date:businessDate(new Date(2026,9,3)),
        cost:businessCost(2.5,false),
        cycle:businessCycleLabel({cycleSamples:1,averageCycleDurationMs:3600000}),
        order:businessSortItems([{name:'B',deliveryMetrics:{completedIssues:1}},{name:'A',deliveryMetrics:{completedIssues:3}}]).map(x=>x.name),
        points,
        delivery:businessDeliveryChart(points),
        cycleChart:businessDeliveryChart(points,true),
        runs:businessTrendChart(points)
      };
    })()
  `);
  assert.equal(values.date, '2026-10-03');
  assert.equal(values.cost, '¥2.50（部分未识别）');
  assert.match(values.cycle, /1(?:\.0)?h|60m/);
  assert.equal(JSON.stringify(values.order), JSON.stringify(['A', 'B']));
  assert.equal(values.points.length, 3);
  assert.equal(values.points[1].completedIssues, 0);
  assert.match(values.delivery, /完成需求/);
  assert.match(values.cycleChart, /平均/);
  assert.match(values.runs, /运行次数/);
});

test('business-domain management searches users and saves a new domain', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/business-domains', (call) => ({
    items: [{ domain: call.body, canView: true, canManage: true }], canAdmin: true, userId: 1,
  }));
  app.router.get('/api/business-domains', {
    items: [{ domain: { id: 'new-domain', name: 'New Domain', description: '', active: true,
      managerIds: [2], viewerIds: [] }, canView: true, canManage: true }],
    canAdmin: true, userId: 1,
  });
  app.evaluate(`
    state.currentUser={id:1,loginName:'admin',isPlatformAdmin:true};
    state.data.users=[{id:1,loginName:'admin',chineseName:'管理员'},{id:2,loginName:'alice',chineseName:'爱丽丝'}];
    businessState.items=[];businessState.canAdmin=true;businessState.userId=1;businessState.editorId='';
    renderBusinessDomainManagement();
  `);
  const host = app.document.querySelector('#platformBusinessDomains');
  const managerSearch = host.querySelector('[data-user-picker="managerIds"] .business-user-search');
  managerSearch.value = '爱丽丝';
  managerSearch.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  host.querySelector('[data-user-picker="managerIds"] [data-add-user="2"]').click();
  const form = host.querySelector('#businessDomainForm');
  form.elements.name.value = 'New Domain';
  form.elements.id.value = 'new-domain';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const call = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/business-domains');
  assert.equal(call.search, '?createOnly=true');
  assert.equal(call.body.name, 'New Domain');
  assert.deepEqual(call.body.managerIds, [2]);
});

test('business report rerenders with the domain list when the report answers first', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const report = { grain: 'day', summary: {}, deliveryMetrics: {}, businessDomains: [], projects: [], deliveryTrend: [] };
  app.router.get('/api/business-domain-report', report);
  app.router.get('/api/projects', []);
  app.router.get('/api/business-domains', async () => {
    await new Promise((resolve) => setTimeout(resolve, 30));
    return { items: [{ domain: { id: 'pay', name: '支付业务', active: true }, canView: true, canManage: false }], canAdmin: false, userId: 1 };
  });
  app.evaluate(`businessState.items=[];businessState.scope='all';businessState.initialized=false;businessState.from='';businessState.to='';loadBusinessDomains()`);
  await new Promise((resolve) => setTimeout(resolve, 80));
  await settle(8);
  const scopes = [...app.document.querySelectorAll('#businessDomainsBody select[name="scope"] option')].map((o) => o.textContent);
  assert.ok(scopes.includes('支付业务'), JSON.stringify(scopes));
  assert.equal(app.evaluate('businessState.loading'), false);
});
