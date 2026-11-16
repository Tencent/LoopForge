package gitwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckoutsFindsClonesBelowTheWorkspace(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	clone := func(rel, remote string) string {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		git(dir, "init", "-q")
		git(dir, "commit", "-q", "--allow-empty", "-m", "x")
		if remote != "" {
			git(dir, "remote", "add", "origin", remote)
		}
		return git(dir, "rev-parse", "HEAD")
	}
	web := clone("web", "https://github.com/acme/web.git")
	git(filepath.Join(root, "web"), "checkout", "-q", "-b", "fix")
	git(filepath.Join(root, "web"), "commit", "-q", "--allow-empty", "-m", "y")
	fix := git(filepath.Join(root, "web"), "rev-parse", "HEAD")
	nested := clone("src/api", "git@github.com:acme/api.git")
	clone(".cache/hidden", "https://github.com/acme/hidden.git")
	clone("a/b/too-deep", "https://github.com/acme/deep.git")

	got := map[string]Checkout{}
	for _, c := range Checkouts(context.Background(), root) {
		got[c.Dir] = c
	}
	if len(got) != 2 {
		t.Fatalf("checkouts = %+v", got)
	}
	if c := got["web"]; len(c.Remotes) != 1 || c.Remotes[0] != "https://github.com/acme/web.git" || len(c.Heads) != 2 || !slices.Contains(c.Heads, web) || !slices.Contains(c.Heads, fix) {
		t.Fatalf("web = %+v", c)
	}
	if c := got["src/api"]; len(c.Remotes) != 1 || c.Remotes[0] != "git@github.com:acme/api.git" || len(c.Heads) != 1 || c.Heads[0] != nested {
		t.Fatalf("src/api = %+v", c)
	}
}

// A checkout belongs to the sandbox user; scanning it from the host must not
// run the programs its config names, nor follow a .git file out of the
// workspace.
func TestCheckoutsDoesNotRunRepositoryConfiguredPrograms(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "ran")
	script := filepath.Join(outside, "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >> "+marker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	repo := filepath.Join(root, "web")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "x")
	git(repo, "remote", "add", "origin", "https://github.com/acme/web.git")
	hooks := filepath.Join(repo, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"reference-transaction", "post-index-change", "pre-commit"} {
		if err := os.Symlink(script, filepath.Join(hooks, hook)); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{"core.fsmonitor": script, "core.hooksPath": hooks, "core.pager": script, "core.askPass": script, "credential.helper": "!" + script} {
		git(repo, "config", key, value)
	}
	// A .git file pointing at a repository outside the workspace is skipped.
	elsewhere := filepath.Join(outside, "host-repo")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	git(elsewhere, "init", "-q")
	git(elsewhere, "commit", "-q", "--allow-empty", "-m", "host")
	linked := filepath.Join(root, "linked")
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+filepath.Join(elsewhere, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Checkouts(context.Background(), root)
	if len(got) != 1 || got[0].Dir != "web" || len(got[0].Remotes) != 1 || len(got[0].Heads) != 1 {
		t.Fatalf("checkouts = %+v", got)
	}
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("scan ran a repository-configured program: %q", data)
	}
}
