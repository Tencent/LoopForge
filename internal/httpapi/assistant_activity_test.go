package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssistantActivityKeepsCommandsAndUpdatesPartialTools(t *testing.T) {
	var filter assistantStreamFilter
	first, _ := assistantToolStreamEvent("command-1", "Bash", json.RawMessage(`{}`))
	complete, _ := assistantToolStreamEvent("command-1", "Bash", json.RawMessage(`{"command":"rg --files chatbot/src"}`))
	if !filter.accept(first) || !filter.accept(complete) || filter.accept(complete) || complete.Detail != "rg --files chatbot/src" {
		t.Fatal("complete command snapshot was lost or duplicated")
	}
	wrapped, ok := assistantToolStreamEvent("command-2", "DeferExecuteTool", json.RawMessage(`{"toolName":"mcp__agentflow_session__workbench_workspace_command","params":{"body":{"command":"go test ./..."}}}`))
	if !ok || wrapped.Detail != "go test ./..." {
		t.Fatalf("wrapped command missing: %+v", wrapped)
	}
}

func TestAssistantActivityPreservesVisibleStreamWithoutModelPromptOrSecrets(t *testing.T) {
	user := domain.User{CodeBuddyAPIKey: "test-personal-key"}
	var log assistantActivityLog
	for _, event := range []assistantStreamEvent{
		{Kind: "thinkingStart"}, {Kind: "thinking", Text: "读取项目信息：test-"}, {Kind: "thinking", Text: "personal-key"},
		{Kind: "tool", ID: "read-1", Tool: "Read", Detail: "readme.md"}, {Kind: "toolDone", ID: "read-1", Failed: true},
		{Kind: "thinkingStart"}, {Kind: "thinking", Text: "准备回答"},
		{Kind: "draftDelta", Detail: "private draft arguments"}, {Kind: "answer", Text: "final answer"},
	} {
		log.add(publicAssistantEvent(event, user))
	}
	events := log.forUser(user)
	raw, _ := json.Marshal(events)
	if len(events) != 4 || strings.Contains(string(raw), user.CodeBuddyAPIKey) || strings.Contains(string(raw), "private draft") || strings.Contains(string(raw), "final answer") {
		t.Fatalf("invalid public transcript: %s", raw)
	}
	if events[0].Text != "读取项目信息：[redacted]" || events[0].At == nil || !events[2].Failed || events[0].ID == events[3].ID {
		t.Fatalf("lost activity metadata: %+v", events)
	}
	original := []assistantMessage{{Role: "assistant", Content: "answer", Activity: events}}
	prompt := assistantPromptMessages(original)
	if len(prompt[0].Activity) != 0 || len(original[0].Activity) == 0 {
		t.Fatal("metadata leaked into prompt or original was mutated")
	}
}

func TestAssistantActivitySurvivesLaterTurnsAndSessionReload(t *testing.T) {
	h, user := workbenchFixture(t)
	bin := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/sh
cat <<'JSON'
{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"先查看文件，再运行命令。"}}}
{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"cmd-1","name":"Bash","input":{}}}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"cmd-1","name":"Bash","input":{"command":"printf verified"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"cmd-1","content":"private output"}]}}
{"type":"result","result":"执行完成"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	sid := "activity-history"
	messages := []assistantMessage{{Role: "user", Content: "检查文件"}}
	for turn := 0; turn < 2; turn++ {
		body, _ := json.Marshal(assistantRequest{SessionID: sid, ProjectID: "workbench", Messages: messages})
		c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
		h.startGeneration(c)
		if rec.Code != 202 {
			t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			g, _ := h.repo.AssistantGeneration(user.ID, sid)
			if g.Status == "completed" {
				break
			}
			if g.Status != "running" || time.Now().After(deadline) {
				t.Fatalf("generation: %+v", g)
			}
			time.Sleep(10 * time.Millisecond)
		}
		saved, ok := h.repo.AssistantSession(user.ID, sid)
		if !ok {
			t.Fatal("missing saved session")
		}
		first := saved.Messages[1].Activity
		raw, _ := json.Marshal(first)
		if len(first) == 0 || !bytes.Contains(raw, []byte("printf verified")) || !bytes.Contains(raw, []byte("先查看文件")) || bytes.Contains(raw, []byte("private output")) {
			t.Fatalf("activity did not survive turn %d: %s", turn, raw)
		}
		if turn == 0 {
			messages = append(saved.Messages, assistantMessage{Role: "user", Content: "再检查一次"})
		}
	}
}
