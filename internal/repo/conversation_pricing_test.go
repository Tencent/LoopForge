package repo

import (
	"fmt"
	"math"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A conversation turn is priced with the model its reply was saved under, and
// conversation CLIs save that ID prefixed with the CLI (codex:gpt-5.5,
// claude-code:claude-sonnet-4-6). docs/token-pricing.md prices every model by
// its published tariff and folds conversation tokens into the same projection
// as SOP nodes, so the prefix must not turn the cost into "unknown".
func TestConversationUsagePricesPrefixedCLIModels(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prefix := fmt.Sprintf("conv-price-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		s.DB.Table("runs").Where("id LIKE ?", prefix+"%").Delete(nil)
	})
	run := domain.Run{
		ID: prefix + "-ws", Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID,
		Status: "waiting", CreatedAt: time.Now().UTC().Truncate(time.Second),
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, Name: "对话工作区"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix, ResponsibleUserID: 7},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	// Codex counts caches inside its input figure; the published gpt-5.5 tariff
	// is $5/M input, $30/M output and $0.5/M cache read.
	usage := domain.TokenUsage{InputTokens: 100000, OutputTokens: 10000, CacheReadTokens: 40000, TotalTokens: 110000}
	if err := s.AddConversationRunUsage(run.ID, "codex:gpt-5.5", usage); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("conversation workspace disappeared")
	}
	if stored.Usage.CostStatus != "estimated" || math.Abs(stored.Usage.CostUSD-0.62) > 1e-9 {
		t.Fatalf("codex conversation usage = %+v, want estimated $0.62", stored.Usage)
	}

	other := prefix + "-claude"
	run.ID = other
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	// Claude Code reports input without its caches, so the 500k cache reads are
	// charged at the $0.3/M read rate on top of $3/M input and $15/M output.
	claude := domain.TokenUsage{InputTokens: 1000000, OutputTokens: 1000000, CacheReadTokens: 500000, TotalTokens: 2000000}
	if err := s.AddConversationRunUsage(other, "claude-code:claude-sonnet-4-6", claude); err != nil {
		t.Fatal(err)
	}
	stored, _ = s.RunState(other)
	if stored.Usage.CostStatus != "estimated" || math.Abs(stored.Usage.CostUSD-18.15) > 1e-9 {
		t.Fatalf("claude conversation usage = %+v, want estimated $18.15", stored.Usage)
	}
}
