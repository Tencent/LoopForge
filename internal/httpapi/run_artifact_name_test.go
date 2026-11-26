package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestArtifactFileNameKeepsTheDeliverableExtension(t *testing.T) {
	for _, tc := range []struct {
		artifact domain.Artifact
		want     string
	}{
		{domain.Artifact{Name: "model", Path: "out/model.bin", MediaType: "application/octet-stream"}, "model.bin"},
		{domain.Artifact{Name: "plan", Path: "plan.md", MediaType: "text/markdown"}, "plan.md"},
		{domain.Artifact{Name: "result", Path: "result.md", MediaType: "text/markdown"}, "result.md"},
		{domain.Artifact{Name: "gate", Path: "gate.json", MediaType: "application/json"}, "gate.json"},
		{domain.Artifact{Name: "notes", Path: "notes", MediaType: "text/plain"}, "notes.txt"},
		{domain.Artifact{Name: "legacy", MediaType: "text/markdown"}, "legacy.md"},
	} {
		if got := artifactFileName(tc.artifact); got != tc.want {
			t.Fatalf("artifactFileName(%q, %q) = %q, want %q", tc.artifact.Name, tc.artifact.Path, got, tc.want)
		}
	}
}
