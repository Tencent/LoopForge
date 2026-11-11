package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshSharedControlDirectoryKeepsUnchangedTree(t *testing.T) {
	workspace := t.TempDir()
	write := func(body string) func(string) error {
		return func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644)
		}
	}
	target, err := refreshSharedControlDirectory(workspace, write("v1"), "skills", "s1")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// A parallel Agent materializing the same skill must not recreate it.
	if _, err := refreshSharedControlDirectory(workspace, write("v1"), "skills", "s1"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged skill directory was recreated")
	}
	if _, err := refreshSharedControlDirectory(workspace, write("v2"), "skills", "s1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(raw) != "v2" {
		t.Fatalf("changed skill not swapped in: %q %v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("staging directories left behind: %v", entries)
	}
}
