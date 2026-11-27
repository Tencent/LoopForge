package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Cursor reports every paragraph of the turn joined as its result. A reply
// that opened with a plan before calling tools must be judged by the text
// written after the tools, as the other CLIs report it.
func TestAssistantProgressTextJudgesTextAfterLastToolCall(t *testing.T) {
	answer := "仓库：登记 1 个 GitHub 仓库，默认分支 main。\n\n成员：只有负责人 roberttwang。"
	plan := "我先查本项目的代码仓库和成员，再分段总结。"
	cases := []struct {
		name     string
		message  string
		events   []domain.AssistantActivityEvent
		progress bool
	}{
		{
			name:    "plan, tools, answer joined by Cursor",
			message: plan + answer,
			events: []domain.AssistantActivityEvent{
				{Kind: "text", Text: plan},
				{Kind: "tool", Tool: "agentflow__repositories_list"},
				{Kind: "toolDone"},
				{Kind: "thinking", Text: "整理结果"},
				{Kind: "text", Text: answer},
			},
		},
		{
			name:     "plan then tools without any answer",
			message:  plan,
			events:   []domain.AssistantActivityEvent{{Kind: "text", Text: plan}, {Kind: "tool", Tool: "Read"}, {Kind: "toolDone"}},
			progress: true,
		},
		{
			name:    "text after the last tool is itself a progress note",
			message: plan + "接下来我会修改 store.py 并补测试。",
			events: []domain.AssistantActivityEvent{
				{Kind: "text", Text: plan},
				{Kind: "tool", Tool: "Read"},
				{Kind: "text", Text: "接下来我会修改 store.py 并补测试。"},
			},
			progress: true,
		},
		{
			name:     "no tool call judges the whole reply",
			message:  "稍等，我先检查一下。",
			events:   []domain.AssistantActivityEvent{{Kind: "text", Text: "稍等，我先检查一下。"}},
			progress: true,
		},
		{
			name:    "subagent tools do not hide Main Bot's answer",
			message: plan + answer,
			events: []domain.AssistantActivityEvent{
				{Kind: "text", Text: plan},
				{Kind: "tool", Tool: "Agent", ID: "dispatch-1"},
				{Kind: "tool", Tool: "Read", Parent: "dispatch-1"},
				{Kind: "agentText", Text: "我先读取文件", Parent: "dispatch-1"},
				{Kind: "toolDone", ID: "dispatch-1"},
				{Kind: "text", Text: answer},
				{Kind: "tool", Tool: "Grep", Parent: "dispatch-1"},
			},
		},
		{
			name:     "a CLI notice after the tools is not an answer",
			message:  plan,
			events:   []domain.AssistantActivityEvent{{Kind: "text", Text: plan}, {Kind: "tool", Tool: "Read"}, {Kind: "text", Text: cliBashNotice}},
			progress: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assistantProgressOnly(assistantProgressText(tc.message, tc.events), nil); got != tc.progress {
				t.Fatalf("progress-only = %v, want %v (judged %q)", got, tc.progress, assistantProgressText(tc.message, tc.events))
			}
		})
	}
}

// End to end through the chat handler: a CLI that streams a plan, calls a
// tool, streams the answer and then reports all of it joined (as Cursor
// does) must finish in one run. Before the fix the joined result was taken
// for a progress note, the CLI ran again and the answer was stored twice.
func TestAssistantJoinedResultAfterToolsIsNotRetriedAsProgress(t *testing.T) {
	h, user := workbenchFixture(t)
	bin := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/sh
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查看项目里的 LOOP。"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"list-1","name":"mcp__agentflow__workflows_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list-1","content":"[]"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"项目中有后端研发 LOOP，共四个节点。\n\n建议直接用它派发需求。"}]}}
{"type":"result","result":"我先查看项目里的 LOOP。项目中有后端研发 LOOP，共四个节点。\n\n建议直接用它派发需求。"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	sid := "joined-progress"
	body, _ := json.Marshal(assistantRequest{SessionID: sid, ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "先说一句你要做什么，然后查看已有 LOOP，分 2 段说明"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(user.ID, sid)
		if g.Status == "completed" {
			break
		}
		if g.Status != "running" || time.Now().After(deadline) {
			// Before the fix: retried as progress until 模型连续只返回进度说明.
			t.Fatalf("generation %s: %s", g.Status, g.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, ok := h.repo.AssistantSession(user.ID, sid)
	if !ok || len(saved.Messages) != 2 {
		t.Fatalf("saved session: %+v", saved)
	}
	var stored struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(saved.Messages[1].Content), &stored); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stored.Message, "共四个节点") != 1 || !strings.Contains(stored.Message, "我先查看项目里的 LOOP。") {
		t.Fatalf("stored reply = %q", stored.Message)
	}
	for _, event := range saved.Messages[1].Activity {
		if event.Kind == "status" && strings.Contains(event.Text, "继续执行尚未完成的步骤") {
			t.Fatalf("progress retry status recorded: %+v", saved.Messages[1].Activity)
		}
	}
}

// The plan-only case still continues: text before the tools and nothing
// after them is a progress note even when the CLI streamed it.
func TestAssistantPlanBeforeToolsWithoutAnswerStillContinues(t *testing.T) {
	h, user := workbenchFixture(t)
	// Count only reply runs: the background title summary uses the CLI too.
	h.titleSummary = func(context.Context, domain.User, string) (string, error) { return "", nil }
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
n=$((n + 1))
echo "$n" > '` + counter + `'
if [ "$n" -gt 1 ]; then
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"项目中有后端研发 LOOP，共四个节点。"}]}}
{"type":"result","result":"项目中有后端研发 LOOP，共四个节点。"}
JSON
exit 0
fi
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查看项目里的 LOOP。"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"list-1","name":"mcp__agentflow__workflows_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list-1","content":"[]"}]}}
{"type":"result","result":"我先查看项目里的 LOOP。"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	h.generate = nil
	body, _ := json.Marshal(assistantRequest{SessionID: "plan-only", ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "查看已有 LOOP 并给出结果"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	calls, _ := os.ReadFile(counter)
	if rec.Code != 200 || strings.TrimSpace(string(calls)) != "2" || !strings.Contains(rec.Body.String(), "共四个节点") {
		t.Fatalf("calls=%s code=%d body=%s", strings.TrimSpace(string(calls)), rec.Code, rec.Body.String())
	}
}

// A Bot reply is judged the same way: the joined plan and answer is the
// reply, and the CLI is not run again in terminal-only mode for it.
func TestBotJoinedResultAfterToolsIsNotRetriedAsProgress(t *testing.T) {
	stubCLIModelDiscovery(t)
	_, h, bot := botFixture(t)
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
echo "$((n + 1))" > '` + counter + `'
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查询一下项目状态。"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"runs-1","name":"mcp__agentflow__runs_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"runs-1","content":"[]"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"项目当前有 2 个运行中的 LOOP，均在实现节点。"}]}}
{"type":"result","result":"我先查询一下项目状态。项目当前有 2 个运行中的 LOOP，均在实现节点。"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-joined", ChatType: "single"}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "项目状态"})
	calls, _ := os.ReadFile(counter)
	if err != nil || strings.TrimSpace(string(calls)) != "1" || !strings.Contains(reply, "均在实现节点") {
		t.Fatalf("calls=%s err=%v reply=%q", strings.TrimSpace(string(calls)), err, reply)
	}
}

// A reply is a progress note only when every line is a plan. The first case
// is the text a real Cursor (grok-4.7) turn wrote after its last tool call.
func TestAssistantProgressOnlyRequiresEveryLineToBeAPlan(t *testing.T) {
	cases := []struct {
		text     string
		progress bool
	}{
		{"我先查了本项目的代码仓库和成员，结论如下。\n\n仅 1 个 GitHub 仓：loopforge-sweep-20261127-1200，分支 main。\n\n所有者编号 2，与当前用户 roberttwang 一致。\n\n加入申请 0 条，接口未返回其他成员。", false},
		{"我先查了仓库：\n- 1 个 GitHub 仓库\n- 默认分支 main", false},
		{"我先查看项目里的 LOOP。", true},
		{"稍等，我先检查", true},
		{"我先看看 store.py。\n接下来我会补测试。", true},
		{"我先读代码。\n\n接下来我将运行测试。", true},
		{"", false},
		{"   \n\n  ", false},
	}
	for _, tc := range cases {
		if got := assistantProgressOnly(tc.text, nil); got != tc.progress {
			t.Errorf("assistantProgressOnly(%q) = %v, want %v", tc.text, got, tc.progress)
		}
	}
}
