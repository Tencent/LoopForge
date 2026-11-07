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

func TestIssueBulkDeleteInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "issue_bulk_delete_test.js", "app/issue-bulk-delete.js").CombinedOutput(); err != nil {
		t.Fatalf("bulk delete interaction: %v\n%s", err, out)
	}
}
