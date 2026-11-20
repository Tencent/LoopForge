package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceEditPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, expected, want string
		ok                   bool
	}{
		{"src/main.go", "sha", filepath.Join("src", "main.go"), true},
		{"src/./main.go", "sha", filepath.Join("src", "main.go"), true},
		{"src/new.go", "missing", filepath.Join("src", "new.go"), true},
		{"src/new.go", "sha", "", false},
		{"absent/new.go", "missing", "", false},
		{".", "missing", "", false},
		{"..", "missing", "", false},
		{"../escape", "missing", "", false},
		{"/etc/passwd", "sha", "", false},
		{".env", "missing", "", false},
		{"src/.git/config", "missing", "", false},
		{"link", "sha", "", false},
		{"link/file", "missing", "", false},
	}
	for _, c := range cases {
		got, err := resolveWorkspaceEditPath(root, c.path, c.expected)
		if c.ok && (err != nil || got != c.want) {
			t.Fatalf("%q/%q: got %q, %v; want %q", c.path, c.expected, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Fatalf("%q/%q: accepted as %q", c.path, c.expected, got)
		}
	}
}

func TestCreateWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	base, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err = createWorkspaceFile(base, "new.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(raw) != "hello" {
		t.Fatalf("content %q, %v", raw, err)
	}
	if err = createWorkspaceFile(base, "new.txt", "overwrite"); err == nil {
		t.Fatal("existing file overwritten")
	}
	if raw, _ = os.ReadFile(filepath.Join(root, "new.txt")); string(raw) != "hello" {
		t.Fatalf("existing file changed: %q", raw)
	}
}

func TestReplaceWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "src", "main.go")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	rel := filepath.Join("src", "main.go")
	if err = replaceWorkspaceFile(base, root, rel, "new", 0640); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "new" {
		t.Fatalf("content %q, %v", raw, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode %v, %v", info.Mode().Perm(), err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "src", ".agentflow-edit-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
	if err = replaceWorkspaceFile(base, root, filepath.Join("missing", "x.go"), "x", 0644); err == nil {
		t.Fatal("replace into a missing directory succeeded")
	}
}
