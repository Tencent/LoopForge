package web

import (
	"os/exec"
	"testing"
)

func TestRequirementAttachments(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "requirement_attachments_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("requirement attachments: %v\n%s", err, out)
	}
}
