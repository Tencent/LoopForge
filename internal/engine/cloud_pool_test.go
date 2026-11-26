package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestCloudPoolPicksOnlyPlatformRunnersForListedProjects(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	runners := []domain.Runner{
		{ID: "user-labelled", OwnerUserID: 7, Status: domain.RunnerStatusOnline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}},
		{ID: "old-builtin", Status: domain.RunnerStatusOnline, RunnerVersion: "0.3.0", Labels: []string{"pool=cloud"}},
		{ID: "offline-builtin", Status: domain.RunnerStatusOffline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}},
		{ID: "unlabelled", Status: domain.RunnerStatusOnline, RunnerVersion: "0.4.0"},
		{ID: "builtin", Status: domain.RunnerStatusOnline, RunnerVersion: "0.4.0", Labels: []string{"os=linux", "pool=cloud"}},
	}
	for _, r := range runners {
		if err := s.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := e.cloudPoolRunner("e2e"); ok {
		t.Fatal("pool routed a run before it was configured")
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool=cloud", Projects: ParseCloudRunnerProjects(" e2e , other ")})
	for i := 0; i < 20; i++ {
		runner, ok := e.cloudPoolRunner("e2e")
		if !ok || runner.ID != "builtin" {
			t.Fatalf("picked %q, %v; want the online platform runner", runner.ID, ok)
		}
	}
	if _, ok := e.cloudPoolRunner("platform"); ok {
		t.Fatal("pool served a project outside the rollout list")
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}})
	if _, ok := e.cloudPoolRunner("platform"); !ok {
		t.Fatal(`"*" did not cover every project`)
	}
}

func TestBindPoolRunnerDiscardsLocalSandbox(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New(s, runtime.NewManager(t.TempDir()))
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}, Required: true})
	if err := s.PutRunner(domain.Runner{ID: "builtin", Status: domain.RunnerStatusOnline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-local", Status: "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "skillhub", ExecutionMode: domain.ExecutionModeCloud},
		Nodes:            map[string]*domain.NodeRun{"fix": {NodeID: "fix", Status: "running"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	sandbox := &recordingSandbox{workspace: e.sandboxes.RunWorkspace(run.ID)}
	e.runEnvs[run.ID] = &runEnvironment{sandbox: sandbox}

	bound, err := e.bindPoolRunner(context.Background(), run, "fix")
	if err != nil {
		t.Fatal(err)
	}
	if bound.ExecutionContext.ExecutionRunnerID != "builtin" {
		t.Fatalf("runner=%q", bound.ExecutionContext.ExecutionRunnerID)
	}
	if !sandbox.closed {
		t.Fatal("moving a run onto the pool runner must remove the control-plane container")
	}
}

func TestBindPoolRunnerWaitsForOriginalWorkspace(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		name := "own workspace"
		if shared {
			name = "parent workspace"
		}
		t.Run(name, func(t *testing.T) {
			e, s := retryEngine(t)
			e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}})
			original := domain.Runner{ID: "original", Name: "builtin-a", Status: domain.RunnerStatusOffline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}}
			for _, runner := range []domain.Runner{
				original,
				{ID: "replacement", Status: domain.RunnerStatusOnline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}},
			} {
				if err := s.PutRunner(runner); err != nil {
					t.Fatal(err)
				}
			}
			run := domain.Run{
				ID: "run-offline", Status: "running",
				ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionPool: domain.ExecutionModeCloud, ExecutionRunnerID: original.ID},
				Repository:       domain.RepositoryContext{RepositoryID: "repo"},
				Nodes: map[string]*domain.NodeRun{
					"implement": {NodeID: "implement", Status: "succeeded", Attempt: 1},
					"verify":    {NodeID: "verify", Status: "running", Attempt: 1},
				},
			}
			if shared {
				run.ExecutionContext.WorkspaceRunID = "parent"
				run.Repository = domain.RepositoryContext{}
				delete(run.Nodes, "implement")
			}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			sandbox := &recordingSandbox{workspace: e.sandboxes.RunWorkspace(run.ID)}
			e.runEnvs[run.ID] = &runEnvironment{sandbox: sandbox}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				run domain.Run
				err error
			}
			done := make(chan result, 1)
			go func() {
				bound, err := e.bindPoolRunner(ctx, run, "verify")
				done <- result{bound, err}
			}()
			select {
			case got := <-done:
				t.Fatalf("must wait for the original runner, got %q, %v", got.run.ExecutionContext.ExecutionRunnerID, got.err)
			case <-time.After(100 * time.Millisecond):
			}
			waiting, _ := s.Run(run.ID)
			if len(waiting.Events) != 1 || waiting.Events[0].Type != "execution.waiting" || !strings.Contains(waiting.Events[0].Message, original.Name) {
				t.Fatalf("missing original runner wait event: %+v", waiting.Events)
			}
			original.Status = domain.RunnerStatusOnline
			if err := s.PutRunner(original); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if got.err != nil || got.run.ExecutionContext.ExecutionRunnerID != original.ID || sandbox.closed {
					t.Fatalf("must retain original workspace: runner=%q closed=%v err=%v", got.run.ExecutionContext.ExecutionRunnerID, sandbox.closed, got.err)
				}
				if got.run.Nodes["verify"].Attempt != 1 || !shared && got.run.Nodes["implement"].Status != "succeeded" {
					t.Fatalf("waiting changed node progress: %+v", got.run.Nodes)
				}
				resumed, _ := s.Run(run.ID)
				if len(resumed.Events) != 2 || resumed.Events[1].Type != "execution.resumed" {
					t.Fatalf("missing runner recovery event: %+v", resumed.Events)
				}
			case <-ctx.Done():
				t.Fatal("did not resume after original runner recovered")
			}
		})
	}
}

func TestBindPoolRunnerWaitCanBeCanceled(t *testing.T) {
	t.Parallel()
	e, s := retryEngine(t)
	run := domain.Run{
		ID: "cancel-wait", Status: "running",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionPool: domain.ExecutionModeCloud, ExecutionRunnerID: "offline", WorkspaceRunID: "parent"},
		Nodes:            map[string]*domain.NodeRun{"verify": {NodeID: "verify", Status: "running"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := e.bindPoolRunner(ctx, run, "verify"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting must honor cancellation: %v", err)
	}
}
