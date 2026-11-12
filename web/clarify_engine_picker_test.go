package web

import (
	"os/exec"
	"testing"
)

// The clarification engine picker groups the catalog by framework and greys out
// what the responsible user cannot run. Boot the shared module plus the real
// picker helpers from the bundle and click through both states.
func TestClarifyEnginePickerGroups(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "clarify_engine_picker_test.js", appBundlePath(t), "app/assistant-models.js").CombinedOutput(); err != nil {
		t.Fatalf("clarify engine picker: %v\n%s", err, out)
	}
}
