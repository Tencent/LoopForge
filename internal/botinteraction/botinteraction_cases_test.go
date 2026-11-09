package botinteraction

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gorilla/websocket"
)

func TestNotificationCardRendering(t *testing.T) {
	run := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{Name: "Workflow", Nodes: []domain.WorkflowNode{{ID: "review", RejectTo: "fix"}, {ID: "fix", Name: "修复"}}},
		Nodes: map[string]*domain.NodeRun{"review": {
			NodeID: "review", Error: "fallback",
			GateResult: &domain.GateResult{Reason: strings.Repeat("reason ", 20), ActionRequired: "fix it", Findings: []domain.GateFinding{{Title: "finding", Evidence: "evidence"}}},
		}},
	}
	item := domain.NotificationDelivery{ID: "d", NodeID: "review", SourceName: "Review", EventType: "waiting"}
	actions := []map[string]any{{"text": "通过", "key": "approve", "style": 1}, {"text": "重试", "key": "retry", "style": 4}, {"text": "退回", "key": "reject", "style": 4}}
	card := notificationCard(run, item, actions, "需求：Issue\nSOP：Workflow")
	if card["action_menu"] != nil {
		t.Fatal("unexpected action menu")
	}
	if got := notificationCardIssue("x\n需求： Issue "); got != "Issue" {
		t.Fatalf("issue = %q", got)
	}
	if got := cardReworkTarget(run, "review"); got != "修复" {
		t.Fatalf("target = %q", got)
	}
	details := notificationCardDetails(run, item, "需求：Issue", "https://example.test/run")
	if !strings.Contains(details, "finding") || !strings.Contains(details, "查看运行详情") {
		t.Fatalf("details = %q", details)
	}
	if got := notificationCardDetails(run, item, "x", "javascript:bad"); strings.Contains(got, "javascript") {
		t.Fatal("unsafe URL included")
	}
	if got := notificationCard(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Name: "W"}, Nodes: map[string]*domain.NodeRun{}}, item, nil, ""); got["sub_title_text"] == "" {
		t.Fatal("fallback card")
	}
}

func TestProcessBuiltInReplies(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cvs-process", BotID: "external", OwnerID: 9, ProjectID: "p", Enabled: true, InteractionEnabled: true}
	if err := store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	cardRun := domain.Run{
		ID: "cvs-live-card", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Name: "WF", Nodes: []domain.WorkflowNode{{ID: "review"}}},
		Nodes:            map[string]*domain.NodeRun{"review": {NodeID: "review", Status: "waiting_approval", Attempt: 1}},
	}
	if err := store.PutRun(cardRun); err != nil {
		t.Fatal(err)
	}
	cardDelivery := domain.NotificationDelivery{
		ID: "cvs-live-delivery", BotID: bot.ID, ProjectID: bot.ProjectID, RunID: cardRun.ID, NodeID: "review",
		Attempt: 1, EventType: "waiting", TargetType: "group", TargetID: "group-chat", Status: "pending", CreatedAt: time.Now(),
	}
	if err := store.DB.Create(&cardDelivery).Error; err != nil {
		t.Fatal(err)
	}
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		sent := false
		for {
			var frame Frame
			if ws.ReadJSON(&frame) != nil {
				return
			}
			_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0})
			if frame.Cmd == "aibot_subscribe" && !sent {
				sent = true
				for i, content := range []string{"/new", "normal request", "empty request"} {
					_ = ws.WriteJSON(map[string]any{
						"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "cvs-" + content},
						"body": map[string]any{"msgid": "cvs-msg-" + string(rune('a'+i)), "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "text", "text": map[string]string{"content": content}},
					})
				}
				_ = ws.WriteJSON(map[string]any{
					"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "cvs-image"},
					"body": map[string]any{"msgid": "cvs-msg-image", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "image"},
				})
				_ = ws.WriteJSON(map[string]any{
					"cmd": "aibot_event_callback", "headers": map[string]string{"req_id": "cvs-action"},
					"body": map[string]any{"msgid": "cvs-msg-action", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "event", "event": map[string]string{"eventtype": "template_card_event", "event_key": "retry", "task_id": "delivery_nonce"}},
				})
				for _, action := range []string{"approve", "pass"} {
					_ = ws.WriteJSON(map[string]any{
						"cmd": "aibot_event_callback", "headers": map[string]string{"req_id": "cvs-" + action},
						"body": map[string]any{"msgid": "cvs-msg-" + action, "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "event", "event": map[string]string{"eventtype": "template_card_event", "event_key": action, "task_id": "delivery_nonce"}},
					})
				}
			}
		}
	}))
	defer server.Close()
	service := New(store)
	service.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	service.Action = func(_ context.Context, _ domain.NotificationBot, item domain.BotInteraction, _ domain.BotConversation) (string, error) {
		switch item.Action {
		case "retry":
			return "", ErrCardActionDeferred
		case "approve":
			return "", errors.New("action failed")
		default:
			return "action completed", nil
		}
	}
	service.Generate = func(_ context.Context, _ domain.NotificationBot, _ domain.BotConversation, _ []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		if item.Content == "normal request" {
			return "", context.DeadlineExceeded
		}
		return "", nil
	}
	for _, queued := range []domain.BotInteraction{
		{ID: "cvs-stale", BotID: bot.ID, SenderID: "user", Content: "old request", CreatedAt: time.Now().Add(-time.Hour)},
		{ID: "cvs-card-invalid", BotID: bot.ID, SenderID: "user", Action: "notification", DeliveryID: "missing", CreatedAt: time.Now()},
		{ID: "cvs-ready", BotID: bot.ID, SenderID: "user", Content: "recovered", CreatedAt: time.Now()},
	} {
		if inserted, err := store.EnqueueBotInteraction(queued, "single", "user"); err != nil || !inserted {
			t.Fatalf("enqueue %#v = %v, %v", queued, inserted, err)
		}
	}
	if err := store.DB.Model(&domain.BotInteraction{}).Where("id = ?", "cvs-ready").Updates(map[string]any{"status": "ready", "reply": "recovered reply"}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	cardDone := make(chan error, 1)
	go func() { cardDone <- service.QueueNotificationCard(ctx, bot, cardDelivery, "") }()
	defer func() {
		cancel()
		<-service.Done
	}()
	eventually(t, func() bool {
		rows, _ := store.BotInteractionRecords(bot.ID)
		if len(rows) != 11 {
			return false
		}
		for _, row := range rows {
			if row.Status != "replied" && row.Status != "expired" {
				return false
			}
		}
		return true
	})
	select {
	case err := <-cardDone:
		if err != nil {
			t.Fatalf("live card = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live card did not complete")
	}
}

func TestConnectionReconnectsAfterLocalClose(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cvs-reconnect", BotID: "external-reconnect", OwnerID: 10, ProjectID: "p", Enabled: true, InteractionEnabled: true}
	if err := store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{}, 3)
	var upgrader websocket.Upgrader
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		attempts++
		connected <- struct{}{}
		if attempts%2 == 0 {
			var frame Frame
			if ws.ReadJSON(&frame) == nil {
				_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 400})
			}
		}
		_ = ws.Close()
	}))
	defer server.Close()
	service := New(store)
	service.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	for i := 0; i < 2; i++ {
		select {
		case <-connected:
		case <-time.After(5 * time.Second):
			cancel()
			<-service.Done
			t.Fatal("missing reconnect")
		}
	}
	cancel()
	<-service.Done
}

func TestSocketBusinessReply(t *testing.T) {
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			var frame Frame
			if ws.ReadJSON(&frame) != nil {
				return
			}
			code := 0
			if frame.Cmd == "cvs_test" {
				code = 400
			}
			_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": code})
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock, err := dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "bot", "secret", func(*socket, Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	defer sock.close()
	var business *ReplyError
	if err := sock.request(ctx, "cvs_test", "", nil); !errors.As(err, &business) || business.Code != 400 {
		t.Fatalf("business reply = %#v", err)
	}
	sock.mu.Lock()
	sock.pending["duplicate"] = make(chan int, 1)
	sock.mu.Unlock()
	if err := sock.request(ctx, "cvs_test", "duplicate", nil); err == nil {
		t.Fatal("duplicate request accepted")
	}
	sock.mu.Lock()
	delete(sock.pending, "duplicate")
	sock.mu.Unlock()
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := sock.request(canceled, "cvs_test", "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
}

func TestReconcileRemovesStoppedConnections(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Setenv("AGENTFLOW_WECOM_WS_URL", "ws://local.invalid")
	service := New(store)
	if service.URL != "ws://local.invalid" {
		t.Fatalf("configured URL = %q", service.URL)
	}
	done := make(chan struct{})
	close(done)
	canceled := false
	service.running["gone"] = &connection{bot: domain.NotificationBot{ID: "gone"}, cancel: func() { canceled = true }, done: done}
	stayBot := domain.NotificationBot{ID: "stay", BotID: "stay-external", OwnerID: 1, ProjectID: "p", Enabled: true, InteractionEnabled: true, UpdatedAt: time.Now().UTC()}
	if err := store.SaveNotificationBot(stayBot, "secret"); err != nil {
		t.Fatal(err)
	}
	stayDone := make(chan struct{})
	service.running["stay"] = &connection{bot: stayBot, cancel: func() {}, done: stayDone}
	service.reconcile(context.Background())
	if !canceled || len(service.running) != 1 {
		t.Fatalf("reconcile canceled=%v running=%d", canceled, len(service.running))
	}
	close(stayDone)
}

func TestNormalizeIncomingMessageVariants(t *testing.T) {
	bot := domain.NotificationBot{ID: "bot", BotID: "external"}
	tests := []struct {
		name string
		cmd  string
		body map[string]any
		ok   bool
		text string
		kind string
		chat string
	}{
		{"voice", "aibot_msg_callback", map[string]any{"msgid": "1", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "voice", "voice": map[string]string{"content": "spoken"}}, true, "spoken", "single", "user"},
		{"group inferred", "aibot_event_callback", map[string]any{"msgid": "2", "aibotid": "external", "chatid": "group", "from": map[string]string{"userid": "user"}, "msgtype": "event", "event": map[string]string{"eventtype": "template_card_event", "event_key": "retry", "task_id": "delivery_nonce"}}, true, "", "group", "group"},
		{"unsupported", "aibot_msg_callback", map[string]any{"msgid": "3", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "image"}, true, "[不支持的消息类型", "single", "user"},
		{"handled action", "aibot_event_callback", map[string]any{"msgid": "4", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "event", "event": map[string]string{"eventtype": "template_card_event", "event_key": "handled", "task_id": "delivery_nonce"}}, false, "", "", ""},
		{"wrong event", "aibot_event_callback", map[string]any{"msgid": "5", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "event", "event": map[string]string{"eventtype": "other"}}, false, "", "", ""},
		{"bad chat", "aibot_msg_callback", map[string]any{"msgid": "6", "aibotid": "external", "chattype": "channel", "chatid": "x", "from": map[string]string{"userid": "user"}, "msgtype": "text", "text": map[string]string{"content": "x"}}, false, "", "", ""},
	}
	for _, tc := range tests {
		raw, _ := json.Marshal(tc.body)
		item, kind, chat, ok := normalizeIncoming(bot, Frame{Cmd: tc.cmd, Body: raw})
		if ok != tc.ok {
			t.Fatalf("%s ok=%v item=%#v", tc.name, ok, item)
		}
		if ok && (kind != tc.kind || chat != tc.chat || (tc.text != "" && !strings.Contains(item.Content, tc.text))) {
			t.Fatalf("%s = %#v %q %q", tc.name, item, kind, chat)
		}
	}
	if (&ReplyError{Code: 1}).Error() == "" {
		t.Fatal("empty reply error")
	}
}

func TestQueueNotificationCardCancellationAndValidation(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	run := domain.Run{
		ID: "cvs-card-run", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Name: "WF", Nodes: []domain.WorkflowNode{{ID: "n", Name: "Review", RejectTo: "fix"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 1}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "bot", ProjectID: "p"}
	item := domain.NotificationDelivery{
		ID: "delivery", BotID: bot.ID, ProjectID: bot.ProjectID, RunID: run.ID, NodeID: "n",
		Attempt: 1, EventType: "waiting", SourceName: "Review", TargetType: "group", TargetID: "chat",
		CreatedAt: time.Now(),
	}
	svc := New(st)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = svc.QueueNotificationCard(ctx, bot, item, "需求：Issue\n"+strings.Repeat("detail ", 30), "https://example.test/run")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queue cancellation = %v", err)
	}
	valid := item
	valid.ID = "delivery-valid"
	cardID := repo.BotMessageID(bot.ID, "notification:"+valid.ID)
	if err := st.DB.Create(&domain.BotInteraction{ID: cardID, BotID: bot.ID, Action: "notification", DeliveryID: valid.ID, Status: "replied"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.QueueNotificationCard(context.Background(), bot, valid, "需求：Issue\nDetails", "https://example.test/run"); err != nil {
		t.Fatalf("valid card queue = %v", err)
	}
	expired := item
	expired.ID = "delivery-expired"
	expiredID := repo.BotMessageID(bot.ID, "notification:"+expired.ID)
	if err := st.DB.Create(&domain.BotInteraction{ID: expiredID, BotID: bot.ID, Action: "notification", DeliveryID: expired.ID, Status: "replied", LastError: "expired"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.QueueNotificationCard(context.Background(), bot, expired, "Details"); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("expired card queue = %v", err)
	}
	failed := item
	failed.ID = "delivery-failed"
	failedID := repo.BotMessageID(bot.ID, "notification:"+failed.ID)
	if err := st.DB.Create(&domain.BotInteraction{ID: failedID, BotID: bot.ID, Action: "other", DeliveryID: failed.ID, Status: "reply_failed"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.QueueNotificationCard(context.Background(), bot, failed, "Details"); err == nil {
		t.Fatal("failed card queue accepted")
	}
	stale := item
	stale.Attempt = 2
	if err := svc.QueueNotificationCard(context.Background(), bot, stale, "x"); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("stale queue = %v", err)
	}
}

func TestNotificationCardTaskBranches(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	bot := domain.NotificationBot{ID: "bot-task", ProjectID: "p"}
	run := domain.Run{
		ID: "card-task-run", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 1}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	delivery := domain.NotificationDelivery{
		ID: "card-task-delivery", BotID: bot.ID, ProjectID: bot.ProjectID, RunID: run.ID, NodeID: "n",
		Attempt: 1, EventType: "waiting", TargetType: "group", TargetID: "chat", Status: "sent", CreatedAt: time.Now(),
	}
	if err := st.DB.Create(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	cardID := repo.BotMessageID(bot.ID, "notification:"+delivery.ID)
	card := domain.BotInteraction{ID: cardID, BotID: bot.ID, CardTaskID: "task-1", Status: "replied"}
	if err := st.DB.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	svc := New(st)
	interaction := domain.BotInteraction{DeliveryID: delivery.ID, CardTaskID: "task-1", Action: "approve"}
	conv := domain.BotConversation{ChatID: "chat", ChatType: "group"}
	if got, err := svc.NotificationCardTask(bot, interaction, conv); err != nil || got.ID != delivery.ID {
		t.Fatalf("card task = %#v, %v", got, err)
	}
	interaction.CardTaskID = "wrong"
	if _, err := svc.NotificationCardTask(bot, interaction, conv); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("wrong task = %v", err)
	}
	if _, err := svc.NotificationCardTask(bot, domain.BotInteraction{DeliveryID: "missing"}, conv); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("missing delivery = %v", err)
	}
	plain := domain.BotInteraction{DeliveryID: delivery.ID, Action: "unknown"}
	if _, err := svc.NotificationCardTask(bot, plain, domain.BotConversation{ChatID: "other", ChatType: "group"}); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("wrong chat = %v", err)
	}
	delivery.CreatedAt = time.Now().Add(-25 * time.Hour)
	if err := st.DB.Save(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotificationCardTask(bot, plain, conv); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("old delivery = %v", err)
	}
	delivery.CreatedAt = time.Now()
	delivery.Status = "failed"
	if err := st.DB.Save(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotificationCardTask(bot, plain, conv); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("failed delivery = %v", err)
	}
	delivery.Status = "sent"
	if err := st.DB.Save(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotificationCardTask(bot, plain, conv); !errors.Is(err, ErrCardExpired) {
		t.Fatalf("unknown action = %v", err)
	}
	single := delivery
	single.ID, single.TargetType, single.TargetID, single.Status = "single-delivery", "single", "openid-target", "sent"
	if err := st.DB.Create(&single).Error; err != nil {
		t.Fatal(err)
	}
	singleTask := "single-task_nonce"
	if err := st.DB.Create(&domain.BotInteraction{
		ID: repo.BotMessageID(bot.ID, "notification:"+single.ID), BotID: bot.ID, CardTaskID: singleTask, Status: "replied",
	}).Error; err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"msgid": "single-action", "aibotid": bot.BotID, "chattype": "single",
		"from": map[string]string{"userid": "internal-user"}, "msgtype": "event",
		"event": map[string]string{"eventtype": "template_card_event", "event_key": "approve", "task_id": singleTask},
	})
	svc.receive(context.Background(), bot, Frame{Cmd: "aibot_event_callback", Body: body}, nil)
	if action, err := st.BotInteraction(repo.BotMessageID(bot.BotID, "single-action")); err != nil || action.Action != "approve" {
		t.Fatalf("single action = %#v, %v", action, err)
	}
	svc.receive(context.Background(), bot, Frame{Cmd: "aibot_event_callback", Body: body}, nil)
	svc.receive(context.Background(), bot, Frame{Cmd: "aibot_event_callback", Body: json.RawMessage(`{`)}, nil)
}

func TestActionsForDeliveryBranches(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	base := domain.Run{
		ID: "cvs-actions", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n", Type: "script", RejectTo: "fix"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 2, ReworkCount: 1}},
		CreatedAt:        time.Now(),
	}
	item := domain.NotificationDelivery{RunID: base.ID, NodeID: "n", Attempt: 2, ReworkCount: 1, EventType: "waiting"}
	cases := []struct {
		name     string
		activity string
		status   string
		event    string
		min      int
	}{
		{"pause", "用户暂停", "waiting_approval", "waiting", 1},
		{"turns", "轮次用尽", "waiting_approval", "waiting", 2},
		{"approval", "review", "waiting_approval", "waiting", 2},
		{"failed", "", "failed", "failed", 2},
	}
	for _, tc := range cases {
		run := base
		node := *base.Nodes["n"]
		node.Activity, node.Status = tc.activity, tc.status
		run.Nodes = map[string]*domain.NodeRun{"n": &node}
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
		item.EventType = tc.event
		if actions := ActionsForDelivery(st, item); len(actions) < tc.min {
			t.Fatalf("%s actions = %#v", tc.name, actions)
		}
	}
	terminal := base
	terminal.Status = "succeeded"
	if err := st.PutRun(terminal); err != nil {
		t.Fatal(err)
	}
	item.EventType = "waiting"
	if actions := ActionsForDelivery(st, item); actions != nil {
		t.Fatalf("terminal actions = %#v", actions)
	}
	other := base
	otherNode := *base.Nodes["n"]
	otherNode.Status = "running"
	other.Nodes = map[string]*domain.NodeRun{"n": &otherNode}
	if err := st.PutRun(other); err != nil {
		t.Fatal(err)
	}
	item.EventType = ""
	if actions := ActionsForDelivery(st, item); actions != nil {
		t.Fatalf("unsupported status actions = %#v", actions)
	}
	item.Attempt = 9
	if actions := ActionsForDelivery(st, item); actions != nil {
		t.Fatalf("stale actions = %#v", actions)
	}
	if actions := ActionsForDelivery(st, domain.NotificationDelivery{RunID: "missing"}); actions != nil {
		t.Fatalf("missing actions = %#v", actions)
	}
}

func TestCardTextActionsAndLabels(t *testing.T) {
	if actionLabel(" approve ") != "通过并继续" || actionLabel("unknown") != "" {
		t.Fatal("labels")
	}
	if got := cardText(" **hello**  `world` ", 8); !strings.HasSuffix(got, "…") || strings.Contains(got, "*") {
		t.Fatalf("text = %q", got)
	}
	buttons, menu := notificationCardActions([]map[string]any{{"key": "approve", "style": 1}, {"key": "custom", "text": "Custom"}})
	if len(buttons) != 2 || menu != nil || buttons[0]["text"] != "通过并继续" {
		t.Fatalf("actions = %#v, %#v", buttons, menu)
	}
	if handledCard("id", "done")["task_id"] != "id" {
		t.Fatal("handled card")
	}
	shortRun := domain.Run{Nodes: map[string]*domain.NodeRun{"n": {Error: "short"}}}
	if got := notificationCardDetails(shortRun, domain.NotificationDelivery{NodeID: "n"}, ""); got != "" {
		t.Fatalf("short details = %q", got)
	}
	if got := notificationCardDetails(shortRun, domain.NotificationDelivery{NodeID: "missing"}, ""); got != "" {
		t.Fatalf("missing details = %q", got)
	}
}
