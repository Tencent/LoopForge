const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const bundle = fs.readFileSync(process.argv[2], 'utf8');
function source(name) {
  const match = new RegExp('^(?:async )?function ' + name + '\\(', 'm').exec(bundle);
  assert(match, name);
  const tail = bundle.slice(match.index);
  const next = /\n(?:async )?function |\n(?:let|const) [A-Za-z]/.exec(tail);
  return next ? tail.slice(0, next.index) : tail;
}
function load(context, names) {
  vm.createContext(context);
  for (const name of names) vm.runInContext(source(name), context);
  return context;
}
async function flush() { for (let i=0; i<12; i++) await Promise.resolve(); }
async function main() {
  const statuses = load({}, ['runStatus','runDisplayStatus','runDisplayStatusClass','runControlSignature','runControlToast']);
  assert.equal(statuses.runStatus('queued'), '排队中');
  assert.equal(statuses.runDisplayStatusClass({status:'queued'}), 'queued');
  assert.match(statuses.runControlToast({status:'queued'},'continued'), /排队/);
  assert.notEqual(statuses.runControlSignature({status:'queued'}), statuses.runControlSignature({status:'running'}));
  const host={dataset:{runId:'r',controlSignature:statuses.runControlSignature({status:'queued'})},querySelectorAll:()=>[],querySelector:()=>null};
  const paint=load({$:()=>host,runControlSignature:statuses.runControlSignature,runPreviewDeclared:()=>false},['shouldPatchRunLive']);
  assert.equal(paint.shouldPatchRunLive({id:'r',status:'queued'}),true);
  for (const status of ['running','failed','cancelled','waiting']) assert.equal(paint.shouldPatchRunLive({id:'r',status}),false,status);
  let redraw=0;
  load(paint,['patchRunLive']);paint.renderRunDetail=()=>redraw++;
  paint.patchRunLive({id:'r',status:'running'});assert.equal(redraw,1);

  const timers=new Map(),listeners={};let nextTimer=0,refreshes=0,view='runs';
  const section={hidden:false};
  const refresh={state:{data:{runs:[{id:'r',status:'queued'}]}},document:{hidden:false,addEventListener:(name,fn)=>listeners[name]=fn},activeViewName:()=>view,$:()=>section,scopedRuns:r=>r,clearInterval:id=>timers.delete(id),setInterval:fn=>{timers.set(++nextTimer,fn);return nextTimer;},refreshRuns:()=>refreshes++};
  vm.createContext(refresh);
  vm.runInContext(bundle.slice(bundle.indexOf('let queuedRunRefreshTimer='),bundle.indexOf('let refreshRunsPending=')),refresh);
  refresh.syncQueuedRunRefresh();refresh.syncQueuedRunRefresh();assert.equal(timers.size,1);
  [...timers.values()][0]();assert.equal(refreshes,1);
  refresh.document.hidden=true;listeners.visibilitychange();assert.equal(timers.size,0);
  refresh.document.hidden=false;listeners.visibilitychange();assert.equal(timers.size,1);assert.equal(refreshes,2);
  section.hidden=true;refresh.syncQueuedRunRefresh();assert.equal(timers.size,0);
  section.hidden=false;view='issues';refresh.syncQueuedRunRefresh();assert.equal(timers.size,0);
  view='runs';refresh.state.data.runs=[{status:'running'}];refresh.syncQueuedRunRefresh();assert.equal(timers.size,0);

  const calls=[],messages=[],run={id:'run/1',status:'waiting',pauseRequested:true};
  const control={state:{selectedRun:run.id,activeRun:run,data:{runs:[run],issues:[]},issueDetailData:{run}},api:async(path,opts)=>{calls.push([path,opts?.method||'GET']);return{id:run.id,status:'queued',pauseRequested:false,admissionState:'queued'};},toast:m=>messages.push(m),readHumanContext:()=>'',refreshIssues:async()=>{},refreshRuns:async()=>{},startLiveRun:id=>calls.push(['stream',id]),stopLiveRun:()=>{},renderRunDetail:()=>{},renderRuns:()=>{},renderIssueDetailTab:()=>{}};
  load(control,['cachedRun','queuedRunActionBlocked','patchCachedRun','paintRunControl','runStatus','runDisplayStatus','runControlToast','sortRunsDescending','syncRunControlState','resumePausedRun']);
  await control.resumePausedRun(run.id);
  assert.deepEqual(calls.slice(0,2),[['/api/runs/run%2F1/resume','POST'],['/api/runs/run%2F1','GET']]);
  assert.equal(control.state.activeRun.status,'queued');assert.equal(control.state.activeRun.pauseRequested,false);
  assert.equal(control.state.issueDetailData.run.status,'queued');assert.equal(control.state.data.runs[0].admissionState,'queued');assert.match(messages[0],/排队/);
  const count=calls.length;
  control.afConfirm=()=>{throw new Error('queued must not confirm')};
  load(control,['pauseRun','retryWaitingNode','approveRun','rejectRun','passFailedNode','deleteRun']);
  for (const name of ['pauseRun','resumePausedRun','retryWaitingNode','approveRun','rejectRun','passFailedNode','deleteRun']) await control[name](run.id,'n');
  assert.equal(calls.length,count);
  load(control,['waitingNodeId','resumeRun','runAction']);
  control.issueIdForRun=()=>'';control.issueExecutionBlocked=()=>false;
  control.runRetryNeedsContext=()=>false;control.withHumanContext=p=>p;
  assert.equal(control.waitingNodeId({status:'queued',nodes:{old:{status:'waiting_approval'}}}),undefined);
  for (const prior of ['failed','succeeded']) {
    control.patchCachedRun(run.id,{status:prior});
    const start=calls.length;
    await control.resumeRun(run.id);
    assert.deepEqual(calls.slice(start,start+2),[['/api/runs/run%2F1/retry','POST'],['/api/runs/run%2F1','GET']]);
    assert.equal(control.state.activeRun.status,'queued');assert.match(messages.at(-1),/排队/);
  }
  control.api=async(path,opts)=>{calls.push([path,opts?.method||'GET']);return{id:run.id,status:'cancelled',admissionState:'unstarted'}};
  await control.runAction(run.id,'cancel',{});
  assert.equal(control.state.activeRun.status,'cancelled');assert.equal(control.state.activeRun.admissionState,'unstarted');assert.equal(messages.at(-1),'已取消排队');
  control.patchCachedRun(run.id,{status:'queued'});
  control.api=async()=>({id:run.id,status:'cancelled',admissionState:'held'});
  await control.runAction(run.id,'cancel',{});assert.equal(messages.at(-1),'运行已取消');
  let resolveControl;
  const raceCalls=[],race={state:{selectedRun:'a',activeRun:{id:'a'},data:{runs:[{id:'a'},{id:'b'}]}},api:()=>new Promise(resolve=>resolveControl=resolve),startLiveRun:id=>raceCalls.push(['start',id]),stopLiveRun:()=>raceCalls.push(['stop']),renderRunDetail:()=>{},renderRuns:()=>{},renderIssueDetailTab:()=>{}};
  load(race,['syncRunControlState','patchCachedRun','paintRunControl','sortRunsDescending']);
  const pending=race.syncRunControlState('a');
  race.state.selectedRun='b';race.state.activeRun={id:'b',status:'running'};
  const streamB={id:'b'};race.state.runStream=streamB;
  resolveControl({id:'a',status:'queued'});await pending;
  assert.equal(race.state.runStream,streamB);assert.deepEqual(raceCalls,[]);assert.equal(race.state.activeRun.id,'b');assert.equal(race.state.data.runs[0].status,'queued');

  for (const name of ['showRunDetail','selectRun']) {
    const streams=[],context={state:{data:{runs:[]}},$:()=>({hidden:false,classList:{add:()=>{}},dataset:{}}),document:{querySelector:()=>({setAttribute:()=>{}})},stopQueuedRunRefresh:()=>{},stopLiveRun:()=>{},stopPreviewPoll:()=>{},syncRunHash:()=>{},api:async()=>({id:'r',status:'queued'}),shouldPatchRunLive:()=>false,renderRunDetail:()=>{},renderRuns:()=>{},startLiveRun:id=>streams.push(id),toast:()=>{}};
    load(context,[name]);context[name]('r');await flush();assert.deepEqual(streams,['r'],name);
  }
  const liveState={selectedRun:'r',data:{runs:[]}},intervals=new Map();let timer=0,status='queued',applied=[];
  const polling={state:liveState,api:async()=>({id:'r',status}),setInterval:fn=>{intervals.set(++timer,fn);return timer},clearInterval:id=>intervals.delete(id),patchCachedRun:()=>{},applyRunPayload:r=>applied.push(r.status),refreshRunPagesDelta:async()=>{},renderRuns:()=>{},renderIssueDetailTab:()=>{}};
  load(polling,['startPollingFallback']);polling.startPollingFallback('r');await flush();assert.equal(intervals.size,1);assert.deepEqual(applied,['queued']);
  status='running';[...intervals.values()][0]();await flush();assert.equal(intervals.size,1);
  status='succeeded';[...intervals.values()][0]();await flush();assert.equal(intervals.size,0);assert.equal(liveState.poll,null);assert.deepEqual(applied,['queued','running','succeeded']);

  const machine=load({state:{runners:[{id:'m',name:'local',ownerUserId:9}]},escapeHTML:String},['runnerConcurrencyLabel','runExecutionChipHTML']);
  assert.match(machine.runnerConcurrencyLabel({ownerUserId:9}),/个人机器/);
  assert.doesNotMatch(machine.runnerConcurrencyLabel({ownerUserId:0,labels:{personal:'true'}}),/不受/);
  assert.match(machine.runExecutionChipHTML({executionContext:{executionMode:'runner',executionRunnerId:'m'}}),/不受项目 10/);
  assert.match(machine.runExecutionChipHTML({concurrencyExempt:true,executionContext:{executionMode:'runner',executionRunnerId:'unlisted'}}),/个人机器/);
  assert.match(machine.runnerConcurrencyLabel({ownerUserId:9}),/不限同时运行数/);
  assert.match(machine.runnerConcurrencyLabel({ownerUserId:9,runLimit:3}),/最多同时运行 3 条/);
  machine.state.runners[0].runLimit=4;
  assert.match(machine.runExecutionChipHTML({executionContext:{executionMode:'runner',executionRunnerId:'m'}}),/本机最多 4 条/);

  const limitUI=load({escapeHTML:s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))},['runnerRunLimitHTML']);
  assert.equal(limitUI.runnerRunLimitHTML({id:'cloud',ownerUserId:0}),'','platform machines have no per-machine limit');
  const unlimited=limitUI.runnerRunLimitHTML({id:'m',name:'mac',ownerUserId:9});
  assert.match(unlimited,/placeholder="不限制" value=""/);
  assert.match(limitUI.runnerRunLimitHTML({id:'m',ownerUserId:9,runLimit:5}),/value="5"/);
  assert.doesNotMatch(limitUI.runnerRunLimitHTML({id:'"><img src=x onerror=alert(1)>',ownerUserId:9}),/<img/,'runner id is escaped');

  const saves=[],notes=[];let refreshed=0,rendered=0;
  const saver={state:{runners:[{id:'m',ownerUserId:9}]},api:async(path,opts)=>{saves.push([path,opts.method,JSON.parse(opts.body)]);return{id:'m',ownerUserId:9,runLimit:JSON.parse(opts.body).runLimit}},toast:m=>notes.push(m),refreshRunners:()=>refreshed++,renderRunnersView:()=>rendered++};
  load(saver,['saveRunnerRunLimit']);
  await saver.saveRunnerRunLimit('m','3');
  await saver.saveRunnerRunLimit('m','  ');
  assert.deepEqual(saves,[['/api/runners/m/run-limit','PUT',{runLimit:3}],['/api/runners/m/run-limit','PUT',{runLimit:0}]]);
  assert.equal(saver.state.runners[0].runLimit,0);assert.match(notes[0],/3 条/);assert.match(notes[1],/不限制/);
  for (const bad of ['0.5','-2','101','abc']) await saver.saveRunnerRunLimit('m',bad);
  assert.equal(saves.length,2,'invalid values are not sent');assert.equal(refreshed,4);

  const nodes=load({issueExecution:()=>({run:{status:'queued',nodes:{old:{status:'failed'}}}})},['currentRunNode']);
  assert.equal(nodes.currentRunNode({}),null);
}
main().catch(err=>{console.error(err);process.exit(1)});
