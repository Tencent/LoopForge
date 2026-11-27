package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func evoReplayTestWorkflow() domain.WorkflowDefinition {
	return domain.WorkflowDefinition{
		ID: "evo-replay", Name: "evo replay", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{
			{ID: "clarify", Name: "需求定型", Type: "agent", AgentID: "developer", ExecutorID: "mock", MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "clarify", Path: "requirement.md", MediaType: "text/markdown", Required: true}}},
			{ID: "accept", Name: "验收", Type: "approval"},
			{ID: "review", Name: "独立评审", Type: "agent", AgentID: "developer", ExecutorID: "mock", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "clarify", Target: "accept", When: "success"}, {ID: "e2", Source: "accept", Target: "review", When: "success"}},
	}
}

func TestStartEvolutionReplayReusesUpstreamNodes(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	developer, _ := s.Agent("developer")
	developer.ExecutorID, developer.Prompt = "mock", "{{input}}"
	if err := s.PutAgent(developer); err != nil {
		t.Fatal(err)
	}
	wf := evoReplayTestWorkflow()
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	source := domain.Run{
		ID: "evo-src", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", Input: "做一个个人主页",
		ExecutionContext: domain.TelemetryContext{ProjectID: "welcome"},
		Nodes: map[string]*domain.NodeRun{
			"clarify": {NodeID: "clarify", Status: "succeeded", Attempt: 1, Output: "需求已定型", Usage: domain.TokenUsage{TotalTokens: 900}},
			"accept":  {NodeID: "accept", Status: "succeeded", Attempt: 1},
			"review":  {NodeID: "review", Status: "succeeded", Attempt: 1, Output: "评审通过"},
		},
		Artifacts: []domain.Artifact{
			newArtifact("evo-src", "clarify", 1, "clarify", "requirement.md", "text/markdown", []byte("# 需求\n\n关键路径：访问主页、点击拨号")),
			newArtifact("evo-src", "review", 1, "review", "review.md", "text/markdown", []byte("PASS")),
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(source); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	exec := domain.TelemetryContext{ProjectID: "welcome", Purpose: domain.RunPurposeEvolutionReplay, EvolutionReplayID: "evr-test"}
	run, reused, err := e.StartEvolutionReplay(source, nil, exec, map[string]bool{"review": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reused, ",") != "accept,clarify" {
		t.Fatalf("reused = %v, want accept and clarify", reused)
	}
	waitFor(t, s, run.ID, "succeeded")
	got, _ := s.RunState(run.ID)
	clarify := got.Nodes["clarify"]
	if clarify.Status != "succeeded" || !strings.Contains(clarify.Activity, "沿用原运行 evo-src") || clarify.Usage.TotalTokens != 0 || clarify.Output != "需求已定型" {
		t.Fatalf("clarify should be taken over from the source run: %+v", clarify)
	}
	if got.Nodes["accept"].Status != "succeeded" {
		t.Fatalf("approval node should not wait: %+v", got.Nodes["accept"])
	}
	review := got.Nodes["review"]
	if review.Status != "succeeded" || !strings.Contains(review.Output, "inputs/clarify") {
		t.Fatalf("review should run with the reused requirement mounted: %+v", review)
	}
	copied := 0
	for _, a := range got.Artifacts {
		if a.NodeID == "clarify" && a.Path == "requirement.md" {
			copied++
			if a.RunID != run.ID || !strings.Contains(a.Content, "点击拨号") {
				t.Fatalf("reused artifact should be copied onto the replay run: %+v", a)
			}
		}
		if a.NodeID == "review" && a.Content == "PASS" {
			t.Fatal("the start node's own source output must not be reused")
		}
	}
	if copied != 1 {
		t.Fatalf("requirement.md copies = %d", copied)
	}
	reusedEvent := false
	for _, ev := range got.Events {
		if ev.Type == "evolution.replay.reused" && strings.Contains(ev.Message, "需求定型") {
			reusedEvent = true
		}
	}
	if !reusedEvent {
		t.Fatal("replay should record which nodes it reused")
	}
}

func TestReplayReusableNodes(t *testing.T) {
	wf := evoReplayTestWorkflow()
	source := domain.Run{Nodes: map[string]*domain.NodeRun{
		"clarify": {Status: "succeeded"}, "accept": {Status: "succeeded"}, "review": {Status: "failed"},
	}}
	if got := replayReusableNodes(wf, source, map[string]bool{"clarify": true}, false); got != nil {
		t.Fatalf("starting at the first node is a full replay: %v", got)
	}
	if got := replayReusableNodes(wf, source, map[string]bool{"missing": true}, false); got != nil {
		t.Fatalf("unknown start nodes fall back to a full replay: %v", got)
	}
	if got := replayReusableNodes(wf, source, map[string]bool{"accept": true}, false); len(got) != 1 || !got["clarify"] {
		t.Fatalf("only nodes before the start are reused: %v", got)
	}
	source.Nodes["accept"].Status = "waiting_approval"
	if got := replayReusableNodes(wf, source, map[string]bool{"review": true}, false); len(got) != 1 || !got["clarify"] {
		t.Fatalf("a node that did not succeed in the source must run again: %v", got)
	}
	source.Artifacts = []domain.Artifact{{NodeID: "clarify", Name: "repository-patch", Content: "diff --git a/x b/x"}}
	if got := replayReusableNodes(wf, source, map[string]bool{"review": true}, true); got != nil {
		t.Fatalf("a skipped node that changed repository code forces a full replay: %v", got)
	}
	if got := replayReusableNodes(wf, source, map[string]bool{"review": true}, false); len(got) != 1 {
		t.Fatalf("without repositories the workspace patch is restored from artifacts: %v", got)
	}
}
