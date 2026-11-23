import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const FAILED_TRACE = '0123456789abcdef0123456789abcdef';
const OK_TRACE = 'fedcba9876543210fedcba9876543210';

const HOUR = 3600000;

// Mirrors /api/schedule-job-executions/dashboard: filters, pages and counts
// the 7×24h windows (by the hour an execution started) on the "server".
function fakeDashboard(list, now = Date.parse('2026-11-16T12:00:00Z')) {
  const text = (e) => [e.jobName, e.jobId, e.traceId, e.error, e.assistantMessage, ...(e.runs || []).flatMap((r) => [r.issueTitle, r.issueId, r.runId])].filter(Boolean).join('\n').toLowerCase();
  const currentFrom = Math.floor(now / HOUR) * HOUR - 167 * HOUR;
  const previousFrom = currentFrom - 168 * HOUR;
  const windowOf = (items, from, to) => {
    const inside = items.filter((e) => { const t = Date.parse(e.startedAt); return t >= from && t < to; });
    const finished = inside.filter((e) => e.status !== 'running');
    const timed = finished.filter((e) => Number(e.durationMs) > 0);
    return {
      total: inside.length, finished: finished.length,
      success: inside.filter((e) => e.status === 'success').length,
      failed: inside.filter((e) => e.status === 'failed').length,
      running: inside.length - finished.length,
      avgMs: timed.length ? Math.floor(timed.reduce((sum, e) => sum + Number(e.durationMs), 0) / timed.length) : 0,
    };
  };
  return (call) => {
    const p = call.url.searchParams;
    const project = list.filter((e) => e.projectId === p.get('projectId'));
    const scoped = project.filter((e) => !p.get('jobId') || e.jobId === p.get('jobId'));
    const since = p.get('since') ? Date.parse(p.get('since')) : 0;
    const q = (p.get('q') || '').trim().toLowerCase();
    const matches = scoped
      .filter((e) => (!p.get('status') || e.status === p.get('status')) && (!since || Date.parse(e.startedAt) >= since) && (!q || text(e).includes(q)))
      .sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt));
    const offset = Number(p.get('offset')) || 0, limit = Number(p.get('limit')) || 20;
    return {
      items: matches.slice(offset, offset + limit),
      total: matches.length,
      overview: { current: windowOf(scoped, currentFrom, Infinity), previous: windowOf(scoped, previousFrom, currentFrom), since: new Date(currentFrom).toISOString() },
      records: project.length,
      recentFailed: windowOf(project, currentFrom, Infinity).failed,
    };
  };
}
const DASHBOARD = /^\/api\/schedule-job-executions\/dashboard\?/;
const dashboardCalls = (app) => app.router.calls.filter((c) => c.path === '/api/schedule-job-executions/dashboard');
// Search waits for a pause in typing before it asks the server.
async function typeSearch(app, value) {
  const search = app.document.querySelector('#scheduleExecSearch');
  search.value = value;
  search.dispatchEvent(new app.window.Event('input'));
  await new Promise((resolve) => setTimeout(resolve, 300));
  await settle();
}
async function change(app, selector, value) {
  const el = app.document.querySelector(selector);
  el.value = value;
  el.dispatchEvent(new app.window.Event('change'));
  await settle();
}

function executions() {
  return [
    { id: `sjx-${FAILED_TRACE}`, jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: FAILED_TRACE, triggerOrigin: 'timer', status: 'failed', error: 'CodeBuddy key expired', startedAt: '2026-11-16T08:00:00Z', finishedAt: '2026-11-16T08:00:05Z', durationMs: 5200 },
    { id: `sjx-${OK_TRACE}`, jobId: 'job-trigger', jobName: '触发配置', jobKind: 'trigger', projectId: 'project-1', traceId: OK_TRACE, triggerOrigin: 'manual_run', status: 'success', pulled: 3, dispatched: 1, runs: [{ issueId: 'issue-1', issueTitle: '登录页改版', runId: 'run-77' }], startedAt: '2026-11-16T07:00:00Z', finishedAt: '2026-11-16T07:01:30Z', durationMs: 90000 },
  ];
}

test('execution dashboard lists history with metrics, filters and copyable traces', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get(DASHBOARD, fakeDashboard(executions()));
  app.router.get('/api/schedule-jobs', () => app.evaluate('state.data.scheduleJobs'));
  app.evaluate(`
    state.projectFilter='project-1';
    state.data.schedulerEnabled=true;
    state.data.scheduleJobs=[
      {id:'job-ai',projectId:'project-1',name:'Daily summary',skipDispatch:true,assistantPrompt:'x',enabled:true,intervalSec:3600,lastStatus:'failed',lastError:'CodeBuddy key expired',lastTraceId:'${FAILED_TRACE}',lastRunAt:'2026-11-16T08:00:00Z'},
      {id:'job-trigger',projectId:'project-1',name:'触发配置',workflowId:'wf',enabled:true,intervalSec:300,lastStatus:'success',lastTraceId:'${OK_TRACE}',lastRunAt:'2026-11-16T07:00:00Z'}
    ];
    window.__copied=[];window.__opened=[];
    scheduleExecNow=()=>Date.parse('2026-11-16T12:00:00Z');
    copyText=(value)=>{window.__copied.push(value)};
    openRunFromId=(id)=>{window.__opened.push(id)};
    renderScheduleJobs();
  `);

  const jobList = app.document.querySelector('#aiScheduleJobList');
  assert.equal(jobList.querySelector('[data-copy-trace]'), null, 'AI job rows do not carry a trace button');
  // Before the history loads, the notes come from the job's latest outcome.
  app.evaluate(`copyScheduleTrace('${FAILED_TRACE}')`);
  const fromJob = app.evaluate('window.__copied.at(-1)');
  assert.equal(fromJob.split('\n')[0], '定时任务执行排障信息');
  assert.equal(fromJob.split('\n')[1], `Trace ID: ${FAILED_TRACE}`);
  assert.match(fromJob, /任务: Daily summary（job-ai）\n类型: AI 提示词任务\n项目: .+（project-1）/);
  assert.match(fromJob, /状态: 失败/);
  assert.match(fromJob, /错误: CodeBuddy key expired/);
  assert.match(fromJob, /页面: http/);

  jobList.querySelector('[data-schedule-history="job-ai"]').click();
  await settle();
  const panel = app.document.querySelector('#aiScheduleExecPanel');
  assert.equal(panel.hidden, false);
  assert.equal(app.document.querySelector('#aiScheduleProjectPanel').hidden, true);
  assert.equal(app.document.querySelector('#aiScheduleExecTab').getAttribute('aria-selected'), 'true');
  const request = dashboardCalls(app).at(-1);
  assert.match(request.search, /projectId=project-1/);
  assert.match(request.search, /jobId=job-ai/, 'the job filter is applied by the server');

  // Opened from the job row: filtered to that job.
  let rows = panel.querySelectorAll('.sched-exec-row');
  assert.equal(rows.length, 1);
  assert.match(rows[0].textContent, /失败.*Daily summary.*CodeBuddy key expired.*2026\/11\/16.*5 秒.*89abcdef/s);
  assert.equal(app.document.querySelector('#aiScheduleExecCount').textContent, '2');
  assert.match(app.document.querySelector('#scheduleExecOverviewScope').textContent, /最近 7 天 · Daily summary/);

  await change(app, '#scheduleExecJob', '');
  rows = panel.querySelectorAll('.sched-exec-row');
  assert.equal(rows.length, 2);
  const metrics = app.document.querySelector('#scheduleExecMetrics').textContent;
  assert.match(metrics, /执行次数\s*2/);
  assert.match(metrics, /成功率\s*50%/);
  assert.equal(app.document.querySelectorAll('#scheduleExecMetrics .sched-exec-metric').length, 4);
  const successCard = app.document.querySelectorAll('#scheduleExecMetrics .sched-exec-metric')[1];
  assert.match(successCard.textContent, /成功率\s*50%\s*成功 1 次\s*失败 1 次/);
  assert.match(successCard.querySelector('.has-failures').textContent, /失败 1 次/);
  assert.match(rows[1].textContent, /候选 3 · 启动 1 · 登录页改版/);
  assert.match(rows[1].textContent, /1 分 30 秒/);

  // Details stay collapsed until the row is expanded.
  const detail = rows[1].nextElementSibling;
  assert.ok(detail.classList.contains('sched-exec-detail'));
  assert.equal(detail.hidden, true);
  rows[1].querySelector('[data-exec-toggle]').click();
  assert.equal(detail.hidden, false);
  assert.equal(rows[1].querySelector('[data-exec-toggle]').getAttribute('aria-expanded'), 'true');
  assert.match(detail.textContent, /触发方式手动运行/);
  assert.match(detail.textContent, new RegExp(OK_TRACE));

  rows[1].querySelector('[data-open-exec-first-run]').click();
  detail.querySelector('[data-open-exec-run]').click();
  assert.equal(app.evaluate('JSON.stringify(window.__opened)'), JSON.stringify(['run-77', 'run-77']));
  rows[1].querySelector('[data-copy-trace]').click();
  const fromExec = app.evaluate('window.__copied.at(-1)');
  assert.equal(fromExec.split('\n')[1], `Trace ID: ${OK_TRACE}`);
  assert.match(fromExec, /类型: SOP 触发/);
  assert.match(fromExec, /触发方式: 手动运行/);
  assert.match(fromExec, /状态: 成功/);
  assert.match(fromExec, /耗时: 1 分 30 秒/);
  assert.match(fromExec, /候选 \/ 启动: 3 \/ 1/);
  assert.match(fromExec, /派发运行: run-77（登录页改版）/);
  assert.doesNotMatch(fromExec, /错误:/);

  await change(app, '#scheduleExecStatus', 'failed');
  assert.match(dashboardCalls(app).at(-1).search, /status=failed/);
  rows = panel.querySelectorAll('.sched-exec-row');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].dataset.status, 'failed');
});

test('an AI execution links to the conversation it ran in', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get(DASHBOARD, fakeDashboard([
    { id: 'sjx-ok', jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: OK_TRACE, status: 'success', assistantMessage: '巡检完成', sessionId: 'chat-run-1', startedAt: '2026-11-16T09:00:00Z', durationMs: 3000 },
    executions()[0],
  ]));
  app.evaluate(`state.projectFilter='project-1';state.data.scheduleJobs=[];renderScheduleJobs();`);
  await app.evaluate(`selectAIScheduleTab('executions')`);
  await settle();
  const rows = app.document.querySelectorAll('#scheduleExecList .sched-exec-row');
  assert.equal(rows.length, 2);
  const link = rows[0].querySelector('[data-open-exec-chat]');
  assert.ok(link, 'execution with a conversation offers an open link');
  const url = new URL(link.href);
  assert.equal(url.searchParams.get('chatSession'), 'chat-run-1');
  assert.equal(url.searchParams.get('projectId'), 'project-1');
  assert.equal(url.hash, '#/collaboration');
  assert.equal(link.target, '_blank');
  assert.equal(rows[1].querySelector('[data-open-exec-chat]'), null, 'a run that failed before opening a conversation has nothing to open');
  const none = rows[1].querySelector('.sched-exec-action.is-unavailable');
  assert.match(none.textContent, /无会话/, 'the slot says why instead of vanishing');
  assert.match(none.title, /创建会话之前/);
  for (const row of rows) {
    const slots = row.querySelector('.sched-exec-action-slots');
    assert.equal(slots.children.length, 2, 'every row has the same two action slots');
    assert.ok(slots.lastElementChild.querySelector('[data-schedule-menu]'));
  }
});

test('an interrupted AI execution that opened a conversation still links to it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get(DASHBOARD, fakeDashboard([
    { id: 'sjx-cut', jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: FAILED_TRACE, status: 'failed', error: '上次运行中断（服务重启）', sessionId: 'chat-cut', startedAt: '2026-11-16T09:00:00Z', durationMs: 247000 },
    { id: 'sjx-run', jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: OK_TRACE, status: 'running', startedAt: '2026-11-16T10:00:00Z' },
  ]));
  app.evaluate(`state.projectFilter='project-1';state.data.scheduleJobs=[];renderScheduleJobs();`);
  await app.evaluate(`selectAIScheduleTab('executions')`);
  await settle();
  const rows = app.document.querySelectorAll('#scheduleExecList .sched-exec-row');
  // Newest first: the running execution, then the interrupted one.
  assert.match(rows[0].querySelector('.is-unavailable').textContent, /准备中/);
  assert.equal(new URL(rows[1].querySelector('[data-open-exec-chat]').href).searchParams.get('chatSession'), 'chat-cut');
});

test('an AI execution whose conversation was cleaned up says so instead of linking to it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get(DASHBOARD, fakeDashboard([
    { id: 'sjx-new', jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: OK_TRACE, status: 'success', assistantMessage: '新的', sessionId: 'chat-new', startedAt: '2026-11-16T10:00:00Z', durationMs: 3000 },
    { id: 'sjx-old', jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: FAILED_TRACE, status: 'success', assistantMessage: '旧的', sessionId: 'chat-old', sessionGone: true, startedAt: '2026-11-10T10:00:00Z', durationMs: 3000 },
  ]));
  app.evaluate(`state.projectFilter='project-1';state.data.scheduleJobs=[];renderScheduleJobs();`);
  await app.evaluate(`selectAIScheduleTab('executions')`);
  await settle();
  const rows = app.document.querySelectorAll('#scheduleExecList .sched-exec-row');
  assert.ok(rows[0].querySelector('[data-open-exec-chat]'), 'a kept conversation still opens');
  assert.equal(rows[1].querySelector('[data-open-exec-chat]'), null, 'no dead link to a cleaned-up conversation');
  const gone = rows[1].querySelector('.sched-exec-action.is-unavailable');
  assert.match(gone.textContent, /会话已清理/);
  assert.match(gone.title, /保留会话数/);
  assert.equal(rows[1].querySelector('.sched-exec-action-slots').children.length, 2, 'the action column stays aligned');
});

test('the overview comes from the server and the table filters and pages there', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const now = Date.parse('2026-11-16T12:00:00Z');
  const list = [];
  const add = (i, jobId, status, ago, durationMs) => list.push({ id: `sjx-${i}`, jobId, jobName: jobId === 'job-a' ? '巡检' : '周报', jobKind: 'assistant', projectId: 'project-1', traceId: `trace-${String(i).padStart(4, '0')}`, triggerOrigin: 'timer', status, error: status === 'failed' ? 'key missing' : '', assistantMessage: status === 'success' ? `完成 ${i}` : '', startedAt: new Date(now - ago).toISOString(), durationMs });
  // This week: 10 ok + 2 failed, 1 minute each; last week: 4 ok + 4 failed, 2 minutes each.
  for (let i = 0; i < 12; i++) add(i, 'job-a', i < 10 ? 'success' : 'failed', (2 + i * 9) * HOUR, 60000);
  for (let i = 12; i < 20; i++) add(i, 'job-a', i < 16 ? 'success' : 'failed', (8 * 24 + (i - 12) * 6) * HOUR, 120000);
  for (let i = 20; i < 25; i++) add(i, 'job-b', 'success', (20 * 24 + i) * HOUR, 30000);
  app.router.get(DASHBOARD, fakeDashboard(list, now));
  app.evaluate(`
    scheduleExecNow=()=>${now};
    state.projectFilter='project-1';
    state.data.scheduleJobs=[{id:'job-a',projectId:'project-1',name:'巡检',skipDispatch:true,assistantPrompt:'x',enabled:true,intervalSec:3600}];
    renderScheduleJobs();
  `);
  await app.evaluate(`selectAIScheduleTab('executions')`);
  await settle();
  const doc = app.document;
  const last = () => new URL(dashboardCalls(app).at(-1).url).searchParams;
  assert.equal(last().get('offset'), '0');
  assert.equal(last().get('limit'), '10', 'only one page is fetched');
  assert.equal(app.router.calls.some((c) => c.path === '/api/schedule-job-executions'), false, 'no bulk download of the history');

  const metric = (tone) => doc.querySelector(`#scheduleExecMetrics [data-tone="${tone}"]`);
  assert.match(metric('accent').textContent, /执行次数\s*12\s*较上周 \+50%/);
  assert.match(metric('success').textContent, /成功率\s*83%\s*成功 10 次\s*失败 2 次/);
  assert.equal(metric('error'), null, 'failure counts share the success-rate card');
  assert.match(metric('success').querySelector('.has-failures').textContent, /失败 2 次/);
  assert.match(metric('info').textContent, /平均耗时\s*1 分 0 秒\s*较上周 −50%/);
  assert.equal(doc.querySelector('#scheduleExecTrend'), null, 'the overview has no trend chart');
  assert.match(doc.querySelector('#scheduleExecFootnote').textContent, /不受保留条数限制/);
  assert.equal(doc.querySelector('#aiScheduleExecCount').textContent, '25');

  // 25 records, 10 per page.
  const rows = () => doc.querySelectorAll('#scheduleExecList .sched-exec-row');
  assert.equal(rows().length, 10);
  assert.match(doc.querySelector('#scheduleExecTotal').textContent, /共 25 条/);
  const pager = doc.querySelector('#scheduleExecPager');
  assert.match(pager.textContent, /共 3 页/);
  pager.querySelector('[data-exec-page="3"]').click();
  await settle();
  assert.equal(last().get('offset'), '20');
  assert.equal(rows().length, 5);
  assert.equal(doc.querySelector('#scheduleExecPager .is-current').textContent, '3');
  assert.equal(doc.querySelector('#scheduleExecPager [aria-label="下一页"]').disabled, true);
  await change(app, '#scheduleExecPageSize', '20');
  assert.equal(last().get('limit'), '20');
  assert.equal(last().get('offset'), '0');
  assert.equal(rows().length, 20);
  assert.match(doc.querySelector('#scheduleExecPager').textContent, /共 2 页/);

  // Search covers names, trace IDs and messages; it resets to the first page.
  const reset = doc.querySelector('#scheduleExecReset');
  assert.equal(reset.disabled, true);
  const before = dashboardCalls(app).length;
  await typeSearch(app, 'trace-0003');
  assert.equal(dashboardCalls(app).length, before + 1, 'typing asks the server once');
  assert.equal(last().get('q'), 'trace-0003');
  assert.equal(rows().length, 1);
  assert.equal(rows()[0].dataset.execId, 'sjx-3');
  assert.equal(reset.disabled, false);
  await typeSearch(app, '周报');
  assert.equal(rows().length, 5);
  await typeSearch(app, '');
  assert.equal(last().has('q'), false);

  await change(app, '#scheduleExecRange', '7d');
  assert.equal(last().get('since'), new Date(now - 7 * 24 * HOUR).toISOString());
  assert.equal(rows().length, 12);
  assert.match(metric('accent').textContent, /执行次数\s*12/, 'the overview ignores the time range');
  reset.click();
  await settle();
  assert.equal(doc.querySelector('#scheduleExecRange').value, '');
  assert.equal(last().has('since'), false);
  assert.equal(rows().length, 20);
  assert.equal(reset.disabled, true);

  // The row ⋯ menu narrows the list to its job.
  const first = rows()[0];
  first.querySelector('[data-schedule-menu]').click();
  const items = [...first.querySelectorAll('.schedule-row-menu [role="menuitem"]')].map(b => b.textContent.trim());
  assert.deepEqual(items, ['复制排障信息', '只看该任务', '编辑任务']);
  first.querySelector('[data-exec-only-job]').click();
  await settle();
  assert.equal(last().get('jobId'), 'job-a');
  assert.equal(doc.querySelector('#scheduleExecJob').value, 'job-a');
  assert.match(doc.querySelector('#scheduleExecTotal').textContent, /共 20 条/);
  assert.equal(rows()[0].querySelector('[data-exec-only-job]'), null, 'already filtered to this job');

  // Clicking the row body expands its detail; the AI reply shows in full.
  rows()[0].querySelector('.sched-exec-main').click();
  assert.equal(rows()[0].nextElementSibling.hidden, false);
  assert.match(rows()[0].nextElementSibling.textContent, /AI 回复完成 0/);
});

test('a page past the end of a shrunken list falls back to the last page', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const list = Array.from({ length: 25 }, (_, i) => ({ id: `sjx-${i}`, jobId: 'job-a', jobName: '巡检', jobKind: 'assistant', projectId: 'project-1', traceId: `t-${i}`, status: 'success', startedAt: new Date(Date.parse('2026-11-16T10:00:00Z') - i * HOUR).toISOString(), durationMs: 1000 }));
  app.router.get(DASHBOARD, fakeDashboard(list));
  app.evaluate(`state.projectFilter='project-1';state.data.scheduleJobs=[];renderScheduleJobs();`);
  await app.evaluate(`selectAIScheduleTab('executions')`);
  await settle();
  app.document.querySelector('#scheduleExecPager [data-exec-page="3"]').click();
  await settle();
  list.splice(12);
  await app.evaluate('refreshScheduleExecutions()');
  await settle();
  const rows = app.document.querySelectorAll('#scheduleExecList .sched-exec-row');
  assert.equal(rows.length, 2);
  assert.equal(app.document.querySelector('#scheduleExecPager .is-current').textContent, '2');
});

test('execution dashboard reports empty, error and duration states', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let fail = true;
  app.router.get(DASHBOARD, (call) => (fail ? { status: 500, body: { error: 'db down' } } : fakeDashboard([])(call)));
  app.evaluate(`state.projectFilter='project-1';state.data.scheduleJobs=[];renderScheduleJobs();`);
  await app.evaluate(`selectAIScheduleTab('executions')`);
  await settle();
  const list = app.document.querySelector('#scheduleExecList');
  assert.match(list.textContent, /读取执行记录失败：db down/);
  fail = false;
  list.querySelector('[data-retry-exec]').click();
  await settle();
  assert.match(list.textContent, /还没有执行记录/);
  assert.equal(app.document.querySelector('#aiScheduleExecCount').hidden, true);
  assert.equal(app.evaluate(`scheduleExecDurationLabel(400)`), '<1 秒');
  assert.equal(app.evaluate(`scheduleExecDurationLabel(42000)`), '42 秒');
  assert.equal(app.evaluate(`scheduleExecDurationLabel(7380000)`), '2 小时 3 分');
  assert.equal(app.evaluate(`scheduleExecTriggerLabel('manual_run')`), '手动运行');
});

test('token metrics use server windows and execution rows expose per-run usage', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const overview = { current: { total: 200, finished: 200, success: 190, failed: 10, metered: 100, tokens: { total: 150000 }, avgTokens: 1500 } };
  const html = app.evaluate(`scheduleExecMetricsHTML(${JSON.stringify(overview)})`);
  assert.match(html, /Token 总消耗.*150,000.*100 次已上报/s);
  assert.equal((html.match(/class="sched-exec-metric"/g) || []).length, 4);
  assert.match(html, /执行次数.*成功率.*95%.*成功 190 次.*失败 10 次.*平均耗时.*Token 总消耗/s);
  assert.doesNotMatch(html, /平均 Token|失败次数/);
  const noFailures = app.evaluate(`scheduleExecMetricsHTML({current:{total:2,finished:2,success:2,failed:0}})`);
  assert.match(noFailures, /成功率.*100%.*成功 2 次.*失败 0 次/s);
  assert.doesNotMatch(noFailures, /has-failures/);
  const e = { id: 'usage', jobKind: 'assistant', status: 'success', model: '<model>', usage: { inputTokens: 1000, outputTokens: 200, totalTokens: 1500, cacheReadTokens: 800, cacheWriteTokens: 50 } };
  assert.match(app.evaluate(`scheduleExecTokenCellHTML(${JSON.stringify(e)})`), />1,500</);
  const detail = app.evaluate(`scheduleExecDetailHTML(${JSON.stringify(e)}, 'usage-detail')`);
  assert.match(detail, /colspan="8"/);
  assert.match(detail, /Token 总量.*1,500.*输入 Token.*1,000.*输出 Token.*200.*缓存读 Token.*800.*缓存写 Token.*50/s);
  assert.match(detail, /&lt;model&gt;/);
  assert.equal(app.evaluate(`scheduleExecUsageTotal({usage:{inputTokens:10,outputTokens:20}})`), 30);
  assert.match(app.evaluate(`scheduleExecTokenCellHTML({jobKind:'assistant',status:'success'})`), /未上报/);
  assert.match(app.evaluate(`scheduleExecTokenCellHTML({jobKind:'assistant',status:'running'})`), /待统计/);
  assert.match(app.evaluate(`scheduleExecMetricsHTML({current:{total:10}})`), /Token 总消耗.*—/s);
});
