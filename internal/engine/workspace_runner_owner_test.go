package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// A platform container records every run's workspace bind with its own
// hostname (engine.go os.Hostname, BindRunWorkspace). Recreating the container
// therefore leaves runs bound to a host that no longer exists. For a run that
// executes on a runner the checkout lives on the runner; the platform-side
// directory only stages prompts and artifacts, so the stale host must not block
// it forever.
func runnerGateRun(id, runnerID, host, root string) domain.Run {
	now := time.Now().UTC()
	return domain.Run{
		ID: id, WorkflowID: "wf-gate", Status: "running",
		WorkspaceHost: host, WorkspaceRoot: root,
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runnerID, ResponsibleUserID: 1,
		},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
			{ID: "gate", Name: "gate", Type: "approval"},
		}},
		Nodes:     map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval"}},
		CreatedAt: now, UpdatedAt: now,
	}
}

// runnerGateEngine builds an execution-enabled engine whose platform hostname
// is new-container, with one online runner.
func runnerGateEngine(t *testing.T) (*Engine, *repo.Store, string) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	root := t.TempDir()
	e := New(s, runtime.NewManagerWithLimit(filepath.Join(root, "workspaces"), 1))
	e.ConfigureExecution("api-new-container", "new-container", time.Minute, true)
	if err := s.PutRunner(domain.Runner{
		ID: "runner-1", Name: "runner-1", Status: domain.RunnerStatusOnline,
		RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation,
	}); err != nil {
		t.Fatal(err)
	}
	return e, s, e.sandboxes.Root()
}

// runnerCheckout is the checkout on the execution machine. It is outside the
// platform root, so losing it would lose work that was never pushed.
func runnerCheckout(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "runner-1", "runs", "the-run", "workspace")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unpushed.patch"), []byte("local change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("work in progress\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertRunnerCheckoutKept(t *testing.T, dir string) {
	t.Helper()
	for name, want := range map[string]string{"unpushed.patch": "local change\n", "notes.md": "work in progress\n"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(body) != want {
			t.Fatalf("runner checkout file %s = %q (%v), want %q", name, body, err, want)
		}
	}
}

// Approving the gate must work: the run continues on its runner, whose
// checkout keeps the unpushed files.
func TestRunnerGateRecoversAfterPlatformHostChange(t *testing.T) {
	t.Parallel()
	e, s, root := runnerGateEngine(t)
	checkout := runnerCheckout(t)
	run := runnerGateRun("the-run", "runner-1", "old-container", root)
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	if err := e.Approve(run.ID, "gate", "checked the diff"); err != nil {
		t.Fatalf("approve a runner gate after the platform host changed: %v", err)
	}
	waitFor(t, s, run.ID, "succeeded")
	got, _ := s.Run(run.ID)
	if got.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("gate status = %s", got.Nodes["gate"].Status)
	}
	if len(runEvents(e, run.ID, "gate.approved")) == 0 {
		t.Fatal("approval was not recorded on the run")
	}
	assertRunnerCheckoutKept(t, checkout)
}

// The next node on that runner repairs the platform staging bind instead of
// reporting that the workspace was lost with the dead host.
func TestRunnerWorkspaceRebindsWithoutReportingLoss(t *testing.T) {
	t.Parallel()
	e, s, root := runnerGateEngine(t)
	checkout := runnerCheckout(t)
	run := runnerGateRun("the-run", "runner-1", "old-container", root)
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	acquired, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.adoptForeignWorkspace(acquired); err != nil {
		t.Fatalf("adopt a runner workspace whose platform host is gone: %v", err)
	}
	got, _ := s.Run(run.ID)
	if got.WorkspaceHost != e.workspaceHost || got.WorkspaceRoot != e.sandboxes.Root() {
		t.Fatalf("bind = %s:%s, want the current platform", got.WorkspaceHost, got.WorkspaceRoot)
	}
	for _, event := range runEvents(e, run.ID, "workspace.rebound") {
		if strings.Contains(event.Message, "丢失") {
			t.Fatalf("rebound reported a lost workspace: %q", event.Message)
		}
	}
	assertRunnerCheckoutKept(t, checkout)
}

// A bind another live instance still serves is not ours to take over.
func TestRunnerGateStaysBlockedByLiveForeignHost(t *testing.T) {
	t.Parallel()
	e, s, root := runnerGateEngine(t)
	run := runnerGateRun("the-run", "runner-1", "other-host", root)
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	heartbeatHost(t, s, "api-other", "other-host")
	acquired, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	if !e.workspaceBlocked(acquired) {
		t.Fatal("a live foreign host must stay blocking")
	}
}

// A checkout claimed by another process on this very host is also not ours.
func TestRunnerGateStaysBlockedBySameHostOtherRoot(t *testing.T) {
	t.Parallel()
	e, s, root := runnerGateEngine(t)
	run := runnerGateRun("the-run", "runner-1", e.workspaceHost, filepath.Join(root, "other-workspace"))
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	acquired, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	if !e.workspaceBlocked(acquired) {
		t.Fatal("a bind to another root on this host must stay blocking")
	}
}

// Reconnecting a runner run never moves it to another machine while the
// original one cannot be reached, so unblocking the gate cannot lose work.
func TestRunnerReconnectKeepsTheOriginalRunner(t *testing.T) {
	t.Parallel()
	e, s, root := runnerGateEngine(t)
	run := runnerGateRun("the-run", "runner-1", "old-container", root)
	run.Nodes["gate"] = &domain.NodeRun{NodeID: "gate", Status: "succeeded"}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunner(domain.Runner{ID: "runner-1", Name: "runner-1", Status: domain.RunnerStatusOffline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Run(run.ID)
	if err := e.validateExecutionReconnect(current, "gate"); err == nil || !strings.Contains(err.Error(), "无法重连") {
		t.Fatalf("offline runner reconnect = %v", err)
	}
	if err := s.PutRunner(domain.Runner{ID: "runner-1", Name: "runner-1", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Run(run.ID)
	current.ExecutionContext.ExecutionRunnerID = "runner-gone"
	if err := s.PutRun(current); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Run(run.ID)
	if err := e.validateExecutionReconnect(current, "gate"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing runner reconnect = %v", err)
	}
}

// Platform-owned checkouts keep their existing protection: a dead host with
// code changes is still reported as lost so a rerun starts from a fresh clone.
func TestCloudRunStillTreatsDeadHostAsLost(t *testing.T) {
	t.Parallel()
	e, s, root := runnerGateEngine(t)
	run := domain.Run{
		ID: "cloud-run", WorkflowID: "wf-gate", Status: "running",
		WorkspaceHost: "old-container", WorkspaceRoot: root,
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Type: "approval"}}},
		Repository:       domain.RepositoryContext{RepositoryID: "repo-1", BaseBranch: "master", WorkBranch: "feature/x"},
		Nodes:            map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "succeeded"}},
		CreatedAt:        time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	acquired, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	if !e.workspaceLost(acquired) {
		t.Fatal("a platform checkout on a dead host with code changes must still be lost")
	}
	if err := e.adoptForeignWorkspace(acquired); err == nil || !strings.Contains(err.Error(), "丢失") {
		t.Fatalf("adopt a lost platform workspace = %v", err)
	}
}
