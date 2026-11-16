package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnmergedFilesPreservesUnicodeAndWhitespace(t *testing.T) {
	_, checkout := workBranchFixture(t)
	path := "  变更 notes.txt "
	for _, branch := range []string{"incoming", "feature/existing"} {
		if branch == "incoming" {
			workBranchGit(t, checkout, "checkout", "-b", branch)
		} else {
			workBranchGit(t, checkout, "checkout", branch)
		}
		if err := os.WriteFile(filepath.Join(checkout, path), []byte(branch+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		workBranchGit(t, checkout, "add", "--", path)
		workBranchGit(t, checkout, "commit", "-qm", branch)
	}
	ctx := context.Background()
	if _, err := C(ctx, checkout, "merge", "incoming"); err == nil {
		t.Fatal("expected an add/add merge conflict")
	}
	if got := UnmergedFiles(ctx, checkout); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("conflict paths = %q, want %q", got, path)
	}
}
