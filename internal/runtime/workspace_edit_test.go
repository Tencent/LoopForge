package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceEditRejectsStaleAndEscapingWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.txt")
	os.WriteFile(path, []byte("old"), 0644)
	sha, err := WriteWorkspaceFile(root, "main.txt", "new", WorkspaceContentSHA("old"))
	if err != nil || sha != WorkspaceContentSHA("new") {
		t.Fatalf("write: %s %v", sha, err)
	}
	if _, err = WriteWorkspaceFile(root, "main.txt", "stale", WorkspaceContentSHA("old")); err == nil {
		t.Fatal("stale write accepted")
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600)
	for _, p := range []string{"../secret", "/etc/passwd", "link/secret", ".env"} {
		if _, err = WriteWorkspaceFile(root, p, "overwrite", WorkspaceContentSHA("secret")); err == nil {
			t.Fatalf("unsafe path allowed: %s", p)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(outside, "secret"))
	if string(raw) != "secret" {
		t.Fatal("outside file modified")
	}
}

func TestWorkspaceEditCreateRequiresMissingVersion(t *testing.T) {
	root := t.TempDir()
	sha, err := WriteWorkspaceFile(root, "new.txt", "new", "missing")
	if err != nil || sha != WorkspaceContentSHA("new") {
		t.Fatal(sha, err)
	}
	if _, err = WriteWorkspaceFile(root, "new.txt", "overwrite", "missing"); err == nil {
		t.Fatal("new file overwrote existing file")
	}
	if _, err = WriteWorkspaceFile(root, "absent.txt", "new", ""); err == nil {
		t.Fatal("creation without missing version")
	}
	os.Symlink(t.TempDir(), filepath.Join(root, "link"))
	if _, err = WriteWorkspaceFile(root, "link/escape.txt", "new", "missing"); err == nil {
		t.Fatal("creation escaped through symlink")
	}
}
