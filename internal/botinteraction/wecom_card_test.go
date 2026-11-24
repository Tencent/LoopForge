package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func wecomTestBot(t *testing.T, store *repo.Store) domain.NotificationBot {
	t.Helper()
	store.PutUser(domain.User{ID: 42, LoginName: "owner", ChineseName: "负责人"})
	store.PutUser(domain.User{ID: 43, LoginName: "member", ChineseName: "成员"})
	store.PutProject(domain.Project{ID: "p", OwnerID: 42, Members: []domain.ProjectMember{{UserID: 43, Role: "member"}}})
	bot := domain.NotificationBot{ID: "cfg-wecom-card", BotID: "wecom-bot", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true}
	if err := store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = store.NotificationBot(bot.ID)
	bindClarifyTestUser(t, store, bot, "zhangsan", 43)
	bindClarifyTestUser(t, store, bot, "lisi", 42)
	return bot
}

// The WeCom confirmation card: drop-downs for the repository, the SOP and
// the operation in one multiple_interaction card (at most ten options, the
// default and the current pick kept); without drop-downs a button card.
func TestWeComDraftCardRendering(t *testing.T) {
	sop := &PushCardSelect{Key: DraftActionSOP, Label: "SOP", Initial: "wf-12"}
	sop.Options = append(sop.Options, PushCardOption{Value: domain.BotDraftDefaultSOP, Text: "项目默认 SOP（研发交付）"})
	for i := 1; i <= 14; i++ {
		sop.Options = append(sop.Options, PushCardOption{Value: fmt.Sprintf("wf-%d", i), Text: fmt.Sprintf("SOP %d", i)})
	}
	repoSel := &PushCardSelect{Key: DraftActionRepo, Label: "仓库", Initial: domain.BotDraftDefaultSOP, Options: []PushCardOption{{Value: domain.BotDraftDefaultSOP, Text: "项目默认仓库（main）"}, {Value: "repo-web", Text: "web"}}}
	card := &PushCard{DraftID: "d1", Tag: "待你确认", Title: "1 项操作", Selects: []*PushCardSelect{repoSel, sop}, Actions: []PushCardAction{{Key: DraftActionConfirm, Text: "确认执行"}, {Key: DraftActionConfirmStart, Text: "确认并启动 SOP"}, {Key: DraftActionCancel, Text: "取消", Danger: true}}, DetailsURL: "https://platform.example/bot-approve.html#draft=d1"}
	task := wecomCardTaskID(card)
	if !strings.HasPrefix(task, "d1_") || len(task) > 128 {
		t.Fatalf("task = %q", task)
	}
	out := wecomCard(card, task)
	if out["card_type"] != "multiple_interaction" || out["task_id"] != task {
		t.Fatalf("card = %v", out)
	}
	selects := out["select_list"].([]map[string]any)
	if len(selects) != 3 || selects[0]["question_key"] != DraftActionRepo || selects[1]["question_key"] != DraftActionSOP || selects[2]["question_key"] != draftOpKey || selects[2]["selected_id"] != DraftActionConfirm {
		t.Fatalf("selects = %v", selects)
	}
	options := selects[1]["option_list"].([]map[string]string)
	if len(options) != wecomSelectLimit || options[0]["id"] != domain.BotDraftDefaultSOP || options[1]["id"] != "wf-12" {
		t.Fatalf("sop options = %v", options)
	}
	if !strings.Contains(out["main_title"].(map[string]string)["desc"], "在平台确认") {
		t.Fatalf("truncation not mentioned: %v", out["main_title"])
	}
	if ops := selects[2]["option_list"].([]map[string]string); len(ops) != 3 || ops[2]["text"] != "取消，不执行" {
		t.Fatalf("operations = %v", ops)
	}
	// Handled: locked drop-downs and a greyed button.
	done := *card
	done.Done("成员 已提交")
	locked := wecomCard(&done, task)
	if locked["replace_text"] == nil || locked["select_list"].([]map[string]any)[0]["disable"] != true {
		t.Fatalf("locked = %v", locked)
	}
	// No drop-downs: buttons and the platform link.
	plain := &PushCard{DraftID: "d2", Tag: "待你确认", Title: "1 项操作", Body: "我会删除这个需求。", Rows: [][2]string{{"操作", "删除需求：旧需求"}}, Actions: card.Actions, DetailsURL: card.DetailsURL, DetailsLabel: "在平台确认"}
	buttons := wecomCard(plain, "d2_x")
	if buttons["card_type"] != "button_interaction" || len(buttons["button_list"].([]map[string]any)) != 3 || buttons["jump_list"].([]map[string]any)[0]["title"] != "在平台确认" {
		t.Fatalf("button card = %v", buttons)
	}
}

func wecomCardFrame(t *testing.T, id, sender, chatType, chatID, key, task string, selected map[string]string) Frame {
	t.Helper()
	items := []map[string]any{}
	for k, v := range selected {
		items = append(items, map[string]any{"question_key": k, "option_ids": map[string]any{"option_id": []string{v}}})
	}
	body := map[string]any{"msgid": id, "aibotid": "wecom-bot", "chattype": chatType, "from": map[string]string{"userid": sender}, "msgtype": "event", "event": map[string]any{"eventtype": "template_card_event", "template_card_event": map[string]any{"card_type": "multiple_interaction", "event_key": key, "task_id": task, "selected_items": map[string]any{"selected_item": items}}}}
	if chatID != "" {
		body["chatid"] = chatID
	}
	raw, _ := json.Marshal(body)
	f := Frame{Cmd: "aibot_event_callback", Body: raw}
	f.Headers.RequestID = "req-" + id
	return f
}

// A submitted WeCom confirmation card stores the picked repository and SOP
// and queues the operation in the conversation of the request; only the
// requester's submission counts.
func TestWeComDraftCardSubmit(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := wecomTestBot(t, store)
	origin := domain.BotInteraction{ID: "ask-wecom", BotID: bot.ID, SenderID: "zhangsan", Content: "建个需求"}
	store.EnqueueBotInteraction(origin, "group", "wr_group")
	sop := &domain.BotDraftSOP{DefaultName: "研发交付", Workflows: []domain.BotDraftSOPOption{{ID: "wf-fix", Name: "缺陷修复"}}, DefaultRepositoryName: "main", Repositories: []domain.BotDraftSOPOption{{ID: "repo-main", Name: "main"}, {ID: "repo-web", Name: "web"}}}
	now := time.Now().UTC()
	draft, err := store.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, SenderID: "zhangsan", UserID: 43, InteractionID: origin.ID, Summary: "1. 保存需求：登录优化（仓库：项目默认 · main）（SOP：项目默认 · 研发交付）", Drafts: "[]", SOP: sop}, now)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	picks := map[string]string{DraftActionRepo: "repo-web", DraftActionSOP: "wf-fix", draftOpKey: DraftActionConfirmStart}
	// Someone else's submission is refused and changes nothing.
	svc.receive(context.Background(), bot, wecomCardFrame(t, "c1", "lisi", "group", "wr_group", DraftActionSubmit, draft.ID+"_n1", picks), &socket{})
	if stored, _ := store.BotPendingDraft(draft.ID, now); stored.RepoChoice != "" || stored.Choice != "" || stored.Status != repo.BotDraftPending {
		t.Fatalf("other submission applied: %+v", stored)
	}
	svc.receive(context.Background(), bot, wecomCardFrame(t, "c2", "zhangsan", "group", "wr_group", DraftActionSubmit, draft.ID+"_n1", picks), &socket{})
	stored, _ := store.BotPendingDraft(draft.ID, now)
	if stored.RepoChoice != "repo-web" || stored.Choice != "wf-fix" {
		t.Fatalf("selections = %+v", stored)
	}
	item, err := store.BotInteraction(repo.BotMessageID(bot.IdentityScope(), "c2"))
	if err != nil {
		t.Fatal(err)
	}
	conv, _ := store.BotConversation(item.ConversationID)
	if item.Action != DraftActionConfirmStart || item.DeliveryID != draft.ID || item.SendRoute != "" || conv.ChatType != "group" || conv.ChatID != "wr_group" {
		t.Fatalf("queued = %+v in %+v", item, conv)
	}
	// A foreign repository in the submission is refused.
	other, _ := store.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, SenderID: "zhangsan", UserID: 43, InteractionID: origin.ID, Summary: "1. x", Drafts: "[]", SOP: sop}, now)
	svc.receive(context.Background(), bot, wecomCardFrame(t, "c3", "zhangsan", "group", "wr_group", DraftActionSubmit, other.ID+"_n2", map[string]string{DraftActionRepo: "repo-foreign", draftOpKey: DraftActionConfirm}), &socket{})
	if _, err := store.BotInteraction(repo.BotMessageID(bot.IdentityScope(), "c3")); err == nil {
		t.Fatal("submission with a foreign repository queued")
	}
	// An operation outside the confirmation set is dropped.
	if _, ok := wecomEvent(bot, wecomCardFrame(t, "c4", "zhangsan", "group", "wr_group", DraftActionSubmit, other.ID+"_n2", map[string]string{draftOpKey: "delete_everything"})); ok {
		t.Fatal("unknown operation accepted")
	}
}

// WeCom clarification card clicks carry the session and the button
// reference in the task ID.
func TestWeComClarifyCardClick(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := wecomTestBot(t, store)
	store.EnqueueBotInteraction(domain.BotInteraction{ID: "start", BotID: bot.ID, SenderID: "lisi", Content: "澄清 LOOP-7"}, "group", "wr_group")
	start, _ := store.BotInteraction("start")
	sess, err := store.CreateBotClarifySession(domain.BotClarifySession{ConfigID: bot.ID, ProjectID: "p", ConversationID: start.ConversationID, ChatType: "group", ChatID: "wr_group", IssueID: "iss", StarterSender: "lisi", StarterUserID: 42, Status: domain.BotClarifyAsking, Questions: []domain.BotClarifyQuestion{{N: 1, Question: "验收？"}}})
	if err != nil {
		t.Fatal(err)
	}
	post := domain.BotInteraction{ID: "post-q", BotID: bot.ID, SenderID: "lisi", Action: ActionClarifyPost, DeliveryID: sess.ID, Quote: ClarifyPostQuestions, Content: "LOOP-7 · 第 1 轮澄清\nQ1 验收？", Reply: "LOOP-7 · 第 1 轮澄清\nQ1 验收？"}
	store.EnqueueBotInteraction(post, "group", "wr_group")
	stored, _ := store.BotInteraction(post.ID)
	card := clarifyPostCard(stored, stored.Reply)
	task := wecomCardTaskID(card)
	if !strings.HasPrefix(task, sess.ID+"_post-q_") || len(task) > 128 {
		t.Fatalf("task = %q", task)
	}
	rendered := wecomCard(card, task)
	if rendered["card_type"] != "button_interaction" || len(rendered["button_list"].([]map[string]any)) != 2 {
		t.Fatalf("card = %v", rendered)
	}
	ev, ok := wecomEvent(bot, wecomCardFrame(t, "k1", "zhangsan", "group", "wr_group", ClarifyActionNext, task, nil))
	if !ok || ev.DeliveryID != sess.ID || ev.Option != "post-q" || ev.Action != ClarifyActionNext {
		t.Fatalf("event = %+v %v", ev, ok)
	}
	if _, ok := wecomEvent(bot, wecomCardFrame(t, "k0", "zhangsan", "group", "wr_group", ClarifyActionNext, sess.ID, nil)); ok {
		t.Fatal("clarification click without reference accepted")
	}
	svc := New(store)
	svc.receive(context.Background(), bot, wecomCardFrame(t, "k1", "zhangsan", "group", "wr_group", ClarifyActionNext, task, nil), &socket{})
	item, err := store.BotInteraction(repo.BotMessageID(bot.IdentityScope(), "k1"))
	if err != nil {
		t.Fatal(err)
	}
	conv, _ := store.BotConversation(item.ConversationID)
	if item.Action != ClarifyActionNext || item.Quote != "post-q" || item.SendRoute != repo.BotSendRoutePushChat || conv.ChatID != "wr_group" {
		t.Fatalf("queued = %+v in %+v", item, conv)
	}
	// A plain member cannot end the clarification.
	svc.receive(context.Background(), bot, wecomCardFrame(t, "k2", "zhangsan", "group", "wr_group", ClarifyActionEnd, task, nil), &socket{})
	if _, err := store.BotInteraction(repo.BotMessageID(bot.IdentityScope(), "k2")); err == nil {
		t.Fatal("member's end queued")
	}
}

// End to end over a WeCom long connection: a reply with a pending draft is
// followed by the confirmation card; submitting it updates the card within
// the callback (locked, with who submitted) and the result is sent into the
// conversation.
func TestWeComDraftCardOverTheConnection(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wecomTestBot(t, store)
	frames := make(chan Frame, 20)
	events := make(chan map[string]any, 4)
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		var writeMu sync.Mutex
		write := func(v any) {
			writeMu.Lock()
			defer writeMu.Unlock()
			_ = ws.WriteJSON(v)
		}
		go func() {
			for ev := range events {
				write(ev)
			}
		}()
		for {
			var f Frame
			if ws.ReadJSON(&f) != nil {
				return
			}
			write(map[string]any{"headers": f.Headers, "errcode": 0})
			if f.Cmd == "aibot_subscribe" {
				write(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "ask"}, "body": map[string]any{"msgid": "m-ask", "aibotid": "wecom-bot", "chattype": "group", "chatid": "wr_group", "from": map[string]string{"userid": "zhangsan"}, "msgtype": "text", "text": map[string]string{"content": "建个需求"}}})
			}
			if f.Cmd != "aibot_subscribe" && f.Cmd != "ping" {
				frames <- f
			}
		}
	}))
	defer srv.Close()
	svc := New(store)
	svc.URL = "ws" + strings.TrimPrefix(srv.URL, "http")
	svc.PublicURL = func() string { return "https://platform.example" }
	draftID := make(chan string, 1)
	svc.Generate = func(ctx context.Context, b domain.NotificationBot, conv domain.BotConversation, _ []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		sop := &domain.BotDraftSOP{DefaultName: "研发交付", Workflows: []domain.BotDraftSOPOption{{ID: "wf-fix", Name: "缺陷修复"}}}
		d, err := store.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: b.ID, SenderID: item.SenderID, UserID: 43, InteractionID: item.ID, Summary: "1. 保存需求：登录优化（SOP：项目默认 · 研发交付）", Drafts: "[]", SOP: sop}, time.Now().UTC())
		draftID <- d.ID
		return "我会创建这个需求。", err
	}
	svc.Action = func(ctx context.Context, b domain.NotificationBot, item domain.BotInteraction, conv domain.BotConversation) (string, error) {
		return "操作已执行：" + item.Action + " 在 " + conv.ChatID, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	defer func() { cancel(); close(events); <-svc.Done }()
	next := func(cmd string) Frame {
		deadline := time.After(12 * time.Second)
		for {
			select {
			case f := <-frames:
				if f.Cmd == cmd {
					return f
				}
			case <-deadline:
				t.Fatalf("no %s frame", cmd)
				return Frame{}
			}
		}
	}
	cardFrame := next("aibot_send_msg")
	var sent struct {
		ChatID       string         `json:"chatid"`
		MsgType      string         `json:"msgtype"`
		TemplateCard map[string]any `json:"template_card"`
	}
	_ = json.Unmarshal(cardFrame.Body, &sent)
	id := <-draftID
	task, _ := sent.TemplateCard["task_id"].(string)
	if sent.ChatID != "wr_group" || sent.MsgType != "template_card" || sent.TemplateCard["card_type"] != "multiple_interaction" || !strings.HasPrefix(task, id+"_") {
		t.Fatalf("card = %s", cardFrame.Body)
	}
	events <- map[string]any{"cmd": "aibot_event_callback", "headers": map[string]string{"req_id": "click"}, "body": map[string]any{"msgid": "m-click", "aibotid": "wecom-bot", "chattype": "group", "chatid": "wr_group", "from": map[string]string{"userid": "zhangsan"}, "msgtype": "event", "event": map[string]any{"eventtype": "template_card_event", "template_card_event": map[string]any{"card_type": "multiple_interaction", "event_key": DraftActionSubmit, "task_id": task, "selected_items": map[string]any{"selected_item": []any{map[string]any{"question_key": DraftActionSOP, "option_ids": map[string]any{"option_id": []string{"wf-fix"}}}, map[string]any{"question_key": draftOpKey, "option_ids": map[string]any{"option_id": []string{DraftActionConfirm}}}}}}}}}
	update := next("aibot_respond_update_msg")
	if update.Headers.RequestID != "click" || !strings.Contains(string(update.Body), `"replace_text"`) || !strings.Contains(string(update.Body), "已提交「确认执行」") || !strings.Contains(string(update.Body), `"disable":true`) {
		t.Fatalf("update = %s", update.Body)
	}
	result := next("aibot_send_msg")
	if !strings.Contains(string(result.Body), "操作已执行：draft_confirm 在 wr_group") {
		t.Fatalf("result = %s", result.Body)
	}
	if stored, _ := store.BotPendingDraft(id, time.Now().UTC()); stored.Choice != "wf-fix" {
		t.Fatalf("choice = %+v", stored)
	}
}
