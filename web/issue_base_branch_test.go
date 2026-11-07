package web

import (
	"os/exec"
	"testing"
)

func TestIssueBaseBranchComposer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "issue_base_branch_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("issue base branch composer: %v\n%s", err, out)
	}
}
