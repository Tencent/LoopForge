package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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

// Operations taken on the platform itself carry no IM identity, so the
// drawer keeps showing the account alone.
func TestIssueOperationHasNoChatIdentityOnPlatformWrite(t *testing.T) {
	s, _, _ := botFixture(t)
	member, _ := s.User(43)
	h := issuesHandlers{deps: &deps{repo: s}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = authedRequest(t, httptest.NewRequest("PUT", "/api/issues", nil), member)
	issue := domain.Issue{ID: "i-platform", ProjectID: "p", Title: "平台页面创建"}
	h.recordIssueOperation(c, &issue, "create", "创建需求")
	if len(issue.Operations) != 1 {
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
	if _, ok := botChatActorFrom(withBotChatActor(context.Background(), domain.BotChannelWeCom, "  ")); ok {
		t.Fatal("empty sender reported a chat actor")
	}
	actor, ok := botChatActorFrom(withBotChatActor(context.Background(), "", "wangxx"))
	if !ok || actor.Channel != domain.BotChannelWeCom || actor.SenderID != "wangxx" {
		t.Fatalf("actor = %+v ok=%t", actor, ok)
	}
	if actor.Label() != "企业微信 wangxx" {
		t.Fatalf("label = %q", actor.Label())
	}
	feishu, _ := botChatActorFrom(withBotChatActor(context.Background(), domain.BotChannelFeishu, "ou_1"))
	if feishu.Label() != "飞书 ou_1" {
		t.Fatalf("label = %q", feishu.Label())
	}
}
