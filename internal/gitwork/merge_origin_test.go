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

func TestMergeLatestIntegrationCompletesRepairedConflictOnNextCall(t *testing.T) {
	_, work := mergeOriginFixture(t, "file.txt")
	ctx := context.Background()
	if conflicts, err := MergeLatestIntegration(ctx, work, "work", "main", "test"); err != nil || len(conflicts) != 1 {
		t.Fatalf("first integration merge = %#v, %v", conflicts, err)
	}
	// A repair agent resolves and stages the conflict in the same checkout but
	// leaves the merge uncommitted; the next call must finish it.
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cvSRunGit(t, "-C", work, "add", "file.txt")
	conflicts, err := MergeLatestIntegration(ctx, work, "work", "main", "test")
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("repaired integration merge = %#v, %v", conflicts, err)
	}
	if mergeInProgress(t, work) {
		t.Fatal("repaired merge is still in progress")
	}
	if _, ok := RevParse(ctx, work, "HEAD^2"); !ok {
		t.Fatal("repaired merge was not committed as a merge commit")
	}
	if got, err := os.ReadFile(filepath.Join(work, "file.txt")); err != nil || string(got) != "resolved\n" {
		t.Fatalf("resolved content = %q, %v", got, err)
	}
}

func TestMergeOriginBranchFetchFailureReturnsError(t *testing.T) {
	_, work := mergeOriginFixture(t, "file.txt")
	ctx := context.Background()
	before, ok := RevParse(ctx, work, "HEAD")
	if !ok {
		t.Fatal("missing HEAD")
	}
	if conflicts, err := MergeLatestRemoteBranch(ctx, work, "missing-branch"); err == nil || conflicts != nil {
		t.Fatalf("remote merge of missing branch = %#v, %v", conflicts, err)
	}
	if conflicts, err := MergeLatestIntegration(ctx, work, "work", "missing-branch", "test"); err == nil || conflicts != nil {
		t.Fatalf("integration merge of missing branch = %#v, %v", conflicts, err)
	}
	if after, _ := RevParse(ctx, work, "HEAD"); after != before {
		t.Fatalf("fetch failure moved HEAD from %s to %s", before, after)
	}
	if mergeInProgress(t, work) {
		t.Fatal("fetch failure left a merge in progress")
	}
}
