import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, settle, WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿]/;
const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];

async function englishPage(extra = []) {
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js', ...extra]) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

// Chinese left in product copy; user data (data-i18n-skip, data-i18n-data) may stay Chinese.
function leftoverChinese(root, { skipHidden = false } = {}) {
  const left = [];
  const skip = `[data-i18n-data],[data-i18n-skip],code${skipHidden ? ',[hidden]' : ''}`;
  for (const el of [root, ...root.querySelectorAll('*')]) {
    if (el.closest(skip)) continue;
    for (const node of el.childNodes) if (node.nodeType === 3 && HAN.test(node.nodeValue)) left.push(node.nodeValue.trim());
    for (const name of ATTRS) {
      const value = el.getAttribute(name);
      if (value && HAN.test(value)) left.push(`${name}="${value}"`);
    }
  }
  return left;
}

const NAME = '巡检-README摘要';
const REPLY = '当前项目共有 1 条需求。';
const ERROR = 'CLI 异常退出，未完成本轮回复';
const JOBS = [
  { id: 'job-ok', projectId: 'project-1', name: NAME, skipDispatch: true, assistantPrompt: 'x', enabled: true, intervalSec: 3600, lastStatus: 'success', lastRunAt: '2026-11-27T05:27:21Z', lastSessionId: 'ses-gone', lastSessionGone: true, lastAssistantMessage: REPLY },
  { id: 'job-failed', projectId: 'project-1', name: '巡检-Cursor', skipDispatch: true, assistantPrompt: 'y', enabled: false, intervalSec: 3600, lastStatus: 'failed', lastRunAt: '2026-11-27T05:32:17Z', lastSessionId: 'ses-failed', lastError: ERROR },
  { id: 'job-live', projectId: 'project-1', name: '巡检-Pi', skipDispatch: true, assistantPrompt: 'z', enabled: true, sessionMode: 'fixed', intervalSec: 3600, lastStatus: 'running', lastRunAt: '2026-11-27T05:32:09Z', lastSessionId: 'ses-live' },
];
const EXECUTIONS = [
  { id: 'sjx-1', jobId: 'job-ok', jobName: NAME, jobKind: 'assistant', projectId: 'project-1', traceId: '0123456789abcdef0123456789abcdef', triggerOrigin: 'manual_run', status: 'success', assistantMessage: REPLY, sessionId: 'ses-gone', sessionGone: true, startedAt: '2026-11-27T05:27:21Z', finishedAt: '2026-11-27T05:28:24Z', durationMs: 63000 },
  { id: 'sjx-2', jobId: 'job-failed', jobName: '巡检-Cursor', jobKind: 'assistant', projectId: 'project-1', traceId: 'fedcba9876543210fedcba9876543210', triggerOrigin: 'manual_run', status: 'failed', error: ERROR, sessionId: 'ses-failed', startedAt: '2026-11-27T05:32:17Z', finishedAt: '2026-11-27T05:32:17Z', durationMs: 400 },
];
const routes = (items = EXECUTIONS) => [
  { method: 'GET', path: '/api/assistant/models', body: { default: 'glm-test', models: [{ id: 'glm-test', name: 'GLM Test', family: 'glm' }], engines: [{ id: 'codebuddy', name: 'CodeBuddy', configured: true }] } },
  { method: 'GET', path: /^\/api\/schedule-job-executions\/dashboard\?/, body: { items, total: items.length, records: items.length, recentFailed: 1, overview: { current: { total: 2, finished: 2, success: 1, failed: 1, running: 0, avgMs: 31700 }, previous: { total: 0 } } } },
];

async function englishCopy(t, app, selectors) {
  const en = await englishPage();
  t.after(() => en.close());
  const root = en.document.createElement('div');
  root.innerHTML = selectors.map((s) => app.document.querySelector(s).outerHTML).join('');
  en.document.body.append(root);
  await new Promise((resolve) => en.setTimeout(resolve, 0));
  return root;
}

async function scheduleApp(t, executions) {
  const app = await loadApp({ routes: routes(executions) });
  t.after(app.close);
  // The app page runs i18n.js in Chinese; lfT answers in English like a page loaded with data-lang="en".
  app.evaluate("window.lfT = (zh, en) => en; toast = () => {};");
  app.router.get('/api/schedule-jobs', () => app.evaluate('state.data.scheduleJobs'));
  app.evaluate(`state.projectFilter='project-1';state.data.schedulerEnabled=true;state.data.scheduleJobs=${JSON.stringify(JOBS)};switchView('aiSchedules');renderScheduleJobs()`);
  await settle(6);
  return app;
}

test('English AI schedule list keeps job names and AI replies as written and leaves no Chinese copy', async (t) => {
  const app = await scheduleApp(t);
  const root = await englishCopy(t, app, ['#aiSchedulesView .tabs, #aiSchedulesView [role=tablist]', '#aiScheduleProjectPanel']);
  assert.deepEqual(leftoverChinese(root), []);
  const names = [...root.querySelectorAll('.schedule-job-main b [data-i18n-skip]')].map((el) => el.textContent);
  assert.deepEqual(names.sort(), [NAME, '巡检-Cursor', '巡检-Pi'].sort());
  const text = root.textContent.replace(/\s+/g, ' ');
  assert.ok(text.includes(REPLY), 'the AI reply is not fragment-translated');
  assert.match(root.querySelector('[data-edit-schedule="job-ok"] small').textContent, new RegExp(`AM, ${REPLY}$`), 'the reply keeps its separator');
  assert.ok(text.includes(ERROR), 'the server error is not fragment-translated');
  assert.doesNotMatch(text, /READMESummary|Current project/);
  assert.match(text, /3 tasks/);
  assert.match(text, /Failed: CLI 异常退出/);
  assert.match(text, /Hourly · Same conversation · Running/);
  assert.deepEqual([...root.querySelectorAll('.schedule-latest-btn')].map((el) => el.textContent), ['Run history (session cleaned up)', 'View failed conversation ↗', 'View live run ↗']);
  // Accessible names: the product words are English, the job name is kept as written.
  assert.equal(root.querySelector('[data-toggle-schedule="job-ok"]').getAttribute('aria-label'), `Enable ${NAME}`);
  assert.equal(root.querySelector('[data-schedule-menu="job-ok"]').getAttribute('aria-label'), `More actions: ${NAME}`);
});

test('English AI schedule editor shows the latest-run bar and run options in English', async (t) => {
  const app = await scheduleApp(t);
  await app.evaluate(`openScheduleJobModal('assistant',state.data.scheduleJobs.find(j=>j.id==='job-failed'))`);
  await settle(6);
  const root = await englishCopy(t, app, ['#scheduleModal']);
  assert.deepEqual(leftoverChinese(root, { skipHidden: true }), []);
  const bar = root.querySelector('#scheduleLatestExecBar');
  assert.match(bar.textContent, /Latest run/);
  assert.equal(bar.querySelector('[data-modal-run-schedule]').textContent, 'Run once now');
  assert.equal(bar.querySelector('[data-modal-jump-latest]').textContent, 'View failed conversation ↗');
  assert.equal(bar.querySelector('.schedule-latest-exec-summary').textContent, ERROR, 'the error is shown as written');
  assert.equal(root.querySelector('#scheduleSaveAndRun').textContent, 'Save and run now');
  assert.equal(root.querySelector('[type=submit]').textContent, 'Save only');
  assert.match(root.querySelector('.schedule-open-after-run').textContent, /Open the conversation after running/);
});

test('English AI schedule editor keeps the live-run hint translated while a run is in progress', async (t) => {
  const app = await scheduleApp(t);
  await app.evaluate(`openScheduleJobModal('assistant',state.data.scheduleJobs.find(j=>j.id==='job-live'))`);
  await settle(6);
  const root = await englishCopy(t, app, ['#scheduleLatestExecBar']);
  assert.deepEqual(leftoverChinese(root), []);
  assert.match(root.textContent, /Running on the server\. Once the conversation is ready, click to watch it live\./);
  assert.equal(root.querySelector('[data-modal-jump-latest]').textContent, 'View live run ↗');
});

test('English run history keeps job names, AI replies and errors as written', async (t) => {
  const app = await scheduleApp(t);
  app.document.querySelector('[data-ai-schedule-tab="executions"]').click();
  await settle(8);
  const root = await englishCopy(t, app, ['#scheduleExecList']);
  const names = [...root.querySelectorAll('.sched-exec-name')].map((el) => el.textContent);
  assert.deepEqual(names, [NAME, '巡检-Cursor']);
  const lines = [...root.querySelectorAll('.sched-exec-main p')].map((el) => el.textContent);
  assert.deepEqual(lines, [REPLY, ERROR]);
  assert.deepEqual([...root.querySelectorAll('.sched-exec-action > span')].map((el) => el.textContent), ['Session cleaned up', 'Open conversation']);
  assert.equal(root.querySelector('[data-schedule-menu="sjx-1"]').getAttribute('aria-label').startsWith(`More actions: ${NAME} `), true);
  const options = [...app.document.querySelectorAll('#scheduleExecJob option')].slice(1);
  assert.ok(options.length && options.every((o) => o.hasAttribute('data-i18n-skip')), 'job names in the filter are never translated');
});

test('English run-history filter keeps the chosen job name in the overview scope', async (t) => {
  const app = await scheduleApp(t);
  app.document.querySelector('[data-ai-schedule-tab="executions"]').click();
  await settle(8);
  const job = app.document.querySelector('#scheduleExecJob');
  job.value = 'job-ok';
  job.dispatchEvent(new app.window.Event('change'));
  await settle(8);
  const root = await englishCopy(t, app, ['#scheduleExecOverviewScope', '#scheduleExecJob']);
  assert.equal(root.querySelector('#scheduleExecOverviewScope').textContent, `(Last 7 days · ${NAME})`);
  const [all, ...names] = [...root.querySelectorAll('option')].map((o) => o.textContent);
  assert.equal(all, 'All tasks');
  assert.deepEqual(names.sort(), ['巡检-Cursor', '巡检-Pi', NAME].sort());
});

test('a job name that equals product copy is shown as typed in English', async (t) => {
  const app = await scheduleApp(t);
  // "执行记录" is product copy that this PR translates to "Run history"; a task
  // named after it is user data and must stay Chinese.
  app.evaluate(`state.data.scheduleJobs=[{id:'job-copy',projectId:'project-1',name:'执行记录',skipDispatch:true,assistantPrompt:'x',enabled:true,intervalSec:3600,lastStatus:'success',lastRunAt:'2026-11-27T05:27:21Z',lastAssistantMessage:'代码审查'}]; renderScheduleJobs()`);
  await settle(4);
  const root = await englishCopy(t, app, ['#aiSchedulesView .tabs, #aiSchedulesView [role=tablist]', '#aiScheduleProjectPanel']);
  const row = root.querySelector('[data-edit-schedule="job-copy"]');
  assert.ok(row);
  assert.ok(row.textContent.includes('执行记录'), 'the task name is kept');
  assert.ok(row.textContent.includes('代码审查'), 'an AI reply that equals product copy is kept');
  assert.ok(!row.textContent.includes('Run history'), 'the task name is not translated to product copy');
  assert.equal(root.querySelector('[data-schedule-menu="job-copy"]').getAttribute('aria-label'), 'More actions: 执行记录');
  assert.equal(root.querySelector('[data-toggle-schedule="job-copy"]').getAttribute('aria-label'), 'Enable 执行记录');
});

test('English run details label every field in English and keep the reply and run titles as written', async (t) => {
  const usage = { totalTokens: 1530, inputTokens: 1200, outputTokens: 330, cacheReadTokens: 900, cacheWriteTokens: 12 };
  const app = await scheduleApp(t, [
    { ...EXECUTIONS[0], model: 'deepseek-v4.1-flash', usage },
    { id: 'sjx-3', jobId: 'job-loop', jobName: 'LOOP 巡检', jobKind: 'dispatch', projectId: 'project-1', traceId: 'aa'.repeat(16), triggerOrigin: 'schedule', status: 'success', pulled: 3, dispatched: 1, runs: [{ runId: 'run-1', issueTitle: '执行记录' }], startedAt: '2026-11-27T06:00:00Z', finishedAt: '2026-11-27T06:00:05Z', durationMs: 5000 },
  ]);
  app.document.querySelector('[data-ai-schedule-tab="executions"]').click();
  await settle(8);
  const root = await englishCopy(t, app, ['#scheduleExecList']);
  const labels = [...root.querySelectorAll('.sched-exec-fields dt, .sched-exec-detail-text > b')].map((el) => el.textContent);
  assert.deepEqual(labels.filter((text) => HAN.test(text)), []);
  for (const label of ['Start time', 'End time', 'Total tokens', 'Input tokens', 'Output tokens', 'Cache read tokens', 'Cache write tokens', 'AI reply', 'Candidates / started', 'Dispatched runs']) {
    assert.ok(labels.includes(label), `${label} is shown`);
  }
  assert.equal(root.querySelector('.sched-exec-detail pre')?.textContent, REPLY);
  assert.equal(root.querySelector('.sched-exec-run [data-i18n-skip]').textContent, '执行记录', 'a dispatched requirement title is kept as typed');
});

test('the Harness select panel and trigger keep user-data options as written in English', async (t) => {
  const en = await englishPage(['app/hui-select.js']);
  t.after(() => en.close());
  const doc = en.document;
  const tick = () => new Promise((resolve) => en.setTimeout(resolve, 0));
  // The run-history job filter: product copy first, then job names marked as
  // data. "执行记录" equals product copy ("Run history") and must stay as typed.
  const options = () => [
    new en.Option('全部任务', ''),
    ...[[NAME, 'job-ok'], ['执行记录', 'job-copy']].map(([name, id]) => { const o = new en.Option(name, id); o.dataset.i18nSkip = ''; return o; }),
    new en.Option('任务失败', 'job-data'),
  ];
  const inline = doc.createElement('select');
  inline.append(...options());
  inline.options[3].setAttribute('data-i18n-data', '');
  doc.body.append(inline);
  await tick();
  inline.dispatchEvent(new en.MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0 }));
  await tick();
  const labels = (root) => [...root.querySelectorAll('.hui-select-option-label')].map((el) => el.textContent);
  const panel = doc.querySelector('.hui-select-panel');
  assert.ok(panel, 'the inline panel opens on mousedown');
  // Product copy is translated; marked user data is not fragment-translated.
  assert.deepEqual(labels(panel), ['All tasks', NAME, '执行记录', '任务失败']);
  assert.deepEqual([...panel.querySelectorAll('.hui-select-option-label')].map((el) => [el.hasAttribute('data-i18n-skip'), el.hasAttribute('data-i18n-data')]), [[false, false], [true, false], [true, false], [false, true]]);
  en.HuiSelect.closeAll?.();

  // Trigger mode shows the chosen option in its own button: the mark follows
  // the selection, so switching back to product copy translates again.
  const trigger = doc.createElement('select');
  trigger.append(...options());
  doc.body.append(trigger);
  const api = en.HuiSelect.enhance(trigger);
  const value = () => api.trigger.querySelector('.hui-select-value');
  await tick();
  assert.equal(value().textContent, 'All tasks');
  const pick = (index) => { trigger.options[index].selected = true; trigger.dispatchEvent(new en.Event('change')); };
  pick(2);
  await tick();
  assert.equal(value().textContent, '执行记录');
  assert.ok(value().hasAttribute('data-i18n-skip'));
  pick(0);
  await tick();
  assert.equal(value().textContent, 'All tasks');
  assert.equal(value().hasAttribute('data-i18n-skip'), false);
  api.trigger.click();
  await tick();
  assert.deepEqual(labels(api.panel), ['All tasks', NAME, '执行记录', '任务失败']);
});

// "AI 定时任务" and "共享任务市场" are long enough for i18n.js to replace them inside
// longer Chinese sentences, so every sentence that embeds them needs its own entry.
// Without one, English showed "AI scheduled task最多引用 12 个项目资产".
test('English sentences that embed the new AI schedule terms are translated whole, never mixed', async (t) => {
  const en = await englishPage();
  t.after(() => en.close());
  const cases = [
    // web/app/sync-schedule.js save checks and results
    ['AI 定时任务最多引用 12 个项目资产', 'An AI scheduled task can reference up to 12 project assets'],
    ['AI 定时任务引用的项目资产无效，请重新选择后保存', 'The AI scheduled task references an invalid project asset. Select it again, then save'],
    ['请填写 AI 定时任务的提示词', 'Enter a prompt for the AI scheduled task'],
    ['AI 定时任务已执行', 'AI scheduled task ran'],
    ['读取 AI 定时任务失败：network down', 'Failed to load AI scheduled tasks: network down'],
    ['当前项目还没有 AI 定时任务，点击右上角「新建 AI 定时任务」。', 'This project has no AI scheduled tasks yet. Click “New AI scheduled task” at the top right.'],
    ['请先在顶栏选择项目，再管理 AI 定时任务。', 'Select a project in the top bar first, then manage AI scheduled tasks.'],
    ['请先在顶栏选择项目，再查看执行记录。', 'Select a project in the top bar first, then view the run history.'],
    // web/app/shared-schedules.js and web/app/bots.js empty states
    ['还没有共享任务。在本项目的独立 AI 定时任务旁点击「发布共享」，即可供其他项目订阅。', 'No shared tasks yet. Click “Publish as shared” next to a standalone AI scheduled task in this project so other projects can subscribe.'],
    ['还没有机器人。三步完成配置，随后在 LOOP 设置或 AI 定时任务中选择这个 Bot。', 'No bots yet. Set one up in three steps, then choose it in LOOP settings or in an AI scheduled task.'],
    ['端到端 = 该需求最近一次执行的开始到结束，含等待。', "End-to-end = from the start to the end of this requirement's latest run, including waits."],
    // Server errors shown as they come back from the API
    ['AI 定时任务必须填写提示词', 'An AI scheduled task needs a prompt'],
    ['AI 定时任务引用功能未就绪', 'AI scheduled task references are not available yet'],
    ['仅独立 AI 定时任务可以发布共享版本', 'Only standalone AI scheduled tasks can publish a shared version'],
    ['只能从当前项目的独立 AI 定时任务发布共享版本', 'Shared versions can only be published from a standalone AI scheduled task in this project'],
    ['订阅需要明确的共享任务和版本，且必须是 AI 定时任务', 'A subscription needs a specific shared task and version, and it must be an AI scheduled task'],
    ['这是本地模拟执行结果，用于验证消息结构、人工确认和 Bot 通知联动。实际任务状态以平台显示为准。', 'This is a simulated local result for checking the message layout, human review and Bot notifications. The platform shows the actual task status.'],
  ];
  for (const [zh, english] of cases) assert.equal(en.lfTranslate(zh), english, zh);
  // The terms on their own still translate.
  assert.equal(en.lfTranslate('AI 定时任务'), 'AI scheduled task');
  assert.equal(en.lfTranslate('共享任务市场'), 'Shared task market');

  // Rendered as a toast, the same path a server error takes.
  const node = en.document.createElement('div');
  node.className = 'toast';
  node.textContent = 'AI 定时任务最多引用 12 个项目资产';
  en.document.body.append(node);
  await new Promise((resolve) => en.setTimeout(resolve, 0));
  assert.equal(node.textContent, 'An AI scheduled task can reference up to 12 project assets');
});

// internal/repo/shared_schedule_assets.go wraps ErrSharedScheduleReferences as
// "共享任务引用资产不可用：<detail>" with asset ids in the detail. A dictionary key for the
// prefix alone turned it into "…is unavailable: agent/ghost-agent 不存在".
test('English shared-task reference errors are translated whole, never half English and half Chinese', async (t) => {
  const en = await englishPage();
  t.after(() => en.close());
  const prefix = 'A project asset referenced by the shared task is unavailable: ';
  const cases = [
    ['共享任务引用资产不可用：agent/ghost-agent 不存在', 'agent/ghost-agent doesn\'t exist'],
    ['共享任务引用资产不可用：agent:ghost 不存在', 'agent:ghost doesn\'t exist'],
    ['共享任务引用资产不可用：agent/x 不存在或尚未共享', 'agent/x doesn\'t exist or isn\'t shared yet'],
    ['共享任务引用资产不可用：skill/s1 不属于当前项目且尚未共享', 'skill/s1 isn\'t in this project and isn\'t shared yet'],
    ['共享任务引用资产不可用：issue 不支持随 AI 定时任务共享', 'issue can\'t be shared with an AI scheduled task'],
    ['共享任务引用资产不可用：最多引用 12 个资产', 'a task can reference up to 12 assets'],
    ['共享任务引用资产不可用：直接引用列表与共享版本不一致', 'the referenced assets don\'t match the shared version'],
    ['共享任务引用资产不可用：agent/a1 的归属已变更，请发布新版本', 'the owner of agent/a1 has changed. Publish a new version'],
    ['共享任务引用资产不可用：sop/flow-1 的权限或归属已变更', 'the access or owner of sop/flow-1 has changed'],
    ['共享任务引用资产不可用：共享 agent/a1 失败：database is locked', 'failed to share agent/a1: database is locked'],
  ];
  for (const [zh, english] of cases) assert.equal(en.lfTranslate(zh), prefix + english, zh);
  // Wrapped once more by a caller, the reference error is still one English sentence.
  assert.equal(en.lfTranslate('执行失败：共享任务引用资产不可用：agent/ghost 不存在'), `Failed: ${prefix}agent/ghost doesn't exist`);
  // Sentences that only share the wording keep translating as before.
  assert.equal(en.lfTranslate('沙箱不属于当前项目且尚未共享'), '沙箱不属于当前项目且尚未共享');
  assert.equal(en.lfTranslate('共享任务使用的角色不属于当前项目且尚未共享'), '共享任务使用的角色不属于当前项目且尚未共享');

  // The publish dialog shows the API error as text, the path the reviewer reproduced.
  const node = en.document.createElement('div');
  node.id = 'sharedSchedulePublishError';
  node.textContent = '共享任务引用资产不可用：agent/ghost-agent 不存在';
  en.document.body.append(node);
  await new Promise((resolve) => en.setTimeout(resolve, 0));
  assert.equal(node.textContent, `${prefix}agent/ghost-agent doesn't exist`);
});

test('English publish toast names the Shared task market without mixing languages or rewriting the task name', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate("window.lfT = (zh, en) => en; window.__toasts = []; toast = (m) => window.__toasts.push(m);");
  const source = { id: 'source-job', projectId: 'project-1', name: '巡检-README摘要', skipDispatch: true, assistantPrompt: '检查需求' };
  app.evaluate(`state.data.scheduleJobs=${JSON.stringify([source])};renderScheduleJobs()`);
  app.router.get('/api/shared-schedule-tasks', []);
  app.router.post('/api/schedule-jobs/source-job/publish', (call) => ({ id: 'shared-1', sourceProjectId: 'project-1', sourceJobId: 'source-job', ...call.body, version: 1 }));
  app.document.querySelector('[data-publish-schedule]').click();
  await settle(4);
  const form = app.document.querySelector('#sharedSchedulePublishForm');
  form.elements.prompt.value = '检查需求';
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(5);
  const toasts = [...app.evaluate('window.__toasts')];
  assert.deepEqual(toasts, ['Published version 1 to the Shared task market. Other projects can subscribe to it now']);
});

test('English Shared task market, version dialog and subscription keep shared task data as written, even when it matches a dictionary key', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  // Every value here is also a dictionary key ("代码审查" → Code review, "测试" → Testing,
  // "修复" → Fix, "执行记录" → Run history), so data-i18n-data would still rewrite it.
  app.evaluate('window.lfT = (zh, en) => en;');
  const shared = { id: 'shared-1', version: 2, sourceProjectId: 'p2', sourceProjectName: '执行记录', sourceJobId: 's', name: '代码审查', description: '测试', changeNote: '修复', prompt: '检查需求', intervalSec: 86400, parameters: [] };
  app.router.get('/api/shared-schedule-tasks', [shared]);
  app.router.get('/api/shared-schedule-tasks/shared-1/versions', [shared]);
  app.evaluate(`state.projectFilter='project-1';sharedScheduleCatalog=${JSON.stringify([shared])};renderSharedScheduleMarket()`);
  await settle(2);
  const market = await englishCopy(t, app, ['#aiScheduleMarketPanel']);
  const card = market.querySelector('.mcp-market-card');
  assert.ok(card, 'the market card renders');
  assert.equal(card.querySelector('h3').textContent, '代码审查');
  assert.equal(card.querySelector('.shared-schedule-market-source').textContent, '执行记录');
  assert.equal(card.querySelector('p').textContent, '测试');

  await app.evaluate("openSharedScheduleVersions('shared-1', false)");
  await settle(4);
  const versions = await englishCopy(t, app, ['#sharedScheduleVersionModal']);
  const versionText = versions.textContent.replace(/\s+/g, ' ');
  for (const kept of ['代码审查', '执行记录', '修复']) assert.ok(versionText.includes(kept), `version dialog keeps ${kept}: ${versionText}`);
  assert.doesNotMatch(versionText, /Code review|Run history|\bFix\b/);
  app.evaluate("document.querySelector('#sharedScheduleVersionModal').classList.remove('open')");

  await app.evaluate("selectAIScheduleTab('market')");
  app.document.querySelector('[data-subscribe-shared]').click();
  await settle(5);
  const binding = await englishCopy(t, app, ['#sharedScheduleBinding']);
  const bindingText = binding.textContent.replace(/\s+/g, ' ');
  for (const kept of ['代码审查', '执行记录', '测试']) assert.ok(bindingText.includes(kept), `subscription keeps ${kept}: ${bindingText}`);
  assert.doesNotMatch(bindingText, /Code review|Run history|Testing/);
  assert.match(bindingText, /Source: 执行记录/);
});
