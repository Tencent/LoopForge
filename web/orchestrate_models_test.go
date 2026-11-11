package web

import (
	"os/exec"
	"testing"
)

func TestOrchestratorCLIModelSelection(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "orchestrate_models_test.js").CombinedOutput(); err != nil {
		t.Fatalf("orchestrator CLI/model selection: %v\n%s", err, out)
	}
}
