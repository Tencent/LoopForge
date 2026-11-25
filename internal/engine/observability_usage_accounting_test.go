package engine

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	flowruntime "agentflow/internal/runtime"
)

func TestMessageUsageSurvivesInterruptedSOP(t *testing.T) {
	for _, provider := range []string{"codebuddy", "claude-code"} {
		for _, tc := range []struct {
			name, streamSession, final string
			wantInput, wantOutput      int64
			startUsage, onlyStart      bool
			repeatFields               bool
			wantRead, wantWrite        int64
			wantCost                   float64
		}{
			{name: "interrupted before result", streamSession: "s1", wantOutput: 13},
			{name: "interrupted after message start", streamSession: "s1", startUsage: true, onlyStart: true, wantInput: 120, wantRead: 512, wantWrite: 32},
			{name: "interrupted after output delta", streamSession: "s1", startUsage: true, wantInput: 120, wantOutput: 13, wantRead: 512, wantWrite: 32},
			{name: "partial deltas preserve message start usage", streamSession: "s1", startUsage: true, repeatFields: true, wantInput: 120, wantOutput: 13, wantRead: 512, wantWrite: 32},
			{name: "missing final usage preserves message start", streamSession: "s1", startUsage: true, final: `{"type":"result","session_id":"s1","is_error":true}`, wantInput: 120, wantOutput: 13, wantRead: 512, wantWrite: 32},
			{name: "final settles message start usage", streamSession: "s1", startUsage: true, final: `{"type":"result","session_id":"s1","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":256,"cache_creation_input_tokens":16}}`, wantInput: 100, wantOutput: 10, wantRead: 256, wantWrite: 16},
			{name: "result has no token usage", streamSession: "s1", final: `{"type":"result","session_id":"s1","is_error":true}`, wantOutput: 13},
			{name: "final replaces provisional usage", streamSession: "s1", final: `{"type":"result","session_id":"s1","usage":{"input_tokens":100,"output_tokens":10}}`, wantInput: 100, wantOutput: 10},
			{name: "explicit final zero", streamSession: "s1", final: `{"type":"result","session_id":"s1","usage":{}}`},
			{name: "cost-only result keeps observed tokens", streamSession: "s1", final: `{"type":"result","session_id":"s1","total_cost_usd":0.25}`, wantOutput: 13, wantCost: 0.25},
			{name: "session identified at result", final: `{"type":"result","session_id":"s1","usage":{"input_tokens":100,"output_tokens":10}}`, wantInput: 100, wantOutput: 10},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				s, err := repo.OpenTest()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.Close)
				e := New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
				run := domain.Run{ID: "partial-usage", Status: "running", Nodes: map[string]*domain.NodeRun{"work": {Provider: provider, Status: "running"}}}
				observer := flowruntime.NewProtocolObserver(domain.ExecutorDefinition{Kind: provider, Protocol: "stream-json"}, func(event domain.TelemetryEvent) {
					event.ID = ""
					e.applyObservation(&run, "work", event)
				})
				startUsage := map[string]int{"input_tokens": 120, "cache_read_input_tokens": 512, "cache_creation_input_tokens": 32}
				if tc.startUsage {
					frame, _ := json.Marshal(map[string]any{"type": "stream_event", "session_id": tc.streamSession, "event": map[string]any{"type": "message_start", "message": map[string]any{"usage": startUsage}}})
					observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: string(frame)})
				}
				frame, _ := json.Marshal(map[string]any{"type": "stream_event", "session_id": tc.streamSession, "event": map[string]any{"type": "message_delta", "usage": map[string]int{"output_tokens": 13}}})
				if !tc.onlyStart {
					for range 2 {
						observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: string(frame)})
					}
					if tc.repeatFields {
						// Repeated input/cache fields after output-only deltas must
						// not rebill the start or erase the observed output cursor.
						partial, _ := json.Marshal(map[string]any{"type": "stream_event", "session_id": tc.streamSession, "event": map[string]any{"type": "message_delta", "usage": startUsage}})
						observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: string(partial)})
						observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: string(frame)})
					}
				}
				if tc.final != "" {
					observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: tc.final})
				}
				e.applyObservation(&run, "work", domain.TelemetryEvent{Category: "process", Name: "process.finished", Phase: "finish", Level: "error"})
				run.Status, run.Nodes["work"].Status = "failed", "failed"
				if err := s.PutRun(run); err != nil {
					t.Fatal(err)
				}
				stored, ok := s.Run(run.ID)
				if !ok {
					t.Fatal("failed run was not stored")
				}
				for _, usage := range []domain.TokenUsage{stored.Usage, stored.Nodes["work"].Usage} {
					if usage.InputTokens != tc.wantInput || usage.OutputTokens != tc.wantOutput || usage.CacheReadTokens != tc.wantRead || usage.CacheWriteTokens != tc.wantWrite || usage.TotalTokens != tc.wantInput+tc.wantOutput || usage.CostUSD != tc.wantCost {
						t.Fatalf("observed usage was lost or counted twice: %+v", usage)
					}
				}
			})
		}
	}
}

func TestMessageUsageMultipleMessagesAndResumedSession(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	run := domain.Run{ID: "resumed-usage", Status: "running", Nodes: map[string]*domain.NodeRun{"work": {Status: "running"}}}
	observer := flowruntime.NewProtocolObserver(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, func(event domain.TelemetryEvent) {
		event.ID = ""
		e.applyObservation(&run, "work", event)
	})
	send := func(frame string) { observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: frame}) }
	send(`{"type":"stream_event","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":13}}}`)
	send(`{"type":"stream_event","session_id":"s1","parent_tool_use_id":"child","event":{"type":"message_delta","usage":{"output_tokens":5}}}`)
	send(`{"type":"stream_event","session_id":"s1","event":{"type":"message_start","message":{}}}`)
	send(`{"type":"stream_event","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":7}}}`)
	if run.Usage.TotalTokens != 25 {
		t.Fatalf("separate model messages or children share a stream cursor: %+v", run.Usage)
	}
	// Pending counters survive a persisted snapshot before the session resumes.
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run, _ = s.Run(run.ID)
	send(`{"type":"result","session_id":"s1","usage":{"input_tokens":100,"output_tokens":30}}`)
	send(`{"type":"stream_event","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":5}}}`)
	send(`{"type":"result","session_id":"s1","usage":{"input_tokens":200,"output_tokens":50}}`)
	if run.Usage.TotalTokens != 250 || run.Nodes["work"].Usage.TotalTokens != 250 {
		t.Fatalf("resumed session rebilled provisional or previous usage: %+v", run.Usage)
	}
	send(`{"type":"stream_event","session_id":"s1","event":{"type":"message_delta","usage":{"output_tokens":4}}}`)
	send(`{"type":"stream_event","session_id":"s2","event":{"type":"message_delta","usage":{"output_tokens":3}}}`)
	send(`{"type":"result","session_id":"s2","usage":{"input_tokens":50,"output_tokens":5}}`)
	if run.Usage.TotalTokens != 309 || run.Nodes["work"].Usage.TotalTokens != 309 {
		t.Fatalf("a different session removed prior interrupted usage: %+v", run.Usage)
	}
}

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
