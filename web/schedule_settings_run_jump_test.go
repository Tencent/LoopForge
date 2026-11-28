package web

import (
	"os/exec"
	"testing"
)

func TestScheduleSettingsImmediateRunAndLatestJump(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const code = fs.readFileSync('app/sync-schedule.js', 'utf8');
const html = fs.readFileSync('index.html', 'utf8');

assert.match(html, /id="scheduleLatestExecBar"/);
assert.match(html, /id="scheduleSaveAndRun"/);
assert.match(html, /name="openAfterRun"/);

global.knownTime = v => (v && !String(v).startsWith('0001-') ? new Date(v) : null);
eval(code.slice(code.indexOf('function scheduleJobRunning('), code.indexOf('function openScheduleLatestExecution(')));

assert.equal(scheduleLatestExecutionAction({ id: 'j0', skipDispatch: true }), null);
assert.deepEqual(
  scheduleLatestExecutionAction({ id: 'j1', skipDispatch: true, projectId: 'p1', lastStatus: 'running', lastSessionId: 'chat-live' }),
  { kind: 'session', sessionId: 'chat-live', projectId: 'p1', label: '查看实时执行 ↗', tone: 'info' },
);
assert.deepEqual(
  scheduleLatestExecutionAction({ id: 'j2', skipDispatch: true, projectId: 'p1', lastStatus: 'success', lastSessionId: 'chat-ok', lastRunAt: '2026-11-26T01:00:00Z' }),
  { kind: 'session', sessionId: 'chat-ok', projectId: 'p1', label: '查看最近执行 ↗', tone: 'success' },
);
assert.deepEqual(
  scheduleLatestExecutionAction({ id: 'j3', skipDispatch: true, projectId: 'p1', lastStatus: 'failed', lastSessionId: 'chat-err', lastRunAt: '2026-11-26T01:00:00Z' }),
  { kind: 'session', sessionId: 'chat-err', projectId: 'p1', label: '查看失败会话 ↗', tone: 'error' },
);
assert.deepEqual(
  scheduleLatestExecutionAction({ id: 'j4', skipDispatch: true, projectId: 'p1', lastStatus: 'success', lastSessionId: 'chat-gone', lastSessionGone: true, lastRunAt: '2026-11-26T01:00:00Z' }),
  { kind: 'executions', jobId: 'j4', label: '执行记录（会话已清理）', tone: 'neutral' },
);

const form = {
  elements: Object.fromEntries(
    ['id', 'name', 'scheduleMode', 'jobKind', 'workflowId', 'useIssueWorkflow', 'source', 'maxDispatch', 'intervalSec', 'cron', 'assistantPrompt', 'assistantModel', 'enabled', 'openAfterRun']
      .map(name => [name, { value: '', checked: false }]),
  ),
  querySelectorAll: () => [],
};
form.elements.timeoutMinutes = { value: '', validity: { valid: true } };
form.elements.name.value = '每日巡检';
form.elements.jobKind.value = 'assistant';
form.elements.intervalSec.value = '3600';
form.elements.assistantPrompt.value = '检查 PR';
form.elements.openAfterRun.checked = true;

let ran = null;
let closed = false;
global.state = { projectFilter: 'p1', data: { scheduleJobs: [], schedulerEnabled: true } };
global.toast = () => {};
global.fillScheduleJobForm = () => {};
global.renderScheduleJobs = () => {};
global.closeScheduleJobModal = () => { closed = true; };
global.notificationPayload = () => [];
global.scheduleNotificationBindings = [];
global.scheduleAssistantReferences = [];
global.scheduleModalVersion = 0;
global.api = async (path, options) => ({ ...JSON.parse(options.body), id: 'job-created' });
global.runScheduleJob = async (id, opts) => { ran = { id, opts }; };

eval(code.slice(code.indexOf('function scheduleReferenceIdentity('), code.indexOf('function syncSchedulePromptEditor(')));
eval(code.slice(code.indexOf('async function saveScheduleJob('), code.indexOf('function scheduleJobOutcomeToast(')));

(async () => {
  await saveScheduleJob({ preventDefault() {}, currentTarget: form }, { runNow: true });
  assert.deepEqual(ran, { id: 'job-created', opts: { openAfterRun: true, stayInModal: false } });
  assert.equal(closed, true);
})().catch(err => { console.error(err); process.exit(1); });
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("schedule settings test failed: %v\n%s", err, out)
	}
}
