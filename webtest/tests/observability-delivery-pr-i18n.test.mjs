import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// 统计 → 交付：GitHub 交付 PR 写成 owner/repo #4（不是工蜂的 !4，也不带平台内部的 github: 前缀），
// 英文界面交付页不残留中文，需求标题、LOOP 名、负责人、项目名照原样显示。

const HAN = /[㐀-鿿]/;
const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];
const TITLE = '修复 Inventory.remove 的两个缺陷';
const LOOP = '测试验证';
const OWNER = '测试 本地';
const PROJECT = '测试项目';

const analytics = {
  summary: { totalRuns: 3, succeededRuns: 2, qualityRuns: 3, successRate: 100, totalTokens: 5000, totalCostCny: 2, costUnknownRuns: 1,
    runsPerIssue: 1, failuresPerIssue: 0, inProgressIssues: 1, weeklyActiveContributors: 1, weeklyDeliveredIssues: 1, weeklyThroughputPerUser: 1 },
  demo: {}, trend: [], runTraces: [], workflows: [], models: [], nodes: [], failures: [], userUsage: [], projectUsage: [], weekHour: [], tokenUsage: [],
  activity: { sessions: 0, collaborativeSessions: 0, requirements: 0, sopRuns: 3 }, activityTrend: [],
  delivery: { issues: 3, delivered: 1, sopSucceeded: 2, withMr: 2, issuesWithMergedMr: 1, mrMerged: 1, totalMrs: 2, mrIntegrated: 0,
    averageDurationMs: 600000, averageExecutionDurationMs: 500000, p50ExecutionDurationMs: 500000, p95ExecutionDurationMs: 600000, totalTokens: 5000, directIssues: 1 },
  deliveries: [
    { issueId: 'gh-shop-1', projectId: 'project-1', issueNumber: 1, externalIid: 1, source: 'github', title: TITLE, workflowName: LOOP, userId: 7,
      runStatus: 'succeeded', sopSucceeded: true, runCount: 1, tokens: 500, costCny: 1, costKnown: true,
      mrs: [{ url: 'https://github.com/acme/shop/pull/4', project: 'github:acme/shop', iid: 4, state: 'opened' }],
      lanes: [{ label: LOOP, started: true, status: 'succeeded' }] },
    { issueId: 'issue-3', projectId: 'project-1', issueNumber: 3, externalIid: 3, source: 'gongfeng', title: 'to_roman', workflowName: 'Fix', userId: 7,
      runStatus: 'succeeded', sopSucceeded: true, delivered: true, runCount: 2, tokens: 500, costCny: 1, costKnown: true,
      mrs: [{ url: 'https://git.example.test/team/shop/-/merge_requests/3', project: 'team/shop', iid: 3, state: 'merged' }] },
    { issueId: 'issue-4', projectId: 'project-1', issueNumber: 4, source: 'local', title: 'chat fix', directDelivery: true, userId: 7,
      runCount: 0, tokens: 10, costCny: 0, costKnown: true,
      // Older rows only carry the PR link fields.
      mrUrl: 'https://github.com/acme/shop/pull/7', mrIid: 7, mrState: 'opened' },
  ],
};

async function deliveryPage(t, { lang = '', scope = 'project' } = {}) {
  const app = await loadApp({ lang });
  t.after(app.close);
  app.evaluate(`
    state.currentUser={id:7,loginName:'alice',chineseName:${JSON.stringify(OWNER)},isGuest:false,isPlatformAdmin:true};
    state.data.users=[state.currentUser];
    state.data.projects[0].key='ATL';state.data.projects[0].name=${JSON.stringify(PROJECT)};state.data.projects[0].ownerId=7;
    state.obsScope=${JSON.stringify(scope)};state.obsUserFilter=${scope === 'user' ? "'7'" : "''"};state.obsTrendGrain='all';
    state.analytics=${JSON.stringify(analytics)};state.obsAnalyticsFull=true;state.obsDeliveryFilter='all';
    state.obsTab='delivery';renderObservability();
  `);
  await settle(4);
  return app;
}
// Values built inside the page come from another realm; compare them as plain data.
const plain = value => JSON.parse(JSON.stringify(value));
const prCells = app => [...app.document.querySelectorAll('#obsBody tr[data-edit-issue]')].map(r => r.cells[11].textContent.trim());

test('delivery table, CSV and report write a GitHub PR as owner/repo #N and a 工蜂 MR as group/repo !N', async t => {
  const app = await deliveryPage(t);
  assert.deepEqual(prCells(app), ['acme/shop #4 · 待合入', 'team/shop !3 · 已合入', '#7 · 待合入']);
  const links = [...app.document.querySelectorAll('#obsBody tr[data-edit-issue] td:nth-child(12) a')].map(a => a.getAttribute('href'));
  assert.deepEqual(links, ['https://github.com/acme/shop/pull/4', 'https://git.example.test/team/shop/-/merge_requests/3', 'https://github.com/acme/shop/pull/7']);
  const csv = app.evaluate('obsCsvContent(obsDeliveryTableDef(true).tables)');
  const report = app.evaluate('buildObsReportHTML()');
  for (const out of [csv, report]) {
    assert.match(out, /acme\/shop #4/);
    assert.match(out, /team\/shop !3/);
    assert.doesNotMatch(out, /github:acme|!4|!7/);
  }
});

// What the English page still shows in Chinese once i18n.js rewrites the text
// the app rendered; user data (data-i18n-skip) is checked separately below.
function leftoverChinese(window, root) {
  const left = [];
  for (const el of [root, ...root.querySelectorAll('*')]) {
    if (el.closest('[data-i18n-skip]')) continue;
    for (const node of el.childNodes) {
      if (node.nodeType === 3 && HAN.test(window.lfTranslate(node.nodeValue))) left.push(node.nodeValue.trim());
    }
    for (const name of ATTRS) {
      const value = el.getAttribute(name);
      if (value && HAN.test(window.lfTranslate(value))) left.push(`${name}="${value}"`);
    }
  }
  return left;
}

for (const scope of ['project', 'user']) {
  test(`English 统计 → 交付 (${scope} scope) leaves no Chinese copy and keeps user names as typed`, async t => {
    const app = await deliveryPage(t, { lang: 'en', scope });
    const { document, window } = app;
    const view = document.querySelector('#observabilityView');
    assert.deepEqual(leftoverChinese(window, view), []);
    const skipped = [...view.querySelectorAll('[data-i18n-skip]')].map(el => el.textContent);
    for (const name of [TITLE, LOOP, PROJECT]) assert.ok(skipped.includes(name), `${name} is shown as typed`);
    assert.ok(skipped.some(text => text.includes(OWNER)), 'owner name is shown as typed');
    // The dictionary would otherwise rewrite these names fragment by fragment.
    assert.notEqual(window.lfTranslate(TITLE), TITLE);
    assert.notEqual(window.lfTranslate(`alice(${OWNER})`), `alice(${OWNER})`);
    const row = document.querySelector('#obsBody tr[data-edit-issue="gh-shop-1"]');
    assert.equal(row.cells[3].textContent, 'LOOP succeeded');
    assert.equal(row.cells[7].textContent, '1 run');
    assert.equal(row.cells[11].textContent.trim(), 'acme/shop #4 · Pending merge');
    assert.equal(row.cells[12].textContent, 'Not delivered');
    assert.match(view.querySelector('.obs-insights').textContent, /1 \/ 3 delivered; the biggest drop is "With a PR → With a merged PR" \(1\)/);
  });
}

test('English funnel and owner charts label the canvas in English', async t => {
  const app = await deliveryPage(t, { lang: 'en' });
  const stages = plain(app.evaluate('obsDeliveryStages()')).map(s => s.name);
  assert.deepEqual(stages, ['With runs', 'LOOP succeeded / PR from chat', 'With a PR', 'With a merged PR', 'Delivered']);
  const funnel = plain(app.evaluate(`(()=>{const o=obsFunnelOption(obsDeliveryStages(),obsChartFormatters(OBS_THEME_PRESETS.light,true));
    return {axis:o.yAxis.data,label:o.series[0].label.formatter({dataIndex:3}),tip:o.tooltip.formatter({dataIndex:3})}})()`));
  assert.deepEqual(funnel.axis, stages);
  assert.match(funnel.label, /biggest drop/);
  assert.doesNotMatch(funnel.label + funnel.tip, HAN);
  const owners = plain(app.evaluate(`(()=>{const o=obsOwnersOption(obsOwnerRows(),obsChartFormatters(OBS_THEME_PRESETS.light,true));
    return {series:o.series.map(s=>s.name),tip:o.tooltip.formatter([{dataIndex:0}])}})()`));
  assert.deepEqual(owners.series, ['Delivered', 'In progress', 'Not delivered']);
  assert.match(owners.tip, /<b data-i18n-skip>alice\(测试 本地\)<\/b>/, 'the owner name in the tooltip is user data');
});

test('Chinese 统计 → 交付 keeps its Chinese copy', async t => {
  const app = await deliveryPage(t);
  const view = app.document.querySelector('#observabilityView');
  assert.match(view.querySelector('.obs-insights').textContent, /1 \/ 3 单已交付，最大流失在「有 PR 的单 → 有 PR 合入的单」（1 单）/);
  assert.deepEqual(plain(app.evaluate('obsDeliveryStages()')).map(s => s.name), ['有执行的单', 'LOOP 成功 / 对话已提 PR', '有 PR 的单', '有 PR 合入的单', '已交付']);
  const row = app.document.querySelector('#obsBody tr[data-edit-issue="issue-3"]');
  assert.equal(row.cells[0].textContent, '工蜂 #3');
  assert.equal(row.cells[7].textContent, '2 次');
  assert.equal(row.cells[12].textContent, '已交付');
});

test('delivery references use the request path when an MR links back to a PR', async t => {
  const app = await deliveryPage(t);
  const cases = [
    ['https://git.example.test/team/shop/-/merge_requests/3?back=/acme/shop/pull/4', 'team/shop !3'],
    ['https://git.example.test/team/shop/-/merge_requests/3#/acme/shop/pull/4', 'team/shop !3'],
    ['https://git.example.test/team/shop/pull/3?view=files', 'team/shop #3'],
  ];
  for (const [url, expected] of cases) {
    const mr = { project: 'team/shop', iid: 3, url };
    assert.equal(app.evaluate(`obsMRRefText(${JSON.stringify(mr)})`), expected);
  }
});
