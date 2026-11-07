package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
)

// 子 SOP 的沙箱用自己的锁，但工作目录是父流程的；父流程正持有工作区时子流程也能打开，删除子流程也不会删掉父流程目录。
func TestAcquireRunInSharesWorkspaceWithOwnLock(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	parent, err := m.AcquireRun(context.Background(), "run-parent", profile, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close(context.Background())
	if err := os.WriteFile(filepath.Join(parent.Workspace(), "pre.txt"), []byte("pre"), 0o644); err != nil {
		t.Fatal(err)
	}
	child, err := m.AcquireRunIn(context.Background(), "run-child", "run-parent", profile, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	if child.Workspace() != parent.Workspace() {
		t.Fatalf("child workspace = %s, want %s", child.Workspace(), parent.Workspace())
	}
	if _, err := os.Stat(filepath.Join(child.Workspace(), "pre.txt")); err != nil {
		t.Fatalf("child misses parent change: %v", err)
	}
	if err := child.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run-child"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent.Workspace(), "pre.txt")); err != nil {
		t.Fatalf("destroying the child removed the parent workspace: %v", err)
	}
}
