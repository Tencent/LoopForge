package repo

import (
	"agentflow/internal/domain"
	"os"
	"strings"
	"testing"
	"time"
)

// A run started from a Feishu/DingTalk chat reports back to that
// conversation without any SOP notification binding: failed and waiting
// nodes and the end of the run, to the group itself for group chats.
func TestRunStartedFromChatReportsBackToThatConversation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testRunOrigins(t, s)
}

// The same statements on a real MySQL/MariaDB (AGENTFLOW_TEST_MYSQL_DSN,
// fresh database).
func TestMySQLChatOriginsAndClarifySessions(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_BOT_TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	}
	if dsn == "" {
		t.Skip("MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testRunOrigins(t, s)
	testClarifySessions(t, s)
}

func testRunOrigins(t *testing.T, s *Store) {
	if s.credentialKeyring() == nil {
		s.SetCredentialKeyring(testCredentialKeyring)
	}
	if err := s.PutProject(domain.Project{ID: "p-origin", Name: "研发项目"}); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "bot-origin", ProjectID: "p-origin", Name: "飞书 Bot", BotID: "cli_origin", Channel: domain.BotChannelFeishu, Enabled: true, InteractionEnabled: true, TenantKey: "tk", TenantState: domain.BotTenantLocked}
	if err := s.SaveNotificationBot(bot, "not-a-real-secret"); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "issue-origin", ProjectID: "p-origin", Title: "登录优化"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "define", Name: "需求定型", Type: "agent"}
	newRun := func(id string, nodeStatus string, events ...domain.RunEvent) domain.Run {
		return domain.Run{ID: id, WorkflowID: "wf", Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "p-origin", IssueID: issue.ID}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", ProjectID: "p-origin", Name: "通用后端交付", Nodes: []domain.WorkflowNode{node}}, Nodes: map[string]*domain.NodeRun{"define": {NodeID: "define", Status: nodeStatus, Attempt: 1, Error: "模型调用失败"}}, Events: events}
	}
	group := domain.BotChatOrigin{ConfigID: bot.ID, ProjectID: "p-origin", ConversationID: "conv-g", ChatType: "group", ChatID: "oc_group", SenderID: "ou_starter"}
	withRun := func(o domain.BotChatOrigin, runID string) domain.BotChatOrigin { o.RunID = runID; return o }
	if err := s.RecordBotChatOrigin(withRun(group, "run-failed")); err != nil {
		t.Fatal(err)
	}
	// A later origin of the same run does not move it.
	moved := withRun(group, "run-failed")
	moved.ChatID = "oc_other"
	if err := s.RecordBotChatOrigin(moved); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	failed := newRun("run-failed", "failed", domain.RunEvent{ID: "e1", NodeID: "define", Type: "node.failed", Timestamp: now}, domain.RunEvent{ID: "e2", Type: "run.failed", Timestamp: now})
	if err := s.PutRun(failed); err != nil {
		t.Fatal(err)
	}
	items, _ := s.NotificationDeliveries("p-origin")
	if len(items) != 1 {
		t.Fatalf("deliveries = %+v", items)
	}
	got := items[0]
	if got.Origin != domain.NotificationOriginChat || got.TargetType != "group" || got.TargetID != "oc_group" || got.EventType != "failed" || got.NodeID != "define" || got.Attempt != 1 {
		t.Fatalf("delivery = %+v", got)
	}
	if !strings.Contains(got.Content, "节点失败") || !strings.Contains(got.Content, "需求：登录优化") || !strings.Contains(got.Content, "原因：模型调用失败") {
		t.Fatalf("content = %q", got.Content)
	}

	// A private origin goes to the sender; the run end is reported once.
	single := group
	single.ChatType, single.ChatID, single.ConversationID = "single", "oc_p2p", "conv-s"
	if err := s.RecordBotChatOrigin(withRun(single, "run-done")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(newRun("run-done", "succeeded", domain.RunEvent{ID: "e3", NodeID: "define", Type: "node.succeeded", Timestamp: now}, domain.RunEvent{ID: "e4", Type: "run.succeeded", Timestamp: now})); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries("p-origin")
	var done *domain.NotificationDelivery
	for i := range items {
		if items[i].RunID == "run-done" {
			if done != nil {
				t.Fatalf("more than one message for the finished run: %+v", items)
			}
			done = &items[i]
		}
	}
	if done == nil || done.TargetType != "single" || done.TargetID != "ou_starter" || done.EventType != "succeeded" || !strings.Contains(done.Content, "SOP 已完成") {
		t.Fatalf("finished run = %+v", done)
	}

	// The first events of a run started from the chat arrive before the
	// run's own origin is recorded: the start origin stored with the run
	// covers them. A run of the same requirement started on the platform
	// names no start origin and is not sent to the chat the requirement
	// came from.
	issueOrigin := group
	issueOrigin.IssueID = issue.ID
	if err := s.RecordBotChatOrigin(issueOrigin); err != nil {
		t.Fatal(err)
	}
	if id, ok := s.LatestBotChatIssue("conv-g"); !ok || id != issue.ID {
		t.Fatalf("latest issue = %q %v", id, ok)
	}
	platform := newRun("run-platform", "waiting_approval", domain.RunEvent{ID: "e5p", NodeID: "define", Type: "approval.waiting", Timestamp: now})
	if err := s.PutRun(platform); err != nil {
		t.Fatal(err)
	}
	early := newRun("run-early", "waiting_approval", domain.RunEvent{ID: "e5", NodeID: "define", Type: "approval.waiting", Timestamp: now})
	early.ExecutionContext.BotOriginID = BotIssueOriginID("conv-g", issue.ID)
	if err := s.PutRun(early); err != nil {
		t.Fatal(err)
	}
	child := newRun("run-child", "waiting_approval", domain.RunEvent{ID: "e5c", NodeID: "define", Type: "approval.waiting", Timestamp: now})
	child.ExecutionContext.BotOriginID, child.ExecutionContext.ParentRunID = early.ExecutionContext.BotOriginID, early.ID
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries("p-origin")
	reported := map[string]int{}
	for _, item := range items {
		if item.EventType == "waiting" && item.TargetID == "oc_group" && strings.Contains(item.Content, "需要人工处理") {
			reported[item.RunID]++
		}
	}
	if reported["run-early"] != 1 || reported["run-platform"] != 0 || reported["run-child"] != 0 {
		t.Fatalf("waiting events reported = %v: %+v", reported, items)
	}

	// Without an origin nothing is sent; WeCom Bots keep their own path.
	other := domain.Issue{ID: "issue-plain", ProjectID: "p-origin", Title: "无来源"}
	_ = s.PutIssue(other)
	plain := newRun("run-plain", "failed", domain.RunEvent{ID: "e6", NodeID: "define", Type: "node.failed", Timestamp: now})
	plain.ExecutionContext.IssueID = other.ID
	before := len(items)
	if err := s.PutRun(plain); err != nil {
		t.Fatal(err)
	}
	if items, _ = s.NotificationDeliveries("p-origin"); len(items) != before {
		t.Fatalf("run without origin notified: %+v", items)
	}
}

// Chat clarification sessions: one per conversation and per requirement,
// optimistic updates, and the lookup of a quoted Bot message.
func TestBotClarifySessionLifecycle(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testClarifySessions(t, s)
}

func testClarifySessions(t *testing.T, s *Store) {
	sess, err := s.CreateBotClarifySession(domain.BotClarifySession{ConfigID: "b", ProjectID: "p", ConversationID: "conv", ChatType: "group", ChatID: "oc", IssueID: "i1", StarterSender: "ou", StarterUserID: 7, Status: domain.BotClarifyAnalyzing})
	if err != nil || sess.ID == "" || sess.Version != 1 {
		t.Fatalf("create = %+v %v", sess, err)
	}
	if existing, err := s.CreateBotClarifySession(domain.BotClarifySession{ConfigID: "b", ConversationID: "conv", IssueID: "i2", Status: domain.BotClarifyAnalyzing}); err != ErrBotClarifyActive || existing.ID != sess.ID {
		t.Fatalf("second session in the conversation = %+v %v", existing, err)
	}
	if _, err := s.CreateBotClarifySession(domain.BotClarifySession{ConfigID: "b", ConversationID: "conv-2", IssueID: "i1", Status: domain.BotClarifyAnalyzing}); err != ErrBotClarifyActive {
		t.Fatalf("same requirement elsewhere = %v", err)
	}
	if !s.ActiveBotClarifyIssue("i1") {
		t.Fatal("requirement not reported as clarified")
	}
	updated, err := s.UpdateBotClarifySession(sess.ID, func(c *domain.BotClarifySession) error {
		c.Status, c.Round = domain.BotClarifyAsking, 1
		c.Questions = []domain.BotClarifyQuestion{{N: 1, Question: "验收方式？"}, {N: 2, Question: "要不要短信？"}}
		return nil
	})
	if err != nil || updated.Version != 2 || len(updated.Questions) != 2 {
		t.Fatalf("update = %+v %v", updated, err)
	}
	updated, _ = s.UpdateBotClarifySession(sess.ID, func(c *domain.BotClarifySession) error {
		c.Question(2).Answers = append(c.Question(2).Answers, domain.BotClarifyAnswer{Sender: "ou", Name: "张三", Text: "要"})
		return nil
	})
	if updated.AnswerCount() != 1 || updated.AllAnswered() || domain.QuestionLabels(updated.Unanswered()) != "Q1" {
		t.Fatalf("answers = %+v", updated)
	}
	if same, err := s.UpdateBotClarifySession(sess.ID, func(*domain.BotClarifySession) error { return ErrBotClarifyUnchanged }); err != nil || same.Version != updated.Version {
		t.Fatalf("unchanged = %+v %v", same, err)
	}
	open, _ := s.OpenBotClarifySessions(10)
	if len(open) != 1 || open[0].Question(2).Answers[0].Name != "张三" {
		t.Fatalf("open = %+v", open)
	}
	if _, err := s.UpdateBotClarifySession(sess.ID, func(c *domain.BotClarifySession) error { c.Status = domain.BotClarifyEnded; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ActiveBotClarifySession("conv"); ok {
		t.Fatal("ended session still active")
	}
	if _, err := s.CreateBotClarifySession(domain.BotClarifySession{ConfigID: "b", ConversationID: "conv", IssueID: "i2", Status: domain.BotClarifyAnalyzing}); err != nil {
		t.Fatalf("new session after the end = %v", err)
	}

	item := domain.BotInteraction{ID: "post-1", BotID: "b", Action: "clarify_post", DeliveryID: sess.ID, PlatformMessageID: "om_card"}
	if _, err := s.EnqueueBotInteraction(item, "group", "oc"); err != nil {
		t.Fatal(err)
	}
	if found, ok := s.BotInteractionByPlatformMessage("b", "om_card"); !ok || found.ID != "post-1" {
		t.Fatalf("quoted message = %+v %v", found, ok)
	}
	if _, ok := s.BotInteractionByPlatformMessage("other-bot", "om_card"); ok {
		t.Fatal("message of another Bot matched")
	}
}

func TestClarifySaveAllowedRoles(t *testing.T) {
	project := domain.Project{OwnerID: 1, Members: []domain.ProjectMember{{UserID: 2, Role: "maintainer"}, {UserID: 3, Role: "member"}, {UserID: 4, Role: "member"}, {UserID: 5, Role: "member"}}}
	issue := domain.Issue{AssigneeUserID: 4}
	sess := domain.BotClarifySession{StarterUserID: 5}
	for id, want := range map[int64]bool{1: true, 2: true, 3: false, 4: true, 5: true, 0: false} {
		if got := domain.ClarifySaveAllowed(project, issue, sess, id); got != want {
			t.Errorf("user %d: got %v want %v", id, got, want)
		}
	}
}

// WeCom origins address the conversation itself (a private chat's chat ID
// is the member's userid); the delivery goes over the live connection.
func TestWeComRunOriginTargetsTheConversation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.credentialKeyring() == nil {
		s.SetCredentialKeyring(testCredentialKeyring)
	}
	s.PutProject(domain.Project{ID: "p-w", Name: "项目"})
	bot := domain.NotificationBot{ID: "bot-w", ProjectID: "p-w", BotID: "aibot", Enabled: true, InteractionEnabled: true}
	if err := s.SaveNotificationBot(bot, "not-a-real-secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBotChatOrigin(domain.BotChatOrigin{ConfigID: bot.ID, ProjectID: "p-w", ConversationID: "conv-w", ChatType: "single", ChatID: "zhangsan", SenderID: "zhangsan", UserID: 42, RunID: "run-w"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-w", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "p-w"}, WorkflowSnapshot: domain.WorkflowDefinition{Name: "交付"}, Events: []domain.RunEvent{{ID: "w1", Type: "run.succeeded", Timestamp: time.Now().UTC()}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	items, _ := s.NotificationDeliveries("p-w")
	if len(items) != 1 || items[0].Origin != domain.NotificationOriginChat || items[0].TargetID != "zhangsan" || items[0].TargetType != "single" || items[0].OriginUserID != 42 {
		t.Fatalf("deliveries = %+v", items)
	}
}
