package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// latestIssueOperation returns the newest operation of the only issue a
// fixture created, so the test asserts on what the platform stored.
func latestIssueOperation(t *testing.T, s *repo.Store, action string) domain.IssueOperation {
	t.Helper()
	var found domain.IssueOperation
	for _, issue := range s.Issues() {
		for _, op := range issue.Operations {
			if op.Action == action && op.At.After(found.At) {
				found = op
			}
		}
	}
	if found.Action == "" {
		t.Fatalf("no %q operation recorded", action)
	}
	return found
}

// A requirement created from a WeCom chat must name both identities: the
// platform account the write ran as, and the WeCom member who asked for it.
func TestIssueOperationNamesWeComSenderOnConfirmedDraft(t *testing.T) {
	s, h, _, ask := confirmModeFixture(t)
	member, _ := s.User(43)
	id := ask()
	code, done := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true)
	if code != 200 || done["status"] != repo.BotDraftExecuted {
		t.Fatalf("confirm = %d %v", code, done)
	}
	op := latestIssueOperation(t, s, "create")
	if op.ActorChannel != domain.BotChannelWeCom || op.ActorSender != "new-wecom-user" {
		t.Fatalf("chat attribution = %q %q", op.ActorChannel, op.ActorSender)
	}
	if op.ActorUserID != 43 || !strings.Contains(op.Actor, "other-user") {
		t.Fatalf("platform actor = %q %d", op.Actor, op.ActorUserID)
	}
}

// The same attribution applies when the Bot writes within the turn instead
// of going through a confirmation card.
func TestIssueOperationNamesWeComSenderOnAutoWrite(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "bot-attribution-test-session-secret")
	s, h, bot := botFixture(t)
	h.api = New(s, h.engine, tracker.NewService(), "", "")
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		session := botTestToolSession(t, user.ID)
		body, _ := json.Marshal(map[string]any{"projectId": "p", "title": "自动写入的需求"})
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "已创建。", Drafts: []assistantDraft{{Tool: "issues_save", Body: body}}}})
		return "", nil
	}
	if _, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, domain.BotInteraction{ID: "turn-auto", SenderID: "new-wecom-user", Content: "帮我建个需求"}); err != nil {
		t.Fatal(err)
	}
	op := latestIssueOperation(t, s, "create")
	if op.ActorChannel != domain.BotChannelWeCom || op.ActorSender != "new-wecom-user" {
		t.Fatalf("chat attribution = %q %q", op.ActorChannel, op.ActorSender)
	}
	if op.ActorUserID != 43 {
		t.Fatalf("platform actor = %q %d", op.Actor, op.ActorUserID)
	}
}

// DingTalk reports the sender's name, so the audit entry names a person and
// not only a staff ID.
func TestIssueOperationNamesDingTalkSender(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "bot-attribution-dingtalk-secret")
	s, h, bot := botFixture(t)
	bot.ID, bot.BotID, bot.Channel, bot.TenantKey, bot.TenantState = "b-dingtalk", "app-dingtalk", domain.BotChannelDingTalk, "tenant", domain.BotTenantLocked
	bot.WriteApproval = domain.BotWriteAuto
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	bindBotTestUser(t, s, bot, "staff-1", 43)
	h.api = New(s, h.engine, tracker.NewService(), "", "")
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		session := botTestToolSession(t, user.ID)
		body, _ := json.Marshal(map[string]any{"projectId": "p", "title": "钉钉发起的需求"})
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "已创建。", Drafts: []assistantDraft{{Tool: "issues_save", Body: body}}}})
		return "", nil
	}
	item := domain.BotInteraction{ID: "turn-ding", SenderID: "staff-1", SenderName: "张三", Content: "帮我建个需求"}
	if _, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, item); err != nil {
		t.Fatal(err)
	}
	op := latestIssueOperation(t, s, "create")
	if op.ActorChannel != domain.BotChannelDingTalk || op.ActorSender != "staff-1" || op.ActorSenderName != "张三" {
		t.Fatalf("chat attribution = %q %q %q", op.ActorChannel, op.ActorSender, op.ActorSenderName)
	}
}

// The issue audit trail is managed by the server, including on creation.
func TestIssueAuditTrailIsServerOwnedOnPlatformCreate(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "platform-issue-audit-test-secret")
	s, h, _ := botFixture(t)
	member, _ := s.User(43)
	api := New(s, h.engine, tracker.NewService(), "", "")
	draft := domain.Issue{ID: "i-platform", ProjectID: "p", Title: "平台页面创建", Operations: []domain.IssueOperation{{Action: "update"}}}
	body, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	req := authedRequest(t, httptest.NewRequest(http.MethodPut, "/api/issues", strings.NewReader(string(body))), member)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	issue, _ := s.Issue(draft.ID)
	if len(issue.Operations) != 1 || issue.Operations[0].Action != "create" {
		t.Fatalf("operations = %+v", issue.Operations)
	}
	op := issue.Operations[0]
	if op.ActorChannel != "" || op.ActorSender != "" {
		t.Fatalf("unexpected chat attribution %q %q", op.ActorChannel, op.ActorSender)
	}
	if op.ActorUserID != 43 {
		t.Fatalf("platform actor = %q %d", op.Actor, op.ActorUserID)
	}
}

// withBotChatActor normalizes the channel and ignores calls without a
// sender, so a missing sender never produces a half-filled audit entry.
func TestBotChatActorContext(t *testing.T) {
	if _, ok := botChatActorFrom(context.Background()); ok {
		t.Fatal("bare context reported a chat actor")
	}
	if _, ok := botChatActorFrom(withBotChatActor(context.Background(), domain.BotChannelWeCom, "  ", "张三")); ok {
		t.Fatal("empty sender reported a chat actor")
	}
	actor, ok := botChatActorFrom(withBotChatActor(context.Background(), "", "wangxx", ""))
	if !ok || actor.Channel != domain.BotChannelWeCom || actor.SenderID != "wangxx" {
		t.Fatalf("actor = %+v ok=%t", actor, ok)
	}
	named, ok := botChatActorFrom(withBotChatActor(context.Background(), domain.BotChannelDingTalk, "  staff-1  ", "  张三  "))
	if !ok || named.Channel != domain.BotChannelDingTalk || named.SenderID != "staff-1" || named.SenderName != "张三" {
		t.Fatalf("named actor = %+v ok=%t", named, ok)
	}
}

// DingTalk is the only channel whose message carries the sender's name. It is
// cached on the binding, so a later action of the same sender that carries no
// name is still attributed to a person.
func TestBotSenderNameIsCachedOnTheBinding(t *testing.T) {
	s, h, bot := botFixture(t)
	bot.ID, bot.BotID, bot.Channel, bot.TenantKey, bot.TenantState = "b-dingtalk", "app-dingtalk", domain.BotChannelDingTalk, "tenant", domain.BotTenantLocked
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	bindBotTestUser(t, s, bot, "staff-1", 43)
	if name := h.botSenderName(bot, "staff-1", ""); name != "" {
		t.Fatalf("unseen sender already named %q", name)
	}
	if name := h.botSenderName(bot, "staff-1", " 张三 "); name != "张三" {
		t.Fatalf("reported name = %q", name)
	}
	// A later card click carries no name and must still resolve it.
	if name := h.botSenderName(bot, "staff-1", ""); name != "张三" {
		t.Fatalf("cached name = %q", name)
	}
	if binding, err := s.BotUserBinding(bot.IdentityScope(), "staff-1"); err != nil || binding.SenderName != "张三" {
		t.Fatalf("binding = %+v err=%v", binding, err)
	}
	// An empty report never clears what is already known.
	if err := s.NoteBotSenderName(bot.IdentityScope(), "staff-1", "  "); err != nil {
		t.Fatal(err)
	}
	if binding, _ := s.BotUserBinding(bot.IdentityScope(), "staff-1"); binding.SenderName != "张三" {
		t.Fatalf("name cleared by an empty report: %q", binding.SenderName)
	}
	// Unbinding drops the cached name with the rest of the identity.
	binding, _ := s.BotUserBinding(bot.IdentityScope(), "staff-1")
	if err := s.RevokeBotUserBinding(binding.ID, 43, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if name := h.botSenderName(bot, "staff-1", ""); name != "" {
		t.Fatalf("name survived revocation: %q", name)
	}
}

// WeCom carries no sender name, so nothing is cached and the audit entry
// keeps naming the sender by userid alone.
func TestWeComSenderHasNoReportedName(t *testing.T) {
	s, h, bot := botFixture(t)
	if name := h.botSenderName(bot, "new-wecom-user", ""); name != "" {
		t.Fatalf("WeCom sender named %q", name)
	}
	if binding, err := s.BotUserBinding(bot.IdentityScope(), "new-wecom-user"); err != nil || binding.SenderName != "" {
		t.Fatalf("binding = %+v err=%v", binding, err)
	}
}
