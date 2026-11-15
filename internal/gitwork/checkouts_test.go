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
