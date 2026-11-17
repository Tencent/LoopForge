package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func TestGitPreparationFallbackVerifiesTheFrozenCheckout(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "code")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := gitwork.WithHTTPCredentials(context.Background(), map[string]gitwork.HTTPCredential{})
	git := func(args ...string) {
		t.Helper()
		if out, err := gitwork.Run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	git("commit", "--allow-empty", "-m", "baseline")
	git("remote", "add", "origin", "https://unfamiliar.example.org/team/code")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("checkout", "-b", "feature/frozen")
	repositories := []domain.RepositoryContext{{RepositoryID: "r", Name: "code", WorkspaceDir: "code", CloneURL: "https://unfamiliar.example.org/team/code.git", BaseBranch: "main", WorkBranch: "feature/frozen"}}
	verify := func(wantError bool) {
		t.Helper()
		err := VerifyGitPreparation(ctx, root, repositories)
		if (err != nil) != wantError {
			t.Fatalf("verification error = %v, want error %v", err, wantError)
		}
	}
	verify(false)
	git("checkout", "main")
	verify(true)
	git("checkout", "feature/frozen")
	git("remote", "set-url", "origin", "https://unfamiliar.example.org/team/other")
	verify(true)
	git("remote", "set-url", "origin", repositories[0].CloneURL)
	repositories[0].RequireExistingWorkBranch = true
	verify(true)
	git("update-ref", "refs/remotes/origin/feature/frozen", "HEAD")
	verify(false)
	repositories[0].WorkspaceDir = "code/missing"
	verify(true)
}

func TestGitPreparationFallbackPromptCarriesTheFailureAndFrozenRequirements(t *testing.T) {
	text := GitPreparationFallbackPrompt([]domain.RepositoryContext{{CloneURL: "https://unfamiliar.example.org/team/code", WorkspaceDir: "code", BaseBranch: "release", WorkBranch: "feature/frozen", RequireExistingWorkBranch: true}}, "authentication mechanism is unsupported")
	for _, required := range []string{"authentication mechanism is unsupported", "https://unfamiliar.example.org/team/code", "release", "feature/frozen", "git credential fill", "禁止打印", "以 fail 结束", "必须继续已有远端分支: true"} {
		if !strings.Contains(text, required) {
			t.Fatalf("fallback omitted requirement %q", required)
		}
	}
}
