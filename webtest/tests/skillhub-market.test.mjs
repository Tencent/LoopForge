import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle, chooseOption } from '../lib/load-app.mjs';

const sampleCategories = {
  items: [
    { key: 'dev', name: '研发工具' },
    { key: 'ops', name: '运维诊断' },
  ],
};

const sampleItems = [
  {
    slug: 'code-review',
    namespace: 'tencent',
    name: 'Code Review Pro',
    version: '1.2.0',
    description: 'Automated code review',
    ownerName: 'Tencent',
    category: 'dev',
    downloads: 12500,
    stars: 340,
    labels: { requires_api_key: 'true', pricing: 'paid' },
  },
  {
    slug: 'git-helper',
    namespace: 'tencent',
    name: 'Git Helper',
    version: '2.0.0',
    description: 'Git helper skill',
    ownerName: 'Tencent',
    category: 'dev',
    downloads: 250000000,
    stars: 80,
    labels: {},
  },
  {
    slug: 'log-analyzer',
    namespace: 'tencent',
    name: 'Log Analyzer',
    version: '1.0.0',
    description: 'Analyze logs',
    ownerName: 'Tencent',
    category: 'ops',
    downloads: 90,
    stars: 5,
    labels: {},
  },
];

const sampleDetail = {
  skill: {
    name: 'Code Review Pro',
    slug: 'code-review',
    namespace: 'tencent',
    summary: 'Detailed summary of Code Review Pro',
    category: 'dev',
    homepage: 'https://skillhub.example.test/code-review',
    downloads: 12500,
    stars: 340,
    labels: { requires_api_key: true },
    subCategories: [{ key: 'lint', name: '静态检查' }, '安全'],
  },
  owner: { displayName: 'Tencent Dev' },
  latestVersion: { version: '1.2.0', createdAt: 1730000000, changelog: 'Initial release' },
  securityReports: {
    TencentSec: { status: 'passed', statusText: '安全通过', safe: true, reportUrl: 'https://sec.example.test/r1' },
    ScannerB: { status: 'high_risk', statusText: '高危告警', safe: false, reportUrl: 'javascript:alert(1)' },
  },
};

const sampleVersions = {
  versions: [
    {
      version: '1.2.0',
      createdAt: 1730000000,
      changelog: 'Add new rules',
      securityReports: sampleDetail.securityReports,
    },
    {
      version: '1.1.0',
      createdAt: 1720000000000,
      changelog: 'Initial version',
      securityReports: {
        TencentSec: { status: 'passed', statusText: '安全通过', safe: true },
      },
    },
  ],
};

const sampleEvaluation = {
  available: true,
  updatedAt: 1730000000,
  userSummary: '整体质量优秀',
  dimensions: {
    trust: {
      userReason: '安全可靠',
      items: {
        scan: { score: 4.5, userReason: '扫描无漏洞' },
        domestic: { score: 5, userReason: '国内直连' },
      },
    },
    reliability: {
      userReason: '稳定',
      items: {
        stability: { score: 3.5 },
        func: { score: null },
      },
    },
  },
};

const sampleFiles = {
  files: [
    { path: 'SKILL.md', size: 128 },
    { path: 'src/helper.py', size: 256 },
    { path: 'big.bin', size: 2 * 1024 * 1024 },
  ],
};

test('SkillHub market helpers, list, drawer, and install/update modal workflows', async (t) => {
  const app = await loadApp({
    routes: [
      { path: '/api/skillhub/categories', body: sampleCategories },
      { path: /^\/api\/skillhub\/skills\?/, body: { items: sampleItems, total: 120 } },
      { path: /^\/api\/skillhub\/skills\/code-review\?/, body: sampleDetail },
      { path: /^\/api\/skillhub\/skills\/code-review\/versions/, body: sampleVersions },
      { path: /^\/api\/skillhub\/skills\/code-review\/evaluation/, body: sampleEvaluation },
      { path: /^\/api\/skillhub\/skills\/code-review\/files/, body: sampleFiles },
      {
        path: /^\/api\/skillhub\/skills\/code-review\/file\?/,
        body: (call) => {
          if (call.search.includes('helper.py')) {
            return { path: 'src/helper.py', previewable: true, content: 'def run():\n  return 1\n' };
          }
          return {
            path: 'SKILL.md',
            previewable: true,
            content: '---\ntitle: Code Review\n---\n# Guide\nHello world',
          };
        },
      },
      { path: /^\/api\/skillhub\/skills\/git-helper\/versions/, body: sampleVersions },
      { path: /^\/api\/skillhub\/skills\/git-helper\?/, body: { ...sampleDetail, latestVersion: { version: '2.1.0' } } },
    ],
  });
  t.after(app.close);

  // Pure helper coverage
  const helpers = app.evaluate(`({
    nullScore: skillhubScoreValue(null),
    numScore: skillhubScoreValue('4.2'),
    dimEmpty: skillhubDimensionScore({}),
    traceEmpty: skillhubTraceScore({}),
    traceVal: skillhubTraceScore(${JSON.stringify(sampleEvaluation.dimensions)}),
    dateZero: skillhubDate(0),
    dateSec: skillhubDate(1730000000),
    dateMs: skillhubDate(1730000000000),
    badUrl: skillhubSafeURL('javascript:void(0)'),
    goodUrl: skillhubSafeURL('https://example.com/a'),
    idEmpty: skillhubIdProblem(''),
    idBad: skillhubIdProblem('***'),
    idDup: skillhubIdProblem('skill-1'),
    idOk: skillhubIdProblem('fresh-skill'),
    pages: skillhubPageList(3, 8),
    warnings: skillhubFileWarnings([
      { path: 'huge.bin', size: 2 * 1024 * 1024 },
      ...Array.from({ length: 501 }, (_, i) => ({ path: 'f' + i, size: 200000 })),
    ]),
  })`);
  assert.equal(helpers.nullScore, null);
  assert.equal(helpers.numScore, 4.2);
  assert.equal(helpers.dimEmpty, null);
  assert.equal(helpers.traceEmpty, null);
  assert.equal(helpers.traceVal, 4.1);
  assert.equal(helpers.dateZero, '');
  assert.match(helpers.dateSec, /^2024-/);
  assert.match(helpers.dateMs, /^2024-/);
  assert.equal(helpers.badUrl, '');
  assert.equal(helpers.goodUrl, 'https://example.com/a');
  assert.match(helpers.idEmpty, /请填写/);
  assert.match(helpers.idBad, /只能包含/);
  assert.match(helpers.idDup, /已被/);
  assert.equal(helpers.idOk, '');
  assert.ok(helpers.pages.includes('…'));
  assert.equal(helpers.warnings.length, 3);

  // Configure installed skills in project-1: git-helper (older v1.0.0 -> update) and log-analyzer (v1.0.0 -> installed)
  app.evaluate(`
    state.data.skills.push(
      { id: 'git-helper', name: 'Git Helper', projectId: 'project-1', source: { provider: 'skillhub', namespace: 'tencent', slug: 'git-helper', version: '1.0.0' } },
      { id: 'log-analyzer', name: 'Log Analyzer', projectId: 'project-1', source: { provider: 'skillhub', namespace: 'tencent', slug: 'log-analyzer', version: '1.0.0' } },
    );
    state.assetType = 'skills';
    state.skillScope = 'skillhub';
    state.selectedAsset = null;
    renderAssets();
  `);
  await settle(10);

  const grid = app.document.querySelector('#skillhubGrid');
  assert.match(grid.textContent, /Code Review Pro/);
  assert.match(grid.textContent, /有新版本 v2.0.0/);
  assert.match(grid.textContent, /已安装 v1.0.0/);

  // Pager click
  const page2 = app.document.querySelector('[data-skillhub-page="2"]');
  page2?.click();
  await settle(6);
  assert.equal(app.evaluate(`skillhubState().page`), 2);

  // Filter & keyword search
  const kw = app.document.querySelector('#skillhubKeyword');
  kw.value = 'review';
  kw.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  await settle(6);
  assert.equal(app.evaluate(`skillhubState().keyword`), 'review');

  const sortSel = app.document.querySelector('#skillhubSort');
  chooseOption(sortSel, 'score');
  await settle(6);
  assert.equal(app.evaluate(`skillhubState().sortBy`), 'score');

  const catSel = app.document.querySelector('#skillhubCategory');
  if (catSel.options.length > 1) {
    chooseOption(catSel, 'dev');
    await settle(6);
    assert.equal(app.evaluate(`skillhubState().category`), 'dev');
  }

  // Open detail drawer via card click
  const firstCard = grid.querySelector('[data-skillhub-open="0"]');
  firstCard.click();
  await settle(12);

  const drawer = app.document.querySelector('#skillhubDrawer');
  assert.ok(drawer.classList.contains('is-open'));
  assert.match(drawer.textContent, /Detailed summary of Code Review Pro/);

  // Switch drawer tabs: files -> preview / source / big file -> quality -> versions
  drawer.querySelector('[data-skillhub-tab="files"]').click();
  await settle(6);
  assert.match(drawer.textContent, /helper\.py/);

  drawer.querySelector('[data-skillhub-mode="source"]')?.click();
  await settle(4);
  drawer.querySelector('[data-skillhub-file="src/helper.py"]')?.click();
  await settle(8);
  assert.match(drawer.textContent, /def run/);

  drawer.querySelector('[data-skillhub-file="big.bin"]')?.click();
  await settle(4);
  assert.match(drawer.textContent, /超过 1MB/);

  drawer.querySelector('[data-skillhub-tab="quality"]').click();
  await settle(4);
  assert.match(drawer.textContent, /TRACE 质量评测/);
  drawer.querySelector('.skillhub-tl-toggle')?.click();

  drawer.querySelector('[data-skillhub-tab="versions"]').click();
  await settle(4);
  assert.match(drawer.textContent, /1\.1\.0/);
  drawer.querySelector('[data-skillhub-version="1.1.0"]')?.click();
  await settle(8);

  // Trigger install modal from drawer
  app.router.post('/api/skillhub/install', (call) => ({
    id: call.body.id,
    name: call.body.name,
    projectId: call.body.projectId,
    source: { provider: 'skillhub', namespace: call.body.namespace, slug: call.body.slug, version: call.body.version },
  }));
  drawer.querySelector('[data-skillhub-drawer-install]')?.click();
  await settle(6);

  const modal = app.document.querySelector('#skillhubModal');
  assert.equal(modal.hidden, false);
  const idInput = app.document.querySelector('#skillhubModalId');
  idInput.value = 'code-review-installed';
  idInput.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  modal.querySelector('form').dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(10);
  assert.equal(modal.hidden, true);

  // Trigger update modal from market card and submit update
  app.router.post('/api/skillhub/update', (call) => ({
    id: call.body.skillId,
    name: 'Git Helper',
    projectId: 'project-1',
    source: { provider: 'skillhub', namespace: 'tencent', slug: 'git-helper', version: call.body.version },
  }));
  const updateBtn = grid.querySelector('[data-skillhub-update="1"]');
  updateBtn?.click();
  await settle(8);
  assert.equal(modal.hidden, false);
  modal.querySelector('form').dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(10);
  assert.equal(modal.hidden, true);

  // Check update helper & close drawer
  const checkBtn = app.document.createElement('button');
  app.document.body.append(checkBtn);
  await app.evaluate(`checkSkillhubUpdate(state.data.skills.find(s => s.id === 'git-helper'), document.body.lastElementChild)`);
  await settle(8);
  app.evaluate(`closeSkillhubModal(); closeSkillhubDrawer();`);
  assert.equal(app.evaluate(`state.skillhubDrawer`), null);
});
