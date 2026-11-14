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

func TestAssistantActivityTextBudgetHelpers(t *testing.T) {
	if activityTextLimit != 32000 || activityBytesLimit != 512<<10 || activityEventLimit != 512 {
		t.Fatalf("budget constants changed: %d %d %d", activityTextLimit, activityBytesLimit, activityEventLimit)
	}

	// 追加：单条正文超限时只置 truncated，原有正文不变。
	var log assistantActivityLog
	log.events = []domain.AssistantActivityEvent{{Kind: "text", Text: strings.Repeat("a", activityTextLimit)}}
	last := &log.events[0]
	log.bytes = activityTextLimit
	if log.growTextLocked(last, "b") || !log.truncated || len(last.Text) != activityTextLimit {
		t.Fatal("growTextLocked accepted text over the per-event limit")
	}

	// 追加：总量超限同样拒绝写入。
	log = assistantActivityLog{events: []domain.AssistantActivityEvent{{Kind: "text"}}, bytes: activityBytesLimit}
	last = &log.events[0]
	if log.growTextLocked(last, "b") || !log.truncated || last.Text != "" {
		t.Fatal("growTextLocked accepted text over the total limit")
	}

	// 追加：未超限时正文与总量同步增长。
	var withRoom assistantActivityLog
	withRoom.events = []domain.AssistantActivityEvent{{Kind: "text", Text: strings.Repeat("a", 1000)}}
	withRoom.bytes = 1000
	if !withRoom.growTextLocked(&withRoom.events[0], strings.Repeat("b", 1000)) {
		t.Fatal("growTextLocked rejected text that fits")
	}
	if len(withRoom.events[0].Text) != 2000 || withRoom.bytes != 2000 {
		t.Fatalf("growTextLocked bookkeeping: %d %d", len(withRoom.events[0].Text), withRoom.bytes)
	}
	// 替换：按替换后的总量判断，旧正文占用的字节要扣掉。
	var replaced assistantActivityLog
	replaced.events = []domain.AssistantActivityEvent{{Kind: "text", Text: strings.Repeat("a", 40000)}}
	replaced.bytes = activityBytesLimit - 100
	if !replaced.replaceTextLocked(&replaced.events[0], strings.Repeat("c", 30000)) {
		t.Fatal("replaceTextLocked ignored the bytes freed by the replaced text")
	}
	if len(replaced.events[0].Text) != 30000 || replaced.bytes != activityBytesLimit-100-40000+30000 {
		t.Fatalf("replaceTextLocked bookkeeping: %d %d", len(replaced.events[0].Text), replaced.bytes)
	}

	// 新事件：条数上限和总量上限都拦。
	full := assistantActivityLog{events: make([]domain.AssistantActivityEvent, activityEventLimit)}
	if full.reserveEventLocked(1) || !full.truncated {
		t.Fatal("reserveEventLocked accepted an event over the count limit")
	}
	heavy := assistantActivityLog{bytes: activityBytesLimit}
	if heavy.reserveEventLocked(1) || !heavy.truncated {
		t.Fatal("reserveEventLocked accepted an event over the total limit")
	}

	// 重发去重只看 text 事件。
	var seen assistantActivityLog
	seen.events = []domain.AssistantActivityEvent{
		{Kind: "thinking", Text: "只在思考里出现"},
		{Kind: "text", Text: "已经展示的段落"},
	}
	if !seen.containsTextLocked("已经展示的段落") || seen.containsTextLocked("只在思考里出现") {
		t.Fatal("containsTextLocked looked at non-text events or missed a text match")
	}
}
