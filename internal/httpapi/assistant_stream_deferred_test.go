package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAssistantDeferredToolShowsActualTargetAndParams(t *testing.T) {
	for _, tc := range []struct{ name, input, tool, detail string }{
		{"real CodeBuddy arguments", `{"toolName":"mcp__agentflow_session__workbench_workspace_read","params":{"file_path":"/workspace/reports/summary.md"}}`, "workbench_workspace_read", "summary.md"},
		{"string params", `{"toolName":"mcp__agentflow_session__agentflow__runs_list","params":"{\"query\":\"latest failed runs\"}"}`, "agentflow__runs_list", "latest failed runs"},
		{"legacy wrapper", `{"tool":"mcp__agentflow_session__tasks_list","query":"pending"}`, "tasks_list", "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := assistantToolStreamEvent("call-1", "DeferExecuteTool", json.RawMessage(tc.input))
			if !ok || ev.Tool != tc.tool || ev.Detail != tc.detail || ev.ID != "call-1" {
				t.Fatalf("event=%+v present=%v", ev, ok)
			}
		})
	}
}

func TestAssistantDeferredEmptyStartDoesNotHideCompletedTarget(t *testing.T) {
	var filter assistantStreamFilter
	start := assistantStreamEvents(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"call-2","name":"DeferExecuteTool","input":{}}}}`)
	for _, ev := range start {
		if filter.accept(ev) {
			t.Fatalf("unresolved wrapper consumed tool ID: %+v", ev)
		}
	}
	snapshot := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-2","name":"DeferExecuteTool","input":{"toolName":"mcp__agentflow_session__agentflow__runs_retry","params":{"path":{"id":"run-1"},"body":{"nodeId":"summary"}}}}]}}`
	events := assistantStreamEvents(snapshot)
	if len(events) != 1 || events[0].Tool != "agentflow__runs_retry" || !filter.accept(events[0]) {
		t.Fatalf("resolved target missing: %+v", events)
	}
	if filter.accept(events[0]) {
		t.Fatal("repeated complete snapshot must not duplicate tool row")
	}
}

func TestAssistantDeferredTerminalToolsStayHidden(t *testing.T) {
	for _, name := range []string{"submit_assistant_reply", "mcp__agentflow_session__submit_assistant_reply", "mcp__agentflow_session__submit_clarification"} {
		raw, _ := json.Marshal(map[string]any{"toolName": name, "params": map[string]any{"message": "done", "drafts": []any{}}})
		if ev, ok := assistantToolStreamEvent("terminal", "DeferExecuteTool", raw); ok {
			t.Fatalf("protocol tool leaked: %+v", ev)
		}
	}
}

func TestAssistantToolSearchShowsRealCodeBuddyQueries(t *testing.T) {
	for _, tc := range []struct{ input, detail string }{
		{`{"queries":["retry run","workspace lease"]}`, "retry run, workspace lease"},
		{`{"tool_names":["mcp__agentflow_session__submit_assistant_reply"]}`, "mcp__agentflow_session__submit_assistant_reply"},
	} {
		tool, detail := assistantToolDisplay("ToolSearch", json.RawMessage(tc.input))
		if tool != "ToolSearch" || detail != tc.detail {
			t.Fatalf("tool=%q detail=%q", tool, detail)
		}
	}
}

func TestAssistantToolResultsPreserveAllCompletionStatesWithoutContent(t *testing.T) {
	events := assistantStreamEvents(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"failed-call","is_error":true,"content":"do-not-forward-secret"},{"type":"tool_result","tool_use_id":"successful-call","content":[{"type":"text","text":"private tool result"}]},{"type":"text","text":"ignore"},{"type":"tool_result","tool_use_id":" "}]}}`)
	if len(events) != 2 || events[0].ID != "failed-call" || !events[0].Failed || events[1].ID != "successful-call" || events[1].Failed {
		t.Fatalf("completion states=%+v", events)
	}
	var filter assistantStreamFilter
	for _, event := range events {
		if event.Kind != "toolDone" || event.Tool != "" || event.Text != "" || event.Detail != "" || !filter.accept(event) {
			t.Fatalf("unexpected completion payload=%+v", event)
		}
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private tool result") {
		t.Fatal("tool content leaked through completion")
	}
}
