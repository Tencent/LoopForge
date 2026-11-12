package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func advanceCheckoutRemote(t *testing.T, remote string) string {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed")
	runGitT(t, filepath.Dir(seed), "clone", "-q", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "new-baseline.txt"), []byte("new baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "add", "new-baseline.txt")
	runGitT(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "advance baseline")
	runGitT(t, seed, "push", "-q", "origin", "master")
	return runGitT(t, seed, "rev-parse", "HEAD")
}

func TestCheckoutRepositoryRecoversInterruptedClone(t *testing.T) {
	remote, workspace := bareRemoteWithBaseline(t), t.TempDir()
	runGitT(t, workspace, "init", "-q", "-b", "master")
	pack := filepath.Join(workspace, ".git", "objects", "pack", "tmp_pack_interrupted")
	if err := os.WriteFile(pack, []byte("unfinished pack"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkoutRepository(context.Background(), workspace, &domain.RunnerRepository{CloneURL: remote, Ref: "master"}); err != nil {
		t.Fatal(err)
	}
	runGitT(t, workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if raw, err := os.ReadFile(filepath.Join(workspace, "README.md")); err != nil || string(raw) != "baseline\n" {
		t.Fatalf("checkout = %q, %v", raw, err)
	}
	if _, err := os.Stat(pack); !os.IsNotExist(err) {
		t.Fatalf("interrupted pack retained: %v", err)
	}
}

func TestCheckoutRepositoryPreservesDamagedCheckoutWithLocalWork(t *testing.T) {
	for _, kind := range []string{"file", "index", "refs", "baseline", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			runGitT(t, workspace, "init", "-q", "-b", "master")
			marker := filepath.Join(workspace, ".git", "keep")
			if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file", "index":
				file := filepath.Join(workspace, "local.txt")
				if err := os.WriteFile(file, []byte("local work"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "index" {
					runGitT(t, workspace, "add", "local.txt")
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
				}
			case "refs":
				remote := bareRemoteWithBaseline(t)
				runGitT(t, workspace, "fetch", "-q", remote, "master:refs/heads/saved")
			case "baseline":
				if err := os.WriteFile(gitwork.BaselinePath(workspace), []byte("frozen"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "git")
				if err := os.Rename(filepath.Join(workspace, ".git"), outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(workspace, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkoutRepository(context.Background(), workspace, &domain.RunnerRepository{CloneURL: "unused", Ref: "master"}); err == nil {
				t.Fatal("damaged checkout with local work was overwritten")
			}
			if raw, err := os.ReadFile(marker); err != nil || string(raw) != "keep" {
				t.Fatalf("original .git changed: %q %v", raw, err)
			}
		})
	}
}

func TestPreparationRefreshesCleanCheckoutAndBaseline(t *testing.T) {
	remote, workspace := bareRemoteWithBaseline(t), t.TempDir()
	inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: remote, Ref: "master", WorkBranch: "feature/retry"}}}
	if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(workspace)
	oldHead := runGitT(t, workspace, "rev-parse", "HEAD")
	want := advanceCheckoutRemote(t, remote)
	// Ordinary nodes keep the frozen checkout, even when its tree is clean.
	if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local"); err != nil {
		t.Fatal(err)
	}
	if got := runGitT(t, workspace, "rev-parse", "HEAD"); got != oldHead {
		t.Fatal("ordinary node changed the run baseline")
	}
	if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local", true); err != nil {
		t.Fatal(err)
	}
	if got := runGitT(t, workspace, "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD = %s, want %s", got, want)
	}
	if raw, err := os.ReadFile(gitwork.BaselinePath(workspace)); err != nil || strings.TrimSpace(string(raw)) != want {
		t.Fatalf("baseline = %q, %v", raw, err)
	}
	after, _ := os.Stat(workspace)
	if !os.SameFile(before, after) {
		t.Fatal("refresh replaced the Docker bind-mount directory inode")
	}
}

func TestRepositoryCanRefreshRejectsSymlinkEscape(t *testing.T) {
	for _, kind := range []string{"git-absolute", "git-relative", "baseline"} {
		t.Run(kind, func(t *testing.T) {
			remote, workspace := bareRemoteWithBaseline(t), t.TempDir()
			inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: remote, Ref: "master"}}}
			if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workspace, ".git")
			outside := filepath.Join(t.TempDir(), "git")
			if kind == "baseline" {
				path = gitwork.BaselinePath(workspace)
				outside = filepath.Join(t.TempDir(), "baseline")
			}
			if err := os.Rename(path, outside); err != nil {
				t.Fatal(err)
			}
			target := outside
			if kind == "git-relative" {
				var err error
				target, err = filepath.Rel(filepath.Dir(path), outside)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if refresh, err := repositoryCanRefresh(context.Background(), workspace, &inv.Repositories[0]); refresh || err == nil {
				t.Fatalf("symlink escape accepted: refresh=%t, err=%v", refresh, err)
			}
		})
	}
}

func TestRepositoryCanRefreshWithoutBaseline(t *testing.T) {
	for _, ref := range []string{"master", ""} {
		t.Run("ref="+ref, func(t *testing.T) {
			remote, workspace := bareRemoteWithBaseline(t), t.TempDir()
			repository := &domain.RunnerRepository{CloneURL: remote, Ref: "master"}
			if err := checkoutRepository(context.Background(), workspace, repository); err != nil {
				t.Fatal(err)
			}
			repository.Ref = ref
			refresh, err := repositoryCanRefresh(context.Background(), workspace, repository)
			if err != nil || refresh != (ref != "") {
				t.Fatalf("missing baseline: refresh=%t, err=%v", refresh, err)
			}
		})
	}
}

func TestPreparationRefreshPreservesLocalChangesAndCommits(t *testing.T) {
	for _, kind := range []string{"tracked", "untracked", "ignored", "commit", "other-branch", "stash"} {
		t.Run(kind, func(t *testing.T) {
			remote, workspace := bareRemoteWithBaseline(t), t.TempDir()
			inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: remote, Ref: "master", WorkBranch: "feature/keep"}}}
			if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local"); err != nil {
				t.Fatal(err)
			}
			file := "local.txt"
			if kind == "tracked" || kind == "stash" {
				file = "README.md"
			}
			if err := os.WriteFile(filepath.Join(workspace, file), []byte("local work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "ignored":
				if err := os.WriteFile(filepath.Join(workspace, ".git", "info", "exclude"), []byte("local.txt\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "commit", "other-branch":
				runGitT(t, workspace, "add", file)
				runGitT(t, workspace, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "local work")
				if kind == "other-branch" {
					runGitT(t, workspace, "checkout", "-q", "master")
				}
			case "stash":
				runGitT(t, workspace, "stash", "push", "-qm", "keep stash")
			}
			head := runGitT(t, workspace, "rev-parse", "HEAD")
			refs := runGitT(t, workspace, "show-ref")
			status := runGitT(t, workspace, "status", "--porcelain=v1", "--ignored=matching")
			advanceCheckoutRemote(t, remote)
			if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local", true); err != nil {
				t.Fatal(err)
			}
			if got := runGitT(t, workspace, "rev-parse", "HEAD"); got != head {
				t.Fatal("refresh discarded local commits")
			}
			if got := runGitT(t, workspace, "show-ref"); got != refs {
				t.Fatal("refresh changed local branches or stash")
			}
			if got := runGitT(t, workspace, "status", "--porcelain=v1", "--ignored=matching"); got != status {
				t.Fatalf("local changes lost: %q -> %q", status, got)
			}
		})
	}
}

func TestPreparationRefreshKeepsCheckoutWhenCloneFails(t *testing.T) {
	remote, workspace := bareRemoteWithBaseline(t), t.TempDir()
	inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: remote, Ref: "master"}}}
	if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local"); err != nil {
		t.Fatal(err)
	}
	head := runGitT(t, workspace, "rev-parse", "HEAD")
	inv.Repositories[0].CloneURL = filepath.Join(t.TempDir(), "missing-remote.git")
	if err := checkoutInvocationRepositories(context.Background(), workspace, inv, "local", true); err == nil {
		t.Fatal("missing remote unexpectedly cloned")
	}
	if got := runGitT(t, workspace, "rev-parse", "HEAD"); got != head {
		t.Fatal("failed refresh changed original checkout")
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledCloneStopsGitChildren(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "orphaned-child")
	script := "#!/bin/sh\n(sleep 0.4; printf orphan > \"$CHECKOUT_CHILD_MARKER\") &\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CHECKOUT_CHILD_MARKER", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := cloneRepository(ctx, t.TempDir(), &domain.RunnerRepository{CloneURL: "test-remote", Ref: "master"}); err == nil {
		t.Fatal("canceled clone succeeded")
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("canceled clone left a Git child running: %v", err)
	}
}

func TestCheckoutRepositoryRejectsCommandRunningCloneURLs(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	for _, cloneURL := range []string{"ext::sh -c touch% " + marker, "--upload-pack=touch " + marker} {
		err := checkoutRepository(context.Background(), t.TempDir(), &domain.RunnerRepository{CloneURL: cloneURL, Ref: "master"})
		if err == nil {
			t.Fatalf("checkoutRepository(%q) succeeded", cloneURL)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatalf("checkoutRepository(%q) executed a command", cloneURL)
		}
	}
}

func TestCloneFailureNeverEchoesCloneURLCredentials(t *testing.T) {
	// Port 1 on loopback refuses at once; git echoes the URL in its error.
	cloneURL := "http://user:s3cret-token@127.0.0.1:1/acme/app.git"
	err := cloneRepository(context.Background(), t.TempDir(), &domain.RunnerRepository{CloneURL: cloneURL, Ref: "master"})
	if err == nil {
		t.Fatal("clone of an unreachable remote succeeded")
	}
	if strings.Contains(err.Error(), "s3cret-token") || !strings.Contains(err.Error(), "127.0.0.1:1/acme/app.git") {
		t.Fatalf("clone error leaks or hides the remote: %v", err)
	}
}
