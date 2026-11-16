package web

import (
	"os/exec"
	"testing"
)

func TestRunActivationWaitsForRestoreAndDeduplicatesClicks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const timers=[],calls=[];
let notice=null;
const button={dataset:{},disabled:false};
const header={after:element=>{notice=element}};
const host={dataset:{runId:'run'},querySelector:selector=>selector==='[data-run-activation-status]'?notice:header,querySelectorAll:()=>[button]};
const $=()=>host;
const state={};
const document={createElement:()=>({dataset:{},attributes:{},setAttribute(name,value){this.attributes[name]=value},remove(){notice=null}})};
const viewed={nodes:{work:{status:'succeeded',attempt:11,reworkCount:0}}};
const cachedRun=()=>viewed;
const setTimeout=fn=>timers.push(fn);
let poll=0,fail=false;
const api=async(path,opts={})=>{
 calls.push({path,...opts});
 if(opts.method==='POST'){
  assert.equal(opts.headers.Prefer,'respond-async');
  const posts=calls.filter(c=>c.method==='POST');
  if(posts.length===1||fail)return {workspaceRestore:{status:'restoring'}};
  if(path.endsWith('/retry'))assert.equal(JSON.parse(opts.body).expected.attempt,11,'delayed retry must retain the original attempt');
  return {ok:true};
 }
 if(fail)return {status:'failed',error:'runner disconnected'};
 return ++poll===1?{status:'restoring'}:{status:'ready'};
};
eval(fs.readFileSync('app/run-activation.js','utf8')+'\nglobalThis.activate=requestRunActivation;');
const settle=async()=>{for(let i=0;i<8;i++)await Promise.resolve()};
(async()=>{
 const first=activate('run','retry',{nodeId:'work',context:'human instructions'});
 const duplicate=activate('run','retry',{nodeId:'work',context:'human instructions'});
 assert.equal(first,duplicate,'repeated clicks must share the pending operation');
 await assert.rejects(activate('run','retry',{nodeId:'other'}),/已有执行请求/,'another decision must not silently replace the pending one');
 await settle();assert.equal(calls.length,1,'202 must not be reported as a completed retry');
 assert.equal(button.disabled,true);assert.match(notice.textContent,/正在恢复工作区/);assert.equal(notice.attributes.role,'status');
 assert.equal(timers.length,1);
 viewed.nodes.work.attempt=12;
 timers.shift()();await settle();assert.equal(calls.filter(c=>c.method==='POST').length,1);
 timers.shift()();await settle();assert.deepEqual(await first,{ok:true});
 const posts=calls.filter(c=>c.method==='POST');
 assert.equal(posts.length,2,'one restoration request followed by one actual retry');
 assert.equal(posts[0].body,posts[1].body,'restore must preserve the selected node and human context');
 assert.equal(button.disabled,false);assert.equal(notice,null,'completed control must clear the restore indicator');
 fail=true;
 const failed=activate('run','resume',{});
 const rejection=assert.rejects(failed,/runner disconnected/);
 await settle();timers.shift()();await rejection;
 fail=false;
 assert.deepEqual(await activate('run','resume',{}),{ok:true},'failure must release the UI operation for explicit retry');
})().catch(err=>{console.error(err);process.exitCode=1});
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("run activation UI: %v\n%s", err, output)
	}
}

func TestRunWorkspaceTabRestoresReclaimedWorkspace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const runPath='/api/runs/run',workspacePath=runPath+'/workspace',restorePath=workspacePath+'/restore';
const reclaimed={id:'run',status:'succeeded',workspaceCleanedAt:'2026-11-16T12:00:00Z'};
const restored={...reclaimed,workspaceCleanedAt:'0001-01-01T00:00:00Z'};
const state={activeRun:reclaimed,selectedRun:'run',runDetailTab:'overview',data:{runs:[reclaimed]}};
const calls=[],timers=[],restoreButton={};
const controlButton={dataset:{},disabled:false};
let notice=null;
const header={after:element=>{notice=element}};
const makeTab=runTab=>({dataset:{runTab},classList:{toggle(_,active){this.active=active}}});
const overviewTab=makeTab('overview'),workspaceTab=makeTab('workspace');
const overviewPane={dataset:{runPane:'overview'},hidden:false};
const pane={
 innerHTML:'',
 dataset:{runPane:'workspace'},
 hidden:true,
 classList:{toggle(){}},
 querySelector:()=>null,
 querySelectorAll:()=>[]
};
const host={
 dataset:{runId:'run'},
 querySelector(selector){
  return {
   '[data-workspace-restore]':restoreButton,'[data-run-pane="workspace"]':pane,
   '[data-run-activation-status]':notice,'.run-detail-header':header
  }[selector]||null;
 },
 querySelectorAll(selector){
  if(selector==='[data-run-tab]')return [overviewTab,workspaceTab];
  if(selector==='[data-run-pane]')return [overviewPane,pane];
  return [controlButton];
 }
};
const $=()=>host;
const document={
 addEventListener(){},
 createElement:()=>({dataset:{},setAttribute(){},remove(){notice=null}})
};
const window={addEventListener(){}};
const lfT=zh=>zh,escapeHTML=value=>String(value).replaceAll('<','&lt;');
const setTimeout=callback=>timers.push(callback);
const patchCachedRun=(_,run)=>{state.activeRun=run};
const paintRunControl=()=>{};
let info={available:false,restorable:false,reason:'工作区已回收'};
let restoreStatus={status:'failed',error:'Runner <离线>'};
const api=async(path,options={})=>{
 calls.push([path,options.method||'GET']);
 if(path===runPath)return restored;
 if(path===workspacePath)return info;
 assert.equal(path,restorePath);
 if(options.method==='POST'){
  assert.equal(options.headers.Prefer,'respond-async');
  return {workspaceRestore:{status:'restoring'}};
 }
 return restoreStatus;
};
eval(fs.readFileSync('app/run-activation.js','utf8')+'\n'+fs.readFileSync('app/runs.js','utf8')+
 '\nglobalThis.loadWorkspace=loadRunWorkspace;'+
 '\nglobalThis.restoreButtonHTML=workspaceRestoreButtonHTML;'+
 '\nbindRunDetailTabs(reclaimed,host);');
(async()=>{
 assert.match(restoreButtonHTML(),/title="工作区是本次运行存放代码和生成文件的目录。恢复会从保存的快照还原文件/);
 await loadWorkspace('run');
 assert.deepEqual(calls,[[workspacePath,'GET']],'reclaimed runs must still check workspace availability');
 assert.equal(restoreButton.hidden,true);
 await workspaceTab.onclick();
 assert.ok(calls.every(([,method])=>method==='GET'),'a run without a snapshot cannot be restored');
 info={...info,checkpointSavedAt:reclaimed.workspaceCleanedAt};
 await loadWorkspace('run',{refresh:true});
 await workspaceTab.onclick();
 assert.equal(restoreButton.hidden,true,'viewers cannot restore a saved snapshot');
 assert.ok(calls.every(([,method])=>method==='GET'),'viewers must not send a restore POST');
 overviewTab.onclick();

 let finishProbe;
 info=new Promise(resolve=>{finishProbe=resolve});
 const failed=loadWorkspace('run',{refresh:true});
 assert.equal(state.runDetailTab,'overview');
 assert.equal(restoreButton.hidden,true);
 await workspaceTab.onclick();
 assert.equal(state.runDetailTab,'workspace');
 info={available:false,restorable:true,reason:'工作区已回收'};
 finishProbe(info);
 await new Promise(setImmediate);
 await workspaceTab.onclick();
 assert.match(pane.innerHTML,/正在恢复工作区/);
 assert.doesNotMatch(pane.innerHTML,/data-vscode-frame/);
 assert.equal(restoreButton.disabled,true);
 assert.equal(calls.filter(([,method])=>method==='POST').length,1);
 timers.shift()();
 await failed;
 assert.match(pane.innerHTML,/role="alert">Runner &lt;离线>/);
 assert.match(pane.innerHTML,/data-workspace-refresh/);
 assert.equal(restoreButton.disabled,false);

 overviewTab.onclick();
 assert.equal(restoreButton.hidden,false);
 assert.equal(pane.hidden,true);
 const pending=restoreButton.onclick();
 assert.equal(state.runDetailTab,'workspace');
 assert.equal(workspaceTab.classList.active,true);
 assert.equal(overviewPane.hidden,true);
 assert.equal(pane.hidden,false);
 await Promise.resolve();
 info={available:true};
 restoreStatus={status:'ready'};
 timers.shift()();
 await pending;
 assert.equal(state.activeRun,restored);
 assert.match(pane.innerHTML,/data-vscode-frame/);
 assert.equal(restoreButton.hidden,true);
 assert.deepEqual(calls.slice(-4),[[restorePath,'POST'],[restorePath,'GET'],[runPath,'GET'],[workspacePath,'GET']]);

 overviewTab.onclick();
 state.activeRun=reclaimed;
 state.runWorkspace=null;
 calls.length=0;
 info={available:false,restorable:true,restore:{status:'restoring'}};
 restoreStatus={status:'restoring'};
 const reattached=loadWorkspace('run');
 await new Promise(setImmediate);
 assert.match(notice.textContent,/正在后台恢复工作区/);
 assert.equal(controlButton.disabled,true);
 await workspaceTab.onclick();
 assert.equal(notice,null,'the workspace already shows the restore progress');
 assert.match(pane.innerHTML,/正在恢复工作区/);
 assert.equal(timers.length,1);
 timers.shift()();
 await new Promise(setImmediate);
 assert.deepEqual(calls,[[workspacePath,'GET'],[restorePath,'GET']]);
 assert.equal(timers.length,1);
 info={available:true};
 restoreStatus={status:'ready'};
 timers.shift()();
 await reattached;
 assert.match(pane.innerHTML,/data-vscode-frame/);
 assert.equal(controlButton.disabled,false);
 assert.ok(calls.every(([,method])=>method==='GET'),'reopening must follow the existing restore without posting again');
})().catch(err=>{console.error(err);process.exitCode=1});
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("run workspace restore UI: %v\n%s", err, output)
	}
}
