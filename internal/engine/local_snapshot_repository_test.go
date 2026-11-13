package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPrepareLocalSnapshotRepositoryCommitsBaseline(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	repository := domain.RepositoryContext{RepositoryID: "repo", LocalPath: source, DefaultBranch: "master", BaseBranch: "master"}
	if err := prepareLocalSnapshotRepository(context.Background(), workspace, source, repository); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(workspace, "app.txt")); err != nil || string(content) != "hello" {
		t.Fatalf("copied content = %q, %v", content, err)
	}
	branch, err := gitC(context.Background(), workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != "agentflow-base" {
		t.Fatalf("branch = %q, %v", branch, err)
	}
	subject, err := gitC(context.Background(), workspace, "log", "-1", "--format=%s")
	if err != nil || strings.TrimSpace(subject) != "LoopForge baseline" {
		t.Fatalf("baseline commit = %q, %v", subject, err)
	}
}

func TestPrepareLocalSnapshotRepositoryRejectsBranchRequests(t *testing.T) {
	t.Parallel()
	for name, repository := range map[string]domain.RepositoryContext{
		"continue work branch": {RepositoryID: "repo", WorkBranch: "feature/x", RequireExistingWorkBranch: true},
		"custom base branch":   {RepositoryID: "repo", DefaultBranch: "master", BaseBranch: "release"},
		"legacy custom base":   {RepositoryID: "repo", BaseBranch: "develop"},
	} {
		source, workspace := t.TempDir(), t.TempDir()
		err := prepareLocalSnapshotRepository(context.Background(), workspace, source, repository)
		if err == nil || !strings.Contains(err.Error(), "local source is not a Git repository") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(workspace, ".git")); !os.IsNotExist(statErr) {
			t.Fatalf("%s: workspace was initialized before rejection", name)
		}
	}
}

func TestPrepareLocalSnapshotRepositoryAcceptsLegacyDefaultBase(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"", "main", "master"} {
		source, workspace := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte(base), 0o644); err != nil {
			t.Fatal(err)
		}
		repository := domain.RepositoryContext{RepositoryID: "repo", BaseBranch: base}
		if err := prepareLocalSnapshotRepository(context.Background(), workspace, source, repository); err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
	}
}
