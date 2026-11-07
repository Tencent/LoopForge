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
	chunkRequest := req
	chunkRequest.Operation = domain.RunnerWorkspaceOperationCheckpointExport
	if blocked := d.readRunnerWorkspace(context.Background(), chunkRequest); blocked.Status != 409 {
		t.Fatal("chunked capture ignored active terminal")
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

func TestRunnerCheckpointTransferUsesExecutionFence(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "checkpoint", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note"), []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	defer d.checkpointTransfers.Close()
	request := domain.RunnerWorkspaceRequest{RunID: "checkpoint", RunStatus: "succeeded", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, Operation: domain.RunnerWorkspaceOperationCheckpointExport}
	gate := d.workspaceGate(request.RunID)
	gate.Lock()
	response := d.readRunnerWorkspace(context.Background(), request)
	gate.Unlock()
	if response.Status != 409 {
		t.Fatal("export raced active execution")
	}
	response = d.readRunnerWorkspace(context.Background(), request)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	var manifest domain.WorkspaceCheckpointTransfer
	if err := json.Unmarshal(response.Payload, &manifest); err != nil || manifest.ID == "" {
		t.Fatal("invalid export", err)
	}
	request.Operation = domain.RunnerWorkspaceOperationCheckpointRead
	request.CheckpointTransfer = &domain.WorkspaceCheckpointTransfer{ID: manifest.ID}
	// An immutable export can be read while new work owns the workspace gate.
	gate.Lock()
	response = d.readRunnerWorkspace(context.Background(), request)
	gate.Unlock()
	if response.Error != "" {
		t.Fatal("export read competed with execution", response.Error)
	}
}

func TestRunnerCheckpointStatusDistinguishesBusyMissingAndSurvivingWorkspace(t *testing.T) {
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	req := domain.RunnerWorkspaceRequest{RunID: "probe", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, Operation: domain.RunnerWorkspaceOperationCheckpointStatus}
	gate := d.workspaceGate(req.RunID)
	gate.RLock()
	response := d.readRunnerWorkspace(context.Background(), req)
	gate.RUnlock()
	if response.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("active execution was mistaken for a missing directory: %+v", response)
	}
	if response = d.readRunnerWorkspace(context.Background(), req); response.Status != 404 || response.Code != domain.WorkspaceErrorMissing {
		t.Fatalf("missing checkout: %+v", response)
	}
	root, err := d.runnerWorkspaceRoot(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	// A cheap presence probe must not archive files or follow workspace links.
	if err = os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if response = d.readRunnerWorkspace(context.Background(), req); response.Status != 200 || response.Error != "" {
		t.Fatalf("surviving checkout was not reusable: %+v", response)
	}
}
