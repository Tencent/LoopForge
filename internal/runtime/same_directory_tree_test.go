package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSameTreeFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("SKILL.md", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
}

func TestSameDirectoryTree(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, b string)
		want   bool
	}{
		{name: "identical", mutate: func(*testing.T, string) {}, want: true},
		{name: "root permission ignored", mutate: func(t *testing.T, b string) {
			if err := os.Chmod(b, 0o700); err != nil {
				t.Fatal(err)
			}
		}, want: true},
		{name: "file content differs", mutate: func(t *testing.T, b string) {
			if err := os.WriteFile(filepath.Join(b, "SKILL.md"), []byte("BODY"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: false},
		{name: "file size differs", mutate: func(t *testing.T, b string) {
			if err := os.WriteFile(filepath.Join(b, "SKILL.md"), []byte("longer body"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: false},
		{name: "extra entry", mutate: func(t *testing.T, b string) {
			if err := os.WriteFile(filepath.Join(b, "extra"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: false},
		{name: "renamed entry", mutate: func(t *testing.T, b string) {
			if err := os.Rename(filepath.Join(b, "sub", "a.txt"), filepath.Join(b, "sub", "b.txt")); err != nil {
				t.Fatal(err)
			}
		}, want: false},
		{name: "permission differs", mutate: func(t *testing.T, b string) {
			if err := os.Chmod(filepath.Join(b, "sub", "a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: false},
		{name: "kind differs", mutate: func(t *testing.T, b string) {
			if err := os.Remove(filepath.Join(b, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(b, "link"), []byte("SKILL.md"), 0o777); err != nil {
				t.Fatal(err)
			}
		}, want: false},
		{name: "symlink target differs", mutate: func(t *testing.T, b string) {
			if err := os.Remove(filepath.Join(b, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("sub", filepath.Join(b, "link")); err != nil {
				t.Fatal(err)
			}
		}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
			for _, root := range []string{a, b} {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
				writeSameTreeFixture(t, root)
			}
			tc.mutate(t, b)
			got, err := sameDirectoryTree(a, b)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("sameDirectoryTree = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameDirectoryTreeMissingRoot(t *testing.T) {
	a := t.TempDir()
	if _, err := sameDirectoryTree(a, filepath.Join(a, "missing")); err == nil {
		t.Fatal("expected error for missing directory")
	}
}
