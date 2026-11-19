package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"context"
	"encoding/json"
	"net/http"
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
