package engine

import (
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

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

type cvESandbox struct {
	id, workspace string
	bindErr       error
	closed        bool
	detached      bool
}

func (s *cvESandbox) ID() string        { return s.id }
func (s *cvESandbox) Workspace() string { return s.workspace }
func (s *cvESandbox) Exec(_ context.Context, command runtime.CommandSpec, sink runtime.EventSink) (string, error) {
	if sink != nil {
		sink(domain.TelemetryEvent{Name: "fake.exec", Message: strings.Join(command.Argv, " ")})
	}
	return strings.Join(command.Argv, " "), nil
}
func (s *cvESandbox) Close(context.Context) error         { s.closed = true; return nil }
func (s *cvESandbox) Driver() string                      { return "fake" }
func (s *cvESandbox) Recreate(context.Context) error      { return s.bindErr }
func (s *cvESandbox) BindWorkspace(context.Context) error { return s.bindErr }
func (s *cvESandbox) Detach() error                       { s.detached = true; return nil }

func TestRunEnvironmentMethodsOutputsAndResources(t *testing.T) {
	sb := &cvESandbox{id: "sandbox", workspace: t.TempDir()}
	env := &runEnvironment{sandbox: sb}
	if env.ID() != "sandbox" || env.Workspace() != sb.workspace || env.Driver() != "fake" {
		t.Fatal("environment identity")
	}
	var observed bool
	if out, err := env.Exec(context.Background(), runtime.CommandSpec{Argv: []string{"echo"}}, func(domain.TelemetryEvent) { observed = true }); err != nil || out != "echo" || !observed {
		t.Fatalf("exec = %q %v %v", out, err, observed)
	}
	if err := env.Recreate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.BindWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.Detach(); err != nil || !sb.detached {
		t.Fatal("detach")
	}
	if err := env.Close(context.Background()); err != nil || !sb.closed {
		t.Fatal("close")
	}
	calls := 0
	if err := env.prepareRepositoriesOnce(func() error { calls++; return errors.New("first") }); err == nil {
		t.Fatal("prepare error lost")
	}
	if err := env.prepareRepositoriesOnce(func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := env.prepareRepositoriesOnce(func() error { calls++; return nil }); err != nil || calls != 2 {
		t.Fatalf("prepare calls = %d %v", calls, err)
	}
	outputs := env.beginReportedOutputs("node", []domain.RunnerInputFile{{Path: "input", Content: "x"}})
	if outputs == nil || env.reportedOutputs("node") != outputs {
		t.Fatal("reported outputs")
	}
	env.endReportedOutputs("node")
	if env.reportedOutputs("node") != nil {
		t.Fatal("reported outputs remained")
	}

	e, st := cvEEngine(t)
	if _, _, err := e.workspaceSandboxProfile(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "missing"}}); err == nil {
		t.Fatal("missing profile accepted")
	}
	profile, resources, err := e.workspaceSandboxProfile(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}})
	if err != nil || profile.ID != "local" {
		t.Fatalf("profile = %+v %+v %v", profile, resources, err)
	}
	snapshot := &domain.RunnerSandboxSpec{Profile: profile, Dockerfile: &domain.DockerfileDefinition{ID: "d"}, Environment: &domain.EnvironmentDefinition{ID: "e"}}
	_, resources, err = e.workspaceSandboxProfile(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxSnapshot: snapshot}})
	if err != nil || resources.Dockerfile.ID != "d" || resources.Environment.ID != "e" {
		t.Fatalf("snapshot resources = %+v %v", resources, err)
	}
	if st == nil {
		t.Fatal("store")
	}
}

func TestLocalWorkspaceCheckpointHandlerOperations(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	e.controlToken = "secret"
	run := domain.Run{ID: "local-control", Status: "failed", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(run.ID)
	if root == "" {
		t.Fatal("empty run workspace")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := func(request domain.RunnerWorkspaceRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(request)
		req := httptest.NewRequest(http.MethodPost, "/internal/workspace-checkpoint", strings.NewReader(string(body)))
		req.Header.Set(controlTokenHeader, "secret")
		rec := httptest.NewRecorder()
		e.HandleWorkspaceCheckpoint(rec, req)
		return rec
	}
	checkpoint := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationCheckpoint})
	if checkpoint.Code != 200 || !strings.Contains(checkpoint.Body.String(), "checkpoint") {
		t.Fatalf("checkpoint = %d %s", checkpoint.Code, checkpoint.Body.String())
	}
	status := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationCheckpointStatus})
	if status.Code != 200 {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	artifacts := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationArtifacts, ArtifactFiles: []domain.WorkspaceArtifactFile{{Path: "restored.txt", Content: "restored", SHA256: runtime.WorkspaceContentSHA("restored")}}})
	if artifacts.Code != 200 || !strings.Contains(artifacts.Body.String(), "restoredArtifacts") {
		t.Fatalf("artifacts = %d %s", artifacts.Code, artifacts.Body.String())
	}
	if body, _ := os.ReadFile(filepath.Join(root, "restored.txt")); string(body) != "restored" {
		t.Fatalf("restored file = %q", body)
	}
	restore := call(domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: domain.RunnerWorkspaceOperationRestore, Checkpoint: []byte("bad")})
	if restore.Code != 200 || !strings.Contains(restore.Body.String(), "error") {
		t.Fatalf("bad restore = %d %s", restore.Code, restore.Body.String())
	}
}

func TestWorkspaceCleanupDeleteAndFacts(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	if e.RunWorkspace("missing") == "" {
		t.Fatal("workspace path missing")
	}
	if _, ok := e.RunWorkspaceFacts("facts"); ok {
		t.Fatal("unexpected facts")
	}
	e.recordWorkspaceFacts("facts", nil)
	e.recordWorkspaceFacts("facts", &domain.RunnerTaskOutputs{Facts: &domain.RunnerWorkspaceFacts{Frontend: true}})
	if facts, ok := e.RunWorkspaceFacts("facts"); !ok || !facts.Frontend {
		t.Fatalf("facts = %+v %v", facts, ok)
	}
	e.recordWorkspaceFacts("facts", &domain.RunnerTaskOutputs{})

	run := domain.Run{ID: "cleanup-local", Status: "succeeded", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(run.ID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	e.cleanupRunWorkspace(run.ID)
	got, _ := st.RunState(run.ID)
	if got.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("workspace not marked cleaned: %+v", got)
	}
	e.cleanupRunWorkspace(run.ID)

	deleted := domain.Run{ID: "delete-local", Status: "failed", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(deleted); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.RunWorkspace(deleted.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteRun(deleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.RunState(deleted.ID); ok {
		t.Fatal("deleted run remained")
	}
	e.destroySandbox("absent")
	e.discardLocalSandbox("absent")
	if count := e.discardIdleRunContainers(context.Background(), time.Now()); count != 0 {
		t.Fatalf("idle discard = %d", count)
	}
}

func TestPreviewSkipAndArchiveNilPaths(t *testing.T) {
	e, st := cvEEngine(t)
	run := domain.Run{ID: "preview-helper", Status: "running", Generation: 0, WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true}}, Nodes: map[string]*domain.NodeRun{
		domain.PreviewBuildNodeID: {NodeID: domain.PreviewBuildNodeID, Status: "running", Attempt: 1},
	}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.archivePreviewAfterBuild("missing")
	e.archivePreviewAfterBuild(run.ID)
	node := domain.WorkflowNode{ID: domain.PreviewBuildNodeID, Name: "Preview"}
	e.completeSkippedPreviewBuild(run.ID, node, 0, 1, run, "no package.json")
	got, _ := st.RunState(run.ID)
	if got.Nodes[node.ID].Status != "succeeded" || !strings.Contains(got.Nodes[node.ID].Output, "no package.json") {
		t.Fatalf("skipped preview = %+v", got.Nodes[node.ID])
	}
}
