import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, settle, WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿]/;
const ATTRS = ['placeholder', 'title', 'aria-label', 'data-tip', 'data-tooltip'];

async function englishPage() {
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

// Chinese left in product copy; user data (data-i18n-data) may stay Chinese.
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
const ROUTES = [
  { method: 'GET', path: '/api/assistant/models', body: { default: 'glm-test', models: [{ id: 'glm-test', name: 'GLM Test', family: 'glm' }], engines: [{ id: 'codebuddy', name: 'CodeBuddy', configured: true }] } },
  { method: 'GET', path: /^\/api\/schedule-job-executions\/dashboard\?/, body: { items: EXECUTIONS, total: 2, records: 2, recentFailed: 1, overview: { current: { total: 2, finished: 2, success: 1, failed: 1, running: 0, avgMs: 31700 }, previous: { total: 0 } } } },
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

async function scheduleApp(t) {
  const app = await loadApp({ routes: ROUTES });
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
  const names = [...root.querySelectorAll('.schedule-job-main b [data-i18n-data]')].map((el) => el.textContent);
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
  assert.ok(options.length && options.every((o) => o.hasAttribute('data-i18n-data')), 'job names in the filter are data');
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
