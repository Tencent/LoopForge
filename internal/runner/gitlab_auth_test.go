package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
)

func TestRunnerGitLabCredentialSelection(t *testing.T) {
	repository := domain.RunnerRepository{CloneURL: "https://gitlab.example.test/team/repo.git", GitAuthBaseURL: "https://gitlab.example.test", GitToken: "runner-test-secret"}
	for _, inv := range []*domain.RunnerInvocation{
		{Repository: &repository}, {Repositories: []domain.RunnerRepository{repository}},
		{Repository: &repository, Repositories: []domain.RunnerRepository{repository}},
	} {
		base, token, err := invocationGitLabCredential(inv)
		if err != nil || base != repository.GitAuthBaseURL || token != repository.GitToken {
			t.Fatalf("selection: %q %q %v", base, token, err)
		}
		if invocationGitToken(inv) != "" {
			t.Fatal("GitLab token became a generic Git credential")
		}
		env := map[string]string{}
		if err := bindInvocationGitLab(env, inv); err != nil || env["AGENTFLOW_GITLAB_TOKEN"] != repository.GitToken {
			t.Fatalf("binding: %v", err)
		}
		if err := runtime.BindGitLabEnvironment(map[string]string{}, base, token); err != nil {
			t.Fatal(err)
		}
	}
	conflict := repository
	conflict.GitToken = "other-secret"
	if _, _, err := invocationGitLabCredential(&domain.RunnerInvocation{Repository: &conflict, Repositories: []domain.RunnerRepository{repository}}); err == nil {
		t.Fatal("conflicting legacy credential accepted")
	}
	mismatch := repository
	mismatch.CloneURL = "https://elsewhere.example.test/team/repo.git"
	if _, _, err := invocationGitLabCredential(&domain.RunnerInvocation{Repository: &mismatch}); err == nil {
		t.Fatal("foreign clone URL accepted")
	}
	credential, ok := gitwork.HostCredentialFrom(gitContextForRunnerRepository(context.Background(), &mismatch))
	if !ok || credential.Token != "" {
		t.Fatal("foreign clone URL received a credential")
	}
}

func TestRunnerGitLabCloneAndNewBranchUseHostAuth(t *testing.T) {
	bin := t.TempDir()
	// This fake git rejects missing host auth or credentials embedded in argv.
	script := `#!/bin/sh
set -eu
test "${AGENTFLOW_GIT_HTTP_PASSWORD:-}" = runner-test-secret || exit 61
test "${AGENTFLOW_GIT_HTTP_USER:-}" = oauth2 || exit 62
case "$*" in *runner-test-secret*) exit 63 ;; esac
case "$*" in *credential.https://gitlab.example.test.helper=*) ;; *) exit 64 ;; esac
case "$*" in
  *"clone --progress --branch main -- https://gitlab.example.test/team/repo.git ."*)
    printf 'clone rejected runner-test-secret\n' >&2; exit 65 ;;
  *"ls-remote --heads origin refs/heads/new-branch"*) exit 0 ;;
  *) exit 66 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repository := &domain.RunnerRepository{CloneURL: "https://gitlab.example.test/team/repo.git", GitAuthBaseURL: "https://gitlab.example.test", GitToken: "runner-test-secret", Ref: "main", WorkBranch: "new-branch"}
	err := cloneRepository(context.Background(), t.TempDir(), repository)
	if err == nil || !strings.Contains(err.Error(), "clone rejected ***") || strings.Contains(err.Error(), repository.GitToken) {
		t.Fatalf("clone error: %v", err)
	}
	// A successful ls-remote proceeds to the local checkout. The fake git
	// intentionally rejects that local command, proving the remote check passed.
	err = createNewRepositoryBranch(context.Background(), t.TempDir(), repository)
	if err == nil || !strings.Contains(err.Error(), "git create work branch") {
		t.Fatalf("new branch did not pass authenticated remote check: %v", err)
	}
}
