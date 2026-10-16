package engine

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// TestLatestInputArtifactsKeepsPerRepositoryPatches verifies that the
// multi-repository run keeps one repository-patch artifact per repository:
// they share the logical name but differ by path, so the (node, name, path)
// dedup key must not collapse them.
func TestLatestInputArtifactsKeepsPerRepositoryPatches(t *testing.T) {
	base := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	artifacts := []domain.Artifact{
		{ID: "a1", NodeID: "impl", Name: "repository-patch", Path: "repository-frontend.patch", RepositoryDir: "frontend", Attempt: 1, CreatedAt: base},
		{ID: "a2", NodeID: "impl", Name: "repository-patch", Path: "repository-backend.patch", RepositoryDir: "backend", Attempt: 1, CreatedAt: base},
		// Newer attempt for the same repository path replaces the older one.
		{ID: "a3", NodeID: "impl", Name: "repository-patch", Path: "repository-frontend.patch", RepositoryDir: "frontend", Attempt: 2, CreatedAt: base.Add(time.Second)},
		// Same node+name+path for a non-repo artifact still dedups.
		{ID: "a4", NodeID: "impl", Name: "report", Path: "report.md", Attempt: 1, CreatedAt: base},
		{ID: "a5", NodeID: "impl", Name: "report", Path: "report.md", Attempt: 2, CreatedAt: base.Add(time.Second)},
	}
	out := latestInputArtifacts(artifacts, map[string]bool{"impl": true}, nil)
	byID := map[string]domain.Artifact{}
	for _, a := range out {
		byID[a.ID] = a
	}
	if _, ok := byID["a1"]; ok {
		t.Fatal("older frontend patch attempt must be replaced by the newer one")
	}
	if _, ok := byID["a2"]; !ok {
		t.Fatal("backend repository patch must survive the dedup")
	}
	if a, ok := byID["a3"]; !ok || a.RepositoryDir != "frontend" {
		t.Fatal("newest frontend repository patch must survive the dedup")
	}
	if _, ok := byID["a4"]; ok {
		t.Fatal("older report attempt must be replaced by the newer one")
	}
	if _, ok := byID["a5"]; !ok {
		t.Fatal("newest report must survive the dedup")
	}
}
