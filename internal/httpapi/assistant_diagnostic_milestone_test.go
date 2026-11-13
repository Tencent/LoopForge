package httpapi

import "testing"

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
