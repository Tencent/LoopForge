package web

import (
	"os/exec"
	"testing"
)

func TestGroupLoopShowsFactsAndKeepsExecutionHistorySeparate(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),fs=require('node:fs');
const source=fs.readFileSync('collaboration.js','utf8');
eval(source.slice(source.indexOf('  function groupLoopStatus('),source.indexOf('  function scheduleGroupState(')));
assert.match(groupLoopStatus({members:[{status:'running'}]},null,false).label,/正在处理/);
assert.match(groupLoopStatus({members:[]},{status:'watching',pending:true},false).label,/等待 Main Bot/);
assert.match(groupLoopStatus({members:[]},{status:'running'},false).label,/Main Bot 正在跟进/);
assert.match(groupLoopStatus({members:[]},{status:'idle'},true).label,/Main Bot 正在跟进/,'human Main Bot generation must be visible too');
assert.match(groupLoopStatus({members:[]},{status:'blocked',error:'needs new direction'},false).label,/等待你的/);
assert.equal(groupVerificationLabel({status:'unverified'}),'尚无检查记录','completed task alone cannot imply verification');
assert.match(groupVerificationLabel({status:'historical'}),/当前修改待验证/);
const groupTime=v=>{const n=Date.parse(v||'');return n>0?n:0};
eval(source.slice(source.indexOf('  function groupEventInAttempt('),source.indexOf('  function groupProcess(')));
const assistant=fs.readFileSync('assistant.js','utf8');
eval(assistant.slice(assistant.indexOf('  function rebaseAutomaticReplies('),assistant.indexOf('  async function sendAssistantTurn(')));
const old=[{role:'user',content:'goal'},{role:'assistant',content:'working'}],sent={role:'user',content:'new direction'};
assert.equal(rebaseAutomaticReplies([...old,sent],[...old,{role:'user',content:'another person'}]),null,'never replay over another human');
assert.equal(rebaseAutomaticReplies([...old,sent],[...old,{role:'assistant',content:'ordinary reply'}]),null,'only trusted automatic replies may be rebased');
const rebased=rebaseAutomaticReplies([...old,sent],[...old,{role:'assistant',content:'done',trigger:'group_followup'}]);
assert.equal(rebased.length,4);assert.equal(rebased.at(-1),sent,'retain the human message and context exactly once');
const current={attempt:2,startedAt:'2026-10-15T08:00:00Z'};
assert.equal(groupEventInAttempt({timestamp:'2026-10-14T08:00:00Z'},current),false);
assert.equal(groupEventInAttempt({timestamp:'2026-10-15T08:01:00Z'},current),true);
assert.equal(groupEventInAttempt({context:{attempt:1},timestamp:'2026-10-15T08:01:00Z'},current),false);
assert.equal(groupEventInAttempt({context:{attempt:2}},current),true);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("group loop regression: %v\n%s", err, out)
	}
}
