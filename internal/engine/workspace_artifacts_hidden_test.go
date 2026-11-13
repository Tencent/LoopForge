package engine

import (
	"agentflow/internal/domain"
	"testing"
	"time"
)

func TestHasHiddenPathSegment(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"report.md":            false,
		"docs/design.md":       false,
		"docs/v1.0/notes.md":   false,
		".env":                 true,
		".git/config":          true,
		"docs/.cache/out.md":   true,
		"backend/docs/.hidden": true,
	}
	for rel, want := range cases {
		if got := hasHiddenPathSegment(rel); got != want {
			t.Errorf("hasHiddenPathSegment(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestArchivedFilesSkipHiddenPaths(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := domain.Run{
		Nodes: map[string]*domain.NodeRun{"a": {Status: "succeeded"}},
		Artifacts: []domain.Artifact{
			archivedTestFile("a", "docs/report.md", "kept", now),
			archivedTestFile("a", ".env", "secret", now),
			archivedTestFile("a", "docs/.cache/tmp.md", "cache", now),
		},
	}
	files := archivedWorkspaceFiles(r, "")
	if len(files) != 1 || files[0].Path != "docs/report.md" {
		t.Fatalf("hidden paths should be skipped: %+v", files)
	}
}
