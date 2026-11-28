package gitwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// issue #717: a successful SOP run's workspace is cleaned after success, but
// the changes were never committed or pushed. The repository-patch artifact
// only stored a filename inventory, so the actual edits were lost with the
// checkout. PatchInventory must produce a real unified diff so the changes
// survive workspace reclamation and can be reapplied to a fresh checkout.

func patchContentFixture(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	workBranchGit(t, ws, "init", "-b", "master")
	workBranchGit(t, ws, "config", "user.email", "test@example.com")
	workBranchGit(t, ws, "config", "user.name", "Test")
	writeFile(t, ws, "base.txt", "base\n")
	workBranchGit(t, ws, "add", "base.txt")
	workBranchGit(t, ws, "commit", "--quiet", "-m", "base")
	return ws
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPatchInventoryStoresActualChanges verifies that the repository-patch
// carries the real unified diff, not just a filename list. The diff must
// contain the edited content and must be applicable to a clean checkout of
// the same baseline.
func TestPatchInventoryStoresActualChanges(t *testing.T) {
	t.Parallel()
	ws := patchContentFixture(t)

	// Simulate node work: modify a tracked file and add a new untracked one.
	writeFile(t, ws, "base.txt", "base\nedited line\n")
	writeFile(t, ws, "new_file.txt", "brand new content\n")

	if err := RecordBaseline(ws, "master"); err != nil {
		t.Fatal(err)
	}

	patch, err := PatchInventory(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) == 0 {
		t.Fatal("patch must not be empty for a dirty workspace")
	}

	text := string(patch)
	// The inventory text must NOT be the only thing present: it must carry the
	// actual diff hunks. A pure inventory has no "diff --git" or "@@" lines.
	if !strings.Contains(text, "diff --git ") {
		t.Fatalf("patch carries only a filename inventory, not the real diff:\n%s", text)
	}
	if !strings.Contains(text, "@@") {
		t.Fatalf("patch has no diff hunks:\n%s", text)
	}
	// The edited content itself must survive so a clean checkout can recover it.
	if !strings.Contains(text, "edited line") {
		t.Fatalf("patch does not contain the modified file content:\n%s", text)
	}
	if !strings.Contains(text, "brand new content") {
		t.Fatalf("patch does not contain the new file content:\n%s", text)
	}

	// A fresh checkout of the baseline must accept the patch and reproduce
	// exactly the same dirty state.
	fresh := t.TempDir()
	workBranchGit(t, fresh, "init", "-b", "master")
	workBranchGit(t, fresh, "config", "user.email", "test@example.com")
	workBranchGit(t, fresh, "config", "user.name", "Test")
	writeFile(t, fresh, "base.txt", "base\n")
	workBranchGit(t, fresh, "add", "base.txt")
	workBranchGit(t, fresh, "commit", "--quiet", "-m", "base")

	patchFile := filepath.Join(fresh, "agentflow.patch")
	if err := os.WriteFile(patchFile, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", fresh, "apply", "--whitespace=nowarn", patchFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply patch to fresh checkout: %v: %s", err, out)
	}

	gotBase, err := os.ReadFile(filepath.Join(fresh, "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBase) != "base\nedited line\n" {
		t.Fatalf("fresh base.txt = %q, want edited content", gotBase)
	}
	gotNew, err := os.ReadFile(filepath.Join(fresh, "new_file.txt"))
	if err != nil {
		t.Fatalf("new_file.txt missing after apply: %v", err)
	}
	if string(gotNew) != "brand new content\n" {
		t.Fatalf("fresh new_file.txt = %q", gotNew)
	}
}

// TestPatchInventoryFallsBackToInventoryWhenDiffTooLarge verifies the size
// guard: when the full diff exceeds MaxPatchBytes, the inventory (filename
// list) is returned instead, so the artifact stays within the size limit.
func TestPatchInventoryFallsBackToInventoryWhenDiffTooLarge(t *testing.T) {
	t.Parallel()
	ws := patchContentFixture(t)

	// A single huge addition forces the diff past the size cap.
	big := strings.Repeat("x", MaxPatchBytes+1024)
	writeFile(t, ws, "base.txt", "base\n"+big)

	if err := RecordBaseline(ws, "master"); err != nil {
		t.Fatal(err)
	}

	patch, err := PatchInventory(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) > MaxPatchBytes {
		t.Fatalf("patch exceeds size cap: %d bytes", len(patch))
	}
	// The fallback must be the inventory format (which states the full diff
	// is not stored), not a truncated diff that cannot be applied.
	text := string(patch)
	if strings.Contains(text, "diff --git ") {
		t.Fatalf("oversized patch must fall back to inventory, got a diff:\n%s", text)
	}
	if !strings.Contains(text, "base.txt") {
		t.Fatalf("inventory must still list the changed file:\n%s", text)
	}
}

// TestPatchInventoryEmptyForCleanWorkspace keeps the no-changes behavior.
func TestPatchInventoryEmptyForCleanWorkspace(t *testing.T) {
	t.Parallel()
	ws := patchContentFixture(t)

	if err := RecordBaseline(ws, "master"); err != nil {
		t.Fatal(err)
	}
	patch, err := PatchInventory(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 0 {
		t.Fatalf("clean workspace should yield empty patch, got %d bytes", len(patch))
	}
}
