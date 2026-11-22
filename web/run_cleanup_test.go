package web

import (
	"os/exec"
	"testing"
)

// The run cleanup panel is assembled from pure helpers; node asserts candidate
// rendering, retention selection, destructive-action enablement and hint copy.
func TestRunCleanupPanel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "run_cleanup_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("run cleanup panel: %v\n%s", err, out)
	}
}
