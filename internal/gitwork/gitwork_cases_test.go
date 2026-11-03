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

func cvSRunGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestGitIdentityAndStageUntracked(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	_, _ = exec.Command("git", "-C", dir, "config", "user.name", "").CombinedOutput()
	_, _ = exec.Command("git", "-C", dir, "config", "user.email", "").CombinedOutput()
	EnsureIdentity(context.Background(), dir)
	if out, err := exec.Command("git", "-C", dir, "config", "user.name").Output(); err != nil || strings.TrimSpace(string(out)) != "AgentFlow" {
		t.Fatalf("name = %q, %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("package keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StageUntracked(dir); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "diff", "--name-only").Output()
	if !strings.Contains(string(out), "keep.go") || strings.Contains(string(out), ".env") {
		t.Fatalf("staged names = %q", out)
	}
	if err := CommitStaged(context.Background(), dir, "empty"); err != nil {
		t.Fatal(err)
	}
}

func TestGitAuthAndPatchHelpers(t *testing.T) {
	for _, tc := range []struct {
		raw, user, token string
	}{
		{"", HTTPUser, ""}, {"oauth2: tok ", OAuthHTTPUser, "tok"}, {"private:x", HTTPUser, "x"}, {"raw", HTTPUser, "raw"},
	} {
		user, token := SplitToken(tc.raw)
		if user != tc.user || token != tc.token {
			t.Fatalf("%q = %q,%q", tc.raw, user, token)
		}
	}
	ctx := WithToken(context.Background(), " x ")
	if TokenFrom(ctx) != "x" || TokenFrom(nil) != "" {
		t.Fatal("context token")
	}
	if len(AuthConfigArgs("")) != 0 || len(AuthConfigArgs("x")) == 0 {
		t.Fatal("auth args")
	}
	for _, remote := range []string{"", "ssh://other/x", "git@bad", "https://user@host/x"} {
		if RewriteRemoteForToken(remote, "token") != remote {
			t.Fatalf("remote %q changed", remote)
		}
	}
	if got := RewriteRemoteForToken("git@git.example.com:group/repo.git", "oauth2:tok"); !strings.Contains(got, "oauth2:tok@") {
		t.Fatalf("remote = %q", got)
	}
	env := WithEnvValue([]string{"A=1", "A=2", "B=3"}, "A", "x")
	if strings.Count(strings.Join(env, "\n"), "A=") != 1 {
		t.Fatalf("env = %#v", env)
	}
	files := ParseNameStatusNumstat([]byte("R100\told.go\tnew.go\nM\tbin\nbad\n"), []byte("2\t3\tnew.go\n-\t-\tbin\n"))
	if len(files) != 2 || files[0].Binary != true || files[1].RenameFrom != "old.go" {
		t.Fatalf("files = %#v", files)
	}
	summary := RenderPatchInventory(files)
	if !strings.Contains(summary, "binary") || !strings.Contains(summary, "old.go -> new.go") {
		t.Fatalf("summary = %s", summary)
	}
	if _, ok := revListCount("", "", ""); ok {
		t.Fatal("empty revisions accepted")
	}
	if SandboxEnv("")["GIT_TERMINAL_PROMPT"] != "0" || SandboxEnv("oauth2:secret")[gitHTTPPassEnv] != "secret" {
		t.Fatal("sandbox environment")
	}
	t.Setenv("GIT_CONFIG_COUNT", "99")
	t.Setenv(gitHTTPPassEnv, "leaked")
	clean := strings.Join(Env("private:token"), "\n")
	if strings.Contains(clean, "GIT_CONFIG_COUNT=99") || strings.Contains(clean, "leaked") || !strings.Contains(clean, gitHTTPPassEnv+"=token") {
		t.Fatalf("clean env = %s", clean)
	}
	dir := t.TempDir()
	if CommandDir(dir) != dir || CommandDir(filepath.Join(dir, "missing")) != dir {
		t.Fatal("command directory")
	}
	for _, message := range []string{"non-fast-forward", "fetch first", "[rejected]"} {
		if !IsNonFastForward(errors.New(message)) {
			t.Fatal(message)
		}
	}
}

func TestGitPathClassifiers(t *testing.T) {
	for _, path := range []string{".env", ".env.prod", ".npmrc"} {
		if !IsCredentialFile(path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{".env.example", ".env.sample", "normal"} {
		if IsCredentialFile(path) {
			t.Fatal(path)
		}
	}
	if got := UnquoteLsFilesPath(`"hello\040world"`); got != "hello world" {
		t.Fatalf("unquote = %q", got)
	}
	if labelled("msg", "label") != "msg (label)" || labelled("msg", "") != "msg" {
		t.Fatal("label")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if files := UnmergedFiles(context.Background(), dir); len(files) != 0 {
		t.Fatalf("unmerged = %#v", files)
	}
	if onWorkBranch(context.Background(), dir, "") || onWorkBranch(context.Background(), t.TempDir(), "main") {
		t.Fatal("invalid workspace treated as work branch")
	}
	if _, err := fetchExistingWorkBranch(context.Background(), dir, ""); err == nil {
		t.Fatal("empty existing branch accepted")
	}
	if _, err := PushExistingWorkBranch(context.Background(), dir, "", "x"); err == nil {
		t.Fatal("strict empty branch accepted")
	}
	if sha, err := PushWorkBranch(context.Background(), dir, "", "x"); err != nil || sha != "" {
		t.Fatalf("non-work branch push = %q, %v", sha, err)
	}
	if sha, err := PullWorkBranch(context.Background(), dir, "main"); err != nil || sha != "" {
		t.Fatalf("non-work branch pull = %q, %v", sha, err)
	}
	if err := StageChanges(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing workspace staged")
	}
	if got, err := MergeLatestIntegration(context.Background(), filepath.Join(dir, "missing"), "work", "main", "x"); err == nil || got != nil {
		t.Fatalf("invalid integration merge = %#v, %v", got, err)
	}
	emptyBaseline := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(BaselinePath(emptyBaseline)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BaselinePath(emptyBaseline), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if patch, err := PatchInventory(emptyBaseline); err != nil || patch != nil {
		t.Fatalf("empty baseline patch = %q, %v", patch, err)
	}
	if err := os.Remove(BaselinePath(emptyBaseline)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(BaselinePath(emptyBaseline), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PatchInventory(emptyBaseline); err == nil {
		t.Fatal("directory baseline accepted")
	}
	if err := RecordBaseline(filepath.Join(emptyBaseline, "missing"), ""); err == nil {
		t.Fatal("missing baseline workspace accepted")
	}
}

func TestMergeLatestHappyPaths(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	work := filepath.Join(root, "work")
	cvSRunGit(t, "init", "--bare", "-q", remote)
	cvSRunGit(t, "init", "-q", seed)
	cvSRunGit(t, "-C", seed, "config", "user.name", "Test")
	cvSRunGit(t, "-C", seed, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cvSRunGit(t, "-C", seed, "add", "file.txt")
	cvSRunGit(t, "-C", seed, "commit", "-q", "-m", "base")
	cvSRunGit(t, "-C", seed, "branch", "-M", "main")
	cvSRunGit(t, "-C", seed, "remote", "add", "origin", remote)
	cvSRunGit(t, "-C", seed, "push", "-q", "-u", "origin", "main")
	cvSRunGit(t, "clone", "-q", "--branch", "main", remote, work)
	cvSRunGit(t, "-C", work, "checkout", "-q", "-b", "work")
	EnsureIdentity(context.Background(), work)
	if conflicts, err := MergeLatestRemoteBranch(context.Background(), work, ""); err != nil || len(conflicts) != 0 {
		t.Fatalf("empty remote merge = %#v, %v", conflicts, err)
	}
	if conflicts, err := MergeLatestRemoteBranch(context.Background(), work, "main"); err != nil || len(conflicts) != 0 {
		t.Fatalf("remote merge = %#v, %v", conflicts, err)
	}
	if conflicts, err := MergeLatestIntegration(context.Background(), work, "work", "main", "test"); err != nil || len(conflicts) != 0 {
		t.Fatalf("integration merge = %#v, %v", conflicts, err)
	}
	if conflicts, err := FinishMergeAfterExcludedConflicts(context.Background(), work, "none"); err != nil || len(conflicts) != 0 {
		t.Fatalf("finish no merge = %#v, %v", conflicts, err)
	}
	if err := os.WriteFile(filepath.Join(work, "pushed.txt"), []byte("pushed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sha, err := PushWorkBranch(context.Background(), work, "work", "push work"); err != nil || sha == "" {
		t.Fatalf("push work = %q, %v", sha, err)
	}
	if sha, err := PushExistingWorkBranch(context.Background(), work, "work", "no changes"); err != nil || sha != "" {
		t.Fatalf("push existing unchanged = %q, %v", sha, err)
	}
	if sha, err := PullExistingWorkBranch(context.Background(), work, "work"); err != nil || sha != "" {
		t.Fatalf("pull existing unchanged = %q, %v", sha, err)
	}
	if _, err := PullExistingWorkBranch(context.Background(), work, "missing"); err == nil {
		t.Fatal("missing existing work branch accepted")
	}
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte("main update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cvSRunGit(t, "-C", seed, "add", "file.txt")
	cvSRunGit(t, "-C", seed, "commit", "-q", "-m", "main update")
	cvSRunGit(t, "-C", seed, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("work update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cvSRunGit(t, "-C", work, "add", "file.txt")
	cvSRunGit(t, "-C", work, "commit", "-q", "-m", "work update")
	conflicts, err := MergeLatestRemoteBranch(context.Background(), work, "main")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "file.txt" {
		t.Fatalf("conflict merge = %#v, %v", conflicts, err)
	}
}
