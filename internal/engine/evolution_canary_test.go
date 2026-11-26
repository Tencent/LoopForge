package engine

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestEvolutionCanarySplitsNewRuns(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	developer, _ := s.Agent("developer")
	developer.ExecutorID, developer.Prompt = "mock", "LIVE {{input}}"
	developer.ProjectID, developer.Shared = "welcome", false
	if err := s.PutAgent(developer); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "evo-canary", Name: "evo canary", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "work", Type: "agent", AgentID: "developer", ExecutorID: "mock", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	trial := domain.EvoProposal{ID: "evp-canary", ProjectID: "welcome", AssetKind: domain.EvoAssetAgent, AssetID: "developer", AssetName: "Developer",
		Status: domain.EvoProposalCanary, BaseContent: developer.Prompt, BaseHash: domain.ContentHash(developer.Prompt), CandidateContent: "CANARY {{input}}",
		Canary: &domain.EvoCanary{Percent: 50, MinRuns: 5, Status: domain.EvoCanaryRunning, StartedAt: time.Now().UTC()}}
	if err := s.PutEvoProposal(trial); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))

	var canaryRun, controlRun domain.Run
	for i := 0; i < 40 && (canaryRun.ID == "" || controlRun.ID == ""); i++ {
		run, err := e.StartWithExecutionContext(wf.ID, fmt.Sprintf("任务 %d", i), domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "welcome"})
		if err != nil {
			t.Fatal(err)
		}
		inCanary := len(run.ExecutionContext.EvolutionCanaryIDs) == 1 && run.ExecutionContext.EvolutionCanaryIDs[0] == trial.ID
		if inCanary != (evolution.CanaryBucket(run.ID, trial.ID) < 50) {
			t.Fatalf("arm must follow the run bucket: %+v", run.ExecutionContext)
		}
		if inCanary && canaryRun.ID == "" {
			canaryRun = run
		} else if !inCanary && controlRun.ID == "" {
			controlRun = run
		} else {
			waitFor(t, s, run.ID, "succeeded")
		}
	}
	if canaryRun.ID == "" || controlRun.ID == "" {
		t.Fatal("a 50% trial should put runs in both arms")
	}
	waitFor(t, s, canaryRun.ID, "succeeded")
	waitFor(t, s, controlRun.ID, "succeeded")
	gotCanary, _ := s.RunState(canaryRun.ID)
	gotControl, _ := s.RunState(controlRun.ID)
	if out := gotCanary.Nodes["work"].Output; !strings.Contains(out, "CANARY") || strings.Contains(out, "LIVE") {
		t.Fatalf("canary arm should run the candidate: %q", out)
	}
	if out := gotControl.Nodes["work"].Output; !strings.Contains(out, "LIVE") || strings.Contains(out, "CANARY") {
		t.Fatalf("control arm should keep the live body: %q", out)
	}
	pinHash := func(runID string) string {
		for _, pin := range s.RunAssetPins(runID) {
			if pin.AssetKind == domain.EvoAssetAgent && pin.AssetID == "developer" {
				return pin.Hash
			}
		}
		return ""
	}
	if pinHash(canaryRun.ID) != domain.ContentHash(trial.CandidateContent) || pinHash(controlRun.ID) != trial.BaseHash {
		t.Fatal("pins must freeze the body each arm started with")
	}
	event := false
	for _, ev := range gotCanary.Events {
		if ev.Type == "evolution.canary" && strings.Contains(ev.Message, "Developer") {
			event = true
		}
	}
	if !event {
		t.Fatal("canary runs should say they take part in a trial")
	}

	trial, _ = s.EvoProposal(trial.ID)
	trial.Canary.Status = domain.EvoCanaryAborted
	if err := s.PutEvoProposal(trial); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		run, err := e.StartWithExecutionContext(wf.ID, "停止后", domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "welcome"})
		if err != nil {
			t.Fatal(err)
		}
		if len(run.ExecutionContext.EvolutionCanaryIDs) != 0 {
			t.Fatal("a stopped trial must not take new runs")
		}
		waitFor(t, s, run.ID, "succeeded")
	}
	replay, err := e.StartWithExecutionContext(wf.ID, "回放", domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "welcome", Purpose: domain.RunPurposeEvolutionReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.ExecutionContext.EvolutionCanaryIDs) != 0 {
		t.Fatal("replays never join trials")
	}
	waitFor(t, s, replay.ID, "succeeded")
}
