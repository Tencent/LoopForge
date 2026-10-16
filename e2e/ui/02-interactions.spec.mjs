// Key user journeys driven through the real UI: create a requirement, approve
// a waiting run from its detail page, edit a SOP in the designer and search
// the knowledge base. Fixtures are created through the API and cleaned up.
import { test, expect, watchErrors, openView } from './fixtures.mjs';
import { get, post, list, cleanup, track, waitRun } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, script, approval, startRun } from '../lib/fixtures.mjs';

test.afterAll(cleanup);

test('requirement composer: create a platform requirement without analysis', async ({ page }) => {
  const errors = watchErrors(page);
  const title = `E2E UI 需求 ${cfg.runTag}-${Date.now() % 100000}`;
  await openView(page, 'issues');
  await page.locator('#newIssue').click();
  await expect(page.locator('#requirementModal')).toHaveClass(/open/);
  await page.locator('#chatInput').fill(`${title}\n\n自动化 UI 测试创建，会被关闭。`);
  await page.locator('#chatCreateDirect').click();
  await page.locator('#confirmOkBtn').click();
  await expect(page.locator('#reqDestinations')).toBeVisible();
  await page.locator('#chatCreateLocal').click();
  await expect(page.locator('#requirementModal')).not.toHaveClass(/open/, { timeout: 30_000 });

  const created = await expect.poll(async () => {
    const items = list(await get(`/api/issues?projectId=${cfg.projectId}&q=${encodeURIComponent(title.slice(0, 20))}`));
    return items.find(i => (i.title + i.description).includes(title))?.id || '';
  }, { timeout: 30_000 }).not.toBe('');
  const issue = list(await get(`/api/issues?projectId=${cfg.projectId}`)).find(i => (i.title + i.description).includes(title));
  track(`issue ${issue.id}`, () => post(`/api/issues/${issue.id}/close`, {}, { expect: [200, 404, 409] }));
  expect(issue.source || 'local').toBe('local');
  expect(errors).toEqual([]);
});

test('requirement composer: AI analysis answers', async ({ page }) => {
  test.skip(!cfg.model, 'AGENTFLOW_E2E_MODEL=0');
  test.setTimeout(420_000);
  const errors = watchErrors(page);
  await openView(page, 'issues');
  await page.locator('#newIssue').click();
  await page.locator('#chatInput').fill('自动化测试：给运行记录列表增加按 SOP 名称筛选的能力。这只是测试，请只做分析，不需要读代码。');
  await page.locator('#chatSend').click();
  await expect(page.locator('#chatStop')).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('#chatStop')).toBeHidden({ timeout: 400_000 });
  await expect.poll(async () => (await page.locator('#reqClarifyPanel').innerText()).trim().length).toBeGreaterThan(20);
  // Close without creating anything.
  await page.locator('#requirementModal .modal-close, #requirementModal button:has-text("×")').first().click();
  const confirm = page.locator('#confirmModal.open #confirmOkBtn');
  if (await confirm.isVisible().catch(() => false)) await confirm.click();
  expect(errors).toEqual([]);
});

test('run detail: tabs render and a waiting approval is approved from the UI', async ({ page }) => {
  const wf = await createWorkflow('ui-approval', [
    script('prep', 'mkdir -p out && echo "# UI E2E" > out/result.md && echo UI_PREP', {
      artifactOutputs: [{ name: 'result', path: 'out/result.md', mediaType: 'text/markdown', required: true }],
    }),
    approval('gate', 'prep'),
    script('finish', 'echo UI_FINISH'),
  ]);
  const { id } = await startRun(wf, 'e2e UI 审批');
  await waitRun(id, r => r.nodes?.gate?.status === 'waiting_approval', { label: 'gate waiting' });

  const errors = watchErrors(page);
  await page.goto(`/#/runs/${encodeURIComponent(id)}`);
  const approve = page.locator('[data-approve="gate"]').first();
  await page.locator('[data-run-node="gate"]').click();
  await expect(approve).toBeVisible();

  for (const tab of ['trace', 'logs', 'workspace', 'eval', 'overview']) {
    await page.locator(`[data-run-tab="${tab}"]`).click();
    const pane = page.locator(`[data-run-pane="${tab}"]`);
    if (await pane.count()) await expect(pane).toBeVisible();
    await page.waitForTimeout(800);
  }
  await page.locator('[data-run-node="prep"]').click();
  await expect(page.locator('[data-artifact]').first()).toBeVisible();

  await page.locator('[data-run-node="gate"]').click();
  await page.locator('[data-approve="gate"]').first().click();
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded after UI approval' });
  expect(run.nodes.finish.status).toBe('succeeded');
  await expect(page.locator('.run-status-wrap .status')).toContainText(/成功|完成|已完成|succeeded/, { timeout: 30_000 });
  expect(errors).toEqual([]);
});

test('SOP designer: rename a node and save a new revision', async ({ page }) => {
  const wf = await createWorkflow('ui-designer', [script('first', 'echo 1'), script('second', 'echo 2')]);
  const before = await get(`/api/workflows/${wf}`);
  const errors = watchErrors(page);
  await page.goto(`/#/flow/${encodeURIComponent(wf)}`);
  await expect(page.locator('#canvas .flow-node')).toHaveCount(2);
  await page.locator('#canvas .flow-node[data-id="second"]').click();
  const name = page.locator('#properties [data-p="name"]');
  await expect(name).toBeVisible();
  await name.fill('UI 改名节点');
  await name.dispatchEvent('change');
  await expect(page.locator('#canvas .flow-node[data-id="second"]')).toContainText('UI 改名节点');
  await page.locator('#saveWorkflow').click();
  await expect.poll(async () => (await get(`/api/workflows/${wf}`)).nodes.find(n => n.id === 'second').name, { timeout: 20_000 }).toBe('UI 改名节点');
  const after = await get(`/api/workflows/${wf}`);
  expect(after.revision).toBeGreaterThan(before.revision);
  expect(errors).toEqual([]);
});

test('knowledge center: search shows cited hits', async ({ page }) => {
  const settings = await get('/api/knowledge/settings');
  test.skip(!settings.ready, 'knowledge base not configured');
  const errors = watchErrors(page);
  await openView(page, 'knowledge');
  await page.locator('[data-kb-tab="search"]').first().click();
  await page.locator('#kbSearchForm input[name="q"]').fill('部署');
  await page.locator('#kbSearchForm button[type="submit"]').click();
  await expect(page.locator('.kb-hit').first()).toBeVisible({ timeout: 60_000 });
  expect(await page.locator('.kb-hit').count()).toBeGreaterThan(0);
  expect(errors).toEqual([]);
});
