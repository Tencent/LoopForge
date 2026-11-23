package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/workspaceprep"
)

func TestSSHCloneWithProgressRequiresManagedKey(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "git-started")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf started > \"$SSH_PROGRESS_GIT_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_PROGRESS_GIT_MARKER", marker)
	var progress []domain.WorkspaceOpening
	ctx := workspaceprep.WithSink(context.Background(), func(p domain.WorkspaceOpening) {
		progress = append(progress, p)
	})
	err := cloneRepository(ctx, t.TempDir(), &domain.RunnerRepository{
		CloneURL: "git@forge.example.org:team/repo.git", UseSSH: true,
		GitToken: "unrelated-https-token", Ref: "main",
	})
	if !errors.Is(err, gitwork.ErrSSHKeyRequired) {
		t.Fatalf("SSH clone must require its managed key, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("started Git without the managed SSH key: %v", err)
	}
	workspaceprep.Result(ctx, err)
	if len(progress) < 2 || progress[0].Phase != "clone" || progress[len(progress)-1].State != "failed" || progress[len(progress)-1].Phase != "clone" {
		t.Fatalf("missing clone failure progress: %+v", progress)
	}
}
