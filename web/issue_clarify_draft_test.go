package web

import (
	"os/exec"
	"testing"
)

func TestIssueClarifyDraftSurvivesRefresh(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "issue_clarify_draft_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("issue clarify draft: %v\n%s", err, out)
	}
}
