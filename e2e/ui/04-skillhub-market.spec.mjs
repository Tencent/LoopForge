// SkillHub market tab on the skills page. The /api/skillhub/* proxy is mocked
// so the journey does not depend on the deployment reaching skillhub.cn;
// nothing is installed.
import { test, expect, watchErrors, openView } from './fixtures.mjs';

const items = [
  { slug: 'pdf-tools', namespace: 'team', name: 'PDF 处理', description: '读取、拆分、合并 PDF。', category: 'office-efficiency', version: '1.3.0', ownerName: 'PDF Team', homepage: 'https://skillhub.cn/skills/team/pdf-tools', downloads: 98000, stars: 412, labels: { requires_api_key: 'false' } },
  { slug: 'find-skill', namespace: 'solo', name: 'find skill', description: '在 SkillHub 查找技能。', category: 'ai-agent', version: '1.0.2', ownerName: 'solo', homepage: 'https://skillhub.cn/skills/solo/find-skill', downloads: 4300, stars: 17, labels: { requires_api_key: 'true' } },
];

async function mockSkillhub(page) {
  const seen = [];
  await page.route('**/api/skillhub/**', route => {
    const url = new URL(route.request().url());
    seen.push(url.pathname + url.search);
    const path = url.pathname;
    if (path === '/api/skillhub/categories') return route.fulfill({ json: { items: [{ key: 'office-efficiency', name: '办公效率' }, { key: 'ai-agent', name: 'AI Agent' }] } });
    if (path === '/api/skillhub/skills') {
      const kw = url.searchParams.get('keyword') || '';
      const hits = items.filter(i => !kw || i.slug.includes(kw) || i.name.includes(kw));
      return route.fulfill({ json: { total: hits.length, page: 1, pageSize: 24, items: hits } });
    }
    if (path === '/api/skillhub/skills/pdf-tools') return route.fulfill({ json: { skill: { ...items[0], summary: '读取、拆分、合并 PDF，提取表格。', downloads: 98000, stars: 412 }, latestVersion: { version: '1.3.0', changelog: '新增表格抽取' }, owner: { displayName: 'PDF Team' } } });
    if (path === '/api/skillhub/skills/pdf-tools/versions') return route.fulfill({ json: { versions: [{ version: '1.3.0', changelog: '新增表格抽取', createdAt: 1761100000000, securityReports: { keen: { status: 'benign', statusText: '安全，无风险', reportUrl: 'https://example.com/r', safe: true } } }, { version: '1.0.0', changelog: 'Initial release', createdAt: 1752500000000 }] } });
    if (path === '/api/skillhub/skills/pdf-tools/files') return route.fulfill({ json: { version: url.searchParams.get('version'), files: [{ path: 'SKILL.md', size: 120 }, { path: 'scripts/inspect.py', size: 300 }] } });
    if (path === '/api/skillhub/skills/pdf-tools/file') return route.fulfill({ json: { path: url.searchParams.get('path'), previewable: true, content: '---\nname: pdf-tools\n---\n# PDF 处理\n\n读取 PDF。' } });
    if (path === '/api/skillhub/skills/pdf-tools/evaluation') return route.fulfill({ json: { available: true, userSummary: '整体质量良好', dimensions: { trust: { items: { scan: { score: 4.5 }, domestic: { score: 4.1 } }, userReason: '来源可核验' }, effectiveness: { items: { accuracy: { score: 4.3 } } } } } });
    return route.fulfill({ status: 404, json: { error: 'not mocked' } });
  });
  return seen;
}

test('skills page: SkillHub market tab searches, filters and opens a detail drawer', async ({ page }) => {
  const errors = watchErrors(page);
  const seen = await mockSkillhub(page);
  await openView(page, 'rules');
  await expect(page.locator('[data-asset-scope="skillhub"]')).toBeHidden();

  await openView(page, 'skills');
  const tab = page.locator('[data-asset-scope="skillhub"]');
  await expect(tab).toBeVisible();
  await tab.click();
  await expect(page.locator('#assetPageDescription')).toHaveText(/从 SkillHub 搜索公开技能/);
  await expect(page.locator('#newAsset')).toBeHidden();
  await expect(page.locator('#assetSearchBox')).toBeHidden();
  await expect(page.locator('#skillhubGrid .skillhub-card')).toHaveCount(2);
  await expect(page.locator('#skillhubGrid')).toContainText('需要 API Key');

  await page.locator('#skillhubKeyword').fill('pdf');
  await page.locator('#skillhubKeyword').press('Enter');
  await expect(page.locator('#skillhubGrid .skillhub-card')).toHaveCount(1);
  expect(seen.some(s => s.startsWith('/api/skillhub/skills?') && s.includes('keyword=pdf') && s.includes('pageSize=24'))).toBe(true);

  await page.locator('#skillhubGrid .skillhub-card').first().click();
  const drawer = page.locator('#skillhubDrawer');
  await expect(drawer).toHaveClass(/is-open/);
  await expect(drawer.locator('#skillhubDrawerTitle')).toContainText('PDF 处理');
  await expect(drawer.locator('.skillhub-stats')).toContainText('4.3');
  await expect(drawer.locator('.skillhub-md')).toContainText('读取 PDF');
  await drawer.locator('[data-skillhub-tab="quality"]').click();
  await expect(drawer).toContainText('TRACE 质量评测');
  await expect(drawer).toContainText('安全，无风险');
  await drawer.locator('[data-skillhub-tab="files"]').click();
  await expect(drawer.locator('.skillhub-ftree')).toContainText('inspect.py');
  await drawer.locator('[data-skillhub-tab="versions"]').click();
  await expect(drawer.locator('.skillhub-vrow')).toHaveCount(2);
  await page.keyboard.press('Escape');
  await expect(drawer).not.toHaveClass(/is-open/);

  await page.locator('[data-asset-scope="project"]').click();
  await expect(page.locator('#skillhubMarket')).toBeHidden();
  await expect(page.locator('#assetList')).toBeVisible();
  expect(errors).toEqual([]);
});
