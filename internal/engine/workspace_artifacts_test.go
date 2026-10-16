package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func archivedTestFile(node, path, content string, at time.Time) domain.Artifact {
	return domain.Artifact{ID: node + path, NodeID: node, Name: node, Path: path, Content: content, SHA256: runtime.WorkspaceContentSHA(content), CreatedAt: at}
}
func TestLegacyConversationRefillsOnlyMissingLatestArtifacts(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now()
	r := domain.Run{ID: "legacy-artifacts", Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{}, Artifacts: []domain.Artifact{archivedTestFile("design", "docs/design.md", "latest", now), archivedTestFile("design-old", "docs/design.md", "old", now.Add(-time.Minute)), archivedTestFile("implement", "change-report.md", "old change", now)}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(r.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "change-report.md"), []byte("group modification"), 0644); err != nil {
		t.Fatal(err)
	}
	token, err := e.BeginConversationWorkspaceUse(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), r.ID, token)
	restored, err := e.PrepareConversationWorkspace(context.Background(), r.ID, token)
	if err != nil || len(restored) != 1 || restored[0] != "docs/design.md" {
		t.Fatalf("restored %v %v", restored, err)
	}
	for path, want := range map[string]string{"docs/design.md": "latest", "change-report.md": "group modification"} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s %s %v", path, got, err)
		}
	}
	got, _ := s.RunState(r.ID)
	if got.Status != "waiting" || !got.PauseRequested {
		t.Fatal("reconstruction changed SOP state")
	}
	if _, err := e.PrepareConversationWorkspace(context.Background(), r.ID, token); err != nil {
		t.Fatal(err)
	}
}
func TestArchivedReplayDoesNotResurrectCurrentOrDownstreamOutputs(t *testing.T) {
	now := time.Now()
	r := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a"}, {ID: "b", ArtifactOutputs: []domain.ArtifactContract{{Path: "shared-report.md"}}}, {ID: "c"}}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}}}, Nodes: map[string]*domain.NodeRun{"a": {Status: "succeeded"}, "b": {Status: "running"}, "c": {Status: "pending"}}, Artifacts: []domain.Artifact{archivedTestFile("a", "input.md", "upstream", now), archivedTestFile("a", "shared-report.md", "older upstream report", now), archivedTestFile("b", "current.md", "stale output", now), archivedTestFile("c", "downstream.md", "stale downstream", now)}}
	files := archivedWorkspaceFiles(r, "b")
	if len(files) != 1 || files[0].Path != "input.md" {
		t.Fatalf("stale result resurrected: %+v", files)
	}
	r.WorkflowSnapshot.CollaborationMode = "group"
	r.WorkflowSnapshot.Edges = nil
	files = archivedWorkspaceFiles(r, "b")
	for _, f := range files {
		if f.Path == "shared-report.md" || f.Path == "current.md" {
			t.Fatal("group current output restored")
		}
	}
}
func TestRealSnapshotKeepsIntentionalArtifactDeletion(t *testing.T) {
	e, id := checkpointFixture(t)
	r, _ := e.repo.RunState(id)
	r.Artifacts = []domain.Artifact{archivedTestFile("old", "deleted.md", "must stay deleted", time.Now())}
	if err := e.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), id, token)
	if err := e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.RunWorkspace(id), "deleted.md")); !os.IsNotExist(err) {
		t.Fatal("real snapshot's deletion was undone")
	}
}

func TestArchivedFilesKeepRepositoryScope(t *testing.T) {
	a := archivedTestFile("design", "docs/design.md", "scoped report", time.Now())
	a.RepositoryDir = "backend"
	r := domain.Run{Artifacts: []domain.Artifact{a}, Nodes: map[string]*domain.NodeRun{"design": {Status: "succeeded"}}}
	files := archivedWorkspaceFiles(r, "")
	if len(files) != 1 || files[0].Path != "backend/docs/design.md" {
		t.Fatalf("lost repository scope: %+v", files)
	}
	r.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "current", ExecutionScope: domain.ExecutionScopeRepositories, ArtifactOutputs: []domain.ArtifactContract{{Path: "docs/design.md"}}}}
	r.Repositories = []domain.RepositoryContext{{WorkspaceDir: "backend"}}
	if got := archivedWorkspaceFiles(r, "current"); len(got) != 0 {
		t.Fatalf("resurrected current scoped output: %+v", got)
	}
}

func TestIdleRunningGroupRefillsArchivedFiles(t *testing.T) {
	e, id := checkpointFixture(t)
	r, _ := e.repo.RunState(id)
	r.Status = "running"
	r.WorkflowSnapshot.CollaborationMode = "group"
	r.Artifacts = []domain.Artifact{archivedTestFile("design", "design.md", "archived report", time.Now())}
	if err := e.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), id, token)
	restored, err := e.PrepareConversationWorkspace(context.Background(), id, token)
	if err != nil || len(restored) != 1 {
		t.Fatal(restored, err)
	}
}

func TestLegacyArchivedFilesRefillOnOwningWorker(t *testing.T) {
	worker, id := checkpointFixture(t)
	worker.workspaceHost = "artifact-worker"
	worker.controlToken = "test-artifact-control"
	run, _ := worker.repo.RunState(id)
	run.Status = "waiting"
	run.PauseRequested = true
	run.Artifacts = []domain.Artifact{archivedTestFile("design", "design.md", "worker report", time.Now())}
	if err := worker.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := worker.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{"workspace_host": worker.workspaceHost, "workspace_root": worker.sandboxes.Root()}).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(worker.HandleWorkspaceCheckpoint))
	defer server.Close()
	if err := worker.repo.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: "artifact-worker", Role: "worker", WorkspaceHost: worker.workspaceHost, ControlURL: server.URL}, time.Minute); err != nil {
		t.Fatal(err)
	}
	controller := New(worker.repo, runtime.NewManager(t.TempDir()))
	controller.workspaceHost = "artifact-controller"
	controller.controlToken = worker.controlToken
	token, err := controller.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.EndConversationWorkspaceUse(context.Background(), id, token)
	restored, err := controller.PrepareConversationWorkspace(context.Background(), id, token)
	if err != nil || len(restored) != 1 {
		t.Fatal(restored, err)
	}
	b, err := os.ReadFile(filepath.Join(worker.RunWorkspace(id), "design.md"))
	if err != nil || string(b) != "worker report" {
		t.Fatal(string(b), err)
	}
	if _, err := os.Stat(controller.RunWorkspace(id)); !os.IsNotExist(err) {
		t.Fatal("created files on API machine", err)
	}
	controller.controlToken = "wrong-token"
	run, _ = worker.repo.RunState(id)
	if _, err := controller.restoreArchivedWorkspaceFiles(context.Background(), run); err == nil {
		t.Fatal("unauthorized service wrote files")
	}
}

func TestLegacyNodeReplayCanReadOriginalReportPaths(t *testing.T) {
	e, s := newRecoveryEngine(t)
	wf := domain.WorkflowDefinition{ID: "legacy-original-paths", Name: "legacy original paths", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "design", Type: "command", Command: "printf archived-design > design.md", ArtifactOutputs: []domain.ArtifactContract{{Name: "design", Path: "design.md", Required: true}}, MaxAttempts: 1},
		{ID: "implement", Type: "command", Command: "test \"$(cat design.md)\" = archived-design && test ! -e current.md && printf fresh > current.md", ArtifactOutputs: []domain.ArtifactContract{{Name: "current", Path: "current.md", Required: true}}, MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{Source: "design", Target: "implement", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	waitForWorkspaceCleanup(t, s, r.ID)
	// The cleaned flag is committed before the cleanup lease is released.
	deadline := time.Now().Add(2 * time.Second)
	for {
		meta, _ := s.WorkspaceCheckpoint(r.ID)
		if meta.LeaseToken == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup lease not released")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// This ephemeral SQLite fixture represents completion before snapshots existed.
	if err := s.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", r.ID).Updates(map[string]any{"archive": nil, "saved_at": nil, "sha256": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(r.ID, "implement", "legacy replay", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	got, _ := s.RunState(r.ID)
	if got.Nodes["design"].Attempt != 1 || got.Nodes["implement"].Attempt != 2 {
		t.Fatal("upstream was rerun or chosen node did not rerun")
	}
}

func TestArchivedRefillDoesNotInventVirtualResultFiles(t *testing.T) {
	r := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "log-only"}}}, Artifacts: []domain.Artifact{{NodeID: "log-only", Name: "result", Path: "result.md", Content: "console output"}, {NodeID: "log-only", Name: "repository-patch", Path: "repository.patch", Content: "inventory"}}}
	if got := archivedWorkspaceFiles(r, ""); len(got) != 0 {
		t.Fatal("invented workspace files", got)
	}
}
