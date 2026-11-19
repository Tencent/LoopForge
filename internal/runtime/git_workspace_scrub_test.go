package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tokens an agent wrote by hand into HOME or a checkout's config are gone
// after the turn; SSH remotes, plain HTTPS remotes and other settings stay.
func TestScrubWorkspaceGitSecrets(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	home, workspace, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".git-credentials"), "https://x:tok-cred@github.com\n")
	write(filepath.Join(home, ".netrc"), "machine github.com login x password tok-netrc\n")
	write(filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  oauth_token: tok-gh\n")
	write(filepath.Join(home, ".gitconfig"), "[user]\n\tname = alice\n[url \"https://x:tok-insteadof@github.com/\"]\n\tinsteadOf = https://github.com/\n[http \"https://github.com/\"]\n\textraHeader = Authorization: Basic dG9rLWhlYWRlcg==\n")
	// A symlinked config is left alone so nothing outside is read or written.
	write(filepath.Join(outside, "config"), "[remote \"o\"]\n\turl = https://x:tok-outside@h.example/r.git\n")
	if err := os.MkdirAll(filepath.Join(home, ".config", "git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "config"), filepath.Join(home, ".config", "git", "config")); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(workspace, "app")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", "https://oauth2:tok-remote@gitlab.example.org/g/app.git"},
		{"remote", "set-url", "--push", "origin", "https://tok-push@gitlab.example.org/g/app.git"},
		{"remote", "add", "ssh", "ssh://git@gitlab.example.org/g/app.git"},
		{"remote", "add", "plain", "https://github.com/o/app.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	cleaned := ScrubWorkspaceGitSecrets(context.Background(), []string{home}, workspace)
	report := strings.Join(cleaned, "\n")
	for _, want := range []string{"HOME/.git-credentials", "HOME/.netrc", "HOME/.config/gh", "HOME/.gitconfig url.<redacted>", "HOME/.gitconfig http.https://github.com/.extraheader", "app/.git/config remote.origin.url", "app/.git/config remote.origin.pushurl"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "tok-") {
		t.Fatalf("report holds a token:\n%s", report)
	}
	for _, name := range []string{".git-credentials", ".netrc", ".config/gh"} {
		if _, err := os.Lstat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", name, err)
		}
	}
	gitconfig, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if strings.Contains(string(gitconfig), "tok-") || strings.Contains(string(gitconfig), "dG9r") || !strings.Contains(string(gitconfig), "alice") {
		t.Fatalf("HOME .gitconfig after scrub:\n%s", gitconfig)
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "config")); !strings.Contains(string(data), "tok-outside") {
		t.Fatal("a symlinked config outside HOME was rewritten")
	}
	config, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	for _, want := range []string{"https://gitlab.example.org/g/app.git", "ssh://git@gitlab.example.org/g/app.git", "https://github.com/o/app.git"} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("checkout config lacks %q:\n%s", want, config)
		}
	}
	if strings.Contains(string(config), "tok-") {
		t.Fatalf("checkout config still holds a token:\n%s", config)
	}
	if again := ScrubWorkspaceGitSecrets(context.Background(), []string{home}, workspace); len(again) != 0 {
		t.Fatalf("second scrub found %v", again)
	}
}
