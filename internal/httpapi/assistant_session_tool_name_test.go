package httpapi

import (
	"encoding/json"
	"testing"
)

func TestAssistantSessionToolName(t *testing.T) {
	cases := map[string]string{
		"mcp__agentflow_session__submit_assistant_reply":      "submit_assistant_reply",
		"  mcp__agentflow_session__workbench_workspace_open ": "workbench_workspace_open",
		"Agent":         "Agent",
		"  Read\n":      "Read",
		"mcp__other__x": "mcp__other__x",
		"":              "",
		"   ":           "",
	}
	for in, want := range cases {
		if got := assistantSessionToolName(in); got != want {
			t.Errorf("assistantSessionToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssistantSessionToolNameCallSites(t *testing.T) {
	if !assistantProtocolTool(" mcp__agentflow_session__submit_clarification ") {
		t.Fatal("prefixed protocol tool must be recognized")
	}
	if _, ok := assistantToolStreamEvent("t1", "mcp__agentflow_session__submit_assistant_reply", nil); ok {
		t.Fatal("protocol tool must not produce a stream event")
	}
	tool, detail := assistantToolDisplay(" mcp__agentflow_session__workbench_publish_artifact", json.RawMessage(`{"title":"报告"}`))
	if tool != "发布产物" || detail != "报告" {
		t.Fatalf("display = %q %q", tool, detail)
	}
	start := `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"r1","name":"mcp__agentflow_session__submit_assistant_reply"}}}`
	events := assistantLineEvents(start)
	if len(events) != 1 || events[0].Kind != "draftStart" || events[0].ID != "r1" {
		t.Fatalf("events = %+v", events)
	}
}
