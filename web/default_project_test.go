package web

import (
	"os/exec"
	"testing"
)

// New users start in a project they belong to, then the welcome project, then the first project.
func TestDefaultProjectPrefersMembershipThenWelcome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict');
const code=require('node:fs').readFileSync('` + appBundlePath(t) + `','utf8');
const start=code.indexOf("const DEFAULT_PROJECT_ID=");
eval(code.slice(start,code.indexOf('\n',code.indexOf('function defaultProjectId(',start))));
assert.equal(defaultProjectId([{id:'agent-chat'},{id:'welcome'},{id:'platform'}]),'welcome');
assert.equal(defaultProjectId([{id:'agentflow'},{id:'platform'}]),'agentflow');
assert.equal(defaultProjectId([{id:'welcome'},{id:'team',ownerId:7}],7),'team');
assert.equal(defaultProjectId([{id:'welcome'},{id:'team',members:[{userId:9}]}],9),'team');
assert.equal(defaultProjectId([{id:'welcome'},{id:'team',ownerId:7}],8),'welcome');
assert.equal(defaultProjectId([]),'');
assert.equal(defaultProjectId(undefined),'');
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("default project check failed: %v\n%s", err, out)
	}
}
