package gitwork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func undeliveredPaths(files []PatchFileSummary) string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Status+" "+f.Path)
	}
	return strings.Join(paths, ",")
}

func TestUndeliveredChangesCleanPushedCheckout(t *testing.T) {
	_, checkout := workBranchFixture(t)
	if files, err := UndeliveredChanges(checkout); err != nil || len(files) != 0 {
		t.Fatalf("pushed clean checkout: files=%v err=%v", files, err)
	}
}

// The #717 shape: implement leaves edits uncommitted on the base branch and
// the run succeeds without pushing anything.
func TestUndeliveredChangesListsUncommittedWork(t *testing.T) {
	_, checkout := workBranchFixture(t)
	writeUndeliveredFile(t, checkout, "README.md", "baseline\nchanged by agent\n")
	writeUndeliveredFile(t, checkout, "src/app.go", "package app\n")
	writeUndeliveredFile(t, checkout, ".env", "TOKEN=secret\n")
	writeUndeliveredFile(t, checkout, "node_modules/dep/index.js", "module.exports = 1\n")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "M README.md,A src/app.go" {
		t.Fatalf("files=%s", got)
	}
	if files[0].Added != 1 || files[1].Added != 1 {
		t.Fatalf("line counts=%+v", files)
	}
}

func TestUndeliveredChangesListsUnpushedCommitsUntilPushed(t *testing.T) {
	_, checkout := workBranchFixture(t)
	writeUndeliveredFile(t, checkout, "feature.txt", "committed only locally\n")
	workBranchGit(t, checkout, "add", "feature.txt")
	workBranchGit(t, checkout, "commit", "-qm", "local only")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "A feature.txt" {
		t.Fatalf("files=%s", got)
	}

	workBranchGit(t, checkout, "push", "-q", "origin", "HEAD:refs/heads/feature/existing")
	if files, err := UndeliveredChanges(checkout); err != nil || len(files) != 0 {
		t.Fatalf("after push: files=%v err=%v", files, err)
	}
}

// A checkout without any remote falls back to the recorded run baseline.
func TestUndeliveredChangesWithoutRemoteUsesBaseline(t *testing.T) {
	_, checkout := workBranchFixture(t)
	if err := RecordBaseline(checkout, "main"); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "remote", "remove", "origin")
	writeUndeliveredFile(t, checkout, "local.txt", "x\n")
	workBranchGit(t, checkout, "add", "local.txt")
	workBranchGit(t, checkout, "commit", "-qm", "local")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "A local.txt" {
		t.Fatalf("files=%s", got)
	}
}

func writeUndeliveredFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
