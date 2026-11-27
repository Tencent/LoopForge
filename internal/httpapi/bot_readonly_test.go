package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A read-only Bot never executes drafts: the reply says so and nothing is
// written. The turn uses the read-only execution policy and prompt.
func TestReadOnlyBotNeverExecutesDrafts(t *testing.T) {
	s, h, bot := botFixture(t)
	bot.WriteApproval = domain.BotWriteReadOnly
	if err := s.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	before := len(s.Issues())
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		if !strings.Contains(prompt, "只读模式") {
			t.Fatal("read-only policy missing from prompt")
		}
		session := botTestToolSession(t, user.ID)
		if !session.ExecMode.ReadOnly || session.ExecMode.AutoWrite {
			t.Fatalf("exec mode = %+v", session.ExecMode)
		}
		body, _ := json.Marshal(map[string]any{"projectId": "p", "title": "不应创建"})
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "已准备创建需求", Drafts: []assistantDraft{{Tool: "issues_save", Body: body}}}})
		return "", nil
	}
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "帮我建个需求"})
	if err != nil || !strings.Contains(reply, "只读模式") {
		t.Fatal(reply, err)
	}
	if len(s.Issues()) != before {
		t.Fatal("read-only Bot executed a draft")
	}
}

func botTestToolSession(t *testing.T, userID int64) *AgentToolSession {
	t.Helper()
	agentToolSessions.mu.Lock()
	defer agentToolSessions.mu.Unlock()
	for _, candidate := range agentToolSessions.sessions {
		if candidate.Mode == AgentToolAssistant && candidate.UserID == userID {
			return candidate
		}
	}
	t.Fatal("no tool session")
	return nil
}

// Removing project access or unbinding takes effect on the next turn.
func TestBotRevocationAppliesToNextTurn(t *testing.T) {
	s, h, bot := botFixture(t)
	if _, err := h.botExecutionUser(bot, "new-wecom-user"); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("p")
	project.Members = []domain.ProjectMember{}
	s.PutProject(project)
	if _, err := h.botExecutionUser(bot, "new-wecom-user"); err == nil {
		t.Fatal("removed member kept execution rights")
	}
	binding, _ := s.BotUserBinding(bot.IdentityScope(), "bound-member")
	if err := s.RevokeBotUserBinding(binding.ID, binding.UserID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.botExecutionUser(bot, "bound-member"); err == nil {
		t.Fatal("unbound sender kept execution rights")
	}
}

// New channels scope identities by configuration: the same platform user ID
// under another configuration is a different identity, and a challenge of
// one configuration cannot be confirmed through another.
func TestChannelBindingIsScopedToConfiguration(t *testing.T) {
	s, h, _ := botFixture(t)
	feishu := domain.NotificationBot{ID: "fs-a", Channel: domain.BotChannelFeishu, BotID: "cli_a", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, Model: domain.DefaultNotificationReportModel}
	other := feishu
	other.ID, other.BotID = "fs-b", "cli_b"
	for _, b := range []domain.NotificationBot{feishu, other} {
		if err := s.SaveNotificationBot(b, "secret"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	token, err := s.NewBotBindingChallenge(feishu, "ou_same", now, false)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.ClaimBotBindingChallenge(token, 43, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmBotUserBinding(other, "ou_same", code, now); err == nil {
		t.Fatal("challenge confirmed through another configuration")
	}
	if _, err := s.ConfirmBotUserBinding(feishu, "ou_same", code, now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.botExecutionUser(feishu, "ou_same"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.botExecutionUser(other, "ou_same"); err == nil {
		t.Fatal("binding leaked to another configuration")
	}
	binding, _ := s.BotUserBinding(feishu.IdentityScope(), "ou_same")
	if binding.ConfigID != feishu.ID || binding.Channel != domain.BotChannelFeishu || binding.BotID != feishu.ID {
		t.Fatalf("binding = %+v", binding)
	}
	// Deleting the configuration revokes its bindings.
	if err := s.DeleteNotificationBot(feishu.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BotUserBinding(feishu.IdentityScope(), "ou_same"); err == nil {
		t.Fatal("binding survived configuration deletion")
	}
}

// Unbound senders get one link; while it is valid further messages point to
// it, explicit requests are limited to one per minute.
func TestBindingLinkRateLimits(t *testing.T) {
	_, h, bot := botFixture(t)
	ask := func(text string) string {
		handled, reply, err := h.identities.message(bot, domain.BotInteraction{SenderID: "fresh-user", Content: text})
		if !handled || err != nil {
			t.Fatalf("%q: handled=%v err=%v", text, handled, err)
		}
		return reply
	}
	if reply := ask("项目状态"); !strings.Contains(reply, "/bot-bind.html#request=") {
		t.Fatalf("first message = %s", reply)
	}
	if reply := ask("还在吗"); strings.Contains(reply, "/bot-bind.html") || !strings.Contains(reply, "上一条") {
		t.Fatalf("second message issued a new link: %s", reply)
	}
	if reply := ask("绑定账号"); strings.Contains(reply, "/bot-bind.html") || !strings.Contains(reply, "频繁") {
		t.Fatalf("explicit request within a minute: %s", reply)
	}
}
