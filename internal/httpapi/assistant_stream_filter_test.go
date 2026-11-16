package httpapi

import "testing"

func TestAppendStreamTextDropsEmptyAndRepeatedFullBlocks(t *testing.T) {
	var acc string
	steps := []struct {
		ev   assistantStreamEvent
		want bool
		acc  string
	}{
		{assistantStreamEvent{Text: ""}, false, ""},
		{assistantStreamEvent{Text: "", Full: true}, false, ""},
		{assistantStreamEvent{Text: "hello ", Full: true}, true, "hello "},
		{assistantStreamEvent{Text: "world"}, true, "hello world"},
		{assistantStreamEvent{Text: "  hello world\n", Full: true}, false, "hello world"},
		{assistantStreamEvent{Text: "!", Full: true}, true, "hello world!"},
	}
	for i, step := range steps {
		if got := appendStreamText(&acc, step.ev); got != step.want {
			t.Fatalf("step %d: appendStreamText = %v, want %v", i, got, step.want)
		}
		if acc != step.acc {
			t.Fatalf("step %d: acc = %q, want %q", i, acc, step.acc)
		}
	}
}

func TestAssistantStreamFilterKeepsAnswerAndThinkingSeparate(t *testing.T) {
	var f assistantStreamFilter
	if !f.accept(assistantStreamEvent{Kind: "answer", Text: "plan"}) {
		t.Fatal("answer delta dropped")
	}
	if !f.accept(assistantStreamEvent{Kind: "thinking", Text: "plan", Full: true}) {
		t.Fatal("thinking full block deduped against answer text")
	}
	if f.accept(assistantStreamEvent{Kind: "answer", Text: "plan", Full: true}) {
		t.Fatal("repeated answer full block kept")
	}
	if f.answer != "plan" || f.thinking != "plan" {
		t.Fatalf("answer=%q thinking=%q", f.answer, f.thinking)
	}
}

func TestAssistantStreamFilterAcceptToolDedupes(t *testing.T) {
	var f assistantStreamFilter
	steps := []struct {
		name string
		ev   assistantStreamEvent
		want bool
	}{
		{"protocol tool hidden", assistantStreamEvent{Tool: "mcp__agentflow_session__submit_assistant_reply", ID: "p1"}, false},
		{"first announce", assistantStreamEvent{Tool: "Bash", ID: "t1"}, true},
		{"empty repeat", assistantStreamEvent{Tool: "Bash", ID: "t1"}, false},
		{"repeat adds detail", assistantStreamEvent{Tool: "Bash", ID: "t1", Detail: "ls"}, true},
		{"identical repeat", assistantStreamEvent{Tool: "Bash", ID: "t1", Detail: "ls"}, false},
		{"repeat with empty detail and input", assistantStreamEvent{Tool: "Bash", ID: "t1", AgentID: "a1"}, false},
		{"agent call adds task", assistantStreamEvent{Tool: "Agent", ID: "t2", Subagent: "Explore", Input: "find x"}, true},
		{"no id empty tool", assistantStreamEvent{}, false},
		{"no id same as last", assistantStreamEvent{Tool: "Agent", Detail: ""}, false},
		{"no id new detail", assistantStreamEvent{Tool: "Agent", Detail: "y"}, true},
		{"no id consecutive repeat", assistantStreamEvent{Tool: "Agent", Detail: "y"}, false},
		{"no id other tool", assistantStreamEvent{Tool: "Read", Detail: "y"}, true},
	}
	for _, step := range steps {
		if got := f.acceptTool(step.ev); got != step.want {
			t.Fatalf("%s: acceptTool = %v, want %v", step.name, got, step.want)
		}
	}
	if f.lastTool != "Read" || f.lastDetail != "y" {
		t.Fatalf("last tool = %q/%q", f.lastTool, f.lastDetail)
	}
}

func TestAssistantStreamFilterToolEndsReplyDraft(t *testing.T) {
	var f assistantStreamFilter
	f.accept(assistantStreamEvent{Kind: "draftStart"})
	if !f.accept(assistantStreamEvent{Kind: "draftDelta", Detail: "x"}) {
		t.Fatal("draft delta dropped while in reply")
	}
	if f.accept(assistantStreamEvent{Kind: "tool", Tool: "submit_clarification"}) {
		t.Fatal("protocol tool forwarded")
	}
	if f.accept(assistantStreamEvent{Kind: "draftDelta", Detail: "x"}) {
		t.Fatal("draft delta kept after a tool event")
	}
}
