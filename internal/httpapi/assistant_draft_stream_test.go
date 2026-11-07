package httpapi

import "testing"

func TestAssistantStreamForwardsReplyDraftDeltasOnly(t *testing.T) {
	var filter assistantStreamFilter
	collect := func(line string) []assistantStreamEvent {
		var out []assistantStreamEvent
		for _, ev := range assistantStreamEvents(line) {
			if filter.accept(ev) {
				out = append(out, ev)
			}
		}
		return out
	}
	toolStart := `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"t1","name":"ToolSearch","input":{}}}}`
	delta := func(s string) string {
		return `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"` + s + `"}}}`
	}
	collect(toolStart)
	if got := collect(delta(`{\"query\":`)); len(got) != 0 {
		t.Fatalf("other tools' arguments must not leak as draft deltas: %+v", got)
	}
	start := collect(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"r1","name":"mcp__agentflow_session__submit_assistant_reply","input":{}}}}`)
	if len(start) != 1 || start[0].Kind != "draftStart" {
		t.Fatalf("reply tool start: %+v", start)
	}
	got := collect(delta(`{\"drafts\":[{\"tool\":\"workflows_save\"`))
	if len(got) != 1 || got[0].Kind != "draftDelta" || got[0].Detail != `{"drafts":[{"tool":"workflows_save"` || got[0].Text != "" {
		t.Fatalf("reply draft delta: %+v", got)
	}
	collect(toolStart)
	if got := collect(delta(`x`)); len(got) != 0 {
		t.Fatalf("a later tool call must stop draft forwarding: %+v", got)
	}
}
