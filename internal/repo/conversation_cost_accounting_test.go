package repo

import (
	"math"
	"testing"

	"agentflow/internal/domain"
)

func TestConversationReportedCostsFallbackAndCoverage(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, id := range []string{"priced-chat", "idle-chat", "cache-chat"} {
		if err := s.PutRun(domain.Run{ID: id, Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowID: domain.ConversationWorkspaceWorkflowID, ExecutionContext: domain.TelemetryContext{ProjectID: "chat-costs", ResponsibleUserID: 7}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		model string
		usage domain.TokenUsage
	}{
		{"unknown-model", domain.TokenUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUSD: .1, CostStatus: "reported"}},
		{"codex:gpt-5.5", domain.TokenUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CacheReadTokens: 40}},
		{"unknown-model", domain.TokenUsage{InputTokens: 1, TotalTokens: 1}},
	} {
		if err := s.AddConversationRunUsage("priced-chat", tc.model, tc.usage); err != nil {
			t.Fatal(err)
		}
	}
	stale, _ := s.RunState("cache-chat")
	if err := s.AddConversationRunUsage("cache-chat", "claude-sonnet-4-6", domain.TokenUsage{CacheReadTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	stale.WorkspaceEditorID = 9
	if err := s.PutRun(stale); err != nil {
		t.Fatal(err)
	}
	if cached, _ := s.RunState("cache-chat"); cached.Usage.CacheReadTokens != 1000 || math.Abs(cached.Usage.CostUSD-.0003) > 1e-10 {
		t.Fatalf("cache-only cost was dropped by a stale workspace write: %+v", cached.Usage)
	}
	for _, scope := range []string{"", "overview"} {
		out := s.Analytics(domain.AnalyticsFilter{ProjectID: "chat-costs", Scope: scope})
		want := .7 + .00434 + .0021
		if math.Abs(out.Summary.TotalCostCNY-want) > 1e-10 || out.Summary.ConversationCostUnknownWorkspaces != 1 || out.Summary.CostUnknownRuns != 0 {
			t.Fatalf("scope %q: %+v", scope, out.Summary)
		}
		if out.ProjectUsage[0].CostKnown || out.UserUsage[0].CostKnown || out.Trend[0].ConversationCostUnknownWorkspaces != 1 || math.Abs(out.Summary.CostCoveragePct-200.0/3) > 1e-9 {
			t.Fatalf("unknown conversation cost was displayed as free: %+v", out)
		}
	}
}
