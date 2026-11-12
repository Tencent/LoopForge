package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// mergeOriginFixture returns a work checkout whose "work" branch and the
// remote main both changed name on top of a shared base, so merging
// origin/main conflicts on exactly that file.
func mergeOriginFixture(t *testing.T, name string) (seed, work string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed = filepath.Join(root, "seed")
	work = filepath.Join(root, "work")
	write := func(dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cvSRunGit(t, "init", "--bare", "-q", remote)
	cvSRunGit(t, "init", "-q", seed)
	cvSRunGit(t, "-C", seed, "config", "user.name", "Test")
	cvSRunGit(t, "-C", seed, "config", "user.email", "test@example.com")
	write(seed, "base\n")
	cvSRunGit(t, "-C", seed, "add", "-f", name)
	cvSRunGit(t, "-C", seed, "commit", "-q", "-m", "base")
	cvSRunGit(t, "-C", seed, "branch", "-M", "main")
	cvSRunGit(t, "-C", seed, "remote", "add", "origin", remote)
	cvSRunGit(t, "-C", seed, "push", "-q", "-u", "origin", "main")
	cvSRunGit(t, "clone", "-q", "--branch", "main", remote, work)
	cvSRunGit(t, "-C", work, "checkout", "-q", "-b", "work")
	EnsureIdentity(context.Background(), work)
	write(seed, "main\n")
	cvSRunGit(t, "-C", seed, "commit", "-q", "-am", "main update")
	cvSRunGit(t, "-C", seed, "push", "-q", "origin", "main")
	write(work, "work\n")
	cvSRunGit(t, "-C", work, "commit", "-q", "-am", "work update")
	return seed, work
}

func mergeInProgress(t *testing.T, work string) bool {
	t.Helper()
	_, err := C(context.Background(), work, "rev-parse", "--verify", "MERGE_HEAD")
	return err == nil
}

func TestMergeLatestRemoteBranchAutoResolvesSOPDeliverableConflict(t *testing.T) {
	_, work := mergeOriginFixture(t, "review-report.md")
	conflicts, err := MergeLatestRemoteBranch(context.Background(), work, "main")
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("SOP-only conflict merge = %#v, %v", conflicts, err)
	}
	if mergeInProgress(t, work) {
		t.Fatal("SOP-only conflict left the merge in progress")
	}
	if _, ok := RevParse(context.Background(), work, "HEAD^2"); !ok {
		t.Fatal("SOP-only conflict merge was not committed")
	}
}

func TestMergeLatestIntegrationLeavesRealConflictInProgress(t *testing.T) {
	_, work := mergeOriginFixture(t, "file.txt")
	conflicts, err := MergeLatestIntegration(context.Background(), work, "work", "main", "test")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "file.txt" {
		t.Fatalf("integration conflict merge = %#v, %v", conflicts, err)
	}
	if !mergeInProgress(t, work) {
		t.Fatal("real conflict must leave the merge in progress for repair")
	}
}

func TestMergeLatestIntegrationAutoResolvesSOPDeliverableConflict(t *testing.T) {
	_, work := mergeOriginFixture(t, "review-report.md")
	conflicts, err := MergeLatestIntegration(context.Background(), work, "work", "main", "test")
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("SOP-only integration merge = %#v, %v", conflicts, err)
	}
	if mergeInProgress(t, work) {
		t.Fatal("SOP-only integration conflict left the merge in progress")
	}
}
