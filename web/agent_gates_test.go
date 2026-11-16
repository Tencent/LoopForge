package web

import (
	"os/exec"
	"testing"
)

func TestAgentGatesKeepMRGatesOnSave(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "agent_gates_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("agent gates: %v\n%s", err, out)
	}
}
