package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

const legacyNewFileDiff = "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"

func initMaterializeGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func TestMaterializeRepositoryPatchInputSummaryOnly(t *testing.T) {
	artifact := domain.Artifact{NodeID: "source", Name: "repository-patch", RepositoryDir: "repo", Content: "summary only"}
	got, err := materializeRepositoryPatchInput(t.TempDir(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	want := "## source / repository-patch (repo)\n\nNo full diff is stored. The run workspace is the source of truth.\n\nsummary only\n\n"
	if got != want {
		t.Fatalf("section=%q want %q", got, want)
	}
}

func TestMaterializeRepositoryPatchInputAppliesLegacyDiffInRepositoryDir(t *testing.T) {
	workspace := t.TempDir()
	repoDir := filepath.Join(workspace, "repo")
	initMaterializeGitRepo(t, repoDir)
	artifact := domain.Artifact{NodeID: "source", Name: "repository-patch", RepositoryDir: "repo", Content: legacyNewFileDiff, SHA256: "patch-sha"}
	got, err := materializeRepositoryPatchInput(workspace, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "## source / repository-patch (repo)\n\nApplied a legacy unified diff (sha patch-sha).\n\n") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("section=%q", got)
	}
	if data, err := os.ReadFile(filepath.Join(repoDir, "new.txt")); err != nil || string(data) != "new\n" {
		t.Fatalf("patched file=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("patch leaked into workspace root: err=%v", err)
	}
}

func TestMaterializeRepositoryPatchInputSkipsWorkspaceWithRepoWork(t *testing.T) {
	workspace := t.TempDir()
	initMaterializeGitRepo(t, workspace)
	if err := os.WriteFile(gitwork.BaselinePath(workspace), []byte("baseline"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dirty.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifact := domain.Artifact{NodeID: "source", Name: "repository-patch", Content: legacyNewFileDiff, SHA256: "patch-sha"}
	got, err := materializeRepositoryPatchInput(workspace, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "## source / repository-patch ()\n\nSkipped applying; this run already owns the workspace.") {
		t.Fatalf("section=%q", got)
	}
	if _, err := os.Stat(filepath.Join(workspace, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("patch applied to owned workspace: err=%v", err)
	}
}

func TestMaterializeRepositoryPatchInputReportsApplyFailure(t *testing.T) {
	// Not a git checkout, so applying the diff fails.
	artifact := domain.Artifact{NodeID: "source", Name: "repository-patch", RepositoryDir: "repo", Content: legacyNewFileDiff}
	_, err := materializeRepositoryPatchInput(t.TempDir(), artifact)
	if err == nil || !strings.HasPrefix(err.Error(), "apply repository-patch from source (repo): ") {
		t.Fatalf("err=%v", err)
	}
}

func TestWriteArtifactInputFileWritesReadOnlyFileUnderRepositoryDir(t *testing.T) {
	workspace := t.TempDir()
	run := domain.Run{}
	artifact := domain.Artifact{NodeID: "source", Name: "notes", MediaType: "text/markdown", RepositoryDir: "repo", Content: "first"}
	if err := writeArtifactInputFile(workspace, run, artifact); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, filepath.FromSlash(artifactInputRelPath(run, artifact)))
	if want := filepath.Join(workspace, ".agentflow", "inputs", "source", "repo", "notes.md"); target != want {
		t.Fatalf("target=%s want %s", target, want)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	// A second write replaces the read-only file in place.
	artifact.Content = "second"
	if err := writeArtifactInputFile(workspace, run, artifact); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "second" {
		t.Fatalf("content=%q err=%v", data, err)
	}
}

func TestWriteArtifactInputFileRejectsNonRegularTarget(t *testing.T) {
	workspace := t.TempDir()
	artifact := domain.Artifact{NodeID: "source", Name: "notes", MediaType: "text/markdown"}
	if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "inputs", "source", "notes.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := writeArtifactInputFile(workspace, domain.Run{}, artifact)
	if err == nil || !strings.Contains(err.Error(), "artifact input path is not a regular file") {
		t.Fatalf("err=%v", err)
	}
}
