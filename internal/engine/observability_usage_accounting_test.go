package engine

import (
	"math"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	flowruntime "agentflow/internal/runtime"
)

func TestCodeBuddyMessageUsageDoesNotResetCumulativeCursor(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	run := domain.Run{ID: "usage-snapshots", Status: "running", Nodes: map[string]*domain.NodeRun{"work": {Provider: "codebuddy", Status: "running"}}}
	observer := flowruntime.NewProtocolObserver(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, func(event domain.TelemetryEvent) {
		event.ID = ""
		e.applyObservation(&run, "work", event)
	})
	for _, frame := range []string{
		`{"type":"stream_event","session_id":"same-session","event":{"type":"message_delta","usage":{"output_tokens":3}}}`,
		`{"type":"result","session_id":"same-session","result":"done","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":1000}}`,
		`{"type":"stream_event","session_id":"same-session","event":{"type":"message_delta","usage":{"output_tokens":5}}}`,
		`{"type":"result","session_id":"same-session","parent_tool_use_id":"child","usage":{"input_tokens":50,"output_tokens":5}}`,
		`{"type":"result","session_id":"same-session","result":"done","usage":{"input_tokens":200,"output_tokens":20,"cache_read_input_tokens":2000}}`,
	} {
		observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: frame})
	}
	for _, usage := range []domain.TokenUsage{run.Usage, run.Nodes["work"].Usage} {
		if usage.InputTokens != 200 || usage.OutputTokens != 20 || usage.CacheReadTokens != 2000 || usage.TotalTokens != 220 {
			t.Fatalf("message snapshots rebilled cumulative usage: %+v", usage)
		}
		priced := domain.AnalyticsPricedUsage("claude-sonnet-4-6", usage, false)
		if math.Abs(priced.CostUSD*domain.AnalyticsUSDToCNY-.0105) > 1e-10 {
			t.Fatalf("public-price estimate includes duplicate usage: %+v", priced)
		}
	}
	progress := false
	for _, event := range run.Telemetry {
		progress = progress || event.Name == "usage.update"
	}
	if !progress {
		t.Fatal("progress telemetry must remain available")
	}
}
