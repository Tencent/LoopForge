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
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

func testCard() *PushCard {
	return &PushCard{DeliveryID: "d-1", Title: "登录优化", Tag: "待人工确认", Reason: "请确认 **方案**", Rows: [][2]string{{"节点", "评审"}, {"SOP", "研发交付"}}, Actions: []PushCardAction{{Key: "approve", Text: "通过并继续"}, {Key: "reject", Text: "返工", Danger: true}}, DetailsURL: "https://platform.example/#/runs/r"}
}

func TestFeishuPushSendsActionCard(t *testing.T) {
	var mu sync.Mutex
	var msgType, content string
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MsgType string `json:"msg_type"`
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		msgType, content = body.MsgType, body.Content
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_card"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ch := newFeishuChannel()
	ch.base = srv.URL
	res := ch.Push(context.Background(), domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli"}, Credential{Secret: "s"}, OutboundMessage{ChatID: "ou_1", Text: "fallback", SendKey: "k", Card: testCard()})
	if res.Outcome != Delivered || msgType != "interactive" {
		t.Fatalf("push = %+v type %s", res, msgType)
	}
	var card struct {
		Header struct {
			Title struct{ Tag, Content string } `json:"title"`
		} `json:"header"`
		Elements []map[string]any `json:"elements"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatal(err)
	}
	if card.Header.Title.Content != "待人工确认：登录优化" || card.Header.Title.Tag != "plain_text" {
		t.Fatalf("header = %+v", card.Header)
	}
	if !strings.Contains(content, `"af_action":"approve"`) || !strings.Contains(content, `"af_delivery":"d-1"`) || !strings.Contains(content, `"type":"danger"`) || !strings.Contains(content, `"url":"https://platform.example/#/runs/r"`) {
		t.Fatalf("buttons = %s", content)
	}
	if strings.Contains(content, "lark_md") {
		t.Fatal("user text rendered as Markdown")
	}
}

func TestDingTalkPushCardNeedsTemplate(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var cardBody map[string]any
	answer := func(w http.ResponseWriter) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"deliverResults": []any{map[string]any{"success": true, "carrierId": "car-1"}}}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"accessToken": "at", "expireIn": 7200})
	})
	mux.HandleFunc("/v1.0/card/instances/createAndDeliver", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		cardBody = map[string]any{}
		json.NewDecoder(r.Body).Decode(&cardBody)
		a := answer
		mu.Unlock()
		a(w)
	})
	mux.HandleFunc("/v1.0/robot/oToMessages/batchSend", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"processQueryKey": "pqk"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ch := newDingTalkChannel()
	ch.api = srv.URL
	bot := domain.NotificationBot{ID: "cfg-card", Channel: domain.BotChannelDingTalk, BotID: "ding-app", CredentialVersion: 1}
	forgetDingTalkToken(bot)
	msg := OutboundMessage{ChatID: "staff-1", Text: "文字", SendKey: "generation-one-key", Card: testCard()}
	if res := ch.Push(context.Background(), bot, Credential{Secret: "s"}, msg); res.Outcome != Delivered || paths[len(paths)-1] != "/v1.0/robot/oToMessages/batchSend" {
		t.Fatalf("without template = %+v %v", res, paths)
	}
	bot.CardTemplateID = "tpl-1.schema"
	res := ch.Push(context.Background(), bot, Credential{Secret: "s"}, msg)
	if res.Outcome != Delivered || res.PlatformMessageID != "car-1" || paths[len(paths)-1] != "/v1.0/card/instances/createAndDeliver" {
		t.Fatalf("card = %+v %v", res, paths)
	}
	if cardBody["cardTemplateId"] != "tpl-1.schema" || cardBody["outTrackId"] != "d-1.generation-o" || cardBody["callbackType"] != "STREAM" || cardBody["openSpaceId"] != "dtv1.card//IM_ROBOT.staff-1" {
		t.Fatalf("body = %v", cardBody)
	}
	if deliver := cardBody["imRobotOpenDeliverModel"].(map[string]any); deliver["robotCode"] != "ding-app" || deliver["spaceType"] != "IM_ROBOT" {
		t.Fatalf("deliver = %v", deliver)
	}
	params := cardBody["cardData"].(map[string]any)["cardParamMap"].(map[string]any)
	if params["show_approve"] != "true" || params["show_reject"] != "true" || params["show_retry"] != "false" || params["text_approve"] != "通过并继续" || params["detail_url"] != "https://platform.example/#/runs/r" {
		t.Fatalf("params = %v", params)
	}
	if !strings.Contains(params["content"].(string), "请确认 ＊＊方案＊＊") {
		t.Fatalf("markdown not neutralized: %v", params["content"])
	}
	if dingtalkCardDelivery("d-1.generation-o") != "d-1" {
		t.Fatal("delivery from track")
	}
	cases := []struct {
		status int
		body   string
		want   SendOutcome
	}{
		{400, `{"code":"param.templateNotExist"}`, Rejected},
		{200, `{"success":true,"result":{"deliverResults":[{"success":false,"errorMsg":"no robot"}]}}`, Rejected},
		{200, `{"success":true}`, Unknown},
		{502, ``, Unknown},
		{429, ``, NotDelivered},
	}
	for _, c := range cases {
		if got := classifyDingTalkCard(c.status, []byte(c.body)); got.Outcome != c.want {
			t.Errorf("%d %s = %+v", c.status, c.body, got)
		}
	}
	if got := classifyDingTalkCard(400, []byte(`{"code":"param.templateNotExist"}`)); !strings.Contains(got.SafeDetail, "模板") {
		t.Errorf("template hint missing: %+v", got)
	}
}

func TestCardCallbacksMapToActionEvents(t *testing.T) {
	tenant := "tk"
	ev := &callback.CardActionTriggerEvent{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "e1", AppID: "cli", TenantKey: "tk"}}, Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_1", TenantKey: &tenant}, Action: &callback.CallBackAction{Value: map[string]any{"af_action": "approve", "af_delivery": "d-1"}}}}
	got, ok := feishuCardEvent(ev)
	if !ok || got.Action != "approve" || got.DeliveryID != "d-1" || got.SenderID != "ou_1" || got.MessageID != "card:e1" || got.AppID != "cli" || got.TenantKey != "tk" || got.ChatType != "single" {
		t.Fatalf("feishu = %+v", got)
	}
	ev.Event.Action.Value["af_action"] = "delete_everything"
	if _, ok := feishuCardEvent(ev); ok {
		t.Fatal("unknown action accepted")
	}
	bot := domain.NotificationBot{BotID: "ding-app"}
	raw := `{"type":"actionCallback","outTrackId":"d-1.abc","corpId":"corp","userId":"staff-1","content":"{\"cardPrivateData\":{\"actionIds\":[\"btn\"],\"params\":{\"action\":\"retry\"}}}"}`
	dt, reason := dingtalkCardEvent(bot, raw, "f1")
	if reason != "" || dt.Action != "retry" || dt.DeliveryID != "d-1" || dt.SenderID != "staff-1" || dt.TenantKey != "corp" || dt.AppID != "ding-app" || dt.MessageID != "card:f1" {
		t.Fatalf("dingtalk = %+v %s", dt, reason)
	}
	if _, reason := dingtalkCardEvent(bot, strings.Replace(raw, "retry", "drop", 1), "f2"); reason != "unknown_action" {
		t.Fatalf("unknown action = %s", reason)
	}
}

// A click on a Feishu or DingTalk action card runs the card action of the
// recipient and answers with a private message; someone else's click is
// queued in a conversation of their own (and refused by the action).
func TestCardActionClickRunsActionAndRepliesPrivately(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-card-click", Channel: domain.BotChannelDingTalk, BotID: "ding-app", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "corp", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.CreateNotificationDelivery(domain.NotificationDelivery{ID: "d-click", ProjectID: "p", BotID: bot.ID, RunID: "r", NodeID: "n", EventType: "waiting", TargetID: "staff-1", TargetType: "single", Content: "x", Status: "sent", CreatedAt: now, NextAttemptAt: now}); err != nil {
		t.Fatal(err)
	}
	service := New(store)
	fake := newFakeChannel()
	ch := pushKindChannel{kindChannel{fake, ChannelDingTalk}, make(chan OutboundMessage, 4)}
	service.RegisterChannel(ch)
	var mu sync.Mutex
	seen := map[string]domain.BotConversation{}
	service.Action = func(ctx context.Context, b domain.NotificationBot, item domain.BotInteraction, conv domain.BotConversation) (string, error) {
		mu.Lock()
		seen[item.SenderID] = conv
		mu.Unlock()
		return "「评审」操作已完成：" + item.Action, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() { cancel(); <-service.Done }()
	conn := waitConn(t, fake)
	click := func(sender, id string) {
		ack := conn.push(t, InboundEvent{Channel: ChannelDingTalk, AppID: "ding-app", TenantKey: "corp", EventID: id, MessageID: "card:" + id, SenderID: sender, ChatID: sender, ChatType: "single", MsgType: "card_action", Action: "approve", DeliveryID: "d-click"})
		if !ack.Accepted {
			t.Fatalf("click not accepted: %+v", ack)
		}
	}
	click("staff-1", "f1")
	select {
	case msg := <-ch.pushes:
		if msg.ChatID != "staff-1" || !strings.Contains(msg.Text, "操作已完成：approve") {
			t.Fatalf("reply = %+v", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no private reply")
	}
	click("staff-2", "f2")
	select {
	case msg := <-ch.pushes:
		if msg.ChatID != "staff-2" {
			t.Fatalf("reply = %+v", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no private reply to the other clicker")
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["staff-1"].ChatID != "staff-1" || seen["staff-1"].ChatType != "single" || seen["staff-2"].ChatID != "staff-2" {
		t.Fatalf("conversations = %+v", seen)
	}
	select {
	case m := <-conn.replies:
		t.Fatalf("webhook used %+v", m)
	default:
	}
}

func TestDingTalkCardCallbackFrameAnswersWithStatus(t *testing.T) {
	var got InboundEvent
	c := &dingtalkConn{channel: newDingTalkChannel(), bot: domain.NotificationBot{BotID: "ding-app"}, ctx: context.Background(), sink: func(ctx context.Context, ev InboundEvent) (InboundAck, error) {
		got = ev
		return InboundAck{Accepted: true}, nil
	}}
	frame := &payload.DataFrame{Type: "CALLBACK", Headers: payload.DataFrameHeader{payload.DataFrameHeaderKTopic: payload.CardInstanceCallbackTopic, payload.DataFrameHeaderKMessageId: "m1"}, Data: `{"outTrackId":"d-9.k","userId":"u","corpId":"c","content":"{\"cardPrivateData\":{\"params\":{\"action\":\"pass\"}}}"}`}
	resp := c.handleCardCallback(frame)
	if got.Action != "pass" || got.DeliveryID != "d-9" || resp.Code != payload.DataFrameResponseStatusCodeKOK || !strings.Contains(resp.Data, "已提交") || !strings.Contains(resp.Data, `"show_pass":"false"`) || !strings.Contains(resp.Data, "updateCardDataByKey") {
		t.Fatalf("event %+v resp %+v", got, resp)
	}
}

func TestDraftCardForReplyWithPendingDraft(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	item := domain.BotInteraction{ID: "turn-1", Reply: "我会创建这个需求。\n\n以下操作需要你在平台确认后才会执行（30 分钟内有效）：\n1. 保存需求：登录优化（SOP：研发交付）\n在平台确认这些操作：\nhttps://p.example/bot-approve.html#draft=x"}
	if svc.DraftCardFor(item, "https://p.example") != nil {
		t.Fatal("card without a draft")
	}
	d, err := store.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: "c", UserID: 7, InteractionID: "turn-1", Summary: "1. 保存需求：登录优化（SOP：研发交付）", Drafts: "[]"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	card := svc.DraftCardFor(item, "https://p.example")
	if card == nil || card.DraftID != d.ID || card.Body != "我会创建这个需求。" || len(card.Rows) != 1 || card.DetailsURL != "https://p.example/bot-approve.html#draft="+d.ID {
		t.Fatalf("card = %+v", card)
	}
	keys := []string{}
	for _, a := range card.Actions {
		keys = append(keys, a.Key)
	}
	if strings.Join(keys, ",") != "draft_confirm,draft_confirm_start,draft_cancel" {
		t.Fatalf("actions = %v", keys)
	}
	raw := string(feishuCardJSON(card))
	if !strings.Contains(raw, `"af_draft":"`+d.ID+`"`) || strings.Contains(raw, "af_delivery") || !strings.Contains(raw, "在平台确认") {
		t.Fatalf("card json = %s", raw)
	}
	// A cancelled draft falls back to the text reply.
	store.CancelBotPendingDraft(d.ID, 7, "x", time.Now().UTC())
	if svc.DraftCardFor(item, "") != nil {
		t.Fatal("card for a cancelled draft")
	}
	ev := &callback.CardActionTriggerEvent{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "e9", AppID: "cli"}}, Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_1"}, Action: &callback.CallBackAction{Value: map[string]any{"af_action": "draft_confirm_start", "af_draft": d.ID}}}}
	if got, ok := feishuCardEvent(ev); !ok || got.Action != DraftActionConfirmStart || got.DeliveryID != d.ID {
		t.Fatalf("draft click = %+v", got)
	}
}

func TestFeishuReplySendsCardWhenGiven(t *testing.T) {
	var mu sync.Mutex
	var msgType string
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/im/v1/messages/om_1/reply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MsgType string `json:"msg_type"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		msgType = body.MsgType
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_r"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ch := newFeishuChannel()
	ch.base = srv.URL
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli"}
	conn := &feishuConn{channel: ch, bot: bot, api: ch.client(bot, Credential{Secret: "s"})}
	card := &PushCard{DraftID: "d", Tag: "待你确认", Title: "1 项操作", Body: "x", Actions: []PushCardAction{{Key: DraftActionConfirm, Text: "确认执行"}}}
	if res := conn.Reply(context.Background(), OutboundMessage{Text: "t", Route: ReplyRoute{MessageID: "om_1"}, Card: card}); res.Outcome != Delivered || msgType != "interactive" {
		t.Fatalf("card reply = %+v %s", res, msgType)
	}
	if res := conn.Reply(context.Background(), OutboundMessage{Text: "t", Route: ReplyRoute{MessageID: "om_1"}}); res.Outcome != Delivered || msgType != "text" {
		t.Fatalf("text reply = %+v %s", res, msgType)
	}
}

// The write-confirmation card of a draft creating one requirement carries a
// SOP drop-down; the requester's pick is stored and shown, others are
// refused, and a click replaces the buttons with a status and is answered
// in the conversation the request came from (a group here).
func TestDraftCardSOPSelectionAndGroupClick(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-draft-card", Channel: domain.BotChannelFeishu, BotID: "cli_d", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "tk", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	// The request was made in a group.
	origin := domain.BotInteraction{ID: repo.BotMessageID(bot.IdentityScope(), "om_ask"), BotID: bot.ID, SenderID: "ou_req", Content: "建个需求", ReplyRoute: "om_ask"}
	if _, err := store.EnqueueBotInteraction(origin, "group", "oc_group"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sop := &domain.BotDraftSOP{Index: 0, DefaultName: "研发交付", Workflows: []domain.BotDraftSOPOption{{ID: "wf-dev", Name: "研发交付"}, {ID: "wf-fix", Name: "缺陷修复"}}}
	draft, err := store.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, SenderID: "ou_req", UserID: 7, InteractionID: origin.ID, Summary: "1. 保存需求：登录优化（SOP：项目默认 · 研发交付）", Drafts: "[]", SOP: sop}, now)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	card := svc.draftCardByID(draft)
	if len(card.Selects) != 1 || card.Selects[0].Label != "SOP" || card.Selects[0].Initial != domain.BotDraftDefaultSOP || len(card.Selects[0].Options) != 3 || card.Rows[0][1] != "1. 保存需求：登录优化" {
		t.Fatalf("card = %+v selects %+v", card, card.Selects)
	}
	raw := string(feishuCardJSON(card))
	if !strings.Contains(raw, `"tag":"select_static"`) || !strings.Contains(raw, `"initial_option":"__default__"`) || !strings.Contains(raw, `"af_action":"draft_sop"`) {
		t.Fatalf("select json = %s", raw)
	}
	event := func(sender, action, option, id string) InboundEvent {
		return InboundEvent{Channel: ChannelFeishu, AppID: "cli_d", TenantKey: "tk", EventID: id, MessageID: "card:" + id, SenderID: sender, ChatID: sender, ChatType: "single", MsgType: "card_action", Action: action, DeliveryID: draft.ID, Option: option, Route: ReplyRoute{MessageID: "om_card"}}
	}
	// Someone else cannot pick or confirm.
	ack, err := svc.accept(context.Background(), bot, event("ou_other", DraftActionSOP, "wf-fix", "e1"))
	if err != nil || ack.Accepted || !strings.Contains(ack.Toast, "只有发起人") {
		t.Fatalf("other = %+v %v", ack, err)
	}
	// An unknown SOP is refused, a listed one is stored.
	if ack, _ := svc.accept(context.Background(), bot, event("ou_req", DraftActionSOP, "wf-foreign", "e2")); !strings.Contains(ack.Toast, "不可选") {
		t.Fatalf("foreign = %+v", ack)
	}
	ack, err = svc.accept(context.Background(), bot, event("ou_req", DraftActionSOP, "wf-fix", "e3"))
	if err != nil || ack.Card == nil || ack.Card.Selects[0].Initial != "wf-fix" || ack.Accepted {
		t.Fatalf("pick = %+v %v", ack, err)
	}
	if stored, _ := store.BotPendingDraft(draft.ID, now); stored.Choice != "wf-fix" {
		t.Fatalf("choice = %q", stored.Choice)
	}
	// Confirm: queued in the group conversation, threaded under the card,
	// and the answer card has no buttons left.
	ack, err = svc.accept(context.Background(), bot, event("ou_req", DraftActionConfirmStart, "", "e4"))
	if err != nil || !ack.Accepted || ack.Card == nil || len(ack.Card.Actions) != 0 || len(ack.Card.Selects) != 0 || !strings.Contains(ack.Card.Status, "确认并启动 SOP") {
		t.Fatalf("confirm = %+v %v", ack, err)
	}
	item, _ := store.BotInteraction(ack.InteractionID)
	conv, _ := store.BotConversation(item.ConversationID)
	if conv.ChatType != "group" || conv.ChatID != "oc_group" || item.ReplyRoute != "om_card" || item.SendRoute != "" || item.Action != DraftActionConfirmStart {
		t.Fatalf("queued = %+v in %+v", item, conv)
	}
	if out := string(feishuCardJSON(ack.Card)); strings.Contains(out, `"tag":"button","text":{"content":"确认执行"`) || !strings.Contains(out, `"tag":"note"`) {
		t.Fatalf("handled card = %s", out)
	}
	resp := feishuCardAnswer(ack)
	if resp.Card == nil || resp.Card.Type != "raw" || resp.Toast == nil {
		t.Fatalf("answer = %+v", resp)
	}
}

// A draft creating one requirement in a project with repositories shows a
// repository drop-down before the SOP; only the requester can pick, only
// offered repositories are accepted.
func TestDraftCardRepositorySelection(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-draft-repo", Channel: domain.BotChannelFeishu, BotID: "cli_r", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "tk", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	origin := domain.BotInteraction{ID: "ask-repo", BotID: bot.ID, SenderID: "ou_req", Content: "建个需求", ReplyRoute: "om_ask"}
	store.EnqueueBotInteraction(origin, "single", "oc_p2p")
	now := time.Now().UTC()
	sop := &domain.BotDraftSOP{DefaultName: "研发交付", DefaultRepositoryName: "lf-loop-sandbox", Repositories: []domain.BotDraftSOPOption{{ID: "repo-main", Name: "lf-loop-sandbox"}, {ID: "repo-web", Name: "web-console"}}}
	draft, err := store.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, SenderID: "ou_req", UserID: 7, InteractionID: origin.ID, Summary: "1. 保存需求：登录优化（仓库：项目默认 · lf-loop-sandbox）（SOP：项目默认 · 研发交付）", Drafts: "[]", SOP: sop}, now)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	card := svc.draftCardByID(draft)
	if len(card.Selects) != 2 || card.Selects[0].Key != DraftActionRepo || card.Selects[0].Label != "仓库" || card.Selects[0].Options[0].Text != "项目默认仓库（lf-loop-sandbox）" || card.Selects[0].Initial != domain.BotDraftDefaultSOP || card.Rows[0][1] != "1. 保存需求：登录优化" {
		t.Fatalf("card = %+v selects %+v", card, card.Selects)
	}
	raw := string(feishuCardJSON(card))
	if !strings.Contains(raw, `"af_action":"draft_repo"`) || !strings.Contains(raw, `"content":"仓库"`) {
		t.Fatalf("json = %s", raw)
	}
	event := func(sender, option, id string) InboundEvent {
		return InboundEvent{Channel: ChannelFeishu, AppID: "cli_r", TenantKey: "tk", EventID: id, MessageID: "card:" + id, SenderID: sender, ChatID: sender, ChatType: "single", MsgType: "card_action", Action: DraftActionRepo, DeliveryID: draft.ID, Option: option, Route: ReplyRoute{MessageID: "om_card"}}
	}
	if ack, _ := svc.accept(context.Background(), bot, event("ou_other", "repo-web", "r1")); !strings.Contains(ack.Toast, "只有发起人") {
		t.Fatalf("other = %+v", ack)
	}
	if ack, _ := svc.accept(context.Background(), bot, event("ou_req", "repo-foreign", "r2")); !strings.Contains(ack.Toast, "不可选") {
		t.Fatalf("foreign = %+v", ack)
	}
	ack, err := svc.accept(context.Background(), bot, event("ou_req", "repo-web", "r3"))
	if err != nil || ack.Card == nil || ack.Card.Selects[0].Initial != "repo-web" || ack.Toast != "已选择仓库" {
		t.Fatalf("pick = %+v %v", ack, err)
	}
	if stored, _ := store.BotPendingDraft(draft.ID, now); stored.RepoChoice != "repo-web" || stored.SelectedRepository() != "repo-web" {
		t.Fatalf("stored = %+v", stored)
	}
	tenant := "tk"
	cb := &callback.CardActionTriggerEvent{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "e9", AppID: "cli_r", TenantKey: "tk"}}, Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_req", TenantKey: &tenant}, Action: &callback.CallBackAction{Value: map[string]any{"af_action": DraftActionRepo, "af_draft": draft.ID}, Option: "repo-main"}}}
	if mapped, ok := feishuCardEvent(cb); !ok || mapped.Action != DraftActionRepo || mapped.Option != "repo-main" || mapped.DeliveryID != draft.ID {
		t.Fatalf("callback = %+v %v", mapped, ok)
	}
	cb.Event.Action.Option = ""
	if _, ok := feishuCardEvent(cb); ok {
		t.Fatal("empty repository pick accepted")
	}
}

// A node question card on Feishu has an answer input and a submit button
// instead of approve and retry; the submitted text is kept with the click,
// an empty answer is refused on the card.
func TestFeishuQuestionCardTakesTheAnswer(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-q", Channel: domain.BotChannelFeishu, BotID: "cli_q", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "tk", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "r-q", Status: "waiting", WorkflowID: "wf", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "交付", Nodes: []domain.WorkflowNode{{ID: "n", Name: "需求定型", Type: "agent"}}},
		Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 1, Activity: "等待人工确认", GateResult: &domain.GateResult{Decision: "human", Reason: "缺登录方式", ActionRequired: "登录要不要支持邮箱？"}}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	delivery := domain.NotificationDelivery{ID: "d-q", ProjectID: "p", BotID: bot.ID, RunID: run.ID, NodeID: "n", Attempt: 1, EventType: "waiting", TargetID: "ou_1", TargetType: "single", Content: "需求：登录优化", Status: "sent", CreatedAt: now, NextAttemptAt: now}
	if err := store.CreateNotificationDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	service := New(store)
	card := service.PushCardFor(delivery, delivery.Content, "https://platform.example/#/runs/r-q")
	if card == nil || card.Tag != "需要补充信息" || len(card.Actions) != 1 || card.Actions[0].Key != "answer" || !strings.Contains(card.Hint, "在下方填写答案") {
		t.Fatalf("card = %+v", card)
	}
	content := string(feishuCardJSON(card))
	for _, want := range []string{`"tag":"form"`, `"tag":"input"`, `"name":"answer"`, `"action_type":"form_submit"`, `"af_action":"answer"`, `"af_delivery":"d-q"`, "问题：登录要不要支持邮箱？", "查看详情"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %s in %s", want, content)
		}
	}
	if strings.Contains(content, `"af_action":"approve"`) || strings.Contains(content, `"af_action":"retry"`) || strings.Contains(content, "待处理") {
		t.Fatalf("question card offers approve/retry: %s", content)
	}

	tenant := "tk"
	click := func(id, answer string) InboundEvent {
		ev := &callback.CardActionTriggerEvent{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: id, AppID: "cli_q", TenantKey: "tk"}}, Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "ou_1", TenantKey: &tenant}, Action: &callback.CallBackAction{Value: map[string]any{"af_action": "answer", "af_delivery": "d-q"}, FormValue: map[string]any{"answer": answer}}}}
		mapped, ok := feishuCardEvent(ev)
		if !ok {
			t.Fatal("answer click not mapped")
		}
		return mapped
	}
	if ev := click("e0", "  都要  "); ev.Action != "answer" || ev.Text != "都要" {
		t.Fatalf("mapped = %+v", ev)
	}
	ack, err := service.accept(context.Background(), bot, click("e1", " "))
	if err != nil || ack.Accepted || ack.Toast != "请先填写答案" {
		t.Fatalf("empty = %+v %v", ack, err)
	}
	ack, err = service.accept(context.Background(), bot, click("e2", "手机号和邮箱都要"))
	if err != nil || !ack.Accepted || ack.Card == nil || !strings.Contains(ack.Card.Status, "已提交答案：手机号和邮箱都要") {
		t.Fatalf("answer = %+v %v", ack, err)
	}
	if done := string(feishuCardJSON(ack.Card)); strings.Contains(done, `"tag":"form"`) {
		t.Fatalf("handled card keeps the form: %s", done)
	}
	stored, err := store.BotInteraction(ack.InteractionID)
	if err != nil || stored.Action != "answer" || stored.Content != "手机号和邮箱都要" || stored.DeliveryID != "d-q" {
		t.Fatalf("stored = %+v %v", stored, err)
	}
}

// In a Feishu group, a reply to a node question the Bot asked there reaches
// the answer handling without @; other unmentioned messages, replies to
// another group's question and replies to an answered question do not.
func TestFeishuGroupReplyToNodeQuestionPassesWithoutMention(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-gq", Channel: domain.BotChannelFeishu, BotID: "cli_gq", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "tk", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "r-gq", Status: "waiting", WorkflowID: "wf", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "交付", Nodes: []domain.WorkflowNode{{ID: "n", Name: "需求定型", Type: "agent"}}},
		Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 1, Activity: "等待人工确认", GateResult: &domain.GateResult{Decision: "human", Reason: "缺登录方式", ActionRequired: "登录要不要支持邮箱？"}}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, d := range []domain.NotificationDelivery{
		{ID: "d-gq", ProjectID: "p", BotID: bot.ID, RunID: run.ID, NodeID: "n", Attempt: 1, EventType: "waiting", TargetID: "oc_group", TargetType: "group", Content: "需求：登录优化", Status: "sent", PlatformMessageID: "om_question", CreatedAt: now, NextAttemptAt: now},
		{ID: "d-other", ProjectID: "p", BotID: bot.ID, RunID: run.ID, NodeID: "n", Attempt: 1, EventType: "waiting", TargetID: "oc_other", TargetType: "group", Content: "需求：登录优化", Status: "sent", PlatformMessageID: "om_other", CreatedAt: now, NextAttemptAt: now},
	} {
		if err := store.CreateNotificationDelivery(d); err != nil {
			t.Fatal(err)
		}
	}
	service := New(store)
	saved, _ := store.NotificationBot(bot.ID)
	reply := func(id, parent string) InboundAck {
		ack, err := service.accept(context.Background(), saved, InboundEvent{Channel: ChannelFeishu, AppID: "cli_gq", TenantKey: "tk", EventID: "e-" + id, MessageID: id, SenderID: "ou_1", ChatID: "oc_group", ChatType: "group", MsgType: "text", Text: "都要", ParentID: parent, Route: ReplyRoute{MessageID: id}})
		if err != nil {
			t.Fatal(err)
		}
		return ack
	}
	if ack := reply("om_plain", ""); ack.Accepted || ack.Ignored != "group_not_mentioned" {
		t.Fatalf("plain = %+v", ack)
	}
	if ack := reply("om_foreign", "om_other"); ack.Accepted || ack.Ignored != "group_not_mentioned" {
		t.Fatalf("other group's question = %+v", ack)
	}
	if ack := reply("om_answer", "om_question"); !ack.Accepted {
		t.Fatalf("reply to question = %+v", ack)
	}
	run.Nodes["n"].Attempt = 2
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if ack := reply("om_late", "om_question"); ack.Accepted || ack.Ignored != "group_not_mentioned" {
		t.Fatalf("answered question = %+v", ack)
	}
}
