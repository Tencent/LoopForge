package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestAutoAssignRanksRunningThenOccupiedWithRandomTies(t *testing.T) {
	platform := autoAssignCandidate{}
	owner := autoAssignCandidate{shared: true, runner: domain.Runner{ID: "owner", OwnerUserID: 1, RunLimit: 5}}
	other := autoAssignCandidate{shared: true, runner: domain.Runner{ID: "other", OwnerUserID: 2, RunLimit: 5}}
	queued := autoAssignCandidate{shared: true, runner: domain.Runner{ID: "queued", RunLimit: 5}}
	full := autoAssignCandidate{shared: true, runner: domain.Runner{ID: "full", RunLimit: 1}}
	loads := map[string]domain.RunnerLoad{
		platform.bucket(): {RunningRuns: 2, OccupiedRuns: 2},
		owner.bucket():    {RunningRuns: 1, OccupiedRuns: 4},
		other.bucket():    {RunningRuns: 1, OccupiedRuns: 2},
		queued.bucket():   {QueuedRuns: 1},
		full.bucket():     {OccupiedRuns: 1},
	}
	candidates := []autoAssignCandidate{platform, owner, other, queued, full}
	ranking := rankAutoAssignCandidates(candidates, loads, 5)
	if len(ranking) != 3 || ranking[0].bucket() != other.bucket() || ranking[1].bucket() != owner.bucket() || ranking[2].bucket() != platform.bucket() {
		t.Fatalf("ranking with full and queued machines: %+v", ranking)
	}
	for _, candidate := range candidates[:3] {
		loads[candidate.bucket()] = domain.RunnerLoad{RunningRuns: 1, OccupiedRuns: 1}
	}
	winners := map[string]bool{}
	for range 100 {
		winners[rankAutoAssignCandidates(candidates, loads, 5)[0].bucket()] = true
	}
	if len(winners) != 3 {
		t.Fatalf("equal machines did not share ties: %v", winners)
	}
	if got := rankAutoAssignCandidates([]autoAssignCandidate{platform}, loads, 0); len(got) != 1 {
		t.Fatal("unlimited platform lost its capacity")
	}
}

type autoAssignFixture struct {
	t  *testing.T
	e  *Engine
	s  *repo.Store
	ec domain.TelemetryContext
	wf domain.WorkflowDefinition
}

func newAutoAssignFixture(t *testing.T) autoAssignFixture {
	t.Helper()
	e, s := retryEngine(t)
	e.ConfigureExecution("auto-api", "auto-host", time.Minute, false)
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "auto-pool", Projects: []string{"*"}, Required: true})
	f := autoAssignFixture{t: t, e: e, s: s, ec: domain.TelemetryContext{ProjectID: "auto-project", ResponsibleUserID: 1},
		wf: domain.WorkflowDefinition{ID: "auto-work", Name: "Auto work", SandboxProfileID: repo.DefaultSandboxProfileID,
			Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "script", Script: "printf test"}}}}
	f.project(f.ec.ProjectID)
	f.putWorkflow(f.wf)
	return f
}

func (f autoAssignFixture) project(id string) {
	f.t.Helper()
	if err := f.s.PutProject(domain.Project{ID: id, Name: id, OwnerID: 1,
		Members: []domain.ProjectMember{{UserID: 2, Role: domain.ProjectRoleMember}}}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.s.SetProjectRunnerPreference(1, id, "", domain.RunnerSelectionModeAuto); err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) putWorkflow(wf domain.WorkflowDefinition) {
	f.t.Helper()
	if err := f.s.PutWorkflow(wf); err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) machine(id string, owner int64, native bool) domain.Runner {
	f.t.Helper()
	runner := domain.Runner{ID: id, Name: id, OwnerUserID: owner, RunnerVersion: "99.0.0",
		Status: domain.RunnerStatusOnline, OS: "linux", NativeSandboxAvailable: native,
		Runtimes: []domain.RunnerRuntime{
			{Provider: "terminal", Status: domain.RuntimeStatusOnline},
			{Provider: "codex", Status: domain.RuntimeStatusOnline, AuthMode: domain.RuntimeAuthLocal},
		}}
	if owner == 0 {
		runner.Labels = []string{"auto-pool"}
	}
	if err := f.s.PutRunner(runner); err != nil {
		f.t.Fatal(err)
	}
	if owner != 0 {
		if err := f.s.SetRunnerSharedProjects(runner.ID, owner, []string{f.ec.ProjectID}); err != nil {
			f.t.Fatal(err)
		}
	}
	return runner
}

func (f autoAssignFixture) limit(runnerID string, limit int) {
	f.t.Helper()
	if runnerID == "" {
		if err := f.s.SetPlatformRunnerRunLimit(limit, 1); err != nil {
			f.t.Fatal(err)
		}
	} else if err := f.s.SetRunnerRunLimit(runnerID, limit); err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) start(wf string, ec domain.TelemetryContext) domain.Run {
	f.t.Helper()
	run, err := f.e.StartWithRepositoriesContext(wf, "test automatic execution", nil, ec)
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f autoAssignFixture) fixed(id, runnerID string) {
	f.t.Helper()
	ec := f.ec
	ec.ExecutionRunnerID = runnerID
	if err := f.s.PutRun(domain.Run{ID: id, Status: "running", ExecutionContext: ec, CreatedAt: time.Now().UTC()}); err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) state(id, want string) domain.Run {
	f.t.Helper()
	run, ok := f.s.RunState(id)
	if !ok || run.Status != want {
		f.t.Fatalf("run %s status = %s (found %v), want %s", id, run.Status, ok, want)
	}
	return run
}

func (f autoAssignFixture) release(id string) {
	f.t.Helper()
	run := f.state(id, "running")
	run.Status = "succeeded"
	if err := f.s.PutRun(run); err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) heartbeat(runner domain.Runner, at time.Time) {
	f.t.Helper()
	if err := f.s.RunnerHeartbeat(runner.ID, runner.Runtimes, runner.RunnerVersion, runner.NativeSandboxAvailable, at, at.Add(time.Hour)); err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) setQueueEnteredAt(id string, at time.Time) {
	f.t.Helper()
	if err := f.s.DB.Table("runs").Where("id = ?", id).Update("queue_entered_at", at).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f autoAssignFixture) sweepAt(at time.Time) {
	f.t.Helper()
	eval := f.e.autoAssignEvaluator()
	eval.CheckAutoRunReachable = func(run domain.Run, _ time.Time) string {
		return f.e.checkAutoRunReachable(run, at)
	}
	if _, err := f.s.PromoteQueuedRunsWithAuto(eval); err != nil {
		f.t.Fatal(err)
	}
}

func TestAutoAssignUnlimitedPlatformAndScope(t *testing.T) {
	f := newAutoAssignFixture(t)
	pool := f.machine("cloud-pool", 0, false)
	f.limit("", 0)
	if !f.e.PlatformRunnerOnline(f.ec.ProjectID) {
		t.Fatal("platform cloud pool should be online")
	}
	f.project("another")
	for i := range 12 {
		ec := f.ec
		if i%2 == 0 {
			ec.ProjectID = "another"
		}
		run := f.start(f.wf.ID, ec)
		if run.Status != "running" || run.ConcurrencyExempt || run.ExecutionContext.ExecutionRunnerID != pool.ID ||
			run.ExecutionContext.ExecutionPool != domain.ExecutionPoolProjectAuto || run.AdmissionState != "held" {
			t.Fatalf("unexpected platform assignment: %s %+v", run.Status, run.ExecutionContext)
		}
	}
	conversationEC := f.ec
	conversationEC.ExecutionPinned = true
	workspace, err := f.e.StartConversationWorkspace("Borrow a workspace", conversationEC, repo.TestNativeSandboxID)
	if err != nil || !workspace.IsWorkspace() || workspace.ExecutionContext.ExecutionPool == domain.ExecutionPoolProjectAuto {
		t.Fatalf("project auto mode changed conversations: %+v, %v", workspace, err)
	}
	ec := f.ec
	ec.WorkspaceRunID = workspace.ID
	borrowed := f.start(f.wf.ID, ec)
	if !borrowed.UsesConversationWorkspace() || borrowed.ExecutionContext.ExecutionPool == domain.ExecutionPoolProjectAuto ||
		borrowed.ExecutionContext.ExecutionRunnerID != "" || !borrowed.ExecutionContext.ExecutionPinned {
		t.Fatalf("borrowed workspace was reassigned: %+v", borrowed.ExecutionContext)
	}
	ec = f.ec
	ec.ProjectID = ""
	if personal := f.start(f.wf.ID, ec); personal.ExecutionContext.ExecutionPool == domain.ExecutionPoolProjectAuto {
		t.Fatalf("unscoped SOP was auto-assigned: %+v", personal.ExecutionContext)
	}
	loads, err := f.s.LoadSnapshots(nil)
	if err != nil || loads[repo.PlatformAdmissionBucketKey] != (domain.RunnerLoad{RunningRuns: 14, OccupiedRuns: 14}) {
		t.Fatalf("load counts workspaces or misses a project: %+v, %v", loads, err)
	}
}

func TestAutoAssignElasticPromotionMergesFixedFIFO(t *testing.T) {
	for _, target := range []string{"platform", "shared"} {
		t.Run(target, func(t *testing.T) {
			f := newAutoAssignFixture(t)
			var fixedRunner, wantRunner string
			if target == "shared" {
				runner := f.machine("shared-only", 2, false)
				fixedRunner, wantRunner = runner.ID, runner.ID
			} else {
				wantRunner = f.machine("pool-only", 0, false).ID
			}
			f.limit(fixedRunner, 1)
			f.fixed("holder", fixedRunner)
			f.fixed("fixed-first", fixedRunner)
			auto := f.start(f.wf.ID, f.ec)
			f.fixed("fixed-last", fixedRunner)
			if auto.Status != "queued" || auto.AdmissionState != "queued" || auto.OwnerInstanceID != "" ||
				auto.ExecutionContext.ExecutionRunnerID != "" || auto.ExecutionContext.ExecutionMode != "" ||
				auto.WorkflowSnapshot.SandboxSnapshot == nil || len(auto.Events) != 1 || auto.Events[0].Type != "run.queued" {
				t.Fatalf("elastic queue persisted a binding or lost its sandbox: %+v", auto)
			}
			f.release("holder")
			f.e.promoteQueuedRuns()
			f.state("fixed-first", "running")
			f.state(auto.ID, "queued")
			f.release("fixed-first")
			f.e.promoteQueuedRuns()
			bound := f.state(auto.ID, "running")
			f.state("fixed-last", "queued")
			if bound.ExecutionContext.ExecutionRunnerID != wantRunner || bound.ConcurrencyExempt != (target == "shared") ||
				len(bound.Events) != 3 || bound.Events[1].Type != "execution.bound" || bound.Events[2].Type != "run.started" {
				t.Fatalf("bound run: %+v", bound)
			}
			f.release(auto.ID)
			f.e.promoteQueuedRuns()
			f.state("fixed-last", "running")
		})
	}
}

func TestAutoAssignFreezesAutomaticSandboxAndSkipsIncompatibleHead(t *testing.T) {
	f := newAutoAssignFixture(t)
	pool := f.machine("docker-platform", 0, false)
	shared := f.machine("native-shared", 2, true)
	f.limit("", 1)
	f.limit(shared.ID, 1)
	f.fixed("pool-holder", "")
	f.fixed("native-holder", shared.ID)

	wf := f.wf
	wf.ID, wf.AutoSelectSandbox = "auto-sandbox", true
	f.putWorkflow(wf)
	calls := 0
	f.e.SelectSOPSandbox = func(_ context.Context, sel SOPSandboxSelection) string {
		calls++
		has := func(id string) bool {
			return slices.ContainsFunc(sel.Candidates, func(c SOPSandboxCandidate) bool { return c.ID == id })
		}
		if !has(repo.TestNativeSandboxID) || !has(repo.DefaultSandboxProfileID) {
			t.Fatalf("selector did not see the union of supported drivers: %+v", sel.Candidates)
		}
		return repo.TestNativeSandboxID
	}
	head := f.start(wf.ID, f.ec)
	tail := f.start(f.wf.ID, f.ec)
	if head.Status != "queued" || tail.Status != "queued" || calls != 1 ||
		head.WorkflowSnapshot.SandboxProfileID != repo.TestNativeSandboxID ||
		!slices.ContainsFunc(head.Events, func(e domain.RunEvent) bool { return e.Type == domain.RunEventSandboxSelected }) {
		t.Fatalf("no frozen automatic selection: head=%+v, tail=%s, calls=%d", head, tail.Status, calls)
	}
	f.release("pool-holder")
	f.e.promoteQueuedRuns()
	f.state(head.ID, "queued")
	if got := f.state(tail.ID, "running"); got.ExecutionContext.ExecutionRunnerID != pool.ID {
		t.Fatalf("tail bound to %s", got.ExecutionContext.ExecutionRunnerID)
	}
	f.release("native-holder")
	f.e.promoteQueuedRuns()
	if got := f.state(head.ID, "running"); got.ExecutionContext.ExecutionRunnerID != shared.ID ||
		got.WorkflowSnapshot.SandboxSnapshot.Profile.Driver != domain.SandboxDriverNative || calls != 1 {
		t.Fatalf("promotion recalculated auto sandbox: %+v, calls=%d", got, calls)
	}
}

func TestAutoAssignFreezesGeneratedWorkBranchAndSkipsOlderRunners(t *testing.T) {
	f := newAutoAssignFixture(t)
	older := f.machine("older-pool", 0, false)
	older.RunnerVersion = domain.RunnerVersionDefaultSelection
	if err := f.s.PutRunner(older); err != nil {
		t.Fatal(err)
	}
	compatible := f.machine("compatible-owner", f.ec.ResponsibleUserID, false)
	f.limit(compatible.ID, 1)
	f.fixed("holder", compatible.ID)

	repository := domain.Repository{
		ID: "auto-branch", ProjectID: f.ec.ProjectID, Name: "Auto branch", DefaultBranch: "main", LocalPath: t.TempDir(),
		WorkBranchNaming: &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingAI, ExecutorID: "codex", Model: "branch-model"},
	}
	if err := f.s.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.e.GenerateSOPWorkBranch = func(_ context.Context, req SOPWorkBranchRequest) (string, error) {
		calls++
		if req.Execution.ExecutionRunnerID != compatible.ID || req.Executor.Executor != "codex" || req.Executor.Model != "branch-model" {
			t.Fatalf("branch naming lost the requested executor, model or eligible machine: %+v", req)
		}
		return "topic/approved-branch", nil
	}
	run, err := f.e.StartWithRepositoriesContext(f.wf.ID, "Name the task branch", []domain.RepositoryContext{
		{RepositoryID: repository.ID, Name: repository.Name, BaseBranch: repository.DefaultBranch, LocalPath: repository.LocalPath},
	}, f.ec)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "queued" || run.ExecutionContext.ExecutionRunnerID != "" ||
		run.Repository.WorkBranch != "topic/approved-branch" || !run.Repository.CreateNewWorkBranch {
		t.Fatalf("expected an unbound run with its generated branch: %+v", run)
	}
	f.release("holder")
	f.e.promoteQueuedRuns()
	bound := f.state(run.ID, "running")
	if bound.ExecutionContext.ExecutionRunnerID != compatible.ID || bound.Repository != run.Repository || calls != 1 {
		t.Fatalf("promotion changed the selected branch or ran on an incompatible machine: runner=%s repo=%+v calls=%d",
			bound.ExecutionContext.ExecutionRunnerID, bound.Repository, calls)
	}
}

func TestAutoAssignRequiresResponsibleUsersCredentials(t *testing.T) {
	f := newAutoAssignFixture(t)
	pool := f.machine("pool-local-login", 0, false)
	other := f.machine("shared-local-login", 2, false)
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "auto-agent", Name: "Auto role", ProjectID: f.ec.ProjectID}); err != nil {
		t.Fatal(err)
	}
	wf := f.wf
	wf.ID, wf.ProjectID = "auto-with-agent", f.ec.ProjectID
	wf.Nodes = []domain.WorkflowNode{{ID: "work", Name: "Agent work", Type: "agent", AgentID: "auto-agent"}}
	f.putWorkflow(wf)
	if _, err := f.e.StartWithRepositoriesContext(wf.ID, "", nil, f.ec); err == nil || !strings.Contains(err.Error(), "执行器凭据") {
		t.Fatalf("another machine's CLI login qualified: %v", err)
	}
	if _, err := f.s.UpdateUserCredentials(1, repo.UserCredentialUpdate{CodexAPIKey: "auto-assign-fixture-key"}); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []autoAssignCandidate{{runner: pool}, {runner: other, shared: true}} {
		if err := f.e.planAutoAssignCandidate(&candidate, wf, f.ec, nil); err != nil ||
			candidate.executorDefaults["work"].Executor != "codex" || sopRunnerAuthMode(candidate.bind(f.ec), candidate.runner, "codex") != domain.RuntimeAuthPersonal {
			t.Fatalf("personal credential unavailable for %s: %+v, %v", candidate.runner.ID, candidate.executorDefaults, err)
		}
	}
	if run := f.start(wf.ID, f.ec); run.WorkflowSnapshot.ExecutorDefaults["work"].Executor != "codex" {
		t.Fatalf("executor defaults not frozen: %+v", run.WorkflowSnapshot.ExecutorDefaults)
	}
	requirements, err := f.e.SOPExecutorRequirements(wf, 1, f.ec.ProjectID)
	if err != nil || len(requirements) != 1 || !requirements[0].Configured {
		t.Fatalf("auto-assign preflight = %+v, %v", requirements, err)
	}
}

func TestAutoAssignOfflineGraceAndRevocation(t *testing.T) {
	f := newAutoAssignFixture(t)
	runner := f.machine("only-shared", 2, false)
	f.limit(runner.ID, 1)
	f.fixed("hold", runner.ID)
	older := f.start(f.wf.ID, f.ec)
	offlineAt := time.Now().UTC().Add(-autoAssignOfflineGrace - time.Second)
	f.setQueueEnteredAt(older.ID, offlineAt.Add(-time.Minute))
	f.heartbeat(runner, offlineAt.Add(-time.Second))
	if err := f.s.MarkRunnerOffline(runner.ID, offlineAt); err != nil {
		t.Fatal(err)
	}

	other := f.machine("other-shared", 2, false)
	f.limit(other.ID, 1)
	f.fixed("other-hold", other.ID)
	recent := f.start(f.wf.ID, f.ec)
	if err := f.s.SetRunnerSharedProjects(other.ID, other.OwnerUserID, nil); err != nil {
		t.Fatal(err)
	}
	f.e.promoteQueuedRuns()
	f.state(recent.ID, "queued")
	if got := f.state(older.ID, "failed"); got.AdmissionState != "unstarted" || !strings.Contains(got.Events[len(got.Events)-1].Message, "2 分钟") {
		t.Fatalf("offline queue did not explain the timeout: %+v", got)
	}
	if err := f.s.SetRunnerSharedProjects(runner.ID, runner.OwnerUserID, nil); err != nil {
		t.Fatal(err)
	}
	f.e.promoteQueuedRuns()
	if got := f.state(recent.ID, "failed"); !strings.Contains(got.Events[len(got.Events)-1].Message, "已无兼容") {
		t.Fatalf("revoked queue did not fail immediately: %+v", got)
	}
	if f.s.ResolveRunnerDefaults(f.ec.ResponsibleUserID, f.ec.ProjectID).ProjectRunnerMode != domain.RunnerSelectionModeAuto {
		t.Fatal("revocation removed auto mode")
	}
}

func TestAutoAssignOfflineGraceStartsWhenBusyRunnerDisconnects(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		reconnects bool
	}{
		{name: "reconnects", reconnects: true},
		{name: "stays-offline"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newAutoAssignFixture(t)
			runner := f.machine("busy-shared", 2, false)
			f.limit(runner.ID, 1)
			holder := f.start(f.wf.ID, f.ec)
			waiting := f.start(f.wf.ID, f.ec)
			f.state(holder.ID, "running")
			f.state(waiting.ID, "queued")

			disconnectedAt := time.Now().UTC().Truncate(time.Second).Add(-autoAssignOfflineGrace - time.Second)
			queuedAt := disconnectedAt.Add(-3 * time.Minute)
			f.setQueueEnteredAt(waiting.ID, queuedAt)
			other := f.machine("long-offline", 2, false)
			if err := f.s.MarkRunnerOffline(other.ID, queuedAt.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			f.heartbeat(runner, disconnectedAt.Add(-time.Second))
			f.sweepAt(disconnectedAt.Add(-time.Second))
			f.state(waiting.ID, "queued")

			if err := f.s.MarkRunnerOffline(runner.ID, disconnectedAt); err != nil {
				t.Fatal(err)
			}
			f.sweepAt(disconnectedAt.Add(time.Second))
			f.state(waiting.ID, "queued")

			if scenario.reconnects {
				f.heartbeat(runner, disconnectedAt.Add(10*time.Second))
				f.release(holder.ID)
				f.e.promoteQueuedRuns()
				if got := f.state(waiting.ID, "running"); got.ExecutionContext.ExecutionRunnerID != runner.ID {
					t.Fatalf("promoted run bound to %s, want %s", got.ExecutionContext.ExecutionRunnerID, runner.ID)
				}
				return
			}
			f.sweepAt(disconnectedAt.Add(autoAssignOfflineGrace))
			f.state(waiting.ID, "queued")
			f.sweepAt(disconnectedAt.Add(autoAssignOfflineGrace + time.Second))
			failed := f.state(waiting.ID, "failed")
			event := failed.Events[len(failed.Events)-1]
			if event.Type != domain.RunEventFailed || !strings.Contains(event.Message, "离线超过 2 分钟") {
				t.Fatalf("unexpected failure event: %+v", event)
			}
		})
	}
}

func TestAutoAssignPlatformPoolBindingAndCompatibility(t *testing.T) {
	f := newAutoAssignFixture(t)
	older := f.machine("older-pool", 0, false)
	older.RunnerVersion = domain.RunnerVersionDefaultSelection
	if err := f.s.PutRunner(older); err != nil {
		t.Fatal(err)
	}
	repositories := []domain.RepositoryContext{{RepositoryID: "origin", Provider: domain.RepositoryProviderSSH}}
	if _, err := f.e.StartWithRepositoriesContext(f.wf.ID, "", repositories, f.ec); err == nil || !strings.Contains(err.Error(), "Git SSH") {
		t.Fatalf("platform accepted a runner without the required Git transport: %v", err)
	}
	compatible := f.machine("compatible-pool", 0, false)
	run, err := f.e.StartWithRepositoriesContext(f.wf.ID, "", repositories, f.ec)
	if err != nil || run.ExecutionContext.ExecutionRunnerID != compatible.ID {
		t.Fatalf("run did not choose a compatible pool backend: %+v, %v", run.ExecutionContext, err)
	}
	compatible.Status = domain.RunnerStatusOffline
	if err := f.s.PutRunner(compatible); err != nil {
		t.Fatal(err)
	}
	f.machine("replacement-pool", 0, false)
	if got, err := f.e.bindPoolRunner(context.Background(), run, "work"); err == nil || got.ExecutionContext.ExecutionRunnerID != compatible.ID {
		t.Fatalf("offline auto-assigned platform run switched machines: %+v, %v", got.ExecutionContext, err)
	}
}

func TestAutoAssignCanRunOnLocalPlatform(t *testing.T) {
	if !runtime.NativeSandboxAvailable(domain.SandboxDriverNative) {
		t.Skip("native sandbox unavailable")
	}
	f := newAutoAssignFixture(t)
	f.e.ConfigureCloudRunnerPool(CloudRunnerPool{})
	f.e.ConfigureExecution("auto-worker", "auto-host", time.Minute, true)
	t.Cleanup(func() { _ = f.e.Drain(context.Background()) })
	wf := f.wf
	wf.ID, wf.SandboxProfileID = "local-auto", repo.TestNativeSandboxID
	wf.Nodes = []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval"}}
	f.putWorkflow(wf)
	run := f.start(wf.ID, f.ec)
	if run.ExecutionContext.ExecutionMode != domain.ExecutionModeCloud || run.ExecutionContext.ExecutionRunnerID != "" ||
		run.ExecutionContext.ExecutionPool != domain.ExecutionPoolProjectAuto || run.ConcurrencyExempt {
		t.Fatalf("local platform was not bound: %+v", run.ExecutionContext)
	}
	waitRun(t, f.s, run.ID, 5*time.Second, func(run domain.Run) bool { return run.Status == "waiting" })
}

func TestAutoAssignPromotionWakeAcrossProcesses(t *testing.T) {
	f := newAutoAssignFixture(t)
	worker := New(f.s, runtime.NewManager(t.TempDir()))
	const token = "promotion-fixture-token"
	worker.ConfigureControl(ControlConfig{Token: token})
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+queuedRunPromotionPath, worker.HandleQueuedRunPromotion)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req := httptest.NewRequest(http.MethodPost, queuedRunPromotionPath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated promotion wake: %d", rec.Code)
	}
	f.e.ConfigureControl(ControlConfig{Token: token, FallbackURL: srv.URL})
	f.e.PromoteQueuedRunsAsync()
	select {
	case <-worker.claimKick:
	case <-time.After(time.Second):
		t.Fatal("API-only process did not wake the worker")
	}
}

func TestAutoAssignSOPIdleReclaimPromotesAndRetryKeepsMachine(t *testing.T) {
	f := newAutoAssignFixture(t)
	runner := f.machine("first-machine", 1, false)
	f.limit(runner.ID, 1)
	parent := f.start(f.wf.ID, f.ec)
	if parent.ExecutionContext.ExecutionRunnerID != runner.ID || !parent.ConcurrencyExempt {
		t.Fatalf("parent target = %+v", parent.ExecutionContext)
	}
	childEC := f.ec
	childEC.ParentRunID, childEC.WorkspaceRunID = parent.ID, parent.ID
	child := f.start(f.wf.ID, childEC)
	if child.Status != "running" || child.ExecutionContext.ExecutionRunnerID != runner.ID || !child.ConcurrencyExempt {
		t.Fatalf("child did not inherit its parent's slot and machine: %+v", child)
	}
	f.release(child.ID)
	parent.Status, parent.Nodes["work"].Status = "failed", "failed"
	if err := f.s.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	waiting := f.start(f.wf.ID, f.ec)
	f.state(waiting.ID, "queued")
	root := filepath.Join(t.TempDir(), "target-checkout")
	writeSOPIdleFile(t, root)
	transfers := &runtime.CheckpointTransfers{}
	t.Cleanup(transfers.Close)
	f.e.WorkspaceCheckpointTransport = func(ctx context.Context, run domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if run.ExecutionContext.ExecutionRunnerID != runner.ID {
			return domain.RunnerWorkspaceResponse{}, fmt.Errorf("checkpoint was routed to %s", run.ExecutionContext.ExecutionRunnerID)
		}
		return transfers.Handle(ctx, root, req), nil
	}
	now := time.Now().UTC()
	parent = setSOPIdleTime(t, f.s, parent, now.Add(-ConversationWorkspaceIdleTTL-time.Minute))
	f.e.cleanupWorkspace(parent, now)
	assertSOPReclaimed(t, f.s, parent.ID)
	f.e.promoteQueuedRuns()
	f.state(waiting.ID, "running")

	other := f.machine("new-default", 1, false)
	if err := f.s.SetProjectDefaultRunner(f.ec.ResponsibleUserID, f.ec.ProjectID, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.e.Retry(parent.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	queued := f.state(parent.ID, "queued")
	if queued.ExecutionContext.ExecutionRunnerID != runner.ID || !queued.ConcurrencyExempt || queued.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("retry changed the machine or restored before admission: %+v", queued)
	}
	f.e.promoteQueuedRuns()
	f.state(parent.ID, "queued")
	f.release(waiting.ID)
	f.e.promoteQueuedRuns()
	got := f.state(parent.ID, "running")
	if got.ExecutionContext.ExecutionRunnerID != runner.ID || got.ExecutionContext.ExecutionPool != domain.ExecutionPoolProjectAuto ||
		got.WorkspaceCleanedAt.IsZero() || !got.ConcurrencyExempt {
		t.Fatalf("retry moved to another machine: %+v", got)
	}
}

func TestAutoAssignSplitAPIWorkerSeesPlatformOnline(t *testing.T) {
	f := newAutoAssignFixture(t)
	f.e.ConfigureCloudRunnerPool(CloudRunnerPool{})
	if f.e.PlatformRunnerOnline(f.ec.ProjectID) {
		t.Fatal("api-only process with no worker heartbeat should report platform offline")
	}
	if err := f.s.HeartbeatEngineInstance(repo.EngineInstance{
		InstanceID: "worker-live", Role: "worker", WorkspaceHost: "worker-host", ControlURL: "http://127.0.0.1:18081",
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if !f.e.PlatformRunnerOnline(f.ec.ProjectID) {
		t.Fatal("api-only process with live worker heartbeat should report platform online")
	}
	run := f.start(f.wf.ID, f.ec)
	if run.Status != "running" || run.ConcurrencyExempt || run.ExecutionContext.ExecutionMode != domain.ExecutionModeCloud ||
		run.ExecutionContext.ExecutionRunnerID != "" || run.ExecutionContext.ExecutionPool != domain.ExecutionPoolProjectAuto {
		t.Fatalf("expected platform assignment via split api+worker, got %s %+v", run.Status, run.ExecutionContext)
	}
	if err := f.s.DeleteEngineInstance("worker-live"); err != nil {
		t.Fatal(err)
	}
	if f.e.PlatformRunnerOnline(f.ec.ProjectID) {
		t.Fatal("platform should return offline once last worker expires")
	}
}

func TestAutoAssignOfflineCloudPoolGracePublishesFailureAndRetries(t *testing.T) {
	f := newAutoAssignFixture(t)
	pool := f.machine("pool-only", 0, false)
	f.limit("", 1)
	holder := f.start(f.wf.ID, f.ec)
	f.state(holder.ID, "running")
	waiting := f.start(f.wf.ID, f.ec)
	f.state(waiting.ID, "queued")

	ch, unsub, err := f.e.Subscribe(waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	select {
	case <-ch: // drain initial queued snapshot
	default:
	}

	offlineAt := time.Now().UTC().Truncate(time.Second).Add(-autoAssignOfflineGrace - time.Second)
	const heartbeatTimeout = 90 * time.Second
	f.setQueueEnteredAt(waiting.ID, offlineAt.Add(-2*time.Minute))
	f.heartbeat(pool, offlineAt.Add(-heartbeatTimeout-time.Second))
	f.s.MarkOfflineRunners(heartbeatTimeout, offlineAt)
	f.sweepAt(offlineAt.Add(30 * time.Second))
	f.state(waiting.ID, "queued")

	f.sweepAt(offlineAt.Add(autoAssignOfflineGrace + time.Second))
	failed := f.state(waiting.ID, "failed")
	if failed.AdmissionState != "unstarted" {
		t.Fatalf("failed elastic run should be unstarted, got %q", failed.AdmissionState)
	}
	select {
	case published := <-ch:
		if published.Status != "failed" {
			t.Fatalf("expected published failed event, got %s", published.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected SSE publish when unreachable auto-assign run fails")
	}

	f.heartbeat(pool, time.Now().UTC())
	f.release(holder.ID)
	if err := f.e.Retry(waiting.ID, "", "", ""); err != nil {
		t.Fatalf("retry unbound failed auto-assign run: %v", err)
	}
	f.state(waiting.ID, "queued")
	f.e.promoteQueuedRuns()
	got := f.state(waiting.ID, "running")
	if got.ExecutionContext.ExecutionRunnerID != pool.ID || got.ConcurrencyExempt {
		t.Fatalf("retried auto-assign run did not bind to recovered pool runner: %+v", got)
	}
}
