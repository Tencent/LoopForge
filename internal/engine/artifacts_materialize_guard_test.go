package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// A repository-patch that cannot be applied must stop materializeInputs: the
// consuming node would otherwise start on a workspace missing upstream work.
func TestMaterializeInputsStopsWhenRepositoryPatchCannotBeApplied(t *testing.T) {
	workspace := t.TempDir()
	wf := domain.WorkflowDefinition{ID: "wf", Edges: []domain.WorkflowEdge{{Source: "source", Target: "consumer"}}}
	run := domain.Run{Artifacts: []domain.Artifact{
		{NodeID: "source", Attempt: 1, Name: "repository-patch", RepositoryDir: "repo", MediaType: "text/x-diff", SHA256: "patch-sha", Content: legacyNewFileDiff},
	}}
	manifest, err := materializeInputs(workspace, wf, domain.WorkflowNode{ID: "consumer"}, run)
	if err == nil || !strings.HasPrefix(err.Error(), "apply repository-patch from source (repo): ") {
		t.Fatalf("manifest=%q err=%v", manifest, err)
	}
	if manifest != "" {
		t.Fatalf("manifest must be empty on failure: %q", manifest)
	}
}

// The input path is checked with Lstat: a planted symlink must be refused
// before chmod/write, so nothing outside the inputs directory is modified.
func TestWriteArtifactInputFileRefusesSymlinkWithoutTouchingItsTarget(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := domain.Artifact{NodeID: "source", Name: "notes", MediaType: "text/markdown", Content: "artifact"}
	target := filepath.Join(workspace, filepath.FromSlash(artifactInputRelPath(domain.Run{}, artifact)))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	err := writeArtifactInputFile(workspace, domain.Run{}, artifact)
	if err == nil || !strings.Contains(err.Error(), "artifact input path is not a regular file") {
		t.Fatalf("err=%v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
		t.Fatalf("symlink target was written: %q err=%v", data, err)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("symlink target mode changed: info=%v err=%v", info, err)
	}
}

// A child SOP shares its parent's checkout and may reuse node IDs, so its
// inputs live under "<run>--<node>". The file must land where the manifest
// (artifactInputRelPath) points, never in the parent's plain "<node>" slot.
func TestMaterializeInputsKeysChildSOPInputsByRun(t *testing.T) {
	workspace := t.TempDir()
	wf := domain.WorkflowDefinition{ID: "wf", Edges: []domain.WorkflowEdge{{Source: "source", Target: "consumer"}}}
	run := domain.Run{ID: "child-run", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: "parent-run"}, Artifacts: []domain.Artifact{
		{NodeID: "source", Attempt: 1, Name: "notes", MediaType: "text/markdown", SHA256: "a", Content: "child notes"},
	}}
	manifest, err := materializeInputs(workspace, wf, domain.WorkflowNode{ID: "consumer"}, run)
	if err != nil {
		t.Fatal(err)
	}
	rel := artifactInputRelPath(run, run.Artifacts[0])
	if want := ".agentflow/inputs/child-run--source/notes.md"; rel != want {
		t.Fatalf("rel=%s want %s", rel, want)
	}
	if !strings.Contains(manifest, rel) {
		t.Fatalf("manifest does not point at %s: %s", rel, manifest)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel))); err != nil || string(data) != "child notes" {
		t.Fatalf("input file=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".agentflow", "inputs", "source")); !os.IsNotExist(err) {
		t.Fatalf("child input written into the parent's node slot: err=%v", err)
	}
}
