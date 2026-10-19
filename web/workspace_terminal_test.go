package web

import (
	"os/exec"
	"testing"
)

func TestWorkspaceTerminalSerializesAndPreservesFailures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const source=fs.readFileSync('extensions/agentflow-workspace/extension.js','utf8');
const fn=source.slice(source.indexOf('function registerWorkspaceTerminal('),source.indexOf('\nfunction activate('));
const tick=()=>new Promise(resolve=>setImmediate(resolve));
async function harness(respond){
 let callback,pty;const writes=[],exits=[],errors=[],calls=[],timers=[];
 class EventEmitter { constructor(){this.event=()=>{}} fire(value){(typeof value==='number'?exits:writes).push(value)} dispose(){} }
 const vscode={EventEmitter,commands:{registerCommand:(name,cb)=>{callback=cb;return{}}},window:{createTerminal:opts=>{pty=opts.pty;return{show(){},dispose(){}}},showErrorMessage:message=>errors.push(message)}};
 const origin=()=> 'http://test';
 const fetch=async(url,opts)=>{const req=JSON.parse(opts.body);calls.push(req);return respond(req)};
 const setTimeout=(cb,delay)=>{if(delay===150)timers.push(cb);else queueMicrotask(cb);return 1},clearTimeout=()=>{};
 eval(fn);registerWorkspaceTerminal({subscriptions:[]},'run');callback();
 return {pty,writes,exits,errors,calls,timers};
}
const ok=body=>({ok:true,status:200,json:async()=>body});
const busy=()=>({ok:false,status:409,json:async()=>({code:'workspace_busy',error:'busy'})});
(async()=>{
 let release,active=0,maxActive=0;
 const h=await harness(async req=>{
  active++;maxActive=Math.max(active,maxActive);
  let value=ok({});
  if(req.operation==='open')value=ok({id:'shell'});
  if(req.operation==='read')value=await new Promise(resolve=>release=()=>resolve(ok({})));
  active--;return value;
 });
 await h.pty.open({columns:80,rows:24});await tick();
 h.pty.setDimensions({columns:100,rows:30});h.pty.handleInput('echo test\r');await tick();
 assert.deepEqual(h.calls.map(x=>x.operation),['open','read']);
 release();await tick();await tick();
 assert.deepEqual(h.calls.map(x=>x.operation),['open','read','resize','input']);assert.equal(maxActive,1);
 h.pty.close();await tick();assert.equal(h.calls.filter(x=>x.operation==='close').length,1);

 const keyboard=await harness(async req=>ok(req.operation==='open'?{id:'shell'}:{}));
 await keyboard.pty.open();await tick();
 const keys=['echo comple','\t','\x1b[A','\x1b[B','\x1b[C','\x1b[D','\r'];
 for(const key of keys)keyboard.pty.handleInput(key);
 await tick();
 assert.deepEqual(keyboard.calls.filter(x=>x.operation==='input').map(x=>x.data),keys,'Tab and arrow keys must reach the shell unchanged and in order');
 keyboard.pty.close();await tick();

 let attempts=0;
 const retry=await harness(async req=>req.operation==='open'?(++attempts<3?busy():ok({id:'shell'})):ok({}));
 await retry.pty.open();await tick();assert.equal(attempts,3);assert.equal(retry.errors.length,0);retry.pty.close();

 for(const failure of ['http','network']){
  const broken=await harness(async req=>{
   if(req.operation==='open')return ok({id:'shell'});
   if(req.operation==='input'){if(failure==='network')throw new Error('network disconnected');return {ok:false,status:409,json:async()=>({error:'shell unavailable'})}}
   return ok({});
  });
  await broken.pty.open();await tick();broken.pty.handleInput('one command\r');await tick();await tick();
  assert.equal(broken.calls.filter(x=>x.operation==='input').length,1,'ambiguous failures must never replay input');
  assert.equal(broken.exits.length,0,'connection failures must keep diagnostic output visible');
  assert.equal(broken.errors.length,1);assert.ok(broken.writes.join('').includes(failure==='http'?'shell unavailable':'network disconnected'));
 }
 const ended=await harness(async req=>ok(req.operation==='open'?{id:'shell'}:{exited:true}));
 await ended.pty.open();await tick();assert.deepEqual(ended.exits,[0]);
 let resolveOpen;
 const late=await harness(async req=>req.operation==='open'?new Promise(resolve=>resolveOpen=resolve):ok({}));
 const opening=late.pty.open();await tick();late.pty.close();resolveOpen(ok({id:'late-shell'}));await opening;await tick();
 assert.deepEqual(late.calls.map(x=>x.operation),['open','close']);
})().catch(err=>{console.error(err);process.exitCode=1});
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("terminal adapter: %v\n%s", err, output)
	}
}
