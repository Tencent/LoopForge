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
	t.Parallel()
	_, err := PrepareClarifyWorkspace(context.Background(), t.TempDir(), domain.Repository{
		ID: "repo", CloneURL: "https://git.example.com/a/b.git",
	}, testRunCredentials(""))
	if err == nil || !strings.Contains(err.Error(), "clone clarify workspace") {
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
	if err := s.PutUser(domain.User{ID: 22, LoginName: "responsible"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGitHTTPCredential(22, "", domain.GitHTTPCredentialSecret{GitHTTPCredential: domain.GitHTTPCredential{Host: "github.com", Name: "GitHub", Platform: domain.GitPlatformGitHub}, Token: "tok_user"}); err != nil {
		t.Fatal(err)
	}
	got := resolveGitHubToken(s, 22)
	if got != "tok_user" {
		t.Fatalf("resolveGitToken = %q, want tok_user", got)
	}
	if got := resolveGitHubToken(s, 999); got != "" {
		t.Fatalf("missing user must not fall back to host env, got %q", got)
	}
}

func TestResolveGitIdentityUsesResponsibleUserLogin(t *testing.T) {
	t.Parallel()
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

func TestResolveGitIdentityForRunMatchesRepositoryAndFallsBack(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 31, LoginName: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGitHTTPCredential(31, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitHub", Platform: domain.GitPlatformGitHub, Host: "github.com"},
		Token:             "tok-gh",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGitHTTPCredential(31, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitLab", Platform: domain.GitPlatformGitLab, Host: "gitlab.example.com"},
		Token:             "tok-gl",
	}); err != nil {
		t.Fatal(err)
	}

	creds := s.GitHTTPCredentialSecrets(context.Background(), 31)
	glRun := domain.Run{
		ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 31},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "gl", CloneURL: "https://gitlab.example.com/group/app.git"}},
	}

	// Without ResolveGitCommitIdentity wired, falls back cleanly to Tier 3.
	offline := &Engine{repo: s}
	if got := offline.resolveGitIdentityForRun(glRun, creds); got.name != "alice" || got.email != "alice@users.noreply.loopforge.invalid" {
		t.Fatalf("offline fallback identity = %+v", got)
	}

	e := &Engine{
		repo: s,
		ResolveGitCommitIdentity: func(_ context.Context, userID int64, cred domain.GitHTTPCredentialSecret) (string, string) {
			if userID != 31 {
				return "", ""
			}
			switch cred.Host {
			case "github.com":
				return "Alice GH", "alice@gh.example.com"
			case "gitlab.example.com":
				return "Alice GL", "alice@gl.example.com"
			default:
				return "", ""
			}
		},
	}

	if got := e.resolveGitIdentityForRun(glRun, creds); got.name != "Alice GL" || got.email != "alice@gl.example.com" {
		t.Fatalf("gitlab repo identity = %+v", got)
	}

	sshRun := domain.Run{
		ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 31},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "ssh-gh", CloneURL: "git@github.com:org/app.git"}},
	}
	if got := e.resolveGitIdentityForRun(sshRun, creds); got.name != "Alice GH" || got.email != "alice@gh.example.com" {
		t.Fatalf("ssh github repo identity = %+v", got)
	}

	// If the matched repo host fails to resolve, falls back to secondary configured credential, then Tier 3.
	if err := s.PutUser(domain.User{ID: 32, LoginName: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGitHTTPCredential(32, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "Other", Platform: domain.GitPlatformOther, Host: "git.other.example"},
		Token:             "tok-other",
	}); err != nil {
		t.Fatal(err)
	}
	bobCreds := s.GitHTTPCredentialSecrets(context.Background(), 32)
	bobRun := domain.Run{
		ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 32},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "other", CloneURL: "https://git.other.example/org/svc.git"}},
	}
	if got := e.resolveGitIdentityForRun(bobRun, bobCreds); got.name != "bob" || got.email != "bob@users.noreply.loopforge.invalid" {
		t.Fatalf("unresolvable platform fallback = %+v", got)
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
	if got := resolveGitHubToken(s, 1); got != "" {
		t.Fatalf("project/repository/env tokens must be ignored, got %q", got)
	}
}

func TestApplyWorkspaceGitAuthRemovesPersistedCredentials(t *testing.T) {
	t.Parallel()
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
