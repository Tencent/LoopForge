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

	"agentflow/internal/repo"
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
	if len(events) != 5 || events[4].Kind != "text" || events[4].Text != "final answer" || strings.Contains(string(raw), user.CodeBuddyAPIKey) || strings.Contains(string(raw), "private draft") {
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

func TestAssistantClaudeCodeReplyKeepsIndentationAcrossDeltas(t *testing.T) {
	const code = "    first()\n    second()"
	const complete = `{"type":"assistant","message":{"id":"msg_code","content":[{"type":"text","text":"    first()\n    second()"}]}}`
	const empty = `{"type":"assistant","message":{"id":"msg_code","content":[{"type":"text","text":""}]}}`
	const result = `{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","result":""}`
	for _, tc := range []struct {
		name      string
		deltas    []string
		streaming bool
	}{
		{name: "single-delta", deltas: []string{code}, streaming: true},
		{name: "split-indentation", deltas: []string{"    ", code[4:]}, streaming: true},
		{name: "non-streaming"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			if _, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{ClaudeCodeAPIKey: "test-claude-key"}); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			output.WriteString("#!/bin/sh\ncat <<'JSON'\n")
			for _, delta := range tc.deltas {
				payload, err := json.Marshal(delta)
				if err != nil {
					t.Fatal(err)
				}
				output.WriteString(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":`)
				output.Write(payload)
				output.WriteString("}}}\n")
			}
			output.WriteString(strings.Join([]string{complete, empty, result, "JSON", ""}, "\n"))
			bin := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(bin, []byte(output.String()), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_CLAUDE_BIN", bin)
			body, err := json.Marshal(assistantRequest{
				SessionID: "claude-" + tc.name, ProjectID: "workbench",
				Executor: "claude-code", Model: "claude-opus-4-8",
				Messages: []assistantMessage{{Role: "user", Content: "Return the example as indented code."}},
			})
			if err != nil {
				t.Fatal(err)
			}
			c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
			if tc.streaming {
				c.Request.Header.Set("Accept", "text/event-stream")
			}
			h.chat(c)
			session, ok := h.repo.AssistantSession(user.ID, "claude-"+tc.name)
			if rec.Code != 200 || !ok || len(session.Messages) != 2 {
				t.Fatalf("chat did not save the reply: status=%d messages=%d body=%s", rec.Code, len(session.Messages), rec.Body)
			}
			saved := session.Messages[1]
			var reply assistantReply
			if err := json.Unmarshal([]byte(saved.Content), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Message != code {
				t.Errorf("saved reply = %q, want %q", reply.Message, code)
			}
			if tc.streaming {
				var paragraphs []string
				for _, event := range saved.Activity {
					if event.Kind == "text" {
						paragraphs = append(paragraphs, event.Text)
					}
				}
				if len(paragraphs) != 1 || paragraphs[0] != code {
					t.Errorf("saved text activity = %q, want [%q]", paragraphs, code)
				}
			}
		})
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
