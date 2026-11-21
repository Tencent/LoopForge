package web

import (
	"os/exec"
	"testing"
)

// The diagnostics panel is assembled from a few pure helpers; node asserts the
// markup and the status/count mapping against the concatenated app bundle.
func TestPlatformDiagnosticsPanel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "platform_diagnostics_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("platform diagnostics panel: %v\n%s", err, out)
	}
}
