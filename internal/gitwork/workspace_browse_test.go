package gitwork

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoveredBrowserRepositoriesReadOnly(t *testing.T) {
	root := t.TempDir()
	write := func(file, text string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, name := range []string{"one", "nested/inside/two"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		git(dir, "init", "-q")
		write(filepath.Join(dir, "README.md"), "original\n")
		write(filepath.Join(dir, ".env"), "example-tracked-content\n")
		git(dir, "add", ".")
		git(dir, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-qm", "fixture")
		write(filepath.Join(dir, "README.md"), name+"\n")
		write(filepath.Join(dir, ".env"), "tracked-example-change\n")
	}
	if err := os.Symlink(filepath.Join(root, "one"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	discovered := DiscoverBrowserRepositories(ctx, root)
	if len(discovered) != 2 || discovered[0].Label != "nested/inside/two" || discovered[1].Label != "one" {
		t.Fatalf("discovered: %+v", discovered)
	}
	ctx = WithBrowserRepositories(ctx, discovered)
	repository, ok := FindBrowserRepository(ctx, root, nil, "one")
	if !ok || repository.Path != filepath.Join(root, "one") {
		t.Fatalf("named repository: %+v", repository)
	}
	if _, err := BrowserGitOutput(ctx, repository, "status"); !errors.Is(err, ErrBrowserGitUnavailable) {
		t.Fatalf("expected sandbox requirement, got %v", err)
	}
	ctx = WithWorkspaceExecutor(ctx, func(ctx context.Context, dir string, spec WorkspaceCommand) (string, error) {
		if !spec.Offline || !spec.StdoutOnly {
			t.Fatal("browser commands must be offline and return only stdout")
		}
		cmd := exec.CommandContext(ctx, "git", spec.Args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", spec.Args, err)
		}
		return string(out), nil
	})
	for _, repository := range discovered {
		status, err := BrowserGitOutput(ctx, repository, "status", "--porcelain=v1", "-z")
		if err != nil || !strings.Contains(string(status), " M README.md\x00") {
			t.Fatalf("unexpected status: %q, %v", status, err)
		}
		diff, err := BrowserGitOutput(ctx, repository, BrowserDiffArgs(repository, "")...)
		if err != nil || !strings.Contains(string(diff), "b/"+repository.Label+"/README.md") || !strings.Contains(string(diff), "+"+repository.Label) || strings.Contains(string(diff), "tracked-example-change") {
			t.Fatalf("unexpected diff: %q, %v", diff, err)
		}
	}
}
