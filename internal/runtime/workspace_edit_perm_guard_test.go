package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// WriteWorkspaceFile must keep the mode bits of the file it overwrites: the
// replace path takes the existing permissions from the caller, so a file that
// was deliberately group-writable must stay group-writable after an edit.
func TestWorkspaceEditPreservesOriginalFileMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "run.sh")
	if err := os.WriteFile(target, []byte("echo old\n"), 0751); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0751); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteWorkspaceFile(root, "run.sh", "echo new\n", WorkspaceContentSHA("echo old\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "echo new\n" {
		t.Fatalf("content %q, %v", raw, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0751 {
		t.Fatalf("mode after overwrite = %04o, want 0751", got)
	}
	// A read-only source file must not be widened by the replace either.
	if err := os.Chmod(target, 0444); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteWorkspaceFile(root, "run.sh", "echo third\n", WorkspaceContentSHA("echo new\n")); err != nil {
		t.Fatal(err)
	}
	if info, err = os.Stat(target); err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("mode after read-only overwrite = %v, %v", info.Mode().Perm(), err)
	}
}
