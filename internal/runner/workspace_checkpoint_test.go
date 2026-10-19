package runner

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunnerWorkspaceCheckpointRestoreAndActiveTerminalProtection(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "checkpoint", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note"), []byte("latest"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	req := domain.RunnerWorkspaceRequest{RunID: "checkpoint", RunStatus: "succeeded", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, Operation: domain.RunnerWorkspaceOperationCheckpoint}
	response := d.readRunnerWorkspace(context.Background(), req)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	var data struct {
		Checkpoint []byte `json:"checkpoint"`
	}
	if err := json.Unmarshal(response.Payload, &data); err != nil {
		t.Fatal(err)
	}
	opened, err := d.workspaceTerminals.Handle(workspace, "checkpoint:1", domain.WorkspaceTerminalRequest{Operation: "open"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked := d.readRunnerWorkspace(context.Background(), req); blocked.Status != 409 {
		t.Fatal("active terminal captured")
	}
	if _, err = d.workspaceTerminals.Handle(workspace, "checkpoint:1", domain.WorkspaceTerminalRequest{Operation: "close", ID: opened.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	req.Operation = domain.RunnerWorkspaceOperationRestore
	req.Checkpoint = data.Checkpoint
	if restored := d.readRunnerWorkspace(context.Background(), req); restored.Error != "" {
		t.Fatal(restored.Error)
	}
	body, err := os.ReadFile(filepath.Join(workspace, "note"))
	if err != nil || string(body) != "latest" {
		t.Fatal("runner changes lost", err)
	}
	if overwritten := d.readRunnerWorkspace(context.Background(), req); overwritten.Error == "" {
		t.Fatal("existing workspace overwritten")
	}
}
