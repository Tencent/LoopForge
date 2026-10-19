package web

import (
	"os/exec"
	"testing"
)

func TestIssueDeleteInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "issue_delete_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("delete interaction: %v\n%s", err, out)
	}
}
