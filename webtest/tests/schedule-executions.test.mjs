import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const FAILED_TRACE = '0123456789abcdef0123456789abcdef';
const OK_TRACE = 'fedcba9876543210fedcba9876543210';

function executions() {
  return [
    { id: `sjx-${FAILED_TRACE}`, jobId: 'job-ai', jobName: 'Daily summary', jobKind: 'assistant', projectId: 'project-1', traceId: FAILED_TRACE, triggerOrigin: 'timer', status: 'failed', error: 'CodeBuddy key expired', startedAt: '2026-11-16T08:00:00Z', finishedAt: '2026-11-16T08:00:05Z', durationMs: 5200 },
    { id: `sjx-${OK_TRACE}`, jobId: 'job-trigger', jobName: '触发配置', jobKind: 'trigger', projectId: 'project-1', traceId: OK_TRACE, triggerOrigin: 'manual_run', status: 'success', pulled: 3, dispatched: 1, runs: [{ issueId: 'issue-1', issueTitle: '登录页改版', runId: 'run-77' }], startedAt: '2026-11-16T07:00:00Z', finishedAt: '2026-11-16T07:01:30Z', durationMs: 90000 },
  ];
}

test('execution dashboard lists history with metrics, filters and copyable traces', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get(/^\/api\/schedule-job-executions\?/, () => executions());
  app.router.get('/api/schedule-jobs', () => app.evaluate('state.data.scheduleJobs'));
  app.evaluate(`
    state.projectFilter='project-1';
    state.data.schedulerEnabled=true;
    state.data.scheduleJobs=[
      {id:'job-ai',projectId:'project-1',name:'Daily summary',skipDispatch:true,assistantPrompt:'x',enabled:true,intervalSec:3600,lastStatus:'failed',lastError:'CodeBuddy key expired',lastTraceId:'${FAILED_TRACE}',lastRunAt:'2026-11-16T08:00:00Z'},
      {id:'job-trigger',projectId:'project-1',name:'触发配置',workflowId:'wf',enabled:true,intervalSec:300,lastStatus:'success',lastTraceId:'${OK_TRACE}',lastRunAt:'2026-11-16T07:00:00Z'}
    ];
    window.__copied=[];window.__opened=[];
    copyText=(value)=>{window.__copied.push(value)};
    openRunFromId=(id)=>{window.__opened.push(id)};
    renderScheduleJobs();
  `);

  const jobList = app.document.querySelector('#aiScheduleJobList');
  const rowCopy = jobList.querySelector('[data-copy-trace]');
  assert.ok(rowCopy, 'failed job row offers a trace copy button');
  rowCopy.click();
  // Before the history loads, the notes come from the job's latest outcome.
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
  const request = app.router.calls.filter((c) => c.path === '/api/schedule-job-executions').at(-1);
  assert.match(request.search, /projectId=project-1/);

  // Opened from the job row: filtered to that job.
  let rows = panel.querySelectorAll('.sched-exec-row');
  assert.equal(rows.length, 1);
  assert.match(rows[0].textContent, /失败.*Daily summary.*定时触发.*CodeBuddy key expired.*5 秒.*Trace/s);
  assert.equal(app.document.querySelector('#aiScheduleExecCount').textContent, '1');

  const jobFilter = app.document.querySelector('#scheduleExecJob');
  jobFilter.value = '';
  jobFilter.dispatchEvent(new app.window.Event('change'));
  rows = panel.querySelectorAll('.sched-exec-row');
  assert.equal(rows.length, 2);
  const metrics = app.document.querySelector('#scheduleExecMetrics').textContent;
  assert.match(metrics, /执行次数\s*2/);
  assert.match(metrics, /成功率\s*50%/);
  assert.match(rows[1].textContent, /候选 3 · 启动 1/);
  assert.match(rows[1].textContent, /1 分 30 秒/);

  rows[1].querySelector('[data-open-exec-run]').click();
  assert.equal(app.evaluate('JSON.stringify(window.__opened)'), JSON.stringify(['run-77']));
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

  const statusFilter = app.document.querySelector('#scheduleExecStatus');
  statusFilter.value = 'failed';
  statusFilter.dispatchEvent(new app.window.Event('change'));
  rows = panel.querySelectorAll('.sched-exec-row');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].dataset.status, 'failed');
});

test('execution dashboard reports empty, error and duration states', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let fail = true;
  app.router.get(/^\/api\/schedule-job-executions\?/, () => (fail ? { status: 500, body: { error: 'db down' } } : []));
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
