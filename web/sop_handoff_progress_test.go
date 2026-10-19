package web

import (
	"os/exec"
	"testing"
)

func TestSOPHandoffProgressPersistsAndRetriesOnlyFailedStage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const source=fs.readFileSync('collaboration.js','utf8');
const sopReturns=new Map(),sopReturnRequests=new Set(),sopReturnLoads=new Map();
let currentRun='a',nodeId='old-node',scope='project',renders=0,refreshes=0,leaves=0,calls=[],notices=[],response=null;
const project=()=>scope,renderPipeline=()=>renders++,paintTasks=()=>{},refresh=async()=>refreshes++;
const assistant={leaveNode:()=>leaves++},toast=x=>notices.push(x),taskTitle=()=> 'Task A';
const taskRows=[{id:'a'},{id:'b'}];
const run={status:'waiting',nodes:{},workflowSnapshot:{nodes:[{id:'build',name:'Build'}]}};
let api=async(path,options)=>{calls.push({path,options});return response};
eval(source.slice(source.indexOf('  function handoffActive('),source.indexOf('  const input=assistant.input;')));
eval(source.slice(source.indexOf('  function cacheSOPHandoff('),source.indexOf('  function restoreSOPDialog(')));
(async()=>{
 response={job:{id:'j1',runId:'a',nodeId:'build',status:'pending',phase:'summarizing'}};
 await returnToSOP('a',{nodeId:'build'},'Task A');
 assert.equal(sopReturnPending(),true,'202 means accepted, not finished');
 assert.equal(calls[0].options.silent,true);assert.deepEqual(JSON.parse(calls[0].options.body),{nodeId:'build'});
 const submitted=calls.length;await returnToSOP('a',{nodeId:'other'},'Task A');assert.equal(calls.length,submitted,'reject duplicate handoff');
 currentRun='b';nodeId='b-node';assert.equal(sopReturnPending(),false,'other tasks remain usable');
 response={job:{id:'j1',runId:'a',nodeId:'build',status:'succeeded',phase:'done'}};
 await loadSOPHandoff('a');assert.equal(leaves,0,'completion must not exit a different task node view');assert.equal(nodeId,'b-node');
 // Loading a historical success must not eject the reader from a selected node.
 sopReturns.clear();currentRun='a';nodeId='historical-node';response={job:{id:'j1',runId:'a',status:'succeeded',phase:'done'}};
 await loadSOPHandoff('a');assert.equal(nodeId,'historical-node');assert.equal(leaves,0);
 // A reload has no client cache. Restore the server-owned job, then keep it when offline.
 sopReturns.clear();currentRun='a';response={job:{id:'j1',runId:'a',status:'running',phase:'saving'}};
 await loadSOPHandoff('a');assert.equal(sopReturnPending(),true);assert.match(handoffPresentation(sopReturns.get('a')).label,/保存交接/);
 const normalAPI=api;api=async()=>{throw new Error('offline')};await loadSOPHandoff('a');assert.equal(sopReturnPending(),true);api=normalAPI;
 response={job:{id:'j1',runId:'a',status:'failed',phase:'starting',summaryArtifactId:'summary',error:'worker unavailable'}};
 await loadSOPHandoff('a');const failed=handoffPresentation(sopReturns.get('a'));
 assert.match(failed.label,/交接已保存/);assert.equal(failed.retry,'重试继续');assert.equal(failed.detail,'worker unavailable');
 response={job:{id:'j1',runId:'a',status:'running',phase:'starting',summaryArtifactId:'summary'}};
 calls=[];await retrySOPHandoff('a');assert.equal(calls[0].path,'/api/runs/a/group/sop/handoff/retry');assert.deepEqual(JSON.parse(calls[0].options.body),{});
 assert.match(handoffPresentation(sopReturns.get('a')).label,/正在继续 SOP/);
 // Slow GETs started before a later refresh cannot replace the new state.
 let slow;api=()=>new Promise(resolve=>slow=resolve);const stale=loadSOPHandoff('a');api=normalAPI;
 response={job:{id:'j1',runId:'a',status:'succeeded',phase:'done'}};await loadSOPHandoff('a');
 slow({job:{id:'j1',status:'running',phase:'summarizing'}});await stale;
 assert.equal(sopReturns.get('a').status,'succeeded');assert.equal(sopReturnPending(),false);
 // A GET during POST acceptance cannot clear its temporary double-click guard.
 sopReturns.set('a',{status:'submitting'});sopReturnRequests.add('a');response={job:null};await loadSOPHandoff('a');assert.equal(sopReturnPending(),true);sopReturnRequests.delete('a');
 response={job:null};await loadSOPHandoff('a');assert.equal(sopReturns.has('a'),false);
 // Batch state repopulates the sidebar after a reload, while protecting an in-flight submit.
 currentRun='';response={jobs:[{id:'j3',runId:'b',status:'running',phase:'summarizing'}]};await refreshSOPHandoffs();assert.equal(sopReturns.get('b').id,'j3');
 sopReturnRequests.add('b');response={jobs:[]};await refreshSOPHandoffs();assert.equal(sopReturns.get('b').id,'j3');sopReturnRequests.delete('b');await refreshSOPHandoffs();assert.equal(sopReturns.has('b'),false);currentRun='a';
 // The modal closes before the acknowledgement; browser lifetime no longer owns processing.
 class Element{constructor(tag){this.tag=tag;this.children=[];this.value='';this.closed=false}
 append(...items){for(const item of items){this.children.push(item);if(this.tag==='select'&&!this.value)this.value=item.value}}
 setAttribute(){} close(){this.closed=true} showModal(){} }
 const dialogs=[],document={createElement:tag=>new Element(tag),body:{append:el=>dialogs.push(el)}};
 const dismissDialog=()=>{};eval(source.slice(source.indexOf('  function restoreSOPDialog('),source.indexOf('  attach.onclick=')));
 let acknowledge;api=(path)=>path.endsWith('/group/sop')?new Promise(resolve=>acknowledge=resolve):Promise.resolve(response);
 restoreSOPDialog();const dialog=dialogs[0],form=dialog.children[0];form.onsubmit({preventDefault(){}});
 assert.equal(dialog.closed,true);assert.equal(sopReturnPending(),true);response={job:{id:'j2',status:'running',phase:'summarizing'}};acknowledge(response);await new Promise(setImmediate);
 assert.equal(sopReturns.get('a').id,'j2');
})().catch(err=>{console.error(err);process.exitCode=1});
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("handoff progress regression: %v\n%s", err, out)
	}
}

func TestSOPHandoffChatReceiptSurvivesSOPModeAndPreviewsSummary(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const source=fs.readFileSync('collaboration.js','utf8');
class Element{
 constructor(tag){this.tag=tag;this.children=[];this.dataset={};this.className='';this.classList={remove:()=>{}}}
 append(...items){for(const el of items){if(el.parent)el.parent.children=el.parent.children.filter(x=>x!==el);this.children.push(el);el.parent=this}}
 replaceChildren(...items){this.children=[];this.append(...items)}
 querySelector(selector){return this.children.find(e=>selector==='[data-sop-handoff]'?!!e.dataset.sopHandoff:selector==='p'?e.tag==='p':selector==='button'?e.tag==='button':e.className?.split(' ').includes(selector.slice(1)))||this.children.map(e=>e.querySelector?.(selector)).find(Boolean)||null}
 remove(){if(this.parent)this.parent.children=this.parent.children.filter(e=>e!==this)}
}
const document={createElement:tag=>new Element(tag),createTextNode:text=>({textContent:text})};
const root=new Element('div'),log=new Element('div'),sopReturns=new Map();let currentRun='a',nodeId='',previews=[];
const icon=()=>new Element('svg'),previewArtifact=(...args)=>previews.push(args),groupTime=v=>Date.parse(v||'')||0;
const handoffActive=job=>['pending','running'].includes(job?.status);
const run={workflowSnapshot:{collaborationMode:'',nodes:[{id:'build',name:'开发实现'}],groupSopHandoff:{id:'h1',nodeId:'build',summaryArtifactId:'summary',summaryPath:'handoffs/h1/handoff.md',createdAt:'2026-10-15T01:00:00Z'}}};
eval(source.slice(source.indexOf('  function paintSOPHandoffReceipt('),source.indexOf('  function paintGroupReceipt(')));
paintSOPHandoffReceipt(log);let receipt=log.querySelector('[data-sop-handoff]');assert.ok(receipt,'SOP mode still displays handoff archive');
assert.match(receipt.querySelector('p').textContent,/开发实现/);receipt.querySelector('button').onclick();assert.equal(previews[0][0],'a');assert.equal(previews[0][1].id,'summary');
// New server state updates the same card, and plain text does not execute HTML.
sopReturns.set('a',{id:'j1',status:'failed',phase:'starting',nodeId:'build',summaryArtifactId:'summary',summaryPath:'handoffs/h1/handoff.md',summaryPreview:'已确认：<script>unsafe</script>\n待办：验证接口'});
paintSOPHandoffReceipt(log);assert.equal(log.children.length,1);assert.match(receipt.querySelector('p').textContent,/尚未启动 SOP/);assert.equal(receipt.querySelector('.collab-handoff-preview').textContent,'已确认：<script>unsafe</script>\n待办：验证接口');
nodeId='build';paintSOPHandoffReceipt(log);assert.equal(log.children.length,0,'node execution view owns its own transcript');
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("handoff chat receipt regression: %v\n%s", err, out)
	}
}
