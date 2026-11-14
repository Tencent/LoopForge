package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func conversationWorkspaceRequest(driver string) domain.RunnerWorkspaceRequest {
	return domain.RunnerWorkspaceRequest{
		RunID:           "run-1",
		WorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		Sandbox:         &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: driver}},
		Workflow:        &domain.WorkflowDefinition{},
	}
}

// Every failed preparation step must release the writer gate it took, or the
// run's workspace stays fenced for later tasks and checkpoints.
func TestWorkspaceConversationFactoryReleasesGateOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request func() domain.RunnerWorkspaceRequest
		want    string
	}{
		{"legacy layout", func() domain.RunnerWorkspaceRequest {
			request := conversationWorkspaceRequest("docker")
			request.WorkspaceLayout = ""
			return request
		}, "legacy per-node workspace layout"},
		{"missing workflow", func() domain.RunnerWorkspaceRequest {
			request := conversationWorkspaceRequest("docker")
			request.Workflow = nil
			return request
		}, "frozen run sandbox and workflow are required"},
		{"sandbox open fails", func() domain.RunnerWorkspaceRequest {
			return conversationWorkspaceRequest("no-such-driver")
		}, "no-such-driver"},
		{"non-docker sandbox", func() domain.RunnerWorkspaceRequest {
			return conversationWorkspaceRequest("local")
		}, "对话工作区仅支持 Docker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
			request := tc.request()
			workspace := filepath.Join(root, "runs", request.RunID, "workspace")
			sb, release, err := d.workspaceConversationFactory(request, workspace)(context.Background(), filepath.Join(root, "native"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if sb != nil || release != nil {
				t.Fatalf("got sandbox %v release %v, want nil on error", sb, release != nil)
			}
			gate := d.requestGate(request)
			if !gate.TryLock() {
				t.Fatal("workspace gate still held after factory error")
			}
			gate.Unlock()
			d.runSandboxMu.Lock()
			defer d.runSandboxMu.Unlock()
			for key, entry := range d.runSandboxes {
				if entry.active != 0 {
					t.Fatalf("run sandbox %s still has %d active users", key, entry.active)
				}
			}
		})
	}
}

func TestWorkspaceConversationFactoryWaitsForWriterGate(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	request := conversationWorkspaceRequest("local")
	gate := d.requestGate(request)
	gate.Lock()
	defer gate.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sb, release, err := d.workspaceConversationFactory(request, filepath.Join(root, "runs", "run-1", "workspace"))(ctx, filepath.Join(root, "native"))
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sb != nil || release != nil {
		t.Fatalf("got sandbox %v release %v, want nil on error", sb, release != nil)
	}
	// The factory never owned the gate, so it must not have unlocked it.
	if gate.TryRLock() {
		gate.RUnlock()
		t.Fatal("factory released a writer gate it did not hold")
	}
	if len(d.runSandboxes) != 0 {
		t.Fatalf("sandbox acquired while workspace was busy: %v", d.runSandboxes)
	}
}

func TestHostConversationFactoryUsesManagedInventory(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	binary := filepath.Join(root, "providers", "codex", "1.2.3", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"current":{"codex":{"version":"1.2.3","binary":"` + binary + `"}},"previous":{},"candidates":{}}`
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{ManagedCLIRoot: root}}
	command := &runtime.ConversationCommand{Provider: "codex", Argv: []string{"codex", "exec"}}
	sb, release, err := d.hostConversationFactory(command)(context.Background(), "")
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if sb != nil || release == nil {
		t.Fatalf("got sandbox %v release %v, want host process with release", sb, release != nil)
	}
	release()
	release()
	if command.Argv[0] != binary {
		t.Fatalf("argv = %v, want managed binary %s", command.Argv, binary)
	}
}

func TestHostConversationFactoryReportsManagedInventoryError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{ManagedCLIRoot: root, ExtraBinaries: map[string]string{"codex": "/explicit/codex"}}}
	command := &runtime.ConversationCommand{Provider: "codex", Argv: []string{"codex"}}
	sb, release, err := d.hostConversationFactory(command)(context.Background(), "")
	if err == nil {
		t.Fatal("factory ignored unreadable managed CLI inventory")
	}
	if sb != nil || release != nil {
		t.Fatalf("got sandbox %v release %v, want nil on error", sb, release != nil)
	}
	if command.Argv[0] != "codex" {
		t.Fatalf("argv rewritten on error: %v", command.Argv)
	}
}
