package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestBrowserSidecarImageReportsReference(t *testing.T) {
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "")
	image, exists, err := BrowserSidecarImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(image, "agentflow-browser-") || len(image) != len("agentflow-browser-")+16 {
		t.Fatalf("image = %q, want agentflow-browser-<16 hex>", image)
	}
	if exists && !DockerImageExists(context.Background(), image) {
		t.Fatalf("exists=true but DockerImageExists(%q) = false", image)
	}
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "ghcr.io/example/agentflow-browser:prebuilt")
	image, _, err = BrowserSidecarImage(context.Background())
	if err != nil || image != "ghcr.io/example/agentflow-browser:prebuilt" {
		t.Fatalf("pinned image = %q, err = %v", image, err)
	}
}

func TestDockerImageExistsRejectsEmptyReference(t *testing.T) {
	if DockerImageExists(context.Background(), "   ") {
		t.Fatal("an empty image reference must not exist")
	}
}
