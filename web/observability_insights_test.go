package web

import (
	"os/exec"
	"testing"
)

func TestExternalUsageView(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "observability_insights_test.js").CombinedOutput(); err != nil {
		t.Fatalf("external usage view: %v\n%s", err, out)
	}
}
