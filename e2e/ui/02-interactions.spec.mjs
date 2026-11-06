// Key user journeys driven through the real UI: create and dispatch a requirement,
// approve waiting runs from the queue and detail page, edit a SOP and search
// the knowledge base. Fixtures are created through the API and cleaned up.
import { test, expect, watchErrors, openView } from './fixtures.mjs';
import { get, post, put, del, list, cleanup, track, waitFor, waitRun } from '../lib/api.mjs';
import { cfg } from '../lib/env.mjs';
import { createWorkflow, createAgent, script, approval, agentNode, startRun, trackRun } from '../lib/fixtures.mjs';

test.afterAll(cleanup);

test('requirement composer: create, find and dispatch a requirement from its drawer', async ({ page }) => {
  const errors = watchErrors(page);
  const title = `E2E UI 需求 ${cfg.runTag}-${Date.now() % 100000}`;
  // A requirement needs a runnable SOP; a fresh database has none in the e2e project.
  const wf = await createWorkflow('ui-requirement', [script('only', 'echo UI_REQUIREMENT')]);
  await page.goto('/#/issues');
  await expect(page.locator('#issuesView')).toBeVisible();
  await page.locator('#newIssue').click();
  await expect(page.locator('#requirementModal')).toHaveClass(/open/);
  await page.locator('#chatInput').fill(`${title}\n\n自动化 UI 测试创建，会被关闭。`);
  await page.locator('#reqSkip').click();
  await expect(page.locator('#reqStepConfirm')).toBeVisible();
  await expect(page.locator('#reqCreate')).toBeVisible();
  await expect(page.locator('#reqTitle')).toHaveValue(title);
  await page.getByRole('button', { name: 'SOP 流程', exact: true }).click();
  await page.getByRole('listbox', { name: 'SOP 流程', exact: true })
    .getByRole('option', { name: /E2E ui-requirement/ }).click();
  await expect(page.locator('#reqWorkflow')).toHaveValue(wf);
  const [created] = await Promise.all([
    page.waitForResponse(r => new URL(r.url()).pathname === '/api/issues' && r.request().method() === 'POST'),
    page.locator('#reqCreate').click(),
  ]);
  expect(created.ok()).toBe(true);
  const issue = await created.json();
  track(`issue ${issue.id}`, () => post(`/api/issues/${issue.id}/close`, {}, { expect: [200, 404, 409] }));
  expect(issue.source || 'local').toBe('local');
  expect(issue.workflowId).toBe(wf);
  await expect(page.locator('#requirementModal')).not.toHaveClass(/open/);

  // Creation opens the drawer automatically. Close it, then exercise the
  // refreshed list's own click binding without reloading or fetching the row.
  const drawer = page.locator('#issueDetailDrawer');
  await expect(drawer).toHaveClass(/open/);
  await drawer.locator('.drawer-close').click();
  await expect(drawer).toHaveAttribute('aria-hidden', 'true');
  const row = page.locator(`#issuesView .issue-table-row[data-id="${issue.id}"]`);
  await expect(row).toBeVisible();
  await expect(row).toContainText(title);
  await row.locator('.issue-title-cell').click();
  await expect(drawer).toHaveAttribute('aria-hidden', 'false');
  await expect(drawer.locator('#issueDetailTitle')).toHaveText(title);
  await drawer.locator('[data-dtab="sop"]').click();
  await expect(drawer.locator('[data-dtab="sop"]')).toHaveClass(/active/);

  const dispatch = drawer.locator(`[data-dispatch-issue="${issue.id}"][data-workflow="${wf}"]`);
  await expect(dispatch).toBeVisible();
  const [dispatched] = await Promise.all([
    page.waitForResponse(r => new URL(r.url()).pathname === `/api/issues/${issue.id}/dispatch` && r.request().method() === 'POST'),
    dispatch.click(),
  ]);
  expect(dispatched.ok()).toBe(true);
  const runId = await waitFor('UI-dispatched requirement run', async () => {
    const runs = list(await get(`/api/issues/${issue.id}/runs`));
    return runs[0]?.id || runs[0]?.runId;
  }, { timeoutMs: 30_000 });
  trackRun(runId);
  const run = await waitRun(runId, r => r.status === 'succeeded', { label: 'requirement SOP succeeds' });
  expect(run.workflowId).toBe(wf);
  expect(run.nodes.only.status).toBe('succeeded');
  expect(errors).toEqual([]);
});

test('requirement composer: AI analysis answers', async ({ page }) => {
  test.skip(!cfg.model, 'AGENTFLOW_E2E_MODEL=0');
  test.setTimeout(420_000);
  const errors = watchErrors(page);
  await openView(page, 'issues');
  await page.locator('#newIssue').click();
  await page.locator('#chatInput').fill('自动化测试：给运行记录列表增加按 SOP 名称筛选的能力。这只是测试，请只做分析，不需要读代码。');
  await page.locator('#reqNext').click();
  await expect(page.locator('#reqStepClarify')).toBeVisible();
  await expect(page.locator('#chatStop')).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('#chatStop')).toBeHidden({ timeout: 400_000 });
  await expect.poll(async () => (await page.locator('#reqClarifyPanel').innerText()).trim().length).toBeGreaterThan(20);
  await expect(page.locator('#reqClarifyPanel .req-q-card, #reqClarifyPanel .req-ready-card, #reqClarifyPanel .req-supplement').first()).toBeVisible();
  // Close without creating anything.
  await page.locator('#requirementModal .requirement-close').first().click();
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
    // These five panes are required by this fixture. Preview is optional
    // and deliberately not part of this list.
    await expect(pane).toBeVisible();
    await expect(page.locator('#runDetail [data-run-tab].active')).toHaveAttribute('data-run-tab', tab);
    await expect(page.locator('#runDetail [data-run-pane]:not([hidden])')).toHaveCount(1);
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

test('approvals queue: approve a waiting run with context from the sidebar', async ({ page }) => {
  const wf = await createWorkflow('ui-approval-queue', [
    script('prep', 'echo QUEUE_PREP'),
    approval('gate', 'prep'),
    script('finish', 'echo QUEUE_FINISH'),
  ]);
  const { id } = await startRun(wf, 'e2e queue approval');
  await waitRun(id, r => r.nodes?.gate?.status === 'waiting_approval', { label: 'queue gate waiting' });

  const errors = watchErrors(page);
  await page.goto('/#/issues');
  await expect(page.locator('#issuesView')).toBeVisible();
  await page.locator('.nav[data-view="approvals"]').click();
  await expect(page).toHaveURL(/#\/approvals$/);
  await expect(page.locator('#approvalsView')).toBeVisible();
  const approve = page.locator(`#approvalList [data-approve="${id}"][data-node="gate"]`);
  const card = page.locator('#approvalList .ap-card').filter({
    has: page.locator(`[data-approve="${id}"][data-node="gate"]`),
  });
  await expect(card).toBeVisible();
  await expect(card).toContainText('E2E ui-approval-queue');
  const context = `Queue approval ${cfg.runTag}`;
  await card.locator('[data-human-context]').fill(context);
  const [response] = await Promise.all([
    page.waitForResponse(r => new URL(r.url()).pathname === `/api/runs/${id}/approve` && r.request().method() === 'POST'),
    approve.click(),
  ]);
  expect(response.ok()).toBe(true);
  const run = await waitRun(id, r => r.status === 'succeeded', { label: 'succeeded after queue approval' });
  expect(run.nodes.gate.status).toBe('succeeded');
  expect(run.nodes.finish.status).toBe('succeeded');
  expect(run.humanContext).toBe(context);
  await expect(card).toHaveCount(0);
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

test('SOP designer: drag the failure outlet onto an upstream step to route rework', async ({ page }) => {
  const wf = await createWorkflow('ui-reject', [script('first', 'echo 1'), script('second', 'echo 2')]);
  const before = await get(`/api/workflows/${wf}`);
  const errors = watchErrors(page);
  await page.goto(`/#/flow/${encodeURIComponent(wf)}`);
  await expect(page.locator('#canvas .flow-node')).toHaveCount(2);
  const outlet = page.locator('#canvas .flow-node[data-id="second"] [data-outlet="reject"]');
  const from = await outlet.boundingBox();
  const to = await page.locator('#canvas .flow-node[data-id="first"]').boundingBox();
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 12 });
  await page.mouse.up();
  await expect(page.locator('#edges [data-reject-from="second"]')).toHaveCount(1);
  await page.locator('#saveWorkflow').click();
  await expect.poll(async () => (await get(`/api/workflows/${wf}`)).nodes.find(n => n.id === 'second').rejectTo, { timeout: 20_000 }).toBe('first');
  const after = await get(`/api/workflows/${wf}`);
  expect(after.nodes.find(n => n.id === 'second').onFailure).toBe('reject');
  expect(after.edges.map(e => [e.source, e.target])).toEqual(before.edges.map(e => [e.source, e.target]));
  expect(errors).toEqual([]);
});

test('SOP designer: header controls keep one 32px row at narrow widths', async ({ page }) => {
  const wf = await createWorkflow('ui-header', [script('first', 'echo 1')]);
  const errors = watchErrors(page);
  await page.goto(`/#/flow/${encodeURIComponent(wf)}`);
  await expect(page.locator('#canvas .flow-node')).toHaveCount(1);
  for (const width of [1000, 820]) {
    await page.setViewportSize({ width, height: 800 });
    for (const id of ['#flowHistory', '#flowSettingsToggle', '#saveWorkflow', '#publishWorkflow']) {
      const box = await page.locator(id).boundingBox();
      expect(box, `${id} at ${width}px`).not.toBeNull();
      expect(Math.round(box.height), `${id} height at ${width}px`).toBe(32);
    }
    const tops = await Promise.all(['#flowSwitch', '#saveWorkflow', '#publishWorkflow'].map(async id => {
      const b = await page.locator(id).boundingBox();
      return b.y + b.height / 2;
    }));
    expect(Math.max(...tops) - Math.min(...tops), `header row at ${width}px`).toBeLessThan(16);
  }
  expect(errors).toEqual([]);
});

test('SOP designer: the agent drawer shows name, role and on-failure in the first screen', async ({ page }) => {
  const agent = await createAgent('ui-drawer', '只用于界面测试');
  const wf = await createWorkflow('ui-drawer', [agentNode('worker', agent), script('after', 'echo 1')]);
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`/#/flow/${encodeURIComponent(wf)}`);
  await expect(page.locator('#canvas .flow-node')).toHaveCount(2);
  await page.locator('#canvas .flow-node[data-id="worker"]').click();
  const body = await page.locator('#properties .fd-db').boundingBox();
  await expect(page.locator('#properties details[data-fold="gates"]')).not.toHaveAttribute('open', '');
  await expect(page.locator('#properties details[data-fold="adv"]')).not.toHaveAttribute('open', '');
  for (const sel of ['[data-p="name"]', '[data-p="agentId"]', '[data-fail="reject"]']) {
    const box = await page.locator(`#properties ${sel}`).boundingBox();
    expect(box, sel).not.toBeNull();
    expect(box.y + box.height, `${sel} in the first screen`).toBeLessThanOrEqual(body.y + body.height + 1);
  }
  expect(errors).toEqual([]);
});

test('knowledge center: search shows cited hits', async ({ page }) => {
  const settings = await get('/api/knowledge/settings');
  test.skip(!settings.ready, 'knowledge base not configured');
  const errors = watchErrors(page);
  await openView(page, 'knowledge');
  await page.locator('[data-kb-tab="search"]').first().click();
  await page.locator('#kbSearchForm input[name="q"]').fill('部署');
  await page.locator('#kbSearchForm input[name="q"]').press('Enter');
  await expect(page.locator('.kb-hit').first()).toBeVisible({ timeout: 60_000 });
  expect(await page.locator('.kb-hit').count()).toBeGreaterThan(0);
  expect(errors).toEqual([]);
});

// The reply to "让 AI 分析" must stay between the question and the alert while
// it streams and after it finishes; it used to stream below the alert and then
// jump above it, which read as "no answer".
test('chat: asking AI about a failed run keeps the reply above the run alert', async ({ page }) => {
  test.skip(!cfg.model, 'AGENTFLOW_E2E_MODEL=0');
  test.setTimeout(420_000);
  // A conversation that has never generated answers 404 to the generation
  // poll; the chat treats that as "nothing running".
  const errors = watchErrors(page, { allow: [/^HTTP 404 GET \/api\/assistant\/sessions\/[^/]+\/generation\?after=/] });
  const wf = await createWorkflow('chat-alert', [script('boom', 'exit 3', { name: '会失败的节点' })]);
  const run = await startRun(wf, 'chat run alert');
  await waitRun(run.id, r => r.status === 'failed', { timeoutMs: 180_000, label: 'run fails' });
  const sid = `chat-e2e${Date.now().toString(16)}`;
  track(`assistant session ${sid}`, () => del(`/api/assistant/sessions/${sid}`));
  await put(`/api/assistant/sessions/${sid}`, {
    id: sid, title: `E2E 运行提醒 ${cfg.runTag}`, projectId: cfg.projectId,
    messages: [
      { role: 'user', content: '帮我跑一下这个 SOP' },
      { role: 'assistant', content: JSON.stringify({ drafts: [], message: `已启动运行 ${run.id}` }) },
    ],
  });
  await page.goto(`/chatbot/?session=${sid}&projectId=${cfg.projectId}`);
  const alert = page.locator('.message-column > .run-alerts');
  await expect(alert).toContainText('会失败的节点', { timeout: 60_000 });
  const tail = () => page.locator('.message-column').evaluate(col => [...col.children]
    .map(el => el.classList.contains('run-alerts') ? 'alert'
      : el.classList.contains('live') ? 'live'
      : el.classList.contains('user') ? 'user'
      : el.classList.contains('assistant') ? 'assistant' : '')
    .filter(Boolean).slice(-3).join(' > '));
  await alert.getByRole('button', { name: '让 AI 分析' }).click();
  await page.getByRole('button', { name: '发送消息' }).click();
  await expect.poll(tail).toBe('user > live > alert');
  await expect.poll(tail, { timeout: 300_000 }).toBe('user > assistant > alert');
  expect(errors).toEqual([]);
});

test('settings at ≤1000px: the drawer opens from the rail and closes once a page is picked', async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 1000 });
  const drawerOpen = () => page.evaluate(() => document.body.classList.contains('sidebar-open'));
  // A deep link shows the page, not the navigation on top of it.
  await page.goto('/#/skills');
  await expect(page.locator('#pageTitle')).toHaveText('技能管理');
  expect(await drawerOpen()).toBe(false);
  // Coming into settings from the workbench offers the navigation…
  await page.evaluate(() => { location.hash = '#/collaboration'; });
  await expect(page.locator('#pageTitle')).toHaveText('AI 工作台');
  await page.locator('#railSettingsEntry').click();
  await expect.poll(drawerOpen).toBe(true);
  // …and picking a page closes it.
  await page.locator('.platform-sidebar .nav[data-view="runners"]').click();
  await expect(page.locator('#pageTitle')).toHaveText('执行机器');
  await expect.poll(drawerOpen).toBe(false);
  await expect(page.locator('#runnersView')).toBeVisible();
});
