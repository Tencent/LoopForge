package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func checkpointFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	e, s := newRecoveryEngine(t)
	r := domain.Run{ID: "checkpoint-run", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(r.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	return e, r.ID
}
func TestConversationWorkspaceReclaimsOriginalAndRestoresThenExpiresAfterOneHour(t *testing.T) {
	e, id := checkpointFixture(t)
	e.reconcileTerminalWorkspaceCleanup()
	r, _ := e.repo.RunState(id)
	if r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("original successful SOP not reclaimed")
	}
	if _, err := os.Stat(e.RunWorkspace(id)); !os.IsNotExist(err) {
		t.Fatal("workspace not removed")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(id)
	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(body) != "uncommitted" {
		t.Fatalf("restore lost changes %q %v", body, err)
	}
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte("follow-up"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	r, _ = e.repo.RunState(id)
	if r.Status != "succeeded" {
		t.Fatal("restore changed SOP outcome")
	}
	e.cleanupWorkspace(r, meta.LastUsedAt.Add(59*time.Minute))
	r, _ = e.repo.RunState(id)
	if !r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("reclaimed before hour")
	}
	token, err = e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	meta, _ = e.repo.WorkspaceCheckpoint(id)
	r, _ = e.repo.RunState(id)
	e = New(e.repo, e.sandboxes) // a restarted engine uses the persisted deadline
	e.cleanupWorkspace(r, meta.LastUsedAt.Add(time.Hour))
	r, _ = e.repo.RunState(id)
	if r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("idle restored workspace never reclaimed")
	}
	token, err = e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), id, token)
	body, err = os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(body) != "follow-up" {
		t.Fatalf("latest change lost: %q %v", body, err)
	}
}
func TestConversationWorkspacePreservesOnSaveFailureAndWhileActive(t *testing.T) {
	e, id := checkpointFixture(t)
	root := e.RunWorkspace(id)
	if err := os.Symlink("/outside", filepath.Join(root, "unsafe")); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	r, _ := e.repo.RunState(id)
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if !r.WorkspaceCleanedAt.IsZero() || meta.Error == "" {
		t.Fatal("unsaved files reclaimed or error lost")
	}
	protected := false
	for _, runID := range e.liveSandboxRunIDs() {
		protected = protected || runID == id
	}
	if !protected {
		t.Fatal("orphan sweep can remove unsaved workspace")
	}
	if err := os.Remove(filepath.Join(root, "unsafe")); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceActive = func(domain.Run) bool { return true }
	e.reconcileTerminalWorkspaceCleanup()
	r, _ = e.repo.RunState(id)
	if !r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("active terminal reclaimed")
	}
	e.WorkspaceActive = nil
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	r, _ = e.repo.RunState(id)
	if !r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("generation lease ignored")
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	r, _ = e.repo.RunState(id)
	if r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("successful retry of snapshot never reclaimed")
	}
}
func TestConversationWorkspaceCannotReclaimRunningNodes(t *testing.T) {
	e, id := checkpointFixture(t)
	r, _ := e.repo.RunState(id)
	r.Nodes = map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "running"}}
	if err := e.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(r, time.Now().Add(2*time.Hour))
	r, _ = e.repo.RunState(id)
	if !r.WorkspaceCleanedAt.IsZero() {
		t.Fatal("active role reclaimed")
	}
}
func TestWorkspaceCheckpointServiceRequiresTokenAndBoundIdleRun(t *testing.T) {
	e, id := checkpointFixture(t)
	call := func(token, body string) int {
		req := httptest.NewRequest("POST", "/internal/workspace-checkpoint", strings.NewReader(body))
		req.Header.Set(controlTokenHeader, token)
		w := httptest.NewRecorder()
		e.HandleWorkspaceCheckpoint(w, req)
		return w.Code
	}
	body, _ := json.Marshal(domain.RunnerWorkspaceRequest{RunID: id, Operation: domain.RunnerWorkspaceOperationCheckpoint})
	if call("", string(body)) != 401 {
		t.Fatal("empty configured token exposed files")
	}
	e.controlToken = "test-token"
	if call("wrong", string(body)) != 401 || call("test-token", `{"runId":"missing","operation":"checkpoint"}`) != 404 || call("test-token", `{"runId":"checkpoint-run","operation":"terminal"}`) != 400 {
		t.Fatal("invalid service request accepted")
	}
	if call("test-token", string(body)) != 200 {
		t.Fatal("authorized capture failed")
	}
	e.WorkspaceActive = func(domain.Run) bool { return true }
	if call("test-token", string(body)) != 409 {
		t.Fatal("active files captured")
	}
}

func TestConversationWorkspaceSplitWorkerSavesReclaimsAndRestoresOnOwner(t *testing.T) {
	worker, id := checkpointFixture(t)
	worker.workspaceHost = "checkpoint-worker"
	worker.controlToken = "shared-test-token"
	run, _ := worker.repo.RunState(id)
	run.WorkspaceHost = worker.workspaceHost
	run.WorkspaceRoot = worker.sandboxes.Root()
	// Bind columns are owned by lease operations; PutRun intentionally cannot
	// change them. Set up the two-host fixture explicitly in its ephemeral DB.
	if err := worker.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{"workspace_host": run.WorkspaceHost, "workspace_root": run.WorkspaceRoot}).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(worker.HandleWorkspaceCheckpoint))
	defer server.Close()
	if err := worker.repo.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: "checkpoint-worker", Role: "worker", WorkspaceHost: worker.workspaceHost, ControlURL: server.URL}, time.Minute); err != nil {
		t.Fatal(err)
	}
	controller := New(worker.repo, runtime.NewManager(t.TempDir()))
	controller.workspaceHost = "checkpoint-controller"
	controller.controlToken = worker.controlToken
	controller.reconcileTerminalWorkspaceCleanup()
	run, _ = worker.repo.RunState(id)
	if run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("worker workspace not marked reclaimed")
	}
	if _, err := os.Stat(worker.RunWorkspace(id)); !os.IsNotExist(err) {
		t.Fatal("owner never removed workspace")
	}
	token, err := controller.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if err = controller.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(worker.RunWorkspace(id), "README.md"))
	if err != nil || string(body) != "uncommitted" {
		t.Fatal("owner files not restored", err)
	}
	run, _ = worker.repo.RunState(id)
	if run.WorkspaceHost != worker.workspaceHost || run.Status != "succeeded" {
		t.Fatal("restore moved workspace to API host")
	}
}

func TestIdleTaskGroupWithoutChatSessionGetsOneHour(t *testing.T) {
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.Status = "waiting"
	run.WorkflowSnapshot.CollaborationMode = "group"
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run, _ = e.repo.RunState(id)
	e.cleanupWorkspace(run, run.UpdatedAt.Add(59*time.Minute))
	run, _ = e.repo.RunState(id)
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if !run.WorkspaceCleanedAt.IsZero() || !meta.FollowUp {
		t.Fatal("group did not receive one hour idle retention")
	}
	e.cleanupWorkspace(run, meta.LastUsedAt.Add(time.Hour))
	run, _ = e.repo.RunState(id)
	if run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("idle group was never reclaimed")
	}
}

// Chat leases are metadata, not evidence that an old checkout was archived.
func TestLegacyWorkspaceReplayAfterMainBotCreatedEmptyCheckpoint(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Minute, false)
	wf := domain.WorkflowDefinition{ID: "legacy-replay", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "first", Type: "command", Command: "true"}, {ID: "second", Type: "command", Command: "true"}}, Edges: []domain.WorkflowEdge{{Source: "first", Target: "second", When: "success"}}}
	r := domain.Run{ID: "legacy-replay", Status: "succeeded", WorkflowID: wf.ID, WorkflowSnapshot: wf, WorkspaceCleanedAt: time.Now().UTC(), Nodes: map[string]*domain.NodeRun{"first": {NodeID: "first", Status: "succeeded", Attempt: 1, Output: "archived output"}, "second": {NodeID: "second", Status: "succeeded", Attempt: 1}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	// Reproduce the failed Main Bot call made after a pre-upgrade cleanup.
	token, err := e.BeginConversationWorkspaceUse(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), r.ID, token); err == nil {
		t.Fatal("missing history was reported restored")
	}
	if err = e.RestoreConversationWorkspaceIfNeeded(context.Background(), r.ID); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("active Main Bot lease was bypassed: %v", err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), r.ID, token); err != nil {
		t.Fatal(err)
	}
	meta, ok := s.WorkspaceCheckpoint(r.ID)
	if !ok || meta.SavedAt != nil {
		t.Fatalf("not an empty lease-only checkpoint: %+v", meta)
	}
	// This is the HTTP retry preflight, then the engine's own retry preflight.
	if err = e.RestoreConversationWorkspaceIfNeeded(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Retry(r.ID, "second", "重新执行", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(r.ID)
	if got.Status != "running" || !got.WorkspaceCleanedAt.IsZero() || got.Nodes["first"].Attempt != 1 || got.Nodes["first"].Output != "archived output" || got.Nodes["second"].Status != "pending" {
		t.Fatalf("legacy replay failed: %+v", got)
	}
	// Skipping an absent snapshot must not create files or claim it was restored.
	if _, err = os.Stat(e.RunWorkspace(r.ID)); !os.IsNotExist(err) {
		t.Fatalf("legacy preflight fabricated a restored checkout: %v", err)
	}
}

func TestExecutionRestoreDoesNotIgnoreCorruptSavedCheckpoint(t *testing.T) {
	e, id := checkpointFixture(t)
	e.reconcileTerminalWorkspaceCleanup()
	if err := e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("sha256", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreConversationWorkspaceIfNeeded(context.Background(), id); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("corrupt saved data silently replaced: %v", err)
	}
	r, _ := e.repo.RunState(id)
	if r.WorkspaceCleanedAt.IsZero() || r.Status != "succeeded" {
		t.Fatal("corrupt checkpoint mutated run state")
	}
}
