package gitwork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordBaselineReportsGitDiagnostic(t *testing.T) {
	workspace := t.TempDir()
	workBranchGit(t, workspace, "init", "-q", "-b", "master")
	err := RecordBaseline(workspace, "master")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing Git diagnostic: %v", err)
	}
}

func TestRecordBaselineRejectsSymlinks(t *testing.T) {
	for _, rel := range []string{".git", ".git/agentflow-baseline"} {
		t.Run(rel, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(workspace, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			victim := outside
			if rel != ".git" {
				victim = filepath.Join(outside, "keep")
				if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(victim, target); err != nil {
				t.Fatal(err)
			}
			if err := RecordBaseline(workspace, "master"); err == nil {
				t.Fatal("symlink accepted")
			}
			if rel != ".git" {
				raw, err := os.ReadFile(victim)
				if err != nil || string(raw) != "keep" {
					t.Fatalf("outside file changed: %q, %v", raw, err)
				}
			}
		})
	}
}

func TestRecordBaselineKeepsFrozenCheckout(t *testing.T) {
	_, workspace := workBranchFixture(t)
	want := workBranchGit(t, workspace, "rev-parse", "origin/main")
	if err := RecordBaseline(workspace, "main"); err != nil {
		t.Fatal(err)
	}
	path := BaselinePath(workspace)
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("baseline = %q, %v; want %q", raw, err, want)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, workspace, "add", "new.txt")
	workBranchGit(t, workspace, "commit", "-qm", "advance HEAD")
	if err := RecordBaseline(workspace, "missing"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("frozen baseline changed: %q, %v", raw, err)
	}
}

func TestRecordBaselineResolvesTheRemoteBaseBranch(t *testing.T) {
	_, checkout := workBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("ahead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "feature.txt")
	workBranchGit(t, checkout, "commit", "-qm", "ahead of main")
	workBranchGit(t, checkout, "push", "-q", "origin", "feature/existing")
	if err := RecordBaseline(checkout, " feature/existing "); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(BaselinePath(checkout))
	if err != nil {
		t.Fatal(err)
	}
	if want := workBranchGit(t, checkout, "rev-parse", "origin/feature/existing"); string(got) != want {
		t.Fatalf("baseline = %q, want origin/feature/existing %q", got, want)
	}
}

func TestRecordBaselineNeverTreatsABranchAsAnOption(t *testing.T) {
	for _, branch := range []string{"--output=pwned", "x\n--batch", "missing-branch"} {
		_, checkout := workBranchFixture(t)
		if err := RecordBaseline(checkout, branch); err != nil {
			t.Fatalf("%q: %v", branch, err)
		}
		got, err := os.ReadFile(BaselinePath(checkout))
		if err != nil {
			t.Fatal(err)
		}
		if want := workBranchGit(t, checkout, "rev-parse", "origin/main"); string(got) != want {
			t.Fatalf("%q: baseline = %q, want origin/main fallback %q", branch, got, want)
		}
		if _, err := os.Stat(filepath.Join(checkout, "pwned")); !os.IsNotExist(err) {
			t.Fatalf("%q: branch name was used as a git option: %v", branch, err)
		}
	}
}
