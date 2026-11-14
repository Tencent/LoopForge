package httpapi

import (
	"encoding/json"
	"testing"
)

func TestAssistantMessageEventsClassifiesBlocks(t *testing.T) {
	blocks := []assistantStreamBlock{
		{Type: "thinking", Thinking: "  plan  "},
		{Type: "thinking", Thinking: "   "},
		{Type: "text", Text: "hello"},
		{Type: "text", Text: `{"json":true}`},
		{Type: "text", Text: `Tool "Bash" does not exist in the current tool set.`},
		{Type: "tool_use", ID: "call-1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/a.go"}`)},
	}
	got := assistantMessageEvents(blocks, false)
	if len(got) != 3 {
		t.Fatalf("events = %+v, want thinking, answer and tool", got)
	}
	if got[0].Kind != "thinking" || got[0].Text != "plan" || !got[0].Full {
		t.Fatalf("thinking event = %+v", got[0])
	}
	if got[1].Kind != "answer" || got[1].Text != "hello" || !got[1].Full {
		t.Fatalf("answer event = %+v", got[1])
	}
	if got[2].Kind != "tool" || got[2].ID != "call-1" {
		t.Fatalf("tool event = %+v", got[2])
	}

	sub := assistantMessageEvents([]assistantStreamBlock{{Type: "text", Text: "report"}}, true)
	if len(sub) != 1 || sub[0].Kind != "agentText" || sub[0].Text != "report" {
		t.Fatalf("sub-agent text = %+v, want agentText", sub)
	}
}

func TestAssistantPartialEventsBlockStartAndDeltas(t *testing.T) {
	var reply assistantStreamPartial
	reply.Type = "content_block_start"
	reply.ContentBlock.Type = "tool_use"
	reply.ContentBlock.ID = " reply "
	reply.ContentBlock.Name = "mcp__agentflow_session__submit_assistant_reply"
	if got := assistantPartialEvents(reply, false); len(got) != 1 || got[0].Kind != "draftStart" || got[0].ID != "reply" {
		t.Fatalf("reply start = %+v, want draftStart", got)
	}
	if got := assistantPartialEvents(reply, true); got != nil {
		t.Fatalf("sub-agent reply start = %+v, want nil", got)
	}

	var thinking assistantStreamPartial
	thinking.Type = "content_block_start"
	thinking.ContentBlock.Type = "thinking"
	if got := assistantPartialEvents(thinking, false); len(got) != 1 || got[0].Kind != "thinkingStart" {
		t.Fatalf("thinking start = %+v", got)
	}

	var draft assistantStreamPartial
	draft.Delta.Type = "input_json_delta"
	draft.Delta.PartialJSON = `{"a"`
	if got := assistantPartialEvents(draft, false); len(got) != 1 || got[0].Kind != "draftDelta" || got[0].Detail != `{"a"` {
		t.Fatalf("draft delta = %+v", got)
	}
	if got := assistantPartialEvents(draft, true); got != nil {
		t.Fatalf("sub-agent draft delta = %+v, want nil", got)
	}

	var text assistantStreamPartial
	text.Delta.Type = "text_delta"
	text.Delta.Text = "tok"
	if got := assistantPartialEvents(text, false); len(got) != 1 || got[0].Kind != "answer" || got[0].Text != "tok" {
		t.Fatalf("text delta = %+v", got)
	}
	if got := assistantPartialEvents(text, true); got != nil {
		t.Fatalf("sub-agent text delta = %+v, want nil", got)
	}

	var think assistantStreamPartial
	think.Delta.Type = "thinking_delta"
	think.Delta.Thinking = "hm"
	if got := assistantPartialEvents(think, true); len(got) != 1 || got[0].Kind != "thinking" || got[0].Text != "hm" {
		t.Fatalf("thinking delta = %+v", got)
	}

	if got := assistantPartialEvents(assistantStreamPartial{}, false); got != nil {
		t.Fatalf("empty event = %+v, want nil", got)
	}
}

func TestAssistantToolResultEventsMarksDoneAndKeepsAgentReport(t *testing.T) {
	blocks := []assistantStreamBlock{
		{Type: "text", Text: "ignored"},
		{Type: "tool_result", ToolUseID: "  "},
		{Type: "tool_result", ToolUseID: " plain ", IsError: true, Content: json.RawMessage(`"secret output"`)},
		{Type: "tool_result", ToolUseID: "agent", Content: json.RawMessage(`[{"type":"text","text":"done\nAgent ID: agent-abc123"}]`)},
	}
	got := assistantToolResultEvents(blocks)
	if len(got) != 2 {
		t.Fatalf("events = %+v, want two toolDone", got)
	}
	if got[0].Kind != "toolDone" || got[0].ID != "plain" || !got[0].Failed || got[0].Output != "" || got[0].AgentID != "" {
		t.Fatalf("plain result = %+v, want completion only", got[0])
	}
	if got[1].Kind != "toolDone" || got[1].ID != "agent" || got[1].AgentID != "agent-abc123" || got[1].Output == "" {
		t.Fatalf("agent result = %+v, want agent ID and report", got[1])
	}
}
