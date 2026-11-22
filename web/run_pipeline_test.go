package web

import (
	"os/exec"
	"testing"
)

// The run pipeline view is assembled from pure helpers; node asserts ordering,
// summary math, error rendering and timeline markup against the app bundle.
func TestRunPipelinePane(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "run_pipeline_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("run pipeline pane: %v\n%s", err, out)
	}
}
