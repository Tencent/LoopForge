package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func bindClarifyTestUser(t *testing.T, s *repo.Store, bot domain.NotificationBot, sender string, userID int64) {
	t.Helper()
	now := time.Now().UTC()
	token, err := s.NewBotBindingChallenge(bot, sender, now, true)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.ClaimBotBindingChallenge(token, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmBotUserBinding(bot, sender, code, now); err != nil {
		t.Fatal(err)
	}
}

// groupChatFixture: a Feishu Bot of project p with an owner (42), a
// maintainer (44), a member (43) and a viewer (45), each bound to a sender.
func groupChatFixture(t *testing.T) (*repo.Store, *Service, domain.NotificationBot) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for id, name := range map[int64]string{42: "owner", 43: "member", 44: "maint", 45: "viewer", 46: "outsider"} {
		store.PutUser(domain.User{ID: id, LoginName: name, ChineseName: "用户" + name})
	}
	store.PutProject(domain.Project{ID: "p", Key: "LOOP", OwnerID: 42, Members: []domain.ProjectMember{{UserID: 43, Role: "member"}, {UserID: 44, Role: "maintainer"}, {UserID: 45, Role: "viewer"}}})
	bot := domain.NotificationBot{ID: "cfg-group", Channel: domain.BotChannelFeishu, BotID: "cli_g", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "tk", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	bot, _ = store.NotificationBot(bot.ID)
	for sender, id := range map[string]int64{"ou_owner": 42, "ou_member": 43, "ou_maint": 44, "ou_viewer": 45, "ou_outsider": 46} {
		bindClarifyTestUser(t, store, bot, sender, id)
	}
	return store, New(store), bot
}

func cardClick(sender, action, delivery, option, id string) InboundEvent {
	return InboundEvent{Channel: ChannelFeishu, AppID: "cli_g", TenantKey: "tk", EventID: id, MessageID: "card:" + id, SenderID: sender, ChatID: sender, ChatType: "single", MsgType: "card_action", Action: action, DeliveryID: delivery, Option: option, CardChatID: "oc_group", Route: ReplyRoute{MessageID: "om_card"}}
}

// A notification card sent to a group: any bound project member may click
// (the action is queued in the group, threaded under the card, and the card
// names who handled it); unbound people and viewers are refused.
func TestGroupNotificationCardClickByAnyMember(t *testing.T) {
	store, svc, bot := groupChatFixture(t)
	now := time.Now().UTC()
	run := domain.Run{ID: "run-g", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{Name: "研发交付", Nodes: []domain.WorkflowNode{{ID: "n", Name: "需求定型", Type: "agent"}}}, ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "failed", Attempt: 1, Error: "模型调用失败"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	delivery := domain.NotificationDelivery{ID: "d-group", ProjectID: "p", BotID: bot.ID, RunID: run.ID, NodeID: "n", SourceName: "需求定型", EventType: "failed", Attempt: 1, TargetID: "oc_group", TargetType: "group", Origin: domain.NotificationOriginChat, Content: "需求：登录优化", Status: "sent", CreatedAt: now, NextAttemptAt: now}
	if err := store.CreateNotificationDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	for sender, want := range map[string]string{"ou_stranger": "绑定平台账号", "ou_viewer": "没有该项目的执行权限", "ou_outsider": "没有该项目的执行权限"} {
		ack, err := svc.accept(context.Background(), bot, cardClick(sender, "retry", delivery.ID, "", "e-"+sender))
		if err != nil || ack.Accepted || !strings.Contains(ack.Toast, want) || ack.Card != nil {
			t.Fatalf("%s: %+v %v", sender, ack, err)
		}
	}
	// A click from another group is not this card's.
	foreign := cardClick("ou_member", "retry", delivery.ID, "", "e-foreign")
	foreign.CardChatID = "oc_elsewhere"
	if ack, _ := svc.accept(context.Background(), bot, foreign); ack.Accepted {
		if item, _ := store.BotInteraction(ack.InteractionID); item.SendRoute != repo.BotSendRoutePush {
			t.Fatalf("foreign group click handled as the group's: %+v", item)
		}
	}
	ack, err := svc.accept(context.Background(), bot, cardClick("ou_member", "retry", delivery.ID, "", "e-member"))
	if err != nil || !ack.Accepted || ack.Card == nil || len(ack.Card.Actions) != 0 || !strings.Contains(ack.Card.Status, "用户member 已提交「重试」") {
		t.Fatalf("member click = %+v %v", ack, err)
	}
	item, _ := store.BotInteraction(ack.InteractionID)
	conv, _ := store.BotConversation(item.ConversationID)
	if conv.ChatType != "group" || conv.ChatID != "oc_group" || item.ReplyRoute != "om_card" || item.SendRoute != "" || item.Action != "retry" {
		t.Fatalf("queued = %+v in %+v", item, conv)
	}
	// DingTalk card callbacks carry no card message: the result is a new
	// group message.
	dt := cardClick("ou_maint", "retry", delivery.ID, "", "e-dt")
	dt.Route, dt.CardChatID = ReplyRoute{}, ""
	ack, _ = svc.accept(context.Background(), bot, dt)
	if item, _ := store.BotInteraction(ack.InteractionID); item.SendRoute != repo.BotSendRoutePushChat {
		t.Fatalf("no card message = %+v", item)
	}
}

func clarifyFixtureSession(t *testing.T, store *repo.Store, bot domain.NotificationBot, starter string, starterID int64) (domain.BotClarifySession, domain.BotInteraction) {
	t.Helper()
	store.PutIssue(domain.Issue{ID: "iss-c", ProjectID: "p", Number: 7, Title: "登录优化", AssigneeUserID: 43})
	// Make the group conversation exist.
	if _, err := store.EnqueueBotInteraction(domain.BotInteraction{ID: "msg-start", BotID: bot.ID, SenderID: starter, Content: "澄清 LOOP-7"}, "group", "oc_group"); err != nil {
		t.Fatal(err)
	}
	start, _ := store.BotInteraction("msg-start")
	sess, err := store.CreateBotClarifySession(domain.BotClarifySession{ConfigID: bot.ID, ProjectID: "p", ConversationID: start.ConversationID, ChatType: "group", ChatID: "oc_group", IssueID: "iss-c", StarterSender: starter, StarterUserID: starterID, Status: domain.BotClarifyConcluded, Summary: "只做手机号登录"})
	if err != nil {
		t.Fatal(err)
	}
	post := domain.BotInteraction{ID: "post-conclusion", BotID: bot.ID, SenderID: starter, Action: ActionClarifyPost, DeliveryID: sess.ID, Quote: ClarifyPostConclusion, Content: "LOOP-7「登录优化」 · 澄清结论\n只做手机号登录", Reply: "LOOP-7「登录优化」 · 澄清结论\n只做手机号登录", SendRoute: repo.BotSendRoutePushChat}
	if _, err := store.EnqueueBotInteraction(post, "group", "oc_group"); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.BotInteraction(post.ID)
	return sess, stored
}

// Clarification cards: the conclusion card's save buttons are limited to
// the responsible person, the starter and maintainers; a click is queued in
// the session's group, threaded under the card, and locks it.
func TestClarifyCardButtonsAndPermissions(t *testing.T) {
	store, svc, bot := groupChatFixture(t)
	sess, post := clarifyFixtureSession(t, store, bot, "ou_owner", 42)
	card := clarifyPostCard(post, post.Reply)
	if card == nil || card.Tag != "澄清结论" || card.Title != "LOOP-7「登录优化」 · 澄清结论" || len(card.Actions) != 3 || card.Actions[0].Key != ClarifyActionSave || card.Actions[0].Ref != post.ID {
		t.Fatalf("card = %+v", card)
	}
	raw := string(feishuCardJSON(card))
	if !strings.Contains(raw, `"af_clarify":"`+sess.ID+`"`) || !strings.Contains(raw, `"af_ref":"post-conclusion"`) || !strings.Contains(raw, `"template":"blue"`) {
		t.Fatalf("card json = %s", raw)
	}
	// Feishu callbacks map the clarification button to an action event.
	tenant := "tk"
	ev := &callback.CardActionTriggerEvent{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "cb1", AppID: "cli_g", TenantKey: "tk"}}, Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_member", TenantKey: &tenant}, Action: &callback.CallBackAction{Value: map[string]any{"af_action": ClarifyActionSave, "af_clarify": sess.ID, "af_ref": post.ID}}, Context: &callback.Context{OpenMessageID: "om_card", OpenChatID: "oc_group"}}}
	mapped, ok := feishuCardEvent(ev)
	if !ok || mapped.Action != ClarifyActionSave || mapped.DeliveryID != sess.ID || mapped.Option != post.ID || mapped.CardChatID != "oc_group" || mapped.Route.MessageID != "om_card" {
		t.Fatalf("mapped = %+v %v", mapped, ok)
	}
	delete(ev.Event.Action.Value, "af_ref")
	if _, ok := feishuCardEvent(ev); ok {
		t.Fatal("clarification click without reference accepted")
	}
	// A plain member (not responsible for the requirement, not the
	// starter) cannot save; a viewer cannot even act.
	store.PutIssue(domain.Issue{ID: "iss-c", ProjectID: "p", Number: 7, Title: "登录优化", AssigneeUserID: 44})
	ack, err := svc.accept(context.Background(), bot, cardClick("ou_member", ClarifyActionSave, sess.ID, post.ID, "c1"))
	if err != nil || ack.Accepted || !strings.Contains(ack.Toast, "只有需求负责人") {
		t.Fatalf("member save = %+v %v", ack, err)
	}
	if ack, _ := svc.accept(context.Background(), bot, cardClick("ou_viewer", ClarifyActionSave, sess.ID, post.ID, "c2")); ack.Accepted || !strings.Contains(ack.Toast, "执行权限") {
		t.Fatalf("viewer = %+v", ack)
	}
	// Moving to another round is open to members, but only while answers
	// are collected.
	if ack, _ := svc.accept(context.Background(), bot, cardClick("ou_member", ClarifyActionNext, sess.ID, post.ID, "c3")); ack.Accepted || !strings.Contains(ack.Toast, "收集回答") {
		t.Fatalf("next while concluded = %+v", ack)
	}
	// The maintainer saves: queued in the group under the card, card locked.
	ack, err = svc.accept(context.Background(), bot, cardClick("ou_maint", ClarifyActionSaveStart, sess.ID, post.ID, "c4"))
	if err != nil || !ack.Accepted || ack.Card == nil || len(ack.Card.Actions) != 0 || !strings.Contains(ack.Card.Status, "用户maint 已选择「保存并启动 SOP」") {
		t.Fatalf("maintainer save = %+v %v", ack, err)
	}
	item, _ := store.BotInteraction(ack.InteractionID)
	conv, _ := store.BotConversation(item.ConversationID)
	if conv.ChatID != "oc_group" || item.DeliveryID != sess.ID || item.Quote != post.ID || item.ReplyRoute != "om_card" || item.Action != ClarifyActionSaveStart {
		t.Fatalf("queued = %+v in %+v", item, conv)
	}
	// Once the session has ended the card says so.
	store.UpdateBotClarifySession(sess.ID, func(s *domain.BotClarifySession) error { s.Status = domain.BotClarifyEnded; return nil })
	if ack, _ := svc.accept(context.Background(), bot, cardClick("ou_owner", ClarifyActionSave, sess.ID, post.ID, "c5")); ack.Accepted || ack.Card == nil || ack.Card.Status != "这次澄清已结束" {
		t.Fatalf("ended = %+v", ack)
	}
}

// "这是对 Q2 的回答吗？" cards go with the Bot's reply and only the speaker
// may answer them.
func TestClarifyConfirmCardOnlyForTheSpeaker(t *testing.T) {
	store, svc, bot := groupChatFixture(t)
	sess, _ := clarifyFixtureSession(t, store, bot, "ou_owner", 42)
	msg := domain.BotInteraction{ID: "msg-unsure", BotID: bot.ID, SenderID: "ou_member", Content: "我觉得要发短信"}
	store.EnqueueBotInteraction(msg, "group", "oc_group")
	stored, _ := store.BotInteraction(msg.ID)
	store.UpdateBotClarifySession(sess.ID, func(s *domain.BotClarifySession) error {
		s.Status = domain.BotClarifyAsking
		s.Questions = []domain.BotClarifyQuestion{{N: 1, Question: "验收方式？"}, {N: 2, Question: "要不要短信？"}}
		s.Pending = []domain.BotClarifyPending{{ID: msg.ID, Sender: "ou_member", Name: "用户member", Text: "我觉得要发短信", N: 2}}
		return nil
	})
	stored.Reply = "用户member，这是对 Q2 的回答吗？"
	card := svc.clarifyCardFor(stored)
	if card == nil || card.Title != "这是对 Q2 的回答吗？" || len(card.Actions) != 2 || card.Actions[0].Text != "是，记为 Q2" || card.Actions[0].Ref != msg.ID {
		t.Fatalf("confirm card = %+v", card)
	}
	if other := svc.clarifyCardFor(domain.BotInteraction{ID: "x", ConversationID: stored.ConversationID}); other != nil {
		t.Fatalf("card on an unrelated reply: %+v", other)
	}
	if ack, _ := svc.accept(context.Background(), bot, cardClick("ou_owner", ClarifyActionYes, sess.ID, msg.ID, "y1")); ack.Accepted || !strings.Contains(ack.Toast, "只有说这句话的人") {
		t.Fatalf("other speaker = %+v", ack)
	}
	ack, err := svc.accept(context.Background(), bot, cardClick("ou_member", ClarifyActionYes, sess.ID, msg.ID, "y2"))
	if err != nil || !ack.Accepted || ack.Card == nil || !strings.Contains(ack.Card.Status, "已选择「是」") {
		t.Fatalf("speaker = %+v %v", ack, err)
	}
	if ack, _ := svc.accept(context.Background(), bot, cardClick("ou_member", ClarifyActionNo, sess.ID, "gone", "y3")); ack.Accepted || !strings.Contains(ack.Toast, "已经确认过") {
		t.Fatalf("settled = %+v", ack)
	}
}

// Messages the platform sends into a conversation go to the group itself
// (Feishu chat_id, DingTalk group API and IM_GROUP cards) or to the sender
// of a private chat.
func TestPushToGroupChats(t *testing.T) {
	var mu sync.Mutex
	var receiveType, receiveID string
	feishu := http.NewServeMux()
	feishu.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t", "expire": 7200})
	})
	feishu.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiveID string `json:"receive_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		receiveType, receiveID = r.URL.Query().Get("receive_id_type"), body.ReceiveID
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_1"}})
	})
	fs := httptest.NewServer(feishu)
	defer fs.Close()
	fch := newFeishuChannel()
	fch.base = fs.URL
	fbot := domain.NotificationBot{ID: "cfg-fg", Channel: domain.BotChannelFeishu, BotID: "cli"}
	if res := fch.Push(context.Background(), fbot, Credential{Secret: "s"}, OutboundMessage{ChatID: "oc_group", ChatType: "group", Text: "x", SendKey: "k1"}); res.Outcome != Delivered || receiveType != "chat_id" || receiveID != "oc_group" {
		t.Fatalf("feishu group = %+v %s %s", res, receiveType, receiveID)
	}
	if res := fch.Push(context.Background(), fbot, Credential{Secret: "s"}, OutboundMessage{ChatID: "ou_1", ChatType: "single", Text: "x", SendKey: "k2"}); res.Outcome != Delivered || receiveType != "open_id" {
		t.Fatalf("feishu single = %+v %s", res, receiveType)
	}

	var paths []string
	var bodies []map[string]any
	ding := http.NewServeMux()
	ding.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"accessToken": "at", "expireIn": 7200})
	})
	record := func(w http.ResponseWriter, r *http.Request) map[string]any {
		body := map[string]any{}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		paths, bodies = append(paths, r.URL.Path), append(bodies, body)
		mu.Unlock()
		return body
	}
	ding.HandleFunc("/v1.0/robot/groupMessages/send", func(w http.ResponseWriter, r *http.Request) {
		record(w, r)
		json.NewEncoder(w).Encode(map[string]any{"processQueryKey": "pqk-group"})
	})
	ding.HandleFunc("/v1.0/card/instances/createAndDeliver", func(w http.ResponseWriter, r *http.Request) {
		record(w, r)
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"deliverResults": []any{map[string]any{"success": true, "carrierId": "car-g"}}}})
	})
	ds := httptest.NewServer(ding)
	defer ds.Close()
	dch := newDingTalkChannel()
	dch.api = ds.URL
	dbot := domain.NotificationBot{ID: "cfg-dg", Channel: domain.BotChannelDingTalk, BotID: "ding-app", CredentialVersion: 1}
	forgetDingTalkToken(dbot)
	res := dch.Push(context.Background(), dbot, Credential{Secret: "s"}, OutboundMessage{ChatID: "cid-group", ChatType: "group", Text: "进展"})
	if res.Outcome != Delivered || res.PlatformMessageID != "pqk-group" || paths[0] != "/v1.0/robot/groupMessages/send" || bodies[0]["openConversationId"] != "cid-group" || bodies[0]["robotCode"] != "ding-app" {
		t.Fatalf("dingtalk group text = %+v %v %v", res, paths, bodies)
	}
	dbot.CardTemplateID = "tpl.schema"
	res = dch.Push(context.Background(), dbot, Credential{Secret: "s"}, OutboundMessage{ChatID: "cid-group", ChatType: "group", Text: "x", SendKey: "key-1", Card: testCard()})
	card := bodies[len(bodies)-1]
	if res.Outcome != Delivered || card["openSpaceId"] != "dtv1.card//IM_GROUP.cid-group" || card["imRobotOpenDeliverModel"] != nil {
		t.Fatalf("dingtalk group card = %+v %v", res, card)
	}
	if deliver, _ := card["imGroupOpenDeliverModel"].(map[string]any); deliver["robotCode"] != "ding-app" {
		t.Fatalf("group deliver = %v", card)
	}
}
