package web

import (
	"os/exec"
	"testing"
)

func TestGroupChatKeepsPMBeforeDispatchedRoles(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict');
const source=require('node:fs').readFileSync('assistant.js','utf8');
let messages=[],group=true;
const groupChat=()=>group;
eval(source.slice(source.indexOf('  function chatTime('),source.indexOf('  function renderTurn(')));
messages=[{role:'user',createdAt:'2026-10-14T09:23:42Z'},
 {role:'assistant',startedAt:'2026-10-14T09:23:43Z',createdAt:'2026-10-14T09:24:50Z'}];
const dispatch=Date.parse('2026-10-14T09:24:42Z');
assert.ok(chatTime(messages[0],0)<chatTime(messages[1],1));
assert.ok(chatTime(messages[1],1)<dispatch);
const original=chatTime(messages[1],1);
messages[1].createdAt='2026-10-14T09:26:00Z';
assert.equal(chatTime(messages[1],1),original,'completion must not reorder the PM');
// Reloading an old transcript has no startedAt. It belongs to the same user turn.
delete messages[1].startedAt;
assert.ok(chatTime(messages[1],1)<dispatch,'legacy PM must precede its dispatched roles');
messages[1].trigger='group_followup';
assert.equal(chatTime(messages[1],1),Date.parse(messages[1].createdAt),'automatic reply stays after the work it follows');
delete messages[1].trigger;
group=false;
assert.equal(chatTime(messages[1],1),Date.parse(messages[1].createdAt),'ordinary chat retains its timestamp');
messages=[{role:'user'},{role:'assistant',createdAt:'2026-10-14T09:24:50Z'}];
assert.ok(chatTime(messages[0],0)<chatTime(messages[1],1),'old untimed user turns keep transcript order');
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("group order regression: %v\n%s", err, out)
	}
}

func TestRestoredSOPPausePanelUsesSelectedNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict');
const source=require('node:fs').readFileSync('app/approvals.js','utf8');
const state={data:{workflows:[]}};
const escapeHTML=String,isScriptWorkflowNode=n=>n.type==='script';
const humanContextFieldHTML=()=>'',cachedRun=()=>null;
eval(source.slice(source.indexOf('function restoredSOPResumeNode('),source.indexOf('function approvalCalloutHTML(')));
const run={id:'restore',pauseRequested:true,status:'waiting',workflowSnapshot:{nodes:[{id:'chosen',name:'选择节点',type:'agent'}]},nodes:{chosen:{status:'pending',activity:'切回 SOP，等待继续'}},events:[{type:'group.sop_restored',nodeId:'chosen'}]};
assert.equal(pausedWorkflowNodes(run)[0].id,'chosen');
assert.ok(pausedRunPanelHTML(run).includes('将从「选择节点」重新启动 SOP'));
assert.ok(!pausedRunPanelHTML(run).includes('脚本会保留'));
run.workflowSnapshot.nodes[0].type='script';
assert.ok(!pausedRunPanelHTML(run).includes('data-pass-node'),'restored pending scripts cannot bypass the SOP');
run.nodes.chosen.status='waiting_approval';run.nodes.chosen.activity='用户暂停';
assert.ok(pausedRunPanelHTML(run).includes('data-pass-node'),'normal paused script controls remain available');
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("restored SOP pause panel: %v\n%s", err, out)
	}
}
