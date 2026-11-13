package web

import (
	"os/exec"
	"testing"
)

func TestDiffCoverageGateUIKeepsSettings(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "diff_coverage_gate_ui_test.js").CombinedOutput(); err != nil {
		t.Fatalf("diff coverage gate ui: %v\n%s", err, out)
	}
}
