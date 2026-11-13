package gitwork

import (
	"strings"
	"testing"
)

func TestResolveCommitSHARejectsMultiLineRef(t *testing.T) {
	_, checkout := workBranchFixture(t)
	for _, ref := range []string{"main\n", "main\rHEAD"} {
		if _, err := resolveCommitSHA(checkout, ref); err == nil || !strings.Contains(err.Error(), "invalid ref") {
			t.Fatalf("%q: err = %v, want invalid ref", ref, err)
		}
	}
}

func TestResolveCommitSHAResolvesCommit(t *testing.T) {
	_, checkout := workBranchFixture(t)
	got, err := resolveCommitSHA(checkout, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if want := workBranchGit(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Fatalf("sha = %q, want %q", got, want)
	}
}

func TestResolveBaselineSHAFallbackOrder(t *testing.T) {
	_, checkout := workBranchFixture(t)
	originMain := workBranchGit(t, checkout, "rev-parse", "origin/main")
	for _, base := range []string{"", "  ", "missing-branch"} {
		got, err := resolveBaselineSHA(checkout, base)
		if err != nil {
			t.Fatalf("%q: %v", base, err)
		}
		if got != originMain {
			t.Fatalf("%q: sha = %q, want origin/main %q", base, got, originMain)
		}
	}
}

func TestResolveBaselineSHAFallsBackToHEAD(t *testing.T) {
	workspace := t.TempDir()
	workBranchGit(t, workspace, "init", "-q", "-b", "trunk")
	workBranchGit(t, workspace, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	got, err := resolveBaselineSHA(workspace, "trunk")
	if err != nil {
		t.Fatal(err)
	}
	if want := workBranchGit(t, workspace, "rev-parse", "HEAD"); got != want {
		t.Fatalf("sha = %q, want HEAD %q", got, want)
	}
}

func TestResolveBaselineSHAReportsLastCandidateError(t *testing.T) {
	workspace := t.TempDir()
	workBranchGit(t, workspace, "init", "-q", "-b", "master")
	_, err := resolveBaselineSHA(workspace, "master")
	if err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("err = %v, want HEAD diagnostic", err)
	}
}
