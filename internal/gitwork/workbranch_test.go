package gitwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageChangesEmptyAndExcludedCheckouts(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		name := "empty"
		if tracked {
			name = "only-excluded-tracked-files"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			ctx := context.Background()
			if _, err := C(ctx, workspace, "init", "-q"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workspace, "config", "mcporter.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tracked {
				if _, err := C(ctx, workspace, "add", "config/mcporter.json"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := C(ctx, workspace, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			staged, err := C(ctx, workspace, "diff", "--cached", "--name-only")
			if err != nil || strings.TrimSpace(staged) != "" {
				t.Fatalf("excluded content was staged: %q, %v", staged, err)
			}
			// A new source file must still be staged in either checkout.
			if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			// Preserve tracked updates and deletions, including the last source.
			if _, err := C(ctx, workspace, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "source"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("updated"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			if _, err := C(ctx, workspace, "diff", "--cached", "--exit-code"); err == nil {
				t.Fatal("source update was not staged")
			}
			if err := os.Remove(filepath.Join(workspace, "source.txt")); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			deleted, err := C(ctx, workspace, "diff", "--cached", "--name-status")
			if err != nil || strings.TrimSpace(deleted) != "D\tsource.txt" {
				t.Fatalf("source deletion was not staged correctly: %q, %v", deleted, err)
			}
		})
	}
}

func workBranchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func workBranchFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source, remote, checkout := filepath.Join(root, "source"), filepath.Join(root, "remote.git"), filepath.Join(root, "checkout")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, source, "init", "-q", "-b", "main")
	workBranchGit(t, source, "config", "user.name", "Test")
	workBranchGit(t, source, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, source, "add", "README.md")
	workBranchGit(t, source, "commit", "-qm", "baseline")
	workBranchGit(t, source, "branch", "feature/existing")
	workBranchGit(t, root, "clone", "-q", "--bare", source, remote)
	workBranchGit(t, root, "clone", "-q", remote, checkout)
	workBranchGit(t, checkout, "checkout", "-q", "feature/existing")
	workBranchGit(t, checkout, "config", "user.name", "Test")
	workBranchGit(t, checkout, "config", "user.email", "test@example.com")
	return remote, checkout
}

func TestExistingWorkBranchPushAndPull(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("continued\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := PushExistingWorkBranch(context.Background(), checkout, "feature/existing", "continue existing work")
	if err != nil || head == "" {
		t.Fatalf("push: head=%q err=%v", head, err)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing"); got != head {
		t.Fatalf("remote head=%s want %s", got, head)
	}
	if branches := workBranchGit(t, remote, "for-each-ref", "--format=%(refname)", "refs/heads/"); strings.Count(branches, "\n") != 1 {
		t.Fatalf("strict continuation created another branch: %s", branches)
	}
	if got, err := PullExistingWorkBranch(context.Background(), checkout, "feature/existing"); err != nil || got != "" {
		t.Fatalf("unchanged pull: head=%q err=%v", got, err)
	}
}

func TestExistingWorkBranchNeverRecreatesMissingRemote(t *testing.T) {
	for _, tagOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "same-named tag"}[tagOnly], func(t *testing.T) {
			remote, checkout := workBranchFixture(t)
			sha := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing")
			workBranchGit(t, remote, "update-ref", "-d", "refs/heads/feature/existing")
			if tagOnly {
				workBranchGit(t, remote, "update-ref", "refs/tags/feature/existing", sha)
			}
			if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("local work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := PullExistingWorkBranch(context.Background(), checkout, "feature/existing"); err == nil {
				t.Fatal("strict pull accepted a missing remote head")
			}
			if _, err := PushExistingWorkBranch(context.Background(), checkout, "feature/existing", "must fail"); err == nil {
				t.Fatal("strict push accepted a missing remote head")
			}
			if _, ok := RevParse(context.Background(), remote, "refs/heads/feature/existing"); ok {
				t.Fatal("strict synchronization recreated the deleted remote branch")
			}
			if _, err := os.Stat(filepath.Join(checkout, "feature.txt")); err != nil {
				t.Fatal("failed strict synchronization lost local work")
			}
		})
	}
}

func TestExistingWorkBranchRejectsNonFastForward(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	peer := filepath.Join(t.TempDir(), "peer")
	workBranchGit(t, filepath.Dir(peer), "clone", "-q", "--branch", "feature/existing", remote, peer)
	workBranchGit(t, peer, "config", "user.name", "Peer")
	workBranchGit(t, peer, "config", "user.email", "peer@example.com")
	if err := os.WriteFile(filepath.Join(peer, "peer.txt"), []byte("peer work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, peer, "add", "peer.txt")
	workBranchGit(t, peer, "commit", "-qm", "peer work")
	workBranchGit(t, peer, "push", "-q", "origin", "feature/existing")
	remoteHead := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing")
	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PushExistingWorkBranch(context.Background(), checkout, "feature/existing", "local work"); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("expected non-fast-forward rejection, got %v", err)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing"); got != remoteHead {
		t.Fatal("strict push overwrote peer history")
	}
	if _, err := PullExistingWorkBranch(context.Background(), checkout, "feature/existing"); err != nil {
		t.Fatalf("pull should rebase independent local work: %v", err)
	}
	if _, err := PushExistingWorkBranch(context.Background(), checkout, "feature/existing", "after pull"); err != nil {
		t.Fatalf("rebased work should fast-forward: %v", err)
	}
}

func TestExistingWorkBranchLeaseRejectsConcurrentDeletion(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Git invokes pre-push after discovering the remote. Removing the ref
	// here simulates a deletion between the strict preflight and update.
	hook := "#!/bin/sh\ngit --git-dir='" + strings.ReplaceAll(remote, "'", "'\\''") + "' update-ref -d refs/heads/feature/existing\n"
	if err := os.WriteFile(filepath.Join(checkout, ".git", "hooks", "pre-push"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PushExistingWorkBranch(context.Background(), checkout, "feature/existing", "local work"); err == nil {
		t.Fatal("strict push accepted a remote deleted during the push")
	}
	if _, ok := RevParse(context.Background(), remote, "refs/heads/feature/existing"); ok {
		t.Fatal("strict push recreated a concurrently deleted remote branch")
	}
}

func TestDefaultWorkBranchMayStillBeCreated(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	workBranchGit(t, remote, "update-ref", "-d", "refs/heads/feature/existing")
	if _, err := PullWorkBranch(context.Background(), checkout, "feature/existing"); err != nil {
		t.Fatalf("default pull should tolerate a not-yet-published branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "generated.txt"), []byte("new issue work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PushWorkBranch(context.Background(), checkout, "feature/existing", "publish generated branch"); err != nil {
		t.Fatalf("default generated work branch should publish: %v", err)
	}
	if _, ok := RevParse(context.Background(), remote, "refs/heads/feature/existing"); !ok {
		t.Fatal("default push no longer creates generated work branches")
	}
}
