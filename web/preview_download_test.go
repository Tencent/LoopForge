package web

import (
	"os/exec"
	"testing"
)

func TestPreviewDownloadInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "preview_download_test.js").CombinedOutput(); err != nil {
		t.Fatalf("preview download interaction: %v\n%s", err, out)
	}
}
