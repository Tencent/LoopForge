package web

import (
	"os/exec"
	"testing"
)

func TestWorkspacePreparationKeepsFinalStateAfterSlowPoll(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const source=fs.readFileSync('vscode.js','utf8');
let editing=true,environmentState=null,preparingEnvironment=false,environmentPoll=0,environmentEpoch=0,environmentRetry=0;
const painted=[];function paintEnvironment(info){environmentState=info;painted.push(info)}
let completeAction,completePoll;
let action=()=>new Promise(resolve=>completeAction=resolve);
let fetch=()=>new Promise(resolve=>completePoll=resolve);
let retryAction;const setTimeout=fn=>{retryAction=fn;return 2},clearTimeout=()=>{};
const setInterval=()=>1,clearInterval=()=>{};const runId='test';
eval(source.slice(source.indexOf('  async function refreshEnvironment('),source.indexOf('  let currentInfo=')));
(async()=>{
 const preparation=prepareEnvironment();
 assert.equal(preparingEnvironment,true);
 const slowPoll=refreshEnvironment();
 completeAction({status:'ready',steps:[{name:'索引',status:'ready'}]});await preparation;
 completePoll({ok:true,json:async()=>({status:'preparing'})});await slowPoll;
 assert.equal(environmentState.status,'ready','slow poll must not replace the completed result');
 assert.equal(preparingEnvironment,false);
 action=async()=>{throw new Error('dependency missing')};await prepareEnvironment();
 assert.equal(environmentState.status,'failed');assert.equal(environmentState.error,'dependency missing');
 assert.equal(preparingEnvironment,false,'preparation failure must permit retry');
 action=async()=>{const e=new Error('temporarily reserved');e.code='workspace_busy';e.retryAfterMs=2000;throw e};await prepareEnvironment();
 assert.equal(environmentState.status,'busy','normal handoff reservation is not preparation failure');
 assert.equal(preparingEnvironment,false);assert.equal(typeof retryAction,'function','busy state must retry without a user click');
 action=async()=>({status:'ready'});await retryAction();assert.equal(environmentState.status,'ready','retry must recover after reservation ends');
 const previous=painted.length;editing=false;await prepareEnvironment();assert.equal(painted.length,previous,'read-only browsing must not execute preparation');
})().catch(err=>{console.error(err);process.exitCode=1});
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("workspace preparation UI: %v\n%s", err, output)
	}
}
