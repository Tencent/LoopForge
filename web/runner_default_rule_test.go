package web

import (
	"os/exec"
	"testing"
)

func TestRunnerDefaultRuleMatchesAPI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "runner_default_rule_test.js").CombinedOutput(); err != nil {
		t.Fatalf("runner default rule: %v\n%s", err, out)
	}
}
