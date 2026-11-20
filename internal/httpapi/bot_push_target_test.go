package httpapi

import (
	"bytes"
	"net/http/httptest"

	"github.com/gin-gonic/gin"

	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubPushChannel is a DingTalk adapter with scripted proactive sends.
type stubPushChannel struct {
	stubChannel
	mu      sync.Mutex
	results []botinteraction.SendResult
	targets []string
}

func (s *stubPushChannel) Push(_ context.Context, _ domain.NotificationBot, _ botinteraction.Credential, msg botinteraction.OutboundMessage) botinteraction.SendResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, msg.ChatID)
	if len(s.results) == 0 {
		return botinteraction.SendResult{Outcome: botinteraction.Delivered}
	}
	r := s.results[0]
	s.results = s.results[1:]
	return r
}

func TestPushTargetSelectionTrialSwitchAndConfirm(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	nh := &notificationHandlers{deps: &deps{repo: f.s, engine: f.e}, service: notification.New(f.s)}
	push := &stubPushChannel{stubChannel: stubChannel{botinteraction.ChannelDingTalk}}
	nh.service.Interactions.RegisterChannel(push)
	decode := func(code int, body []byte) map[string]any {
		out := map[string]any{"_code": float64(code)}
		json.Unmarshal(body, &out)
		return out
	}
	rec := cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{"projectId": "platform", "name": "钉钉通知", "channel": "dingtalk", "botId": "ding-app", "secret": "good", "enabled": true, "tenantKey": "ding-corp"}, nh.bots, nil, true)
	created := decode(rec.Code, rec.Body.Bytes())
	if rec.Code != 200 {
		t.Fatalf("create = %v", created)
	}
	id := created["id"].(string)
	stored, _ := f.s.NotificationBot(id)
	if _, err := f.s.RecordBotTenantCandidate(id, "ding-corp", "e1", stored.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	stored, _ = f.s.NotificationBot(id)
	bindBotTestUser(t, f.s, stored, "staff-1", f.user.ID)
	put := func(body map[string]any) map[string]any {
		t.Helper()
		current, _ := f.s.NotificationBot(id)
		req := map[string]any{"projectId": "platform", "name": "钉钉通知", "channel": "dingtalk", "botId": "ding-app", "enabled": true, "configVersion": current.ConfigVersion}
		for k, v := range body {
			req[k] = v
		}
		rec := cvBDirectHandlerRequest(t, f, http.MethodPut, "/api/notification-bots/"+id, req, nh.bot, map[string]string{"id": id}, true)
		return decode(rec.Code, rec.Body.Bytes())
	}
	rec = cvBDirectHandlerRequest(t, f, http.MethodGet, "/api/notification-bots/"+id+"/sessions", nil, nh.sessions, map[string]string{"id": id}, true)
	if out := decode(rec.Code, rec.Body.Bytes()); rec.Code != 200 || out["total"].(float64) != 1 {
		t.Fatalf("sessions = %v", out)
	}
	if out := put(map[string]any{"targetId": "stranger"}); out["code"] != "push_target_unverified" {
		t.Fatalf("unbound target = %v", out)
	}
	push.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown, SafeDetail: "钉钉返回服务端错误，送达结果未知"}}
	if out := put(map[string]any{"targetId": "staff-1"}); out["_code"].(float64) != 502 || out["code"] != "push_trial_failed" || out["outcome"] != "unknown" {
		t.Fatalf("unknown trial = %v", out)
	}
	if b, _ := f.s.NotificationBot(id); b.Target != nil {
		t.Fatal("target saved after an unknown trial")
	}
	if out := put(map[string]any{"targetId": "staff-1"}); out["_code"].(float64) != 200 {
		t.Fatalf("trial = %v", out)
	}
	bot, _ := f.s.NotificationBot(id)
	if bot.Target == nil || bot.Target.ID != "staff-1" || bot.Target.VerifiedAt == nil {
		t.Fatalf("target = %+v", bot.Target)
	}
	// Saving the same verified target again sends no new trial.
	trials := len(push.targets)
	if out := put(map[string]any{"targetId": "staff-1", "name": "钉钉通知"}); out["_code"].(float64) != 200 || len(push.targets) != trials {
		t.Fatalf("resave = %v trials %d", out, len(push.targets)-trials)
	}
	// Switching with queued notifications needs an explicit choice.
	now := time.Now().UTC()
	queued := domain.NotificationDelivery{ID: "queued", ProjectID: "platform", BotID: id, EventType: "failed", TargetID: "staff-1", TargetType: "single", Content: "x", Status: "pending", CreatedAt: now, NextAttemptAt: now.Add(time.Hour)}
	if err := f.s.CreateNotificationDelivery(queued); err != nil {
		t.Fatal(err)
	}
	bindBotTestUser(t, f.s, bot, "staff-2", f.user.ID)
	if out := put(map[string]any{"targetId": "staff-2"}); out["code"] != "pending_deliveries" || out["pending"].(float64) != 1 {
		t.Fatalf("switch without choice = %v", out)
	}
	if out := put(map[string]any{"targetId": "staff-2", "pendingTargetAction": "migrate"}); out["_code"].(float64) != 200 {
		t.Fatalf("migrate = %v", out)
	}
	if item, _ := f.s.NotificationDelivery("queued"); item.TargetID != "staff-2" || item.Status != "pending" {
		t.Fatalf("migrated = %+v", item)
	}
	// Manual confirmation of an unknown outcome.
	unknown := domain.NotificationDelivery{ID: "unknown", ProjectID: "platform", BotID: id, EventType: "failed", TargetID: "staff-2", Content: "x", Status: "send_unknown", CreatedAt: now, NextAttemptAt: now}
	if err := f.s.CreateNotificationDelivery(unknown); err != nil {
		t.Fatal(err)
	}
	rec = cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-deliveries/unknown/confirm?projectId=platform", nil, nh.retry, map[string]string{"deliveryId": "unknown"}, true)
	if rec.Code >= 300 {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	if item, _ := f.s.NotificationDelivery("unknown"); item.Status != "sent" || item.ReceiptSource != "manual" || item.ConfirmedBy != f.user.ID {
		t.Fatalf("confirmed = %+v", item)
	}
	rec = cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-deliveries/queued/confirm?projectId=platform", nil, nh.retry, map[string]string{"deliveryId": "queued"}, true)
	if rec.Code != 409 {
		t.Fatalf("confirm pending = %d", rec.Code)
	}
	// The test button re-runs the trial.
	rec = cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-bots/"+id+"/test", nil, nh.test, map[string]string{"id": id}, true)
	if rec.Code != 200 || push.targets[len(push.targets)-1] != "staff-2" {
		t.Fatalf("test = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPushResendEndpointRequiresVerifiedOriginalSender(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	nh := &notificationHandlers{deps: &deps{repo: f.s, engine: f.e}, service: notification.New(f.s)}
	push := &stubPushChannel{stubChannel: stubChannel{botinteraction.ChannelDingTalk}}
	nh.service.Interactions.RegisterChannel(push)
	bh := &botInteractionHandlers{deps: nh.deps, notifications: nh}
	rec := cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-bots", map[string]any{"projectId": "platform", "name": "钉钉", "channel": "dingtalk", "botId": "ding-app", "secret": "good", "enabled": true, "interactionEnabled": true, "tenantKey": "ding-corp"}, nh.bots, nil, true)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var created domain.NotificationBot
	json.Unmarshal(rec.Body.Bytes(), &created)
	if _, err := f.s.RecordBotTenantCandidate(created.ID, "ding-corp", "e1", created.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	bot, _ := f.s.NotificationBot(created.ID)
	bindBotTestUser(t, f.s, bot, "staff-1", f.user.ID)
	item := domain.BotInteraction{ID: "expired-1", BotID: bot.ID, ProjectID: "platform", SenderID: "staff-1", Content: "进度", Reply: "答复", Status: domain.BotReplyFailed, TerminalReason: "route_expired", DeliveryGeneration: 1, SendKey: "old", CreatedAt: time.Now().UTC()}
	if err := f.s.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	act := func(path string) (int, map[string]any) {
		raw, _ := json.Marshal(map[string]any{"actionKey": "key-" + path, "reason": "过期"})
		req := httptest.NewRequest(http.MethodPost, "/api/bot-interactions/expired-1/"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://"+req.Host)
		req = authedRequest(t, req, f.user)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		c.Params = gin.Params{{Key: "interactionId", Value: "expired-1"}}
		bh.act(c)
		out := map[string]any{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	records := func() domain.BotInteraction {
		w := cvBDirectHandlerRequest(t, f, http.MethodGet, "/api/notification-bots/"+bot.ID+"/interactions", nil, bh.records, map[string]string{"id": bot.ID}, true)
		var out struct {
			Messages []domain.BotInteraction `json:"messages"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Messages[0]
	}
	if records().PushResendAvailable {
		t.Fatal("unverified sender offered push resend")
	}
	if code, out := act("push-resend"); code != 409 || out["code"] != "push_target_unverified" {
		t.Fatalf("unverified = %d %v", code, out)
	}
	rec = cvBDirectHandlerRequest(t, f, http.MethodPost, "/api/notification-bots/"+bot.ID+"/targets/verify", map[string]any{"targetId": "staff-1"}, nh.verifyTarget, map[string]string{"id": bot.ID}, true)
	if rec.Code != 200 || push.targets[len(push.targets)-1] != "staff-1" {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	if !records().PushResendAvailable {
		t.Fatal("verified sender not offered push resend")
	}
	if code, out := act("resend"); code != 409 || out["code"] != "route_expired" {
		t.Fatalf("plain resend = %d %v", code, out)
	}
	code, out := act("push-resend")
	if code != 200 || out["sendRoute"] != "push" || out["status"] != domain.BotReady {
		t.Fatalf("push resend = %d %v", code, out)
	}
	stored, _ := f.s.BotInteraction("expired-1")
	if stored.DeliveryGeneration != 2 || stored.SendKey == "old" || stored.ResentBy != f.user.ID {
		t.Fatalf("stored = %+v", stored)
	}
}

// A Feishu action card click by the notification recipient approves the
// waiting node with the recipient's platform identity; a second click on the
// same card reports it as handled.
func TestFeishuCardClickApprovesWaitingNode(t *testing.T) {
	s, h, bot := botFixture(t)
	bot.ID, bot.BotID, bot.Channel, bot.TenantKey, bot.TenantState = "b-feishu-card", "cli_card", domain.BotChannelFeishu, "tk", domain.BotTenantLocked
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	bindBotTestUser(t, s, bot, "ou-member", 43)
	run := domain.Run{ID: "feishu-card-run", Status: "waiting", WorkflowID: "wf", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", ProjectID: "p", Nodes: []domain.WorkflowNode{{ID: "gate", Type: "human", Name: "人工确认"}}}, Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval", Attempt: 1}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	delivery := domain.NotificationDelivery{ID: "d-feishu", BotID: bot.ID, ProjectID: "p", RunID: run.ID, NodeID: "gate", SourceName: "人工确认", EventType: "waiting", Attempt: 1, TargetID: "ou-member", TargetType: "single", Status: "sent", CreatedAt: time.Now().UTC()}
	if err := s.CreateNotificationDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	card := h.notifications.service.Interactions.PushCardFor(delivery, "需求：登录优化\nSOP：交付", "https://platform.example/#/runs/"+run.ID)
	if card == nil || card.Title != "登录优化" || len(card.Actions) == 0 || card.Actions[0].Key != "approve" {
		t.Fatalf("card = %+v", card)
	}
	item := domain.BotInteraction{DeliveryID: delivery.ID, Action: "approve", SenderID: "ou-member"}
	conv := domain.BotConversation{ChatID: "ou-member", ChatType: "single"}
	reply, err := h.handleBotCardAction(context.Background(), bot, item, conv)
	if err != nil || !strings.Contains(reply, "操作已完成") {
		t.Fatal(reply, err)
	}
	updated, _ := s.RunState(run.ID)
	if updated.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("node = %s", updated.Nodes["gate"].Status)
	}
	if reply, _ := h.handleBotCardAction(context.Background(), bot, item, conv); !strings.Contains(reply, "过期") {
		t.Fatalf("second click = %q", reply)
	}
	if h.notifications.service.Interactions.PushCardFor(delivery, "", "") != nil {
		t.Fatal("card offered after the node moved on")
	}
}
