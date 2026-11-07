package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func waitGeneration(t *testing.T, h *assistantHandlers, userID int64, sessionID string, done func(domain.AssistantGeneration) bool) domain.AssistantGeneration {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(userID, sessionID)
		if done(g) {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation did not settle: %+v", g)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A stopped turn used to leave nothing in the transcript, so its thinking,
// tool calls and partial answer disappeared as soon as the next message ran.
func TestStoppedGenerationKeepsTraceAndNextTurnSeesIt(t *testing.T) {
	h, u := workbenchFixture(t)
	entered := make(chan struct{}, 2)
	var mu sync.Mutex
	var prompts []string
	h.generate = func(ctx context.Context, prompt string, _ domain.User) (string, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		turn := len(prompts)
		mu.Unlock()
		if turn > 1 {
			return `{"message":"已按 CI 继续","drafts":[]}`, nil
		}
		sink := ctx.Value(assistantGenerationSinkKey{}).(func(string, any))
		sink("thought", assistantStreamEvent{Kind: "tool", Tool: "runs_get", Text: "run-zg", ID: "tool-1"})
		sink("thought", assistantStreamEvent{Kind: "answer", Text: "正在查看 zg 的节点"})
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"stopped-chat","messages":[{"role":"user","content":"查一下失败节点"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generation never started")
	}
	g, _ := h.repo.AssistantGeneration(u.ID, "stopped-chat")
	if g.MessageCount != 1 {
		t.Fatalf("message count: %d", g.MessageCount)
	}
	if err := h.repo.CancelAssistantGeneration(u.ID, "stopped-chat", g.ID); err != nil {
		t.Fatal(err)
	}
	waitGeneration(t, h, u.ID, "stopped-chat", func(g domain.AssistantGeneration) bool { return g.Status == "cancelled" })

	s, _ := h.repo.AssistantSession(u.ID, "stopped-chat")
	if len(s.Messages) != 2 {
		t.Fatalf("stopped reply not kept: %+v", s.Messages)
	}
	kept := s.Messages[1]
	if kept.Role != "assistant" || kept.Unfinished != "生成已停止" || kept.StartedAt == nil {
		t.Fatalf("unfinished reply: %+v", kept)
	}
	if replyMessageText(kept.Content) != "正在查看 zg 的节点" {
		t.Fatalf("partial answer: %s", kept.Content)
	}
	foundTool := false
	for _, step := range kept.Activity {
		foundTool = foundTool || (step.Kind == "tool" && step.Tool == "runs_get")
	}
	if !foundTool {
		t.Fatalf("activity lost: %+v", kept.Activity)
	}

	// The browser resends the stored transcript plus the queued message.
	next := append(append([]domain.AssistantMessage{}, s.Messages...), domain.AssistantMessage{Role: "user", Content: "应该看 CI 而不是 lint"})
	body, _ := json.Marshal(map[string]any{"projectId": "workbench", "sessionId": "stopped-chat", "messages": next})
	c, rec = workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("next turn after unfinished reply: %d %s", rec.Code, rec.Body.String())
	}
	waitGeneration(t, h, u.ID, "stopped-chat", func(g domain.AssistantGeneration) bool { return g.Status == "completed" })
	s, _ = h.repo.AssistantSession(u.ID, "stopped-chat")
	if len(s.Messages) != 4 || s.Messages[1].Unfinished == "" || s.Messages[3].Unfinished != "" {
		t.Fatalf("transcript after next turn: %+v", s.Messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(prompts[1], "这一轮回复未完成：生成已停止") || !strings.Contains(prompts[1], "runs_get") {
		t.Fatalf("next prompt does not explain the unfinished turn: %s", prompts[1])
	}
}

func TestStoppedGenerationWithoutProgressAddsNoReply(t *testing.T) {
	h, u := workbenchFixture(t)
	entered := make(chan struct{}, 1)
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"empty-stop","messages":[{"role":"user","content":"你好"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d", rec.Code)
	}
	<-entered
	g, _ := h.repo.AssistantGeneration(u.ID, "empty-stop")
	_ = h.repo.CancelAssistantGeneration(u.ID, "empty-stop", g.ID)
	waitGeneration(t, h, u.ID, "empty-stop", func(g domain.AssistantGeneration) bool { return g.Status == "cancelled" })
	if s, _ := h.repo.AssistantSession(u.ID, "empty-stop"); len(s.Messages) != 1 {
		t.Fatalf("a turn without progress must not add a reply: %+v", s.Messages)
	}
}

// A server that died mid-turn is only noticed when the generation is read.
func TestInterruptedGenerationKeepsTraceOnce(t *testing.T) {
	h, u := workbenchFixture(t)
	session := domain.AssistantSession{ID: "dead-server", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "看下失败原因", CreatedAt: time.Now().UTC()}}}
	g := domain.AssistantGeneration{ID: "gen-dead", SessionID: "dead-server", UserID: u.ID, ActorID: u.ID, Status: "running", StartedAt: time.Now().UTC(), MessageCount: 1}
	if err := h.repo.BeginAssistantGeneration(g, session); err != nil {
		t.Fatal(err)
	}
	g.Stream = &domain.AssistantStreamSnapshot{Answer: "先查了运行", Activity: []domain.AssistantActivityEvent{{Kind: "tool", Tool: "runs_get", Text: "run-1"}}}
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.DB.Exec("UPDATE assistant_generations SET updated_at = ? WHERE session_id = ?", time.Now().Add(-time.Minute).UTC(), "dead-server").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		c, rec := workbenchContext(u, "GET", "/api/assistant/sessions/dead-server/generation", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: "dead-server"}}
		h.generation(c)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"interrupted"`) {
			t.Fatalf("generation read: %d %s", rec.Code, rec.Body.String())
		}
	}
	s, _ := h.repo.AssistantSession(u.ID, "dead-server")
	if len(s.Messages) != 2 || s.Messages[1].Unfinished == "" || replyMessageText(s.Messages[1].Content) != "先查了运行" {
		t.Fatalf("interrupted reply must be kept exactly once: %+v", s.Messages)
	}
}

func TestUnfinishedReplyIgnoresReplacedGeneration(t *testing.T) {
	h, u := workbenchFixture(t)
	session := domain.AssistantSession{ID: "replaced", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "q"}}}
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "gen-new", SessionID: "replaced", UserID: u.ID, Status: "running"}, session); err != nil {
		t.Fatal(err)
	}
	ok, err := h.repo.AppendUnfinishedAssistantReply(u.ID, "replaced", "gen-old", 1, domain.AssistantMessage{Role: "assistant", Content: "{}", Unfinished: "x"})
	if err != nil || ok {
		t.Fatalf("stale generation wrote a reply: %v %v", ok, err)
	}
	ok, err = h.repo.AppendUnfinishedAssistantReply(u.ID, "replaced", "gen-new", 2, domain.AssistantMessage{Role: "assistant", Content: "{}", Unfinished: "x"})
	if err != nil || ok {
		t.Fatalf("moved transcript got a reply: %v %v", ok, err)
	}
}

func TestUnfinishedTurnNoteForResumedSession(t *testing.T) {
	unfinished := domain.AssistantMessage{Role: "assistant", Unfinished: "生成已停止", Content: `{"message":"看了 zg"}`, Activity: []domain.AssistantActivityEvent{{Kind: "tool", Tool: "runs_get"}}}
	messages := []domain.AssistantMessage{
		{Role: "user", Content: "早先的问题"},
		{Role: "assistant", Content: `{"message":"已答"}`},
		{Role: "user", Content: "查失败节点"},
		unfinished,
		{Role: "user", Content: "看 CI"},
	}
	note := assistantUnfinishedTurnNote(messages)
	for _, want := range []string{"用户：查失败节点", "生成已停止", "runs_get", "看了 zg"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note misses %q: %s", want, note)
		}
	}
	if strings.Contains(note, "早先的问题") || strings.Contains(note, "看 CI") {
		t.Fatalf("note must cover only the unfinished stretch: %s", note)
	}
	if assistantUnfinishedTurnNote(messages[:3]) != "" {
		t.Fatal("a finished previous turn needs no note")
	}
	prompt := assistantPromptMessages(messages)
	if prompt[3].Unfinished != "" || !strings.Contains(prompt[3].Content, "未完成") || prompt[3].Activity != nil {
		t.Fatalf("prompt message: %+v", prompt[3])
	}
}
