package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
)

func commitOnBranch(t *testing.T, remote, branch, path, content string) {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "c")
	runGitT(t, filepath.Dir(clone), "clone", "-q", "--branch", branch, remote, clone)
	if err := os.WriteFile(filepath.Join(clone, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, clone, "add", path)
	runGitT(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "change "+path)
	runGitT(t, clone, "push", "-q", "origin", branch)
}

// 基线对齐也在云端执行机器上做：干净合并直接推送，冲突带着冲突片段返回，集成分支不动。
func TestAlignBaselineRunsOnPoolRunner(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	e.ConfigureCloudRunnerPool(engine.CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}, Required: true})
	startPoolRunner(t, s, server.URL, "builtin-a")
	remote := bareRemoteWithBaseline(t)
	seedIntegrationBranch(t, remote, "integration", "other.txt", "integration only\n")
	commitOnBranch(t, remote, "master", "main.txt", "from default\n")
	repository := domain.RepositoryContext{RepositoryID: "repo", Name: "svc", CloneURL: remote, DefaultBranch: "master", IntegrateBranch: "integration"}

	if err := e.AlignBaseline(repository, ""); err != nil {
		t.Fatal(err)
	}
	files := runGitT(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/integration")
	if !strings.Contains(files, "main.txt") || !strings.Contains(files, "other.txt") {
		t.Fatalf("integration after align: %s", files)
	}

	commitOnBranch(t, remote, "master", "README.md", "default side\n")
	commitOnBranch(t, remote, "integration", "README.md", "integration side\n")
	before := runGitT(t, remote, "rev-parse", "refs/heads/integration")
	err := e.AlignBaseline(repository, "")
	var conflict *engine.AlignConflictError
	if !errors.As(err, &conflict) || strings.Join(conflict.Files, ",") != "README.md" || len(conflict.Blocks) != 1 || !strings.Contains(conflict.Blocks[0].Theirs, "default side") {
		t.Fatalf("align conflict = %v (%+v)", err, conflict)
	}
	if after := runGitT(t, remote, "rev-parse", "refs/heads/integration"); after != before {
		t.Fatal("conflicted align moved the integration branch")
	}
	var tasks int
	for _, task := range s.RunnerTasks() {
		if task.Action == domain.RunnerTaskActionAlignBaseline {
			tasks++
		}
	}
	if tasks != 2 {
		t.Fatalf("align tasks = %d, want 2", tasks)
	}
}
