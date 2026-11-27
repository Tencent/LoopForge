package web

import (
	"os/exec"
	"testing"
)

func TestCommandPaletteFindsRequirementsByKey(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "command_palette_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("command palette: %v\n%s", err, out)
	}
}
