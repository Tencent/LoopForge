package engine

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func enablePreview(t *testing.T, engine *Engine, store *repo.Store) *preview.Service {
	t.Helper()
	svc := preview.NewService(store, preview.NewDBStore(store), preview.Config{
		PublicScheme:        "http",
		FrameAncestors:      "'self'",
		MaxFiles:            50,
		MaxCompressed:       1 << 20,
		MaxExpanded:         4 << 20,
		DefaultRetention:    24 * time.Hour,
		FinalizationTimeout: time.Minute,
		SessionTTL:          time.Minute,
	})
	engine.SetPreview(svc)
	return svc
}

func TestPendingPreviewHoldsWorkspaceCleanup(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(t.TempDir()))
	enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-hold", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPreview(domain.RunPreview{
		ID: "preview-preview-hold", RunID: run.ID, Kind: "static", Status: domain.PreviewPending, SourcePath: "dist", EntryFile: "index.html",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunWorkspaceCleanupHold(run.ID, time.Now().UTC().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}

	engine.reconcileTerminalWorkspaceCleanup()

	kept, _ := store.Run(run.ID)
	if !kept.WorkspaceCleanedAt.IsZero() {
		t.Fatal("pending preview must hold workspace cleanup")
	}
}

func TestReadyPreviewReleasesWorkspaceCleanup(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	svc := enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-ready", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !svc.BeginFinalization(run) {
		t.Fatal("expected preview finalization to hold cleanup")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := store.PreviewByRun(run.ID)
		if ok && p.Status == domain.PreviewReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, ok := store.PreviewByRun(run.ID)
	if !ok || p.Status != domain.PreviewReady {
		t.Fatalf("preview not ready: %+v", p)
	}
	// Ready is persisted before the asynchronous finalizer clears its cleanup
	// hold and releases the workspace. Wait for that observable completion,
	// rather than racing it with a single cleanup pass and closing the store.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		engine.reconcileTerminalWorkspaceCleanup()
		cleaned, _ := store.Run(run.ID)
		_, statErr := os.Stat(workspace)
		if !cleaned.WorkspaceCleanedAt.IsZero() && os.IsNotExist(statErr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ready preview should release workspace cleanup")
}

func TestBeginFinalizationSkipsNonFrontendWorkspace(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	svc := enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-skip-go", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module inventory-api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if svc.BeginFinalization(run) {
		t.Fatal("non-frontend workspace must not hold cleanup for archive")
	}
	p, ok := store.PreviewByRun(run.ID)
	if !ok || p.Status != domain.PreviewSkipped {
		t.Fatalf("preview status = %+v ok=%v", p, ok)
	}
	if svc.BeginFinalization(run) {
		t.Fatal("skipped preview must not hold cleanup on a second pass")
	}
	again, _ := store.PreviewByRun(run.ID)
	if again.Status != domain.PreviewSkipped {
		t.Fatalf("second finalization revived skip: %+v", again)
	}
	engine.reconcileTerminalWorkspaceCleanup()
	cleaned, _ := store.Run(run.ID)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		t.Fatal("skipped preview should still clean the workspace")
	}
}

func TestStartInjectsPreviewBuildNode(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	wf := domain.WorkflowDefinition{
		ID: "preview-auto-build", Name: "preview", SandboxProfileID: "local",
		Preview: &domain.PreviewContract{Enabled: true, Model: "gpt-5.6-terra"},
		Nodes: []domain.WorkflowNode{
			{ID: "impl", Name: "实现", Type: "script", ScriptLanguage: "shell", Script: "printf ok\n", MaxAttempts: 1},
		},
	}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	engine := New(store, runtime.NewManager(t.TempDir()))
	engine.ConfigureExecution("test-instance", "localhost", time.Minute, false)
	run, err := engine.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.WorkflowSnapshot.Nodes) != 2 {
		t.Fatalf("snapshot nodes = %d, want injected preview-build", len(run.WorkflowSnapshot.Nodes))
	}
	last := run.WorkflowSnapshot.Nodes[1]
	if last.ID != domain.PreviewBuildNodeID || last.AgentID != domain.PreviewBuildAgentID {
		t.Fatalf("injected node = %+v", last)
	}
	if run.Nodes[domain.PreviewBuildNodeID] == nil || run.Nodes[domain.PreviewBuildNodeID].Status != "pending" {
		t.Fatalf("preview-build node run = %+v", run.Nodes[domain.PreviewBuildNodeID])
	}
	stored, _ := store.Workflow(wf.ID)
	if len(stored.Nodes) != 1 {
		t.Fatalf("catalog SOP must stay without the injected node: %+v", stored.Nodes)
	}
	agent, ok := workflowAgent(store, run.WorkflowSnapshot, domain.PreviewBuildAgentID)
	if !ok || agent.ExecutorID != domain.PreviewBuildExecutor || agent.Model != "gpt-5.6-terra" {
		t.Fatalf("synthetic agent = %+v ok=%v", agent, ok)
	}
}

func TestCancelledRunDoesNotCreatePreview(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(t.TempDir()))
	enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-cancel", Status: "cancelled",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	engine.reconcileTerminalWorkspaceCleanup()
	if _, ok := store.PreviewByRun(run.ID); ok {
		t.Fatal("cancelled run must not create a preview")
	}
	cleaned, _ := store.Run(run.ID)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		t.Fatal("cancelled run should still clean immediately")
	}
}

func TestRecoverMissingPreviewFinalizerHoldsWorkspace(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-crash-gap", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(workspace, "dist", "index.html")
	if err := os.WriteFile(index, []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	engine.recoverMissingPreviewFinalizers()
	if _, ok := store.PreviewByRun(run.ID); !ok {
		t.Fatal("crash recovery must create the missing preview row")
	}
	engine.reconcileTerminalWorkspaceCleanup()
	p, _ := store.PreviewByRun(run.ID)
	if p.Status == domain.PreviewPending || p.Status == domain.PreviewArchiving {
		if _, err := os.Stat(index); err != nil {
			t.Fatal("workspace must survive cleanup until archive finishes")
		}
		kept, _ := store.Run(run.ID)
		if !kept.WorkspaceCleanedAt.IsZero() {
			t.Fatal("recovered pending preview must hold workspace cleanup")
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := store.PreviewByRun(run.ID)
		if ok && p.Status == domain.PreviewReady {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, _ = store.PreviewByRun(run.ID)
	t.Fatalf("recovered preview never became ready: %+v", p)
}

func TestSucceededPreviewWithoutRecordLosesWorkspaceWithoutRecover(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-lost-without-recover", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(workspace, "dist", "index.html")
	if err := os.WriteFile(index, []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	engine.reconcileTerminalWorkspaceCleanup()
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatal("without recovery, terminal cleanup deletes the workspace")
	}
	if _, ok := store.PreviewByRun(run.ID); ok {
		t.Fatal("cleanup-only path must not invent a preview")
	}
}

func TestDeleteRunRevokesPreviewSession(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	svc := enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-delete-run", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !svc.BeginFinalization(run) {
		t.Fatal("expected preview finalization")
	}
	deadline := time.Now().Add(5 * time.Second)
	var ready domain.RunPreview
	for time.Now().Before(deadline) {
		p, ok := store.PreviewByRun(run.ID)
		if ok && p.Status == domain.PreviewReady {
			ready = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready.Status != domain.PreviewReady {
		t.Fatalf("preview not ready: %+v", ready)
	}
	session, urlStr, err := svc.CreateSession(ready, nil, "")
	if err != nil || urlStr == "" || session.ID == "" {
		t.Fatalf("session %v %s", err, urlStr)
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, ok := preview.ParsePreviewPath(u.Path)
	if !ok {
		t.Fatalf("token from %s", urlStr)
	}
	if _, _, ok := svc.LookupSession(plain); !ok {
		t.Fatalf("session should be live before delete: %s", urlStr)
	}
	if err := engine.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.PreviewByRun(run.ID); ok {
		t.Fatal("preview metadata survived run delete")
	}
	if _, _, ok := svc.LookupSession(plain); ok {
		t.Fatal("preview URL must stop working after run delete")
	}
}

func TestWaitingRunArchivesPreviewAfterBuild(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	svc := enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-waiting-build", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes: map[string]*domain.NodeRun{
			domain.PreviewBuildNodeID: {NodeID: domain.PreviewBuildNodeID, Status: "succeeded"},
			"confirm-demo":            {NodeID: "confirm-demo", Status: "waiting_approval"},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(workspace, "dist", "index.html")
	if err := os.WriteFile(index, []byte("<html>demo</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	engine.recoverBuiltPreviewArchives()
	deadline := time.Now().Add(5 * time.Second)
	var ready domain.RunPreview
	for time.Now().Before(deadline) {
		p, ok := store.PreviewByRun(run.ID)
		if ok && p.Status == domain.PreviewReady {
			ready = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready.Status != domain.PreviewReady {
		t.Fatalf("preview not ready while the run is still waiting: %+v", ready)
	}
	if svc.BeginFinalization(run) {
		t.Fatal("a ready preview must not be archived again when the run later succeeds")
	}
	engine.reconcileTerminalWorkspaceCleanup()
	if _, err := os.Stat(index); err != nil {
		t.Fatal("workspace must stay until the run itself finishes")
	}
	kept, _ := store.Run(run.ID)
	if kept.Status != "waiting" || !kept.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("waiting run was cleaned: status=%s cleaned=%v", kept.Status, kept.WorkspaceCleanedAt)
	}
}

func TestWaitingRunnerPreviewDoesNotSkipEmptyControlPlane(t *testing.T) {
	root := t.TempDir()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(root))
	enablePreview(t, engine, store)
	if err := store.PutRunner(domain.Runner{ID: "runner-preview", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "preview-waiting-runner", Status: "waiting",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner-preview"},
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes: map[string]*domain.NodeRun{
			domain.PreviewBuildNodeID: {NodeID: domain.PreviewBuildNodeID, Status: "succeeded"},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := engine.sandboxes.RunWorkspace(run.ID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	engine.recoverBuiltPreviewArchives()
	p, ok := store.PreviewByRun(run.ID)
	if !ok || p.Status != domain.PreviewArchiving {
		t.Fatalf("runner preview = %+v ok=%v", p, ok)
	}
	task, ok := store.RunnerTask("rt-preview-archive-" + run.ID)
	if !ok || task.Status != domain.RunnerTaskQueued || task.Action != domain.RunnerTaskActionArchivePreview {
		t.Fatalf("archive task = %+v ok=%v", task, ok)
	}
	kept, _ := store.Run(run.ID)
	if !kept.WorkspaceCleanedAt.IsZero() {
		t.Fatal("queueing the runner archive cleaned a waiting run")
	}
}

func TestSkippedPreviewIsNotRevivedWhileWaiting(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(t.TempDir()))
	enablePreview(t, engine, store)
	run := domain.Run{
		ID: "preview-waiting-skip", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
		Nodes: map[string]*domain.NodeRun{
			domain.PreviewBuildNodeID: {NodeID: domain.PreviewBuildNodeID, Status: "succeeded"},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPreview(domain.RunPreview{
		ID: "preview-preview-waiting-skip", RunID: run.ID, Kind: "static", Status: domain.PreviewSkipped, SourcePath: "dist", EntryFile: "index.html",
	}); err != nil {
		t.Fatal(err)
	}
	engine.recoverBuiltPreviewArchives()
	p, _ := store.PreviewByRun(run.ID)
	if p.Status != domain.PreviewSkipped {
		t.Fatalf("recovery revived a skipped preview: %+v", p)
	}
}
