// Every navigation entry renders its view without page errors, console errors
// or failed same-origin requests — once in the data-rich project users work in
// and once in the e2e project.
import { test, expect, watchErrors } from './fixtures.mjs';

// Keep in sync with viewTitles() in web/app.js; a renamed or dropped view is
// a visible product change the suite should notice.
const TITLES = {
  collaboration: 'AI 工作台', issues: '需求列表', aiSchedules: '定时任务', bots: '通道管理', assistant: 'AI 助手', runs: '运行记录',
  approvals: '待我确认', sops: '流程模板', flow: '流程设计', agents: '角色定义', mcp: '能力资产', rules: '能力资产', skills: '能力资产',
  dockerfiles: '环境资产', environments: '环境资产', sandboxes: '环境资产', observability: '统计', projects: '项目配置',
  platform: '平台管理', runners: '执行机器', settings: '个人配置', guide: '访问密钥',
};
// Only offered for some roles; the app redirects otherwise.
const CONDITIONAL = ['platform'];
const ASSET_VIEWS = ['rules', 'skills', 'dockerfiles', 'environments', 'sandboxes'];
const RETIRED = ['metrics', 'knowledge', 'businessDomains', 'businessDomainManagement'];
const projects = [process.env.AGENTFLOW_E2E_VIEW_PROJECT || 'welcome', null];

test('legacy routes land on their replacements', async ({ page }) => {
  const errors = watchErrors(page);
  for (const [legacy, view] of [['dashboard', 'collaboration'], ['access', 'guide'], ['assets', 'dockerfiles'], ['management', 'settings'], ['playbooks', 'sops']]) {
    await page.goto(`/#/${legacy}`);
    await expect(page.locator('#pageTitle')).toHaveText(TITLES[view]);
  }
  // An unknown view opened directly (a fresh load, not an in-page hash change)
  // restores the last view used, and the workbench when there is none.
  await page.goto('about:blank');
  await page.goto('/#/no-such-view');
  await expect(page.locator('#pageTitle')).toHaveText(TITLES.sops);
  await page.evaluate(() => localStorage.removeItem('agentflow.activeView'));
  await page.goto('about:blank');
  await page.goto('/#/no-such-view');
  await expect(page.locator('#pageTitle')).toHaveText(TITLES.collaboration);
  expect(errors).toEqual([]);
});

// SlidingTabs enhances each type-tab row and its rule forces display:flex, so
// only a real browser shows whether the inactive group actually disappears.
test('merged asset pages show only their own type tabs', async ({ page }) => {
  const errors = watchErrors(page);
  const visibleTabs = () => page.evaluate(() => [...document.querySelectorAll('[data-asset-type-tab]')]
    .filter(el => el.getClientRects().length).map(el => el.dataset.assetTypeTab));
  const cases = [
    ['mcp', ['mcp', 'rules', 'skills'], 'mcp'],
    ['rules', ['mcp', 'rules', 'skills'], 'rules'],
    ['sandboxes', ['dockerfiles', 'environments', 'sandboxes'], 'sandboxes'],
    ['skills', ['mcp', 'rules', 'skills'], 'skills'],
  ];
  for (const [view, tabs, active] of cases) {
    await page.goto(`/#/${view}`);
    await expect(page.locator('#pageTitle')).toHaveText(TITLES[view]);
    await expect.poll(visibleTabs, { message: `${view} type tabs` }).toEqual(tabs);
    await expect(page.locator(`[data-asset-type-tab="${active}"]:visible`)).toHaveAttribute('aria-selected', 'true');
    const nav = view === 'sandboxes' ? 'dockerfiles' : 'mcp';
    await expect(page.locator(`.platform-sidebar .nav[data-view="${nav}"]`)).toHaveClass(/\bactive\b/);
  }
  // Clicking a tab switches the page and keeps the single sidebar entry selected.
  await page.goto('/#/sandboxes');
  await expect(page.locator('#pageTitle')).toHaveText(TITLES.sandboxes);
  await page.locator('#assetsView [data-asset-type-tab="environments"]:visible').click();
  await expect(page).toHaveURL(/#\/environments/);
  await expect.poll(visibleTabs).toEqual(['dockerfiles', 'environments', 'sandboxes']);
  await expect(page.locator('[data-asset-type-tab="environments"]:visible')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('.platform-sidebar .nav[data-view="dockerfiles"]')).toHaveClass(/\bactive\b/);
  expect(errors).toEqual([]);
});

for (const project of projects) {
  test(`every view renders cleanly (${project || 'e2e project'})`, async ({ page, identity }) => {
    const projectId = project || identity.projectId;
    await page.addInitScript(id => localStorage.setItem('agentflow.activeProject', id), projectId);
    const errors = watchErrors(page);
    await page.goto('/#/collaboration');
    await expect(page.locator('#pageTitle')).toHaveText(TITLES.collaboration);
    const views = await page.$$eval('[data-view]', els => [...new Set(els.map(e => e.dataset.view))]);
    expect(views.length).toBeGreaterThanOrEqual(15);
    expect(views.filter(v => RETIRED.includes(v)), 'retired modules are not offered').toEqual([]);
    expect(views.filter(v => !TITLES[v]), 'navigation entries without a known title').toEqual([]);

    for (const view of views) {
      await test.step(view, async () => {
        const before = errors.length;
        await page.evaluate(v => { location.hash = `#/${v}`; }, view);
        if (CONDITIONAL.includes(view)) {
          await page.waitForTimeout(500);
          if (!(await page.evaluate(v => location.hash.startsWith(`#/${v}`), view))) return;
        }
        const container = ASSET_VIEWS.includes(view) ? 'assets' : view;
        const pane = page.locator(`#${container}View`);
        await expect(pane).toBeVisible();
        if (TITLES[view]) await expect(page.locator('#pageTitle')).toHaveText(TITLES[view]);
        await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {});
        // The AI workbench is the native chat app inside an iframe.
        const content = view === 'collaboration' ? page.frameLocator('#chatNextFrame').locator('body') : pane;
        await expect.poll(async () => (await content.innerText()).trim().length, { message: `${view} rendered nothing` }).toBeGreaterThan(10);
        await expect(pane.locator('text=/undefined|NaN|\\[object Object\\]/')).toHaveCount(0);
        await page.screenshot({ path: test.info().outputPath(`${projectId}-${view}.png`), fullPage: false });
        expect(errors.slice(before), `errors on ${view}`).toEqual([]);
      });
    }
    expect(errors).toEqual([]);
  });
}
