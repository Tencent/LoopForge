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

func TestIDEStartupReusesPreparedEnvironment(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const { createDOM } = require('./hui_fake_dom.js');

async function openIDE({ status, readOnly = false, preparation }) {
  const dom = createDOM();
  const { document, window } = dom;
  const host = dom.html('<div id="workbench"></div>');
  host.before = node => document.body.insertBefore(node, host);
  const appendChild = document.head.appendChild.bind(document.head);
  document.head.appendChild = node => {
    appendChild(node);
    if (node.tagName === 'SCRIPT') queueMicrotask(() => node.onload());
    return node;
  };

  const preparationMethods = [];
  const commands = [];
  let startWorkbench;
  window.URLSearchParams = URLSearchParams;
  window.location = new URL('https://ide.test/vscode.html?run=test&embed=1');
  window.require = (_modules, start) => {
    startWorkbench = () => start({
      create() {},
      commands: { executeCommand: command => commands.push(command) }
    }, { URI: { from: value => value, parse: value => value } });
  };
  window.require.config = () => {};
  window.setInterval = () => 1;
  window.clearInterval = () => {};
  window.fetch = async (url, { method = 'GET' } = {}) => {
    const response = body => ({ ok: true, json: async () => body });
    switch (url) {
      case '/api/runs/test/workspace':
        return response({ available: true, readOnly });
      case '/api/runs/test/workspace/use':
        return response({});
      case '/api/runs/test/workspace/preparation':
        preparationMethods.push(method);
        return response(method === 'GET' ? { status } : await preparation);
      default:
        throw new Error('Unexpected request ' + method + ' ' + url);
    }
  };
  dom.load('vscode.js');
  await dom.settle();
  assert.equal(typeof startWorkbench, 'function', 'the workbench should load in parallel with preparation');
  return {
    terminal: document.querySelector('button[aria-label="新建终端"]'),
    status: document.querySelector('.vscode-toolbar-status'),
    preparationMethods, commands, startWorkbench, settle: dom.settle
  };
}

(async () => {
  const warm = await openIDE({ status: 'ready' });
  assert.deepEqual(warm.preparationMethods, ['GET'], 'a ready environment needs no preparation POST');
  assert.equal(warm.terminal.disabled, true, 'the terminal command needs the workbench');
  warm.startWorkbench();
  assert.equal(warm.terminal.disabled, false);
  warm.terminal.onclick();
  assert.deepEqual(warm.commands, ['agentflow.newTerminal']);

  let finishPreparation;
  const preparation = new Promise(resolve => { finishPreparation = resolve; });
  const cold = await openIDE({ status: 'unprepared', preparation });
  assert.deepEqual(cold.preparationMethods, ['GET', 'POST']);
  cold.startWorkbench();
  assert.equal(cold.terminal.disabled, true);
  assert.match(cold.status.textContent, /正在准备终端环境/);
  finishPreparation({ status: 'ready' });
  await cold.settle();
  assert.equal(cold.terminal.disabled, false);
  assert.doesNotMatch(cold.status.textContent, /正在准备终端环境/);

  const viewer = await openIDE({ status: 'unprepared', readOnly: true });
  viewer.startWorkbench();
  assert.deepEqual(viewer.preparationMethods, ['GET']);
  assert.equal(viewer.terminal.disabled, true);
})().catch(error => { console.error(error); process.exitCode = 1; });
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("IDE startup: %v\n%s", err, output)
	}
}
