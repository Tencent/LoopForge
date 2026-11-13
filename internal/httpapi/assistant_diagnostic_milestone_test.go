package httpapi

import (
	"reflect"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestNativeMilestoneClassifiesProtocolEvents(t *testing.T) {
	cases := []struct {
		name      string
		raw       map[string]any
		milestone string
		dedupe    bool
	}{
		{"claude system init", map[string]any{"type": "system", "subtype": "init"}, "cli.initialized", true},
		{"codex initialize result", map[string]any{"result": map[string]any{"userAgent": "codex_cli_rs/0.40.0"}}, "cli.initialized", true},
		{"init event string", map[string]any{"event": "init"}, "cli.initialized", true},
		{"system non-init", map[string]any{"type": "system", "subtype": "status"}, "", false},
		{"codex turn started", map[string]any{"method": "turn/started"}, "model.turn_started", false},
		{"pi turn start", map[string]any{"type": "turn_start"}, "model.turn_started", false},
		{"codex agent delta", map[string]any{"method": "item/agentMessage/delta"}, "model.first_output_delta", true},
		{"codex reasoning summary delta", map[string]any{"method": "item/reasoning/summaryTextDelta"}, "model.first_output_delta", true},
		{"codex reasoning delta", map[string]any{"method": "item/reasoning/textDelta"}, "model.first_output_delta", true},
		{"claude content delta", map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta"}}, "model.first_output_delta", true},
		{"claude other stream event", map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start"}}, "", false},
		{"model streaming", map[string]any{"type": "model.streaming"}, "model.first_output_delta", true},
		{"pi message update", map[string]any{"type": "message_update"}, "model.first_output_delta", true},
		{"codex turn completed", map[string]any{"method": "turn/completed"}, "model.turn_finished", false},
		{"claude result", map[string]any{"type": "result"}, "protocol.result_received", false},
		{"final", map[string]any{"type": "final"}, "protocol.result_received", false},
		{"pi agent end", map[string]any{"type": "agent_end"}, "protocol.result_received", false},
		{"init wins over result type", map[string]any{"type": "result", "event": "init"}, "cli.initialized", true},
		{"unrelated", map[string]any{"type": "assistant", "method": "item/started"}, "", false},
		{"empty", map[string]any{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			milestone, dedupe := nativeMilestone(tc.raw)
			if milestone != tc.milestone || dedupe != tc.dedupe {
				t.Fatalf("nativeMilestone(%v) = (%q, %v), want (%q, %v)", tc.raw, milestone, dedupe, tc.milestone, tc.dedupe)
			}
		})
	}
}

// nativeMetadata must keep honoring nativeMilestone's dedupe flag: one-shot
// milestones are recorded once per attempt, repeating ones every time, and a
// new attempt starts its own dedupe window.
func TestNativeMetadataRecordsMilestonesWithPerAttemptDedupe(t *testing.T) {
	g := domain.AssistantGeneration{ID: "milestone-turn", SessionID: "milestone-session", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	user := domain.User{ID: 1}
	first := d.begin("codex", "model-a", false, user)
	for _, line := range []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"system","subtype":"init"}`,
		`{"method":"turn/started"}`,
		`{"method":"item/agentMessage/delta"}`,
		`{"method":"item/reasoning/textDelta"}`,
		`{"method":"turn/completed"}`,
		`{"method":"turn/started"}`,
		`{"method":"item/agentMessage/delta"}`,
		`{"method":"turn/completed"}`,
		`{"type":"system","subtype":"status"}`,
	} {
		first.protocol(line)
	}
	second := d.begin("codex", "model-a", true, user)
	second.protocol(`{"type":"system","subtype":"init"}`)
	second.protocol(`{"method":"item/agentMessage/delta"}`)

	counts := map[string]map[string]int{}
	for _, e := range d.snapshot(g).Timeline {
		if e.Source != "cli" {
			continue
		}
		if counts[e.AttemptID] == nil {
			counts[e.AttemptID] = map[string]int{}
		}
		counts[e.AttemptID][e.Kind]++
	}
	wantFirst := map[string]int{"cli.initialized": 1, "model.turn_started": 2, "model.first_output_delta": 1, "model.turn_finished": 2}
	wantSecond := map[string]int{"cli.initialized": 1, "model.first_output_delta": 1}
	if got := counts[first.id]; !reflect.DeepEqual(got, wantFirst) {
		t.Fatalf("first attempt milestones = %v, want %v", got, wantFirst)
	}
	if got := counts[second.id]; !reflect.DeepEqual(got, wantSecond) {
		t.Fatalf("second attempt milestones = %v, want %v", got, wantSecond)
	}
}
