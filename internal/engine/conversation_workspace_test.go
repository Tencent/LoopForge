package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNodeCheckpointArchiveIsFilteredOnlyAfterCleanupClaim(t *testing.T) {
	e, id := checkpointFixture(t)
	root := e.RunWorkspace(id)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-q")
	git(root, "add", ".")
	git(root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	if _, err := gitwork.CreateNodeCheckpoint(context.Background(), root, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	run, _ := e.repo.RunState(id)
	run.ExecutionContext.NodeCheckpointsEnabled = true
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	stale, _ := e.repo.RunState(id)
	if err := e.repo.PutRun(stale); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _, err := e.claimIdleConversationWorkspace(stale, time.Now().UTC()); err != nil || claimed {
		t.Fatalf("stale cleanup: claimed=%v err=%v", claimed, err)
	}
	assertArchiveRefs := func(want bool) {
		t.Helper()
		archive, err := e.repo.WorkspaceCheckpointArchive(id)
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(t.TempDir(), "restored")
		if err := runtime.RestoreWorkspaceCheckpoint(dest, archive); err != nil {
			t.Fatal(err)
		}
		if got := git(dest, "for-each-ref", "--format=%(refname)", "refs/loopforge"); (got != "") != want {
			t.Fatalf("archived refs = %q, want present=%v", got, want)
		}
	}
	assertArchiveRefs(true)
	current, _ := e.repo.RunState(id)
	_, claimed, release, err := e.claimIdleConversationWorkspace(current, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("cleanup: claimed=%v err=%v", claimed, err)
	}
	defer release()
	assertArchiveRefs(false)
}

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
	t.Parallel()
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
	t.Parallel()
	e, id := checkpointFixture(t)
	root := e.RunWorkspace(id)
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /outside\n"), 0600); err != nil {
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
	if err := os.Remove(filepath.Join(root, ".git")); err != nil {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestIdleWorkspaceWithoutSharedChatGetsOneHour(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.Status = "waiting"
	run.Kind = domain.RunKindWorkspace
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

func TestReleasedConversationWorkspaceSkipsIdleHourUntilUsedAgain(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.Status = "waiting"
	run.Kind = domain.RunKindWorkspace
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	run, _ = e.repo.RunState(id)
	e.cleanupWorkspace(run, time.Now().UTC())
	if run, _ = e.repo.RunState(id); !run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("a recently used workspace was reclaimed")
	}

	// A workspace used after its release keeps the idle hour.
	now := time.Now().UTC()
	if err = e.repo.ReleaseWorkspace(id, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = e.repo.TouchWorkspaceUse(id, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(run, now.Add(3*time.Second))
	if run, _ = e.repo.RunState(id); !run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("a workspace used again after release was reclaimed")
	}

	if err = e.repo.ReleaseWorkspace(id, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(run, now.Add(5*time.Second))
	if run, _ = e.repo.RunState(id); run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("released workspace waited out the idle hour")
	}
	token, err = e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), id, token)
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil || string(body) != "uncommitted" {
		t.Fatalf("switching back lost files: %q %v", body, err)
	}
}

// Merge compatibility: child SOPs now use their parent's checkout. Only the
// owning run may checkpoint/restore it; child completion must leave it intact.
func TestChildWorkspaceCheckpointBelongsToParent(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	parent, _ := e.repo.RunState(id)
	parent.Status = "running"
	if err := e.repo.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	child := domain.Run{ID: "checkpoint-child", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ParentRunID: id, WorkspaceRunID: id}, Nodes: map[string]*domain.NodeRun{}}
	if err := e.repo.PutRun(child); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(child, time.Now().UTC())
	if _, saved := e.repo.WorkspaceCheckpoint(child.ID); saved {
		t.Fatal("child archived the active parent checkout")
	}
	if data, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil || string(data) != "uncommitted" {
		t.Fatal("child cleanup removed parent files", err)
	}
	token, err := e.BeginConversationWorkspaceUse(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), child.ID, token)
	if err := e.EnsureConversationWorkspace(context.Background(), child.ID, token); err == nil {
		t.Fatal("child independently restored the parent checkout")
	}
}

// Chat leases are metadata, not evidence that an old checkout was archived.
func TestLegacyWorkspaceReplayAfterMainBotCreatedEmptyCheckpoint(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// A runner only checks repositories out on a prepare request, so opening a
// fresh chat workspace must ask for one before the first read.
func TestMaterializeRunnerConversationWorkspaceRequestsCheckout(t *testing.T) {
	e, s := newRecoveryEngine(t)
	if err := s.PutRunner(domain.Runner{ID: "chat-runner", RunnerVersion: domain.RunnerVersionConversationWorkspace, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "chat-workspace", Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "chat-runner", RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "web", Name: "web", CloneURL: "https://git.example.com/team/web.git", BaseBranch: "main", WorkBranch: "feature/chat", WorkspaceDir: "web"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	var requests []domain.RunnerWorkspaceRequest
	busy := 1
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		requests = append(requests, request)
		if busy > 0 {
			busy--
			return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is in use; preparation waits for active execution or terminal to finish"}, nil
		}
		raw, _ := json.Marshal(runtime.WorkspacePreparationStatus{Status: "ready", Steps: []runtime.WorkspacePreparationStep{}})
		return domain.RunnerWorkspaceResponse{Payload: raw}, nil
	}
	if err := e.MaterializeConversationWorkspace(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("busy runner must be retried once, got %d requests", len(requests))
	}
	got := requests[1]
	if got.Operation != domain.RunnerWorkspaceOperationPrepare || got.RefreshCleanRepositories || got.PreparationInvocation == nil || len(got.PreparationInvocation.Repositories) != 1 || got.PreparationInvocation.Repositories[0].WorkBranch != "feature/chat" || got.Sandbox == nil || got.Workflow == nil {
		t.Fatalf("checkout request = %+v", got)
	}

	// Git rejecting the clone names the fix instead of a raw git error.
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Status: 400, Error: "prepare workspace repositories: git clone https://git.example.com/team/web.git: exit status 128 (fatal: Authentication failed)"}, nil
	}
	if err := e.MaterializeConversationWorkspace(context.Background(), run.ID); err == nil || !strings.Contains(err.Error(), "Git Token") {
		t.Fatalf("auth failure not explained: %v", err)
	}

	// Once a snapshot exists the workspace has been materialized; later tool
	// calls must not reach the runner again.
	saved := time.Now().UTC()
	if err := s.DB.Create(&repo.WorkspaceCheckpoint{RunID: run.ID, LastUsedAt: saved, SavedAt: &saved}).Error; err != nil {
		t.Fatal(err)
	}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		t.Fatalf("saved workspace re-prepared: %s", request.Operation)
		return domain.RunnerWorkspaceResponse{}, nil
	}
	if err := e.MaterializeConversationWorkspace(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
}
