package web

import (
	"os/exec"
	"testing"
)

func TestApprovalBadgePreload(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "approval_badge_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("approval badge preload: %v\n%s", err, out)
	}
}
