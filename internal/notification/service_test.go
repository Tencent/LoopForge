package notification

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	chats      []domain.BotChat
	err        error
	sent       int
	content    string
	onSessions func()
}

func (f *fakeProvider) Sessions(context.Context, string, string) ([]domain.BotChat, error) {
	if f.onSessions != nil {
		f.onSessions()
	}
	return f.chats, f.err
}
func (f *fakeProvider) Send(_ context.Context, _, _, _, content string) error {
	f.sent++
	f.content = content
	return f.err
}
func serviceFixture(t *testing.T) (*Service, *fakeProvider, domain.NotificationBot, domain.Run) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	svc := New(store)
	svc.PublicURL = "https://platform.example"
	provider := &fakeProvider{chats: []domain.BotChat{{ID: "chat", Name: "研发群", Type: "group"}}}
	svc.Provider = provider
	bot := domain.NotificationBot{ID: "bot", ProjectID: "p", Name: "Bot", BotID: "wecom", Enabled: true, Target: &provider.chats[0], SecretConfigured: true}
	if err = store.SaveNotificationBot(bot, "temporary-secret"); err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "n", Name: "开发", Type: "agent", Notifications: []domain.NodeNotification{{BotID: bot.ID, Events: []string{"waiting", "succeeded"}}}}
	run := domain.Run{ID: "r", WorkflowSnapshot: domain.WorkflowDefinition{Name: "交付", Nodes: []domain.WorkflowNode{node}}, ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Status: "waiting", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 1}}, Events: []domain.RunEvent{{ID: "e", NodeID: "n", Type: "gate.waiting", Timestamp: time.Now().UTC()}}}
	if err = store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return svc, provider, bot, run
}
func TestNotificationSendAndExpire(t *testing.T) {
	t.Run("send", func(t *testing.T) {
		svc, f, bot, _ := serviceFixture(t)
		ok, err := svc.ProcessOne(context.Background())
		if err != nil || !ok || f.sent != 1 {
			t.Fatalf("process %v %v sent %d", ok, err, f.sent)
		}
		items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
		if items[0].Status != "sent" || !strings.Contains(f.content, "https://platform.example/#/runs/r") {
			t.Fatalf("delivery %+v content %s", items, f.content)
		}
	})
	t.Run("resolved gate", func(t *testing.T) {
		svc, f, bot, run := serviceFixture(t)
		run.Nodes["n"].Status = "succeeded"
		run.Status = "succeeded"
		if err := svc.Store.PutRun(run); err != nil {
			t.Fatal(err)
		}
		_, err := svc.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
		if f.sent != 0 || items[0].Status != "expired" {
			t.Fatalf("stale reminder sent %+v", items)
		}
	})
	t.Run("permission", func(t *testing.T) {
		svc, f, bot, _ := serviceFixture(t)
		f.err = failure(850003)
		_, err := svc.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
		if f.sent != 0 || items[0].Status != "awaiting_authorization" {
			t.Fatalf("permission not classified %+v", items)
		}
	})
	t.Run("missing target", func(t *testing.T) {
		svc, f, bot, _ := serviceFixture(t)
		f.chats = nil
		_, err := svc.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
		if f.sent != 0 || items[0].Status != "target_unavailable" {
			t.Fatalf("target fallback %+v", items)
		}
	})
	t.Run("disable", func(t *testing.T) {
		svc, f, bot, _ := serviceFixture(t)
		bot.Enabled = false
		if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
			t.Fatal(err)
		}
		_, err := svc.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
		if f.sent != 0 || items[0].Status != "cancelled" {
			t.Fatal("disabled bot sent")
		}
	})
}
func TestPlaintextCredential(t *testing.T) {
	svc, _, bot, _ := serviceFixture(t)
	secret, err := svc.Store.BotCredential(bot.ID)
	if err != nil || secret != "temporary-secret" {
		t.Fatal("credential was not stored as plaintext")
	}
	if got, err := svc.secret(bot); err != nil || got != secret {
		t.Fatal("credential unavailable without encryption configuration")
	}
}

func TestResolvedGateDuringSessionRefreshIsNotSent(t *testing.T) {
	svc, f, bot, run := serviceFixture(t)
	f.onSessions = func() {
		run.Status = "succeeded"
		run.Nodes["n"].Status = "succeeded"
		if err := svc.Store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	_, err := svc.ProcessOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if f.sent != 0 || items[0].Status != "expired" {
		t.Fatal("resolved gate was notified after a slow session query")
	}
}

func TestNodeRemindersIgnoreLegacyAIAndCompletion(t *testing.T) {
	for _, event := range []string{"waiting", "succeeded", "failed"} {
		t.Run(event, func(t *testing.T) {
			svc, f, bot, run := serviceFixture(t)
			if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
				t.Fatal(err)
			}
			binding := &run.WorkflowSnapshot.Nodes[0].Notifications[0]
			binding.ContentMode = "ai"
			binding.Template = ""
			binding.Events = []string{event}
			run.Nodes["n"].Output = "报告\n```notification\n{\"title\":\"自由标题\",\"summary\":\"已有验证结果\",\"body\":\"模型编写的内容\"}\n```"
			run.Events[0].Type = map[string]string{"waiting": "gate.waiting", "succeeded": "node.succeeded", "failed": "node.failed"}[event]
			run.Status = map[string]string{"waiting": "waiting", "succeeded": "succeeded", "failed": "failed"}[event]
			run.Nodes["n"].Status = map[string]string{"waiting": "waiting_approval", "succeeded": "succeeded", "failed": "failed"}[event]
			run.ID = "pure-ai-" + event
			if err := svc.Store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			_, err := svc.ProcessOne(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
			if event == "succeeded" {
				if f.sent != 0 || len(items) != 0 {
					t.Fatal("node completion must not notify")
				}
				return
			}
			if f.sent != 1 || len(items) != 1 || items[0].Status != "sent" || items[0].ContentSource != "template" || strings.Contains(f.content, "模型编写") {
				t.Fatalf("expected standard %s reminder: %+v %s", event, items, f.content)
			}

		})
	}
}
func TestLegacyWaitingPolicyStandardizedAfterApproval(t *testing.T) {
	svc, f, bot, run := serviceFixture(t)
	if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
		t.Fatal(err)
	}
	run.ID = "waiting-event"
	binding := &run.WorkflowSnapshot.Nodes[0].Notifications[0]
	binding.WaitingPolicy = "event"
	binding.ContentMode = "ai"
	binding.Template = ""
	binding.Events = []string{"waiting"}
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	// Approve before the worker has had time to discover a session and send.
	run.Status = "succeeded"
	run.Nodes["n"].Status = "succeeded"
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ProcessOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if f.sent != 0 || len(items) != 1 || items[0].Status != "expired" {
		t.Fatalf("event policy dropped reminder %+v %s", items, f.content)
	}
}

func TestSOPCompletionSendsOnceAfterAllNodeEvents(t *testing.T) {
	calls := 0

	svc, f, bot, run := serviceFixture(t)
	if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
		t.Fatal(err)
	}
	svc.GenerateReport = func(_ context.Context, actual domain.Run, model, prompt string) (domain.AINotificationContent, error) {
		calls++
		if actual.ID != "sop-report" || model != domain.DefaultNotificationReportModel || !strings.Contains(prompt, "校验通过") {
			t.Fatal("missing report input")
		}
		return domain.AINotificationContent{Title: "需求已完成", Body: "已实现功能，校验通过"}, nil
	}
	run.ID = "sop-report"
	run.WorkflowSnapshot.Notifications = []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai"}}
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: "script", Name: "脚本校验", Type: "script"})
	run.Nodes["n"].Status = "succeeded"
	run.Nodes["n"].Output = "```notification\n{\"title\":\"开发完成\",\"body\":\"已实现功能\"}\n```"
	run.Nodes["script"] = &domain.NodeRun{Status: "succeeded", Output: "校验通过"}
	run.Events = []domain.RunEvent{{ID: "node-done", NodeID: "n", Type: "node.succeeded", Timestamp: time.Now().UTC()}}
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 {
		t.Fatal("node completion sent before SOP completion")
	}
	run.Status = "succeeded"
	run.Events = append(run.Events, domain.RunEvent{ID: "sop-done", Type: "run.succeeded", Timestamp: time.Now().UTC()})
	for i := 0; i < 2; i++ {
		if err := svc.Store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ = svc.Store.NotificationDeliveries(bot.ProjectID)
	if calls != 1 || len(items) != 1 || f.sent != 1 || items[0].ReportState != "generated" || items[0].ReportRequest != "" || items[0].NodeID != "" || items[0].ContentSource != "ai" || !strings.Contains(f.content, "已实现功能") || !strings.Contains(f.content, "校验通过") {
		t.Fatalf("%+v %s", items, f.content)
	}
}

func TestUnifiedSOPBotCoversNodesWithoutBindings(t *testing.T) {
	for _, tc := range []struct{ kind, event, status, trigger string }{{"agent", "gate.waiting", "waiting_approval", "waiting"}, {"script", "node.failed", "failed", "failed"}, {"approval", "gate.waiting", "waiting_approval", "waiting"}} {
		t.Run(tc.kind, func(t *testing.T) {
			svc, f, bot, run := serviceFixture(t)
			if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
				t.Fatal(err)
			}
			run.ID = "unified-" + tc.kind
			run.WorkflowSnapshot.Notifications = []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai", Template: "仅供完成汇报的自定义内容"}}
			run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "n", Name: "当前步骤", Type: tc.kind}}
			run.Nodes["n"].Status = tc.status
			run.Nodes["n"].Error = "测试原因"
			run.Events[0].Type = tc.event
			if err := svc.Store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
			if len(items) != 1 || f.sent != 1 || items[0].EventType != tc.trigger || items[0].BotID != bot.ID || strings.Contains(f.content, "仅供完成") || !strings.Contains(f.content, "当前步骤") {
				t.Fatalf("%+v %s", items, f.content)
			}
		})
	}
}
func TestClearingUnifiedBotDisablesAllNotifications(t *testing.T) {
	svc, f, bot, run := serviceFixture(t)
	if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
		t.Fatal(err)
	}
	run.ID = "disabled-sop"
	run.WorkflowSnapshot.Notifications = nil
	run.WorkflowSnapshot.Nodes[0].Notifications = nil
	run.Events = append(run.Events, domain.RunEvent{ID: "done", Type: "run.succeeded", Timestamp: time.Now().UTC()})
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 || f.sent != 0 {
		t.Fatalf("disabled SOP sent %+v", items)
	}
}

func TestUnifiedSOPSetupFailureUsesStandardReminder(t *testing.T) {
	svc, f, bot, run := serviceFixture(t)
	if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
		t.Fatal(err)
	}
	run.ID = "setup-failure"
	run.Status = "failed"
	run.WorkflowSnapshot.Notifications = []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded"}, Template: "完成汇报专用"}}
	run.WorkflowSnapshot.Nodes[0].Notifications = nil
	run.Events = []domain.RunEvent{{ID: "failed", Type: "run.failed", Message: "沙箱启动失败", Timestamp: time.Now().UTC()}}
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 || f.sent != 1 || !strings.Contains(f.content, "沙箱启动失败") || strings.Contains(f.content, "完成汇报专用") {
		t.Fatalf("%+v %s", items, f.content)
	}
}

// A run started from a WeCom chat reports back over the Bot's live
// connection, without the wecom-cli session list: text events become a
// queued chat message, and a Bot without chat cannot reach the
// conversation.
func TestWeComOriginDeliveryUsesTheLiveConnection(t *testing.T) {
	svc, provider, bot, run := serviceFixture(t)
	provider.err = context.Canceled // the CLI path must not be used
	now := time.Now().UTC()
	item := domain.NotificationDelivery{ID: "origin-done", ProjectID: "p", BotID: bot.ID, RunID: run.ID, EventType: "succeeded", SourceType: "run", SourceID: run.ID, Origin: domain.NotificationOriginChat, TargetID: "wr_group", TargetType: "group", Content: "### SOP 已完成", Status: "pending", CreatedAt: now, NextAttemptAt: now}
	if err := svc.Store.CreateNotificationDelivery(item); err != nil {
		t.Fatal(err)
	}
	drain := func() {
		for i := 0; i < 10; i++ {
			if processed, err := svc.ProcessOne(context.Background()); err != nil || !processed {
				return
			}
		}
	}
	drain()
	got, _ := svc.Store.NotificationDelivery(item.ID)
	if got.Status != "target_unavailable" || !strings.Contains(got.LastError, "未开启对话") {
		t.Fatalf("without chat = %+v", got)
	}
	bot.InteractionEnabled = true
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.RetryNotification("p", item.ID); err != nil {
		t.Fatal(err)
	}
	stop := sendChatPosts(t, svc, domain.BotReplied)
	drain()
	stop()
	if got, _ = svc.Store.NotificationDelivery(item.ID); got.Status != "sent" || provider.sent != 0 {
		t.Fatalf("with chat = %+v sent=%d", got, provider.sent)
	}
	rows, err := svc.Store.BotInteractionRecords(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	var post domain.BotInteraction
	for _, row := range rows {
		if row.DeliveryID == item.ID && row.Action == botinteraction.ActionChatPost {
			post = row
			break
		}
	}
	conv, _ := svc.Store.BotConversation(post.ConversationID)
	if post.Action != botinteraction.ActionChatPost || post.DeliveryID != item.ID || post.SendRoute != repo.BotSendRoutePushChat || conv.ChatType != "group" || conv.ChatID != "wr_group" || !strings.Contains(post.Content, "SOP 已完成") {
		t.Fatalf("queued = %+v in %+v", post, conv)
	}
}

// settlePending takes the fixture's own notifications out of the queue.
func settlePending(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("status = ?", "pending").Update("status", "cancelled").Error; err != nil {
		t.Fatal(err)
	}
}

// sendChatPosts plays the live connection: queued chat posts end in status.
func sendChatPosts(t *testing.T, svc *Service, status string) func() {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			svc.Store.DB.Model(&domain.BotInteraction{}).Where("action = ? AND status NOT IN ?", botinteraction.ActionChatPost, []string{domain.BotReplied, domain.BotReplyFailed, domain.BotReplyUnknown}).Update("status", status)
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	return func() { close(done); <-stopped }
}

// A WeCom chat post is "sent" only once the live connection sent it: a
// failure retries (re-sending the message), an unconfirmed send is kept for
// checking.
func TestWeComOriginDeliveryFollowsTheSendResult(t *testing.T) {
	svc, _, bot, run := serviceFixture(t)
	bot.InteractionEnabled = true
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	settlePending(t, svc)
	now := time.Now().UTC()
	item := domain.NotificationDelivery{ID: "origin-done", ProjectID: "p", BotID: bot.ID, RunID: run.ID, EventType: "succeeded", SourceType: "run", SourceID: run.ID, Origin: domain.NotificationOriginChat, TargetID: "wr_group", TargetType: "group", Content: "### SOP 已完成", Status: "pending", CreatedAt: now, NextAttemptAt: now}
	if err := svc.Store.CreateNotificationDelivery(item); err != nil {
		t.Fatal(err)
	}
	process := func(status string) domain.NotificationDelivery {
		t.Helper()
		stop := sendChatPosts(t, svc, status)
		defer stop()
		if ok, err := svc.ProcessOne(context.Background()); err != nil || !ok {
			t.Fatalf("process %v %v", ok, err)
		}
		got, _ := svc.Store.NotificationDelivery(item.ID)
		return got
	}
	if got := process(domain.BotReplyFailed); got.Status != "pending" || !strings.Contains(got.LastError, "发送失败") {
		t.Fatalf("failed send = %+v", got)
	}
	if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", item.ID).Update("next_attempt_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if got := process(domain.BotReplyUnknown); got.Status != "send_unknown" {
		t.Fatalf("unconfirmed send = %+v", got)
	}
	if err := svc.Store.RetryNotification(item.ProjectID, item.ID); err != nil {
		t.Fatal(err)
	}
	if got := process(domain.BotReplied); got.Status != "sent" || got.DeliveryGeneration != 1 {
		t.Fatalf("manual resend = %+v", got)
	}
	original, err := svc.Store.BotInteraction(repo.BotMessageID(bot.ID, "chat-post:"+item.ID))
	if err != nil || original.Status != domain.BotReplyUnknown {
		t.Fatalf("original interaction = %+v, %v", original, err)
	}
}

// A run started from a private WeCom chat reports back there only while
// the sender is still bound to an account with access to the project.
func TestWeComPrivateOriginRechecksTheSender(t *testing.T) {
	svc, _, bot, run := serviceFixture(t)
	bot.InteractionEnabled = true
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	settlePending(t, svc)
	svc.Store.PutUser(domain.User{ID: 1, LoginName: "owner"})
	svc.Store.PutUser(domain.User{ID: 7, LoginName: "dev"})
	svc.Store.PutProject(domain.Project{ID: "p", OwnerID: 1, Members: []domain.ProjectMember{{UserID: 7, Role: "member"}}})
	now := time.Now().UTC()
	token, err := svc.Store.NewBotBindingChallenge(bot, "zhangsan", now, true)
	if err != nil {
		t.Fatal(err)
	}
	code, err := svc.Store.ClaimBotBindingChallenge(token, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.ConfirmBotUserBinding(bot, "zhangsan", code, now); err != nil {
		t.Fatal(err)
	}
	deliver := func(id string) domain.NotificationDelivery {
		t.Helper()
		item := domain.NotificationDelivery{ID: id, ProjectID: "p", BotID: bot.ID, RunID: run.ID, EventType: "succeeded", SourceType: "run", SourceID: run.ID, Origin: domain.NotificationOriginChat, TargetID: "zhangsan", TargetType: "single", Content: "### SOP 已完成", Status: "pending", CreatedAt: now, NextAttemptAt: now}
		if err := svc.Store.CreateNotificationDelivery(item); err != nil {
			t.Fatal(err)
		}
		stop := sendChatPosts(t, svc, domain.BotReplied)
		defer stop()
		if ok, err := svc.ProcessOne(context.Background()); err != nil || !ok {
			t.Fatalf("process %v %v", ok, err)
		}
		got, _ := svc.Store.NotificationDelivery(id)
		return got
	}
	if got := deliver("member"); got.Status != "sent" {
		t.Fatalf("member = %+v", got)
	}
	svc.Store.PutProject(domain.Project{ID: "p", OwnerID: 1, Members: []domain.ProjectMember{}})
	got := deliver("removed")
	if got.Status != "target_unavailable" || !strings.Contains(got.LastError, "没有该项目的权限") {
		t.Fatalf("removed member = %+v", got)
	}
	if _, err := svc.Store.BotInteraction(repo.BotMessageID(bot.ID, "chat-post:removed")); err == nil {
		t.Fatal("a message was queued for a member without access")
	}
}

// WeCom template cards have no text input: a node question goes out as
// text with the question and how to answer, on the SOP notification and on
// the message back to the conversation the run came from.
func TestWeComNodeQuestionIsSentAsText(t *testing.T) {
	svc, provider, bot, run := serviceFixture(t)
	bot.InteractionEnabled = true
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	run.Nodes["n"].Activity = "等待人工确认"
	run.Nodes["n"].GateResult = &domain.GateResult{Decision: "human", Reason: "缺登录方式", ActionRequired: "登录要不要支持邮箱？"}
	run.Events = nil
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ok, err := svc.ProcessOne(context.Background())
	if err != nil || !ok || provider.sent != 1 || !strings.Contains(provider.content, "问题：登录要不要支持邮箱？") || !strings.Contains(provider.content, "回答方式：@我 发送「回答：你的答案」，或引用本条消息回复") {
		t.Fatalf("binding %v %v sent=%d %q", ok, err, provider.sent, provider.content)
	}
	items, _ := svc.Store.NotificationDeliveries("p")
	if !strings.HasPrefix(provider.content, botinteraction.WeComNodeQuestionRefPrefix+items[0].ID+"\n") {
		t.Fatalf("binding question is missing its delivery reference: %q", provider.content)
	}
	if _, err := svc.Store.BotInteraction(repo.BotMessageID(bot.ID, "notification:"+items[0].ID)); err == nil {
		t.Fatal("a card was queued for the question")
	}

	now := time.Now().UTC()
	origin := domain.NotificationDelivery{ID: "origin-q", ProjectID: "p", BotID: bot.ID, RunID: run.ID, NodeID: "n", Attempt: 1, EventType: "waiting", SourceType: "run", SourceID: run.ID, Origin: domain.NotificationOriginChat, TargetID: "wr_group", TargetType: "group", Content: "### 节点需要补充信息\n节点：开发", Status: "pending", CreatedAt: now, NextAttemptAt: now}
	if err := svc.Store.CreateNotificationDelivery(origin); err != nil {
		t.Fatal(err)
	}
	defer sendChatPosts(t, svc, domain.BotReplied)()
	if ok, err := svc.ProcessOne(context.Background()); err != nil || !ok {
		t.Fatalf("origin %v %v", ok, err)
	}
	post, err := svc.Store.BotInteraction(repo.BotMessageID(bot.ID, "chat-post:"+origin.ID))
	if err != nil || post.Action != botinteraction.ActionChatPost ||
		!strings.HasPrefix(post.Content, botinteraction.WeComNodeQuestionRefPrefix+origin.ID+"\n") ||
		!strings.Contains(post.Content, "问题：登录要不要支持邮箱？") {
		t.Fatalf("origin post = %+v %v", post, err)
	}
}
