package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCommandDirPrefersExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "persist-run")
	if err := os.Mkdir(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := CommandDir(ws); got != ws {
		t.Fatalf("gitCommandDir existing = %q, want %q", got, ws)
	}
	missing := filepath.Join(root, "gone")
	if got := CommandDir(missing); got != root {
		t.Fatalf("gitCommandDir missing = %q, want parent %q", got, root)
	}
}

func TestWithEnvValueReplacesPWD(t *testing.T) {
	got := WithEnvValue([]string{"PATH=/bin", "PWD=/old", "LANG=C"}, "PWD", "/ws")
	if strings.Join(got, ",") != "PATH=/bin,PWD=/ws,LANG=C" {
		t.Fatalf("withEnvValue = %v", got)
	}
}

func TestRewriteRemoteForGitToken(t *testing.T) {
	token := "tok_secret"
	got := RewriteRemoteForToken("git@git.example.com:dns-ai/skillhub.git", token)
	if got != "https://private:tok_secret@git.example.com/dns-ai/skillhub.git" {
		t.Fatalf("ssh rewrite = %q", got)
	}
	got = RewriteRemoteForToken("https://git.example.com/dns-ai/skillhub.git", token)
	if got != "https://private:tok_secret@git.example.com/dns-ai/skillhub.git" {
		t.Fatalf("https rewrite = %q", got)
	}
	got = RewriteRemoteForToken("https://oauth2:already@git.example.com/dns-ai/skillhub.git", token)
	if got != "https://private:tok_secret@git.example.com/dns-ai/skillhub.git" {
		t.Fatalf("oauth2 user should be rewritten to private, got %q", got)
	}
	got = RewriteRemoteForToken("https://private:already@git.example.com/dns-ai/skillhub.git", token)
	if got != "https://private:already@git.example.com/dns-ai/skillhub.git" {
		t.Fatalf("existing private user rewrite = %q", got)
	}
	if RewriteRemoteForToken("/tmp/local-repo", token) != "/tmp/local-repo" {
		t.Fatalf("local path should stay unchanged")
	}
	if RewriteRemoteForToken("git@git.example.com:dns-ai/skillhub.git", "") != "git@git.example.com:dns-ai/skillhub.git" {
		t.Fatalf("empty token should keep original remote")
	}
}

func TestRedactGitOutput(t *testing.T) {
	t.Setenv(TokenEnv, "tok_env")
	got := Redact("fatal: https://private:tok_repo@git.example.com/x.git tok_env", "tok_repo")
	if strings.Contains(got, "tok_repo") || strings.Contains(got, "tok_env") {
		t.Fatalf("token leaked in git output: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("expected redaction, got %q", got)
	}
}

func TestGitEnvAddsInsteadOfWhenTokenSet(t *testing.T) {
	t.Setenv(TokenEnv, "")
	env := Env("tok_secret")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=") {
		t.Fatal("expected GIT_CONFIG_COUNT")
	}
	if !strings.Contains(joined, "git@git.example.com:") {
		t.Fatal("expected ssh insteadOf")
	}
	if !strings.Contains(joined, "https://private:tok_secret@git.example.com/") {
		t.Fatal("expected private user insteadOf")
	}
	if !strings.Contains(joined, gitHTTPPassEnv+"=tok_secret") {
		t.Fatal("credential helper password was not provided through the environment")
	}
}

func TestGitTokenFromContextOverridesEnv(t *testing.T) {
	t.Setenv(TokenEnv, "tok_env")
	ctx := WithToken(context.Background(), "tok_repo")
	if got := TokenFrom(ctx); got != "tok_repo" {
		t.Fatalf("gitTokenFrom = %q, want tok_repo", got)
	}
	if os.Getenv(TokenEnv) != "tok_env" {
		t.Fatal("env token should remain set")
	}
}

func TestGitAuthConfigArgs(t *testing.T) {
	if AuthConfigArgs("") != nil {
		t.Fatal("empty token should not add -c")
	}
	args := AuthConfigArgs("tok_secret")
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "credential.helper=") {
		t.Fatalf("missing credential.helper: %q", args)
	}
	if !strings.Contains(joined, "url.https://private:tok_secret@git.example.com/.insteadof=git@git.example.com:") {
		t.Fatalf("missing ssh insteadOf: %q", args)
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && strings.HasPrefix(args[i+1], "credential.helper=!") && strings.Contains(args[i+1], "tok_secret") {
			t.Fatalf("token interpolated into credential helper: %s", args[i+1])
		}
	}
}

func TestCredentialHelperLeavesTokenOutOfShell(t *testing.T) {
	token := "tok\";touch /tmp/pwned;#"
	for _, arg := range AuthConfigArgs(token) {
		if strings.HasPrefix(arg, "credential.helper=!") && strings.Contains(arg, token) {
			t.Fatalf("credential helper contains the token: %s", arg)
		}
	}
	env := strings.Join(Env(token), "\n")
	if !strings.Contains(env, gitHTTPPassEnv+"="+token) {
		t.Fatal("helper cannot read the token from the environment")
	}
}

func TestCIncludesOutputOnFailure(t *testing.T) {
	_, err := C(context.Background(), t.TempDir(), "status")
	if err == nil {
		t.Fatal("expected git error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exit status") {
		t.Fatalf("missing exit status: %v", err)
	}
	if !strings.Contains(msg, "fatal") && !strings.Contains(msg, "not a git repository") {
		t.Fatalf("git stderr discarded: %v", err)
	}
}

func TestGitDiscoveryStopsBeforeParentRepository(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	if _, err := C(ctx, parent, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := C(ctx, workspace, "rev-parse", "--show-toplevel"); err == nil {
		t.Fatal("workspace discovered the parent repository")
	}
	if _, err := C(ctx, workspace, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if got, err := C(ctx, workspace, "rev-parse", "--show-toplevel"); err != nil || got != workspace {
		t.Fatalf("own checkout unavailable: %s %v", got, err)
	}
}
