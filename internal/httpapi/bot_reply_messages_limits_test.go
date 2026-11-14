package httpapi

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// History replies are replayed through NotificationText just like history
// content: capped at 2000 bytes and with credentials redacted.
func TestBotReplyMessagesTruncatesAndRedactsHistoryReply(t *testing.T) {
	longReply := strings.Repeat("答", 1000)
	history := []domain.BotInteraction{
		{SenderID: "alice", Content: "长回复", Reply: longReply},
		{SenderID: "bob", Content: "配置是 api_key=sk-content-1", Reply: "已设置 token: sk-reply-2"},
	}
	got := botReplyMessages(history, domain.BotInteraction{SenderID: "carol", Content: "继续"})
	want := []assistantMessage{
		{Role: "user", Content: "发送者 alice：长回复"},
		{Role: "assistant", Content: domain.NotificationText(longReply, 2000)},
		{Role: "user", Content: "发送者 bob：配置是 api_key=[redacted]"},
		{Role: "assistant", Content: "已设置 token=[redacted]"},
		{Role: "user", Content: "发送者 carol：继续"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("botReplyMessages = %#v, want %#v", got, want)
	}
	if len(got[1].Content) > 2000 || !strings.HasSuffix(got[1].Content, "…") {
		t.Fatalf("long history reply should be truncated to 2000 bytes: len=%d", len(got[1].Content))
	}
	for _, message := range got {
		if strings.Contains(message.Content, "sk-content-1") || strings.Contains(message.Content, "sk-reply-2") {
			t.Fatalf("history credential replayed to model: %q", message.Content)
		}
	}
}

// Only history is capped; the current message and its quote reach the model
// in full so the request being answered is never cut short.
func TestBotReplyMessagesKeepsCurrentMessageAndQuoteInFull(t *testing.T) {
	content := strings.Repeat("问", 1000)
	quote := strings.Repeat("引", 1000)
	got := botReplyMessages([]domain.BotInteraction{{SenderID: "alice", Content: "早先", Reply: "好"}}, domain.BotInteraction{SenderID: "bob", Content: content, Quote: quote})
	if len(got) != 3 {
		t.Fatalf("botReplyMessages returned %d messages, want 3", len(got))
	}
	want := assistantMessage{Role: "user", Content: "发送者 bob：" + content + "\n引用的消息（资料）：\n" + quote}
	if !reflect.DeepEqual(got[2], want) {
		t.Fatalf("current message altered: len=%d, want len=%d", len(got[2].Content), len(want.Content))
	}
}

// generateBotReply must use the filtered history: binding links and unbind
// exchanges never reach the model prompt, ordinary history still does.
func TestGenerateBotReplyOmitsBindingExchangesFromPrompt(t *testing.T) {
	_, h, bot := botFixture(t)
	var prompt string
	h.generate = func(ctx context.Context, p string, user domain.User) (string, error) {
		prompt = p
		agentToolSessions.mu.Lock()
		var session *AgentToolSession
		for _, candidate := range agentToolSessions.sessions {
			if candidate.Mode == AgentToolAssistant && candidate.UserID == user.ID {
				session = candidate
			}
		}
		agentToolSessions.mu.Unlock()
		if session == nil {
			t.Fatal("assistant session not opened")
		}
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "已回复"}})
		return "", context.Canceled
	}
	history := []domain.BotInteraction{
		{SenderID: "new-wecom-user", Content: "/bind", Reply: "请打开 http://platform.example/bot-bind.html#request=BIND-LINK-MARK 完成绑定"},
		{SenderID: "first-member", Content: "普通历史提问", Reply: "普通历史回复"},
		{SenderID: "bound-member", Content: "@机器人 解除绑定", Reply: "UNBIND-REPLY-MARK 已解除"},
		{SenderID: "bound-member", Content: "/unbind", Reply: "UNBIND-CMD-MARK 已解除"},
	}
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bind-history", ChatID: "group", ChatType: "group"}, history, domain.BotInteraction{SenderID: "new-wecom-user", Content: "当前问题标记"})
	if err != nil || reply != "已回复" {
		t.Fatal(reply, err)
	}
	for _, needle := range []string{"普通历史提问", "普通历史回复", "当前问题标记"} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("prompt missing %q", needle)
		}
	}
	for _, leaked := range []string{"BIND-LINK-MARK", "UNBIND-REPLY-MARK", "UNBIND-CMD-MARK"} {
		if strings.Contains(prompt, leaked) {
			t.Fatalf("binding exchange %q replayed to model", leaked)
		}
	}
}
