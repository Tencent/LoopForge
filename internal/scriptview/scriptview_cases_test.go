//go:build linux

package scriptview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinuxMaskHelpers(t *testing.T) {
	if err := removeMasks(nil); err != nil {
		t.Fatal(err)
	}
	if err := applyMasks(nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".agents", ".codex", ".claude", ".codebuddy"} {
		if !instructionDirectory(name) {
			t.Fatal(name)
		}
	}
	if instructionDirectory(".github") {
		t.Fatal("unexpected instruction directory")
	}
	if got := decodeMountPath(`a\040b\011c\012d\134e`); got != "a b\tc\nd\\e" {
		t.Fatalf("decoded = %q", got)
	}
}

func TestExecuteRefreshFailsClosedOrCompletes(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- execute(Config{Refresh: true})
	}()
	select {
	case <-done:
		// Both outcomes are valid: restricted test containers reject namespace
		// entry, while privileged builders can refresh an empty mask list.
	case <-time.After(10 * time.Second):
		t.Fatal("script view namespace setup hung")
	}
}

func TestApplyMaskValidationAndFilesystemBranches(t *testing.T) {
	const workspace = "/workspace"
	createdWorkspace := false
	if _, err := os.Stat(workspace); os.IsNotExist(err) {
		if err := os.Mkdir(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		createdWorkspace = true
	}
	rootDir := filepath.Join(workspace, "cvs-scriptview")
	_ = os.RemoveAll(rootDir)
	if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "isolation", "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agentflow", "isolation", "empty-file"), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(rootDir)
		if createdWorkspace {
			_ = os.RemoveAll(workspace)
		}
	})
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := applyMask(root, filepath.Join(rootDir, "AGENTS.md")); err != nil {
		t.Fatalf("missing mask should be ignored: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := applyMask(root, filepath.Join(rootDir, "AGENTS.md")); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside symlink error = %v", err)
	}
	if err := os.Remove(filepath.Join(rootDir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}

	realAgents := filepath.Join(workspace, "cvs-real", ".agents")
	if err := os.MkdirAll(realAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(realAgents)) })
	if err := os.Symlink(realAgents, filepath.Join(rootDir, ".agents")); err != nil {
		t.Fatal(err)
	}
	if err := applyMask(root, filepath.Join(rootDir, ".agents")); err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("instruction symlink error = %v", err)
	}

	regular := filepath.Join(rootDir, "CLAUDE.md")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyMask(root, regular); err == nil || !strings.Contains(err.Error(), "not read-only") {
		t.Fatalf("writable source error = %v", err)
	}
}

func TestRemoveMasksRejectsWritableSources(t *testing.T) {
	rootDir := "/workspace/cvs-scriptview-remove"
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootDir) })
	err := removeMasks([]string{filepath.Join(rootDir, "AGENTS.md")})
	if err == nil || !strings.Contains(err.Error(), "not read-only") {
		t.Fatalf("removeMasks error = %v", err)
	}
}

// This runs in the disposable go test process and intentionally leaves that
// process capability-free. Keeping the current UID/GID avoids changing file
// ownership while exercising the same final privilege drop as the helper.
func TestZDropPrivilegesForDisposableTestProcess(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	err = dropPrivileges(processIdentity{uid: os.Geteuid(), gid: os.Getegid(), groups: groups})
	if err != nil {
		t.Fatalf("drop privileges: %v", err)
	}
}
