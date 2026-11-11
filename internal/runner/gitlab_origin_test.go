package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestRunnerGitLabOriginAllowsCanonicalEquivalents(t *testing.T) {
	for _, remote := range []string{"https://gitlab.example.test/group/repo.git", "https://gitlab.example.test/group/repo", "git@gitlab.example.test:group/repo.git"} {
		t.Run(remote, func(t *testing.T) {
			dir := t.TempDir()
			runGitT(t, dir, "init", "-q")
			runGitT(t, dir, "remote", "add", "origin", remote)
			inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: "https://gitlab.example.test/group/repo.git", GitAuthBaseURL: "https://gitlab.example.test", GitToken: "requester-secret", WorkBranch: "feature", SyncWorkBranch: true}}}
			if err := requireTaskGitLabOrigins(context.Background(), dir, inv); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerGitLabOriginPreflightBeforeAnyRepositoryMutation(t *testing.T) {
	root := t.TempDir()
	var repositories []domain.RunnerRepository
	var originalHeads []string
	for _, name := range []string{"github-first", "gitlab-second"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitT(t, dir, "init", "-q", "-b", "feature")
		runGitT(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-qm", "baseline")
		originalHeads = append(originalHeads, runGitT(t, dir, "rev-parse", "HEAD"))
		if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("pending work\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		repository := domain.RunnerRepository{Name: name, Dir: name, WorkBranch: "feature", SyncWorkBranch: true, Ref: "main", CloneURL: "https://github.com/group/repo.git", GitToken: "github-secret"}
		if name == "gitlab-second" {
			repository.CloneURL, repository.GitAuthBaseURL, repository.GitToken = "https://gitlab.example.test/group/repo.git", "https://gitlab.example.test", "requester-secret"
		}
		runGitT(t, dir, "remote", "add", "origin", repository.CloneURL)
		if name == "gitlab-second" {
			runGitT(t, dir, "config", "remote.origin.pushurl", "https://oauth2:other-users-secret@gitlab.example.test/group/wrong.git")
		}
		repositories = append(repositories, repository)
	}
	heads, err := syncTaskWorkBranches(context.Background(), root, &domain.RunnerInvocation{Repositories: repositories}, true, "publish")
	if err == nil || len(heads) != 0 || !strings.Contains(err.Error(), "destination does not match") || strings.Contains(err.Error(), "other-users-secret") {
		t.Fatalf("preflight result: %v %v", heads, err)
	}
	for index, repository := range repositories {
		dir := filepath.Join(root, repository.Dir)
		if got := runGitT(t, dir, "rev-parse", "HEAD"); got != originalHeads[index] {
			t.Fatal("preflight committed changes")
		}
		if got := runGitT(t, dir, "status", "--porcelain"); !strings.Contains(got, "?? change.txt") {
			t.Fatalf("preflight changed working tree: %s", got)
		}
	}
}

func TestRunnerConversationPushUsesRequesterCommitIdentity(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	dir := t.TempDir()
	runGitT(t, dir, "clone", "-q", remote, ".")
	runGitT(t, dir, "checkout", "-qb", "feature")
	runGitT(t, dir, "config", "user.name", "previous-owner")
	runGitT(t, dir, "config", "user.email", "previous-owner@example.test")
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("new work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := &domain.RunnerInvocation{GitUserName: "requester", GitUserEmail: "requester@example.test", Repositories: []domain.RunnerRepository{{CloneURL: remote, Ref: "master", WorkBranch: "feature", SyncWorkBranch: true}}}
	if _, err := syncTaskWorkBranches(context.Background(), dir, inv, true, "requested change"); err != nil {
		t.Fatal(err)
	}
	if got := runGitT(t, dir, "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD"); got != "requester <requester@example.test>|requester <requester@example.test>" {
		t.Fatalf("wrong commit attribution: %s", got)
	}
}
