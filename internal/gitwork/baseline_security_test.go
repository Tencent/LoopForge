package gitwork

import (
	"os"
	"path/filepath"
	"testing"
)

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
