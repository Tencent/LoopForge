package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"context"
	"strings"
	"testing"
	"time"
)

func botFixture(t *testing.T) (*repo.Store, *assistantHandlers, domain.NotificationBot) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	s.PutUser(domain.User{ID: 42, LoginName: "bot-owner", CodeBuddyAPIKey: "owner-key", CodeBuddyInternetEnvironment: "ioa"})
	s.PutUser(domain.User{ID: 43, LoginName: "other-user", CodeBuddyAPIKey: "other-key", CodeBuddyInternetEnvironment: "ioa"})
	s.PutProject(domain.Project{ID: "p", OwnerID: 42, Members: []domain.ProjectMember{{UserID: 43, Role: "member"}}})
	bot := domain.NotificationBot{ID: "b", BotID: "external", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, Model: domain.DefaultNotificationReportModel}
	if err = s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, nil)
	e.ConfigureExecution("api", "test", time.Minute, false)
	d := &deps{repo: s, engine: e}
	h := &assistantHandlers{deps: d, notifications: &notificationHandlers{deps: d, service: notification.New(s)}}
	h.notifications.service.PublicURL = "http://platform.example"
	h.identities = &botIdentityHandlers{deps: d, publicURL: h.notifications.service.PublicURL}
	bindBotTestUser(t, s, bot, "new-wecom-user", 43)
	bindBotTestUser(t, s, bot, "bound-member", 42)
	return s, h, bot
}
func TestBotUsesBoundSenderIdentityAutoModeAndSharedChatContext(t *testing.T) {
	s, h, bot := botFixture(t)
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		if user.ID != 43 || user.CodeBuddyAPIKey != "other-key" || user.CodeBuddyInternetEnvironment != "ioa" {
			t.Fatal("wrong credentials")
		}
		if !strings.Contains(prompt, "自动执行") || !strings.Contains(prompt, "发送者 new-wecom-user") || !strings.Contains(prompt, "历史回复") {
			t.Fatal("missing shared context or auto mode")
		}
		agentToolSessions.mu.Lock()
		var session *AgentToolSession
		for _, candidate := range agentToolSessions.sessions {
			if candidate.Mode == AgentToolAssistant && candidate.UserID == 43 {
				session = candidate
			}
		}
		agentToolSessions.mu.Unlock()
		if session == nil || !session.ExecMode.AutoWrite || session.ExecMode.ConfirmDangerous || session.ExecMode.DeferSOP {
			t.Fatal("Bot did not reuse full auto assistant")
		}
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "查询完成 other-key"}})
		return "", context.Canceled
	}
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "session", ChatID: "new-group", ChatType: "group"}, []domain.BotInteraction{{SenderID: "first-member", Content: "历史提问", Reply: "历史回复"}}, domain.BotInteraction{SenderID: "new-wecom-user", Content: "现在有什么进展"})
	if err != nil || reply != "查询完成 [redacted]" {
		t.Fatal(reply, err)
	}
	// No fallback to the Bot owner when the sender has a different identity.
	bot.InteractionEnabled = false
	s.SaveNotificationBot(bot, "")
	if _, err = h.botExecutionUser(bot, "new-wecom-user"); err == nil {
		t.Fatal("disabled Bot executed")
	}
}
func TestCardActionsAreScopedAndCannotApproveAnotherAttempt(t *testing.T) {
	s, h, bot := botFixture(t)
	run := domain.Run{ID: "card-run", Status: "waiting", WorkflowID: "wf", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", ProjectID: "p", Nodes: []domain.WorkflowNode{{ID: "gate", Type: "human", Name: "人工确认"}}}, Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval", Attempt: 1, ReworkCount: 0}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	delivery := domain.NotificationDelivery{ID: "d", BotID: bot.ID, ProjectID: "p", RunID: run.ID, NodeID: "gate", EventType: "waiting", Attempt: 1, TargetID: "group", TargetType: "group", Status: "sent", CreatedAt: time.Now().UTC()}
	if err := s.CreateNotificationDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	item := domain.BotInteraction{DeliveryID: delivery.ID, Action: "approve", SenderID: "bound-member"}
	conv := domain.BotConversation{ChatID: "group", ChatType: "group"}
	foreign := conv
	foreign.ChatID = "other-group"
	if _, err := h.notifications.service.Interactions.NotificationCardTask(bot, item, foreign); err == nil {
		t.Fatal("foreign chat used card")
	}
	reply, err := h.handleBotCardAction(context.Background(), bot, item, conv)
	if err != nil || !strings.Contains(reply, "操作已完成") {
		t.Fatal(reply, err)
	}
	updated, _ := s.RunState(run.ID)
	if updated.Nodes["gate"].Status != "succeeded" {
		t.Fatal("button did not approve")
	}
	reply, _ = h.handleBotCardAction(context.Background(), bot, item, conv)
	if !strings.Contains(reply, "过期") {
		t.Fatal("duplicate card remained actionable")
	}
	updated.Status = "waiting"
	updated.Nodes["gate"].Status = "waiting_approval"
	updated.Nodes["gate"].Attempt = 2
	s.PutRun(updated)
	if actions := botinteraction.ActionsForDelivery(s, delivery); len(actions) != 0 {
		t.Fatal("old card applies to new attempt")
	}
	err = h.engine.Approve(run.ID, "gate", "old click", engine.NodeExpectation{NodeID: "gate", Status: "waiting_approval", Attempt: 1})
	if err == nil {
		t.Fatal("engine accepted stale expectation")
	}
	final, _ := s.RunState(run.ID)
	if final.Nodes["gate"].Status != "waiting_approval" || final.Nodes["gate"].Attempt != 2 {
		t.Fatal("old click mutated new attempt")
	}
}
func TestBotTerminalRetryCannotRepeatCachedWriteTool(t *testing.T) {
	session := &AgentToolSession{Mode: AgentToolAssistant, ExecMode: assistantExecModeForApproval(AssistantApprovalAuto)}
	session.SetTerminalOnly()
	gateway := &agentToolGateway{}
	_, err := gateway.callTool(context.Background(), session, []byte(`{"name":"agentflow__runs_retry","arguments":{"path":{"id":"r"},"body":{"nodeId":"n"}}}`))
	if err == nil {
		t.Fatal("terminal-only retry permitted cached write")
	}
}

func TestBotMissingSubmissionRetriesOnlyTerminalStep(t *testing.T) {
	_, h, bot := botFixture(t)
	calls := 0
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		calls++
		if calls == 1 {
			return "查询结果已经准备好，但未提交终态", nil
		}
		if strings.Contains(prompt, "原始查询标记") || !strings.Contains(prompt, "不要重新查询") {
			t.Fatal("retry restarted task", prompt)
		}
		agentToolSessions.mu.Lock()
		var session *AgentToolSession
		for _, candidate := range agentToolSessions.sessions {
			if candidate.Mode == AgentToolAssistant && candidate.UserID == user.ID {
				session = candidate
			}
		}
		agentToolSessions.mu.Unlock()
		if session == nil || !session.terminalToolsOnly() {
			t.Fatal("retry permits repeated operations")
		}
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "查询已提交"}})
		return "", context.Canceled
	}
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "原始查询标记"})
	if err != nil || reply != "查询已提交" || calls != 2 {
		t.Fatal(reply, err, calls)
	}
}

func bindBotTestUser(t *testing.T, s *repo.Store, bot domain.NotificationBot, sender string, userID int64) {
	t.Helper()
	now := time.Now().UTC()
	token, err := s.NewBotBindingChallenge(bot.BotID, sender, now)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.ClaimBotBindingChallenge(token, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmBotUserBinding(bot.BotID, sender, code, now); err != nil {
		t.Fatal(err)
	}
}
func TestBotUnboundAndMissingCredentialsNeverUseOwner(t *testing.T) {
	s, h, bot := botFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		t.Fatal("unbound actor reached model")
		return "", nil
	}
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{}, nil, domain.BotInteraction{SenderID: "unknown", Content: "创建需求"})
	if err != nil || !strings.Contains(reply, "bot-bind.html#request=") {
		t.Fatal(reply, err)
	}
	if _, err := h.botExecutionUser(bot, "unknown"); err == nil {
		t.Fatal("unbound user used owner")
	}
	s.UpdateUserCredentials(43, repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: true})
	reply, err = h.generateBotReply(context.Background(), bot, domain.BotConversation{}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "创建需求"})
	if err != nil || !strings.Contains(reply, "你的平台账号尚未配置") {
		t.Fatal(reply, err)
	}
	project, _ := s.Project("p")
	project.Members = []domain.ProjectMember{}
	s.PutProject(project)
	if _, err = h.botExecutionUser(bot, "new-wecom-user"); err == nil {
		t.Fatal("revoked member inherited owner permission")
	}
}
func TestUnboundCardActorCannotMutateRun(t *testing.T) {
	s, h, bot := botFixture(t)
	run := domain.Run{ID: "identity-gate", Status: "waiting", Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	reply, err := h.handleBotCardAction(context.Background(), bot, domain.BotInteraction{SenderID: "unknown", Action: "approve"}, domain.BotConversation{})
	if err != botinteraction.ErrCardActionDeferred || !strings.Contains(reply, "关联你的平台账号") {
		t.Fatal(reply, err)
	}
	after, _ := s.RunState(run.ID)
	if after.Nodes["gate"].Status != "waiting_approval" {
		t.Fatal("unbound click changed run")
	}
}
