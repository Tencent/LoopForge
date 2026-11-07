package httpapi

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestAssistantVisibleReplyKeepsParagraphsBetweenTools(t *testing.T) {
	var log assistantActivityLog
	log.add(assistantStreamEvent{Kind: "answer", Text: "第一"})
	log.add(assistantStreamEvent{Kind: "answer", Text: "段。"})
	log.add(assistantStreamEvent{Kind: "answer", Text: "第一段。", Full: true})
	log.add(assistantStreamEvent{Kind: "tool", ID: "a", Tool: "issues_list"})
	log.add(assistantStreamEvent{Kind: "toolDone", ID: "a"})
	// The finished block is streamed again after the tool. It stays where it was.
	log.add(assistantStreamEvent{Kind: "answer", Text: "第一段。", Full: true})
	log.add(assistantStreamEvent{Kind: "answer", Text: "第二段。"})
	events := log.snapshot()
	var kinds []string
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	if strings.Join(kinds, ",") != "text,tool,toolDone,text" {
		t.Fatalf("order: %s", strings.Join(kinds, ","))
	}
	if got := assistantVisibleReply("第二段。", events); got != "第一段。\n\n第二段。" {
		t.Fatalf("joined reply: %q", got)
	}
	if got := assistantVisibleReply("第二段。\n\n汇总", events); got != "第一段。\n\n第二段。\n\n汇总" {
		t.Fatalf("tail reply: %q", got)
	}
	log.add(assistantStreamEvent{Kind: "answerReset"})
	left := log.snapshot()
	if len(left) != 2 || left[0].Kind != "tool" || strings.Contains(assistantVisibleReply("", left), "第一段") {
		t.Fatalf("reset kept paragraphs: %+v", left)
	}
	h := &assistantHandlers{}
	prompt := h.turnPrompt(assistantRequest{Messages: []assistantMessage{
		{Role: "user", Content: "先查项目"},
		{Role: "assistant", Content: `{"message":"第一段。","drafts":[]}`},
		{Role: "user", Content: "改查仓库", Trigger: "steer"},
	}}, "", true)
	if !strings.Contains(prompt, "第一段。") || !strings.Contains(prompt, "改查仓库") || !strings.Contains(prompt, "新的要求") {
		t.Fatalf("steer prompt: %s", prompt)
	}
}

func TestCLIParagraphsSurviveTheFinalResult(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"第 1 段。"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"第 1 段。"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"第 1 段。"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"第 2 段。"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"第 2 段。"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"pwd"}}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"第 10 段。\n\n| 项 | 结果 |\n| 轮次 | 10 |"}]}}`,
	}
	var filter assistantStreamFilter
	var log assistantActivityLog
	for _, line := range lines {
		for _, ev := range assistantStreamEvents(line) {
			if filter.accept(ev) {
				log.add(ev)
			}
		}
	}
	events := log.snapshot()
	var texts []string
	for _, event := range events {
		if event.Kind == "text" {
			texts = append(texts, strings.TrimSpace(event.Text))
		}
	}
	if strings.Join(texts, "|") != "第 1 段。|第 2 段。|第 10 段。\n\n| 项 | 结果 |\n| 轮次 | 10 |" {
		t.Fatalf("paragraphs: %#v\nevents: %+v", texts, events)
	}
	final := "第 10 段。\n\n| 项 | 结果 |\n| 轮次 | 10 |"
	got := assistantVisibleReply(final, events)
	for _, part := range []string{"第 1 段。", "第 2 段。", "第 10 段。", "轮次"} {
		if !strings.Contains(got, part) {
			t.Fatalf("visible reply lost %q: %s", part, got)
		}
	}
	if strings.Count(got, "第 1 段。") != 1 || strings.Count(got, "第 10 段。") != 1 {
		t.Fatalf("paragraph repeated or dropped: %s", got)
	}
}

func TestAssistantVisibleReplyDropsARepeatedParagraph(t *testing.T) {
	events := []domain.AssistantActivityEvent{{Kind: "text", Text: "收到，这是一个新测试对话。"}}
	once := "收到，这是一个新测试对话。"
	for _, final := range []string{once, once + "\n\n" + once, "收到，这是一个新测试对话。 "} {
		if got := assistantVisibleReply(final, events); got != once {
			t.Fatalf("final %q stored %q", final, got)
		}
	}
	parts := []domain.AssistantActivityEvent{
		{Kind: "text", Text: "第一段。"},
		{Kind: "tool", ID: "a"},
		{Kind: "text", Text: "第二段。"},
	}
	if got := assistantVisibleReply("第一段。\n\n第二段。\n\n第一段。\n\n第二段。", parts); got != "第一段。\n\n第二段。" {
		t.Fatalf("whole reply repeated: %q", got)
	}
	if got := assistantVisibleReply("第二段。\n\n汇总", parts); got != "第一段。\n\n第二段。\n\n汇总" {
		t.Fatalf("new tail dropped: %q", got)
	}
}

func TestAssistantReplyKeepsMarkdownSpacing(t *testing.T) {
	var log assistantActivityLog
	for _, piece := range []string{"##", " ", "合入结果", "\n", "| 项 | 值 |", "\n", "|---|---|"} {
		log.add(assistantStreamEvent{Kind: "answer", Text: piece})
	}
	events := log.snapshot()
	const want = "## 合入结果\n| 项 | 值 |\n|---|---|"
	if len(events) != 1 || events[0].Kind != "text" || events[0].Text != want {
		t.Fatalf("stream dropped markdown space: %#v", events)
	}
	broken := []domain.AssistantActivityEvent{{Kind: "text", Text: "##合入结果|项 |值 |\n|---|---|"}}
	if got := assistantVisibleReply(want, broken); got != want {
		t.Fatalf("stored the copy without spaces: %q", got)
	}
	spacedLast := []domain.AssistantActivityEvent{
		{Kind: "text", Text: "第一段。"},
		{Kind: "tool", ID: "a"},
		{Kind: "text", Text: "##合入结果|项 |"},
	}
	if got := assistantVisibleReply("## 合入结果\n| 项 |", spacedLast); got != "第一段。\n\n## 合入结果\n| 项 |" {
		t.Fatalf("earlier paragraph lost while restoring the heading: %q", got)
	}
}

func TestStoppedGenerationKeepsPartialTranscript(t *testing.T) {
	h, user := workbenchFixture(t)
	started := make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		sink := ctx.Value(assistantGenerationSinkKey{}).(func(string, any))
		sink("thought", assistantStreamEvent{Kind: "answer", Text: "已经写到这里"})
		sink("thought", assistantStreamEvent{Kind: "tool", ID: "t1", Tool: "issues_list", Text: "正在查看需求"})
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"partial-chat","messages":[{"role":"user","content":"查一下项目"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("generation never wrote")
	}
	g, _ := h.repo.AssistantGeneration(user.ID, "partial-chat")
	if err := h.repo.CancelAssistantGeneration(user.ID, "partial-chat", g.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		saved, _ := h.repo.AssistantSession(user.ID, "partial-chat")
		current, _ := h.repo.AssistantGeneration(user.ID, "partial-chat")
		if current.Status == "cancelled" && len(saved.Messages) == 2 {
			body := saved.Messages[1].Content
			if !strings.Contains(body, "已经写到这里") || !strings.Contains(body, `"stopped":true`) {
				t.Fatalf("partial reply: %s", body)
			}
			var sawTool bool
			for _, event := range saved.Messages[1].Activity {
				if event.Kind == "tool" && event.Tool == "issues_list" {
					sawTool = true
				}
			}
			if !sawTool {
				t.Fatalf("tool row missing: %+v", saved.Messages[1].Activity)
			}
			return
		}
		if current.Status != "running" && current.Status != "cancelled" && current.Status != "" {
			t.Fatalf("generation: %+v", current)
		}
		if time.Now().After(deadline) {
			t.Fatalf("partial transcript was not kept: status=%s messages=%d", current.Status, len(saved.Messages))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSteerContinuesFromPartialTranscript(t *testing.T) {
	h, user := workbenchFixture(t)
	started := make(chan struct{})
	var mu sync.Mutex
	var prompts []string
	h.generate = func(ctx context.Context, prompt string, _ domain.User) (string, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		n := len(prompts)
		mu.Unlock()
		if n == 1 {
			sink := ctx.Value(assistantGenerationSinkKey{}).(func(string, any))
			sink("thought", assistantStreamEvent{Kind: "answer", Text: "第一段说明"})
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "已改为查看仓库", nil
	}
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"steer-chat","messages":[{"role":"user","content":"先查项目"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("generation never wrote")
	}
	g, _ := h.repo.AssistantGeneration(user.ID, "steer-chat")
	c, rec = workbenchContext(user, "POST", "/api/assistant/sessions/steer-chat/generation/steer?generationId="+g.ID, bytes.NewBufferString(`{"content":"改查仓库","approvalMode":"confirm"}`))
	c.Params = gin.Params{{Key: "id", Value: "steer-chat"}}
	h.steerGeneration(c)
	if rec.Code != 200 {
		t.Fatalf("steer: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		saved, _ := h.repo.AssistantSession(user.ID, "steer-chat")
		current, _ := h.repo.AssistantGeneration(user.ID, "steer-chat")
		if current.Status == "completed" && len(saved.Messages) == 4 {
			if !strings.Contains(saved.Messages[1].Content, "第一段说明") {
				t.Fatalf("partial missing: %s", saved.Messages[1].Content)
			}
			if saved.Messages[2].Content != "改查仓库" || saved.Messages[2].Trigger != "steer" {
				t.Fatalf("steer message: %+v", saved.Messages[2])
			}
			if !strings.Contains(saved.Messages[3].Content, "已改为查看仓库") {
				t.Fatalf("follow-up: %s", saved.Messages[3].Content)
			}
			mu.Lock()
			got := append([]string(nil), prompts...)
			mu.Unlock()
			if len(got) < 2 || !strings.Contains(got[1], "第一段说明") || !strings.Contains(got[1], "改查仓库") {
				t.Fatalf("follow-up prompt: %v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("steer did not continue: status=%s messages=%d prompts=%d", current.Status, len(saved.Messages), len(prompts))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
