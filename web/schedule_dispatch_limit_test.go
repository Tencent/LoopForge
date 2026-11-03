package web

import (
	"os/exec"
	"testing"
)

func TestScheduleDispatchLimitForm(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const code = fs.readFileSync('app/sync-schedule.js', 'utf8');
const html = fs.readFileSync('index.html', 'utf8');
const input = html.match(/<input[^>]*name="maxDispatch"[^>]*>/)[0];
assert.ok(input.includes('type="number"') && input.includes('min="1"') && input.includes('max="10"'));
assert.ok(!input.includes('required'), 'dispatch limit must be optional');
const form = {elements: Object.fromEntries(['id','name','scheduleMode','jobKind','workflowId','useIssueWorkflow','source','maxDispatch','intervalSec','cron','assistantPrompt','assistantModel','enabled'].map(name => [name,{value:''}]))};
Object.assign(form.elements.name,{value:'trigger job'});
form.elements.jobKind.value='dispatch';
form.elements.workflowId.value='wf';
form.elements.intervalSec.value='3600';
form.querySelectorAll=()=>[{value:'todo'}];
let saved, error;
global.state={projectFilter:'platform',data:{scheduleJobs:[]}};
global.toast=message=>{error=message};
global.fillScheduleJobForm=()=>{};
global.renderScheduleJobs=()=>{};
global.api=async (path,options)=>{saved=JSON.parse(options.body); return {...saved,id:'job'}};
global.notificationPayload=()=>[];
global.scheduleNotificationBindings=[];
eval(code.slice(code.indexOf('async function saveScheduleJob('),code.indexOf('function scheduleJobOutcomeToast(')));
async function submit(value) {
  saved=undefined; error=undefined; form.elements.maxDispatch.value=value;
  await saveScheduleJob({preventDefault(){},currentTarget:form});
  return saved;
}
(async()=>{
  assert.ok(!Object.hasOwn(await submit(''),'maxDispatch'), 'blank must omit maxDispatch');
  assert.equal((await submit('1')).maxDispatch,1);
  assert.equal((await submit('3')).maxDispatch,3);
  assert.equal((await submit('10')).maxDispatch,10);
  for(const value of ['0','-1','11','1.5','invalid']) {
    assert.equal(await submit(value),undefined, 'invalid limit must not be submitted');
    assert.ok(error.includes('1–10'));
  }
  assert.ok(!Object.hasOwn(await submit(''),'maxDispatch'), 'clearing a limit must restore the default');
  form.elements.jobKind.value='assistant';
  form.elements.assistantPrompt.value='report';
  assert.ok(!Object.hasOwn(await submit('3'),'maxDispatch'), 'AI jobs must not include the dispatch limit');
})().catch(error=>{console.error(error);process.exit(1)});
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("dispatch limit form: %v\n%s", err, out)
	}
}
