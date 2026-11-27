package web

import (
	"os/exec"
	"testing"
)

func TestWorkbenchSendsHumanContextOnce(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "human_context_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("human context: %v\n%s", err, out)
	}
}
