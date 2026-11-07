package web

import (
	"os/exec"
	"testing"
)

func TestClarificationComposerInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "clarification_composer_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("clarification composer interaction: %v\n%s", err, out)
	}
}
