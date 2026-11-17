package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestDestructiveOperationHoldsSandboxLock(t *testing.T) {
	m := NewManager(t.TempDir())
	lock, acquired, err := m.tryAcquireLock("run-held")
	if err != nil || !acquired {
		t.Fatalf("lock: acquired=%v err=%v", acquired, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err = m.AcquireRun(ctx, "run-held", domain.SandboxProfile{Driver: "local"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err == nil {
		t.Fatal("sandbox opened while its destructive-operation lock was held")
	}
	if err := unlockFile(lock); err != nil {
		t.Fatal(err)
	}
	sandbox, err := m.AcquireRun(context.Background(), "run-held", domain.SandboxProfile{Driver: "local"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	_ = sandbox.Close(context.Background())
}

func TestDestroyRunReportsBusyUntilSandboxCloses(t *testing.T) {
	m := NewManager(t.TempDir())
	sandbox, err := m.AcquireRun(context.Background(), "run-busy", domain.SandboxProfile{Driver: "local"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run-busy"); !errors.Is(err, ErrSandboxInUse) {
		t.Fatalf("destroy while in use: %v", err)
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run-busy"); err != nil {
		t.Fatal(err)
	}
}

func TestChildSandboxProtectsSharedWorkspaceFromReclamation(t *testing.T) {
	m := NewManager(t.TempDir())
	child, err := m.AcquireRunIn(context.Background(), "child", "parent", domain.SandboxProfile{Driver: "local"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child.Workspace(), "uncommitted.txt")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("parent"); !errors.Is(err, ErrSandboxInUse) {
		t.Fatalf("destroy shared workspace: %v", err)
	}
	if _, err := m.ReclaimExcept(nil); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != "keep" {
		t.Fatalf("active shared workspace was reclaimed: %q, %v", body, err)
	}
	if err := child.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists: %v", err)
	}
}

func TestEphemeralWorkspaceExcludesSharedSandbox(t *testing.T) {
	m := NewManager(t.TempDir())
	profile := domain.SandboxProfile{Driver: "local"}
	ephemeral, err := m.Create(context.Background(), "run", "node", profile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = m.AcquireRunIn(ctx, "other", "run-node", profile, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared use of ephemeral workspace: %v", err)
	}
	if err := ephemeral.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	shared, err := m.AcquireRunIn(context.Background(), "other", "run-node", profile, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	_ = shared.Close(context.Background())
}

func TestDestroyRunReportsBusyMatchingWorkspace(t *testing.T) {
	m := NewManager(t.TempDir())
	sandbox, err := m.AcquireRun(context.Background(), "run-node", domain.SandboxProfile{Driver: "local"}, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run"); !errors.Is(err, ErrSandboxInUse) {
		t.Fatalf("destroy with active matching workspace: %v", err)
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRun("run"); err != nil {
		t.Fatal(err)
	}
}
