package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestPrepareClarifyWorkspaceRequiresGitToken(t *testing.T) {
	_, err := PrepareClarifyWorkspace(context.Background(), t.TempDir(), domain.Repository{
		ID: "repo", CloneURL: "https://git.example.com/a/b.git",
	}, "")
	if err == nil || !strings.Contains(err.Error(), "需求责任人未配置 Git Token") {
		t.Fatalf("empty token error = %v", err)
	}
}

func TestResolveGitTokenUsesResponsibleUserOnly(t *testing.T) {
	t.Setenv(gitTokenEnv, "tok_env")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 22, LoginName: "responsible", GitToken: "tok_user"}); err != nil {
		t.Fatal(err)
	}
	got := resolveGitToken(s, 22)
	if got != "tok_user" {
		t.Fatalf("resolveGitToken = %q, want tok_user", got)
	}
	if got := resolveGitToken(s, 999); got != "" {
		t.Fatalf("missing user must not fall back to host env, got %q", got)
	}
}

func TestResolveGitIdentityUsesResponsibleUserLogin(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 22, LoginName: " example-admin ", ChineseName: "测试用户"}); err != nil {
		t.Fatal(err)
	}
	got := resolveGitIdentity(s, 22)
	if got.name != "example-admin" || got.email != "example-admin@users.noreply.loopforge.invalid" {
		t.Fatalf("resolveGitIdentity = %q <%s>", got.name, got.email)
	}
	if got := resolveGitIdentity(s, 999); got != (gitIdentity{}) {
		t.Fatalf("missing user identity = %+v", got)
	}
}

func TestProjectAndRepositoryTokensAreIgnored(t *testing.T) {
	t.Setenv(gitTokenEnv, "tok_env")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	repo := domain.Repository{ID: "repo-token", ProjectID: "platform", Name: "skillhub", GongfengProjectID: "g/skillhub", GitToken: "tok_repo"}
	if err := s.PutRepository(repo); err != nil {
		t.Fatal(err)
	}
	if got := resolveGitToken(s, 1); got != "" {
		t.Fatalf("project/repository/env tokens must be ignored, got %q", got)
	}
}

func TestApplyWorkspaceGitAuthRemovesPersistedCredentials(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "master", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitDir := filepath.Join(dir, ".git")
	for _, name := range []string{gitAuthIncludeFile, gitAskPassFile, gitAskPassSecretFile} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte("tok_secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "config", "--local", "include.path", gitAuthIncludeFile).CombinedOutput(); err != nil {
		t.Fatalf("configure legacy include: %v: %s", err, out)
	}
	ctx := withGitToken(context.Background(), "tok_secret")
	if err := applyWorkspaceGitAuth(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "--local", "--get", "include.path").CombinedOutput(); err == nil {
		t.Fatalf("legacy include survived: %s", out)
	}
	for _, name := range []string{gitAuthIncludeFile, gitAskPassFile, gitAskPassSecretFile} {
		if _, err := os.Stat(filepath.Join(gitDir, name)); !os.IsNotExist(err) {
			t.Fatalf("credential file %s survived: %v", name, err)
		}
	}
}
