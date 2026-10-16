package runeval

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestBuildFactsDetectsReworkAndToolStorm(t *testing.T) {
	start := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	run := domain.Run{
		ID:         "run-1",
		WorkflowID: "wf-fix",
		Status:     "succeeded",
		Input:      "fix login",
		CreatedAt:  start,
		UpdatedAt:  start.Add(20 * time.Minute),
		Usage:      domain.TokenUsage{TotalTokens: 9000, InputTokens: 7000, CacheReadTokens: 100},
		ToolCalls:  12,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:   "wf-fix",
			Name: "Issue fix",
			Nodes: []domain.WorkflowNode{
				{ID: "fix", Name: "修复", Type: "agent", AgentID: "fixer", MaxAttempts: 1, MaxReworks: 0},
				{ID: "verify", Name: "验证", Type: "script", MaxAttempts: 1},
			},
		},
		Nodes: map[string]*domain.NodeRun{
			"fix":    {NodeID: "fix", Status: "succeeded", Attempt: 3, ReworkCount: 2, ToolCalls: 10, StartedAt: start, FinishedAt: start.Add(12 * time.Minute), Error: "same boom", Usage: domain.TokenUsage{TotalTokens: 8000}},
			"verify": {NodeID: "verify", Status: "succeeded", Attempt: 1, StartedAt: start.Add(12 * time.Minute), FinishedAt: start.Add(13 * time.Minute)},
		},
		Events: []domain.RunEvent{
			{Type: "reject", NodeID: "fix", Message: "verify failed"},
			{Type: "retry", NodeID: "fix", Message: "retry"},
		},
		Artifacts: []domain.Artifact{{NodeID: "fix", Name: "fix-report.md"}},
		Telemetry: []domain.TelemetryEvent{
			{Sequence: 1, NodeID: "fix", Category: "lifecycle", Name: "node.start", Phase: "start"},
			{Sequence: 2, NodeID: "fix", Category: "lifecycle", Name: "node.start", Phase: "start"},
			{Sequence: 3, NodeID: "fix", Category: "tool", Name: "Read", Phase: "start", Tool: &domain.ToolObservation{Name: "Read"}},
			{Sequence: 4, NodeID: "fix", Category: "tool", Name: "Read", Phase: "finish", Tool: &domain.ToolObservation{Name: "Read"}, Error: &domain.ErrorObservation{Message: "not found"}},
			{Sequence: 5, NodeID: "fix", Category: "tool", Name: "Read", Phase: "finish", Tool: &domain.ToolObservation{Name: "Read"}, Error: &domain.ErrorObservation{Message: "not found"}},
			{Sequence: 6, NodeID: "fix", Category: "tool", Name: "Read", Phase: "finish", Tool: &domain.ToolObservation{Name: "Read"}, Error: &domain.ErrorObservation{Message: "not found"}},
			{Sequence: 7, NodeID: "fix", Category: "error", Message: "same boom"},
			{Sequence: 8, NodeID: "verify", Category: "verification", Name: "gate.fail", Phase: "finish"},
			{Sequence: 9, Category: "reasoning", Name: "delta", Phase: "progress", Message: "should skip"},
		},
	}
	facts := BuildFacts(run, domain.RunEvalCatalog{})
	if facts.ExecDurationMs < 12*60*1000 {
		t.Fatalf("exec duration = %d", facts.ExecDurationMs)
	}
	if len(facts.Nodes) != 2 || facts.Nodes[0].ReworkCount != 2 || facts.Nodes[0].Lifecycle != 2 {
		t.Fatalf("nodes = %+v", facts.Nodes)
	}
	if len(facts.ToolRepeats) == 0 || facts.ToolRepeats[0].Failed < 3 {
		t.Fatalf("tool repeats = %+v", facts.ToolRepeats)
	}
	if len(facts.PingPong) == 0 {
		t.Fatal("expected reject/retry ping-pong")
	}
	for _, ev := range facts.Digest {
		if ev.Category == "reasoning" {
			t.Fatal("reasoning progress should be omitted from digest")
		}
	}
	if len(facts.Digest) < 6 {
		t.Fatalf("digest too small: %d", len(facts.Digest))
	}
}

func TestParseModelResultScoresByWeight(t *testing.T) {
	raw := `{
	  "summary": "返工过多，建议提高 maxAttempts 并收紧门禁。",
	  "dimensions": {"control":40,"latency":80,"tools":50,"tokens":70,"recovery":30,"delivery":90},
	  "findings":[{"severity":"error","dimension":"control","nodeId":"fix","message":"返工 2 次"}],
	  "recommendations":[{"title":"提高重试","target":"workflow","action":"update_node","nodeId":"fix","set":{"maxAttempts":3}}]
	}`
	got, err := ParseModelResult(raw, domain.RunEvalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Score != 59 {
		t.Fatalf("score=%d want 59", got.Score)
	}
	if got.Passed {
		t.Fatal("should fail default threshold 70")
	}
	if len(got.Recommendations) != 1 || !got.Recommendations[0].Applyable() {
		t.Fatalf("recommendations = %+v", got.Recommendations)
	}
}

func TestApplyRecommendationUpdatesAgentGateAndWorkflowAttempts(t *testing.T) {
	wf := domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{{ID: "fix", MaxAttempts: 1}}}
	if err := ApplyRecommendation(domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "fix", Set: map[string]any{"maxAttempts": 3}}, &wf, nil); err != nil {
		t.Fatal(err)
	}
	if wf.Nodes[0].MaxAttempts != 3 {
		t.Fatalf("maxAttempts=%d", wf.Nodes[0].MaxAttempts)
	}
	agent := domain.AgentDefinition{ID: "fixer", Instructions: "修 bug", Verification: []domain.VerificationGate{{ID: "exists", Name: "文件存在", Commands: []string{"test -f a"}}}}
	if err := ApplyRecommendation(domain.RunEvalRecommendation{
		Target: "agent", Action: "upsert_gate", TargetID: "fixer",
		Gate: &domain.VerificationGate{ID: "exists", Name: "增量测试", Commands: []string{"go test ./..."}, Required: true, OnFailure: "reject"},
	}, nil, &agent); err != nil {
		t.Fatal(err)
	}
	if got := agent.Verification[0]; got.Name != "增量测试" || got.OnFailure != "reject" {
		t.Fatalf("gate = %+v", got)
	}
	if err := ApplyRecommendation(domain.RunEvalRecommendation{Target: "agent", Action: "append_instructions", Text: "不要空转 Read"}, nil, &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Instructions == "修 bug" {
		t.Fatal("instructions should be appended")
	}
}
