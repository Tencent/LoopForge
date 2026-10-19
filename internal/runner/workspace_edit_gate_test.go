package runner

import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceWriteFenceExcludesTasksAndStaleWrites(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "fenced")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	req := domain.RunnerWorkspaceRequest{RunID: "fenced", RunStatus: "waiting", Operation: domain.RunnerWorkspaceOperationWrite, Path: "note.txt", Content: "after", ExpectedSHA256: runtime.WorkspaceContentSHA("before")}
	gate := d.workspaceGate(req.RunID)
	gate.RLock()
	blocked := d.readRunnerWorkspace(context.Background(), req)
	gate.RUnlock()
	if blocked.Status != 409 {
		t.Fatalf("active task allowed write: %+v", blocked)
	}
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 200 {
		t.Fatalf("write: %+v", result)
	}
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 409 {
		t.Fatal("stale write allowed")
	}
	// A late remote file operation keeps the runner-local fence even after the
	// API has stopped waiting. A new task cannot enter until it actually ends.
	gate.Lock()
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() { gate.RLock(); close(entered); gate.RUnlock(); close(done) }()
	select {
	case <-entered:
		t.Fatal("task overlapped pending file operation")
	case <-time.After(40 * time.Millisecond):
	}
	gate.Unlock()
	<-done
}

func TestWorkspaceTerminalFenceAllowsOwnerSaveAndClosesOnRelease(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "terminal", "workspace")
	os.MkdirAll(workspace, 0700)
	os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("before"), 0600)
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	req := domain.RunnerWorkspaceRequest{RunID: "terminal", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, RunStatus: "waiting", Operation: domain.RunnerWorkspaceOperationTerminal, Sandbox: &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "local"}}, Workflow: &domain.WorkflowDefinition{}, TerminalOwner: 7, Terminal: &domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}}
	opened := d.readRunnerWorkspace(context.Background(), req)
	if opened.Status != 200 {
		t.Fatal(opened)
	}
	defer d.workspaceTerminals.CloseScope("terminal:7")
	gate := d.workspaceGate(req.RunID)
	if gate.TryRLock() {
		gate.RUnlock()
		t.Fatal("agent could enter while human shell active")
	}
	write := domain.RunnerWorkspaceRequest{RunID: req.RunID, WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, RunStatus: "waiting", Operation: domain.RunnerWorkspaceOperationWrite, TerminalOwner: 7, Path: "note.txt", Content: "edited", ExpectedSHA256: runtime.WorkspaceContentSHA("before")}
	if res := d.readRunnerWorkspace(context.Background(), write); res.Status != 200 {
		t.Fatal("owner could not save with terminal open", res)
	}
	write.TerminalOwner = 8
	if res := d.readRunnerWorkspace(context.Background(), write); res.Status != 409 {
		t.Fatal("other user bypassed terminal fence", res)
	}
	req.Terminal.Operation = "close-all"
	if res := d.readRunnerWorkspace(context.Background(), req); res.Status != 200 {
		t.Fatal(res)
	}
	if !gate.TryRLock() {
		t.Fatal("terminal fence not released")
	}
	gate.RUnlock()
}

func TestMainBotGroupWorkspaceWriterKeepsHashChecks(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "group-write")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	req := domain.RunnerWorkspaceRequest{RunID: "group-write", RunStatus: "running", Operation: domain.RunnerWorkspaceOperationWrite, Path: "note.txt", Content: "after", ExpectedSHA256: runtime.WorkspaceContentSHA("before")}
	gate := d.workspaceGate(req.RunID)
	gate.RLock()
	defer gate.RUnlock()
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 409 {
		t.Fatal("ordinary writer bypassed active SOP", result)
	}
	req.CollaborationMode = "group"
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 200 {
		t.Fatal("group write blocked", result)
	}
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 409 {
		t.Fatal("stale group write overwrote file", result)
	}
}
