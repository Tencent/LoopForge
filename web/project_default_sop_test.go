package web

import (
	"os/exec"
	"testing"
)

func TestProjectDefaultSOP(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "project_default_sop_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("project default SOP: %v\n%s", err, out)
	}
}
