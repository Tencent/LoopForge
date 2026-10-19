package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNotificationCardShowsStructuredReasonAndRequiredAction(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Name: "验收 SOP"}, Nodes: map[string]*domain.NodeRun{"review": {Error: "generic runner failed", GateResult: &domain.GateResult{Reason: "缺少回滚方案，无法自动批准", ActionRequired: "请负责人补充回滚方案"}}}}
	item := domain.NotificationDelivery{ID: "delivery", NodeID: "review", SourceName: "方案审查", EventType: "waiting"}
	card := notificationCard(run, item, nil, "### 需要人工处理\n需求：未关联需求\nSOP：验收 SOP\n节点：方案审查\ngeneric runner failed")
	body := card["sub_title_text"].(string)
	if !strings.Contains(body, "缺少回滚方案") || !strings.Contains(body, "请负责人补充回滚方案") || strings.Contains(body, "generic") {
		t.Fatal(body)
	}
	if card["main_title"].(map[string]string)["desc"] != "节点：方案审查" {
		t.Fatal(card)
	}
	if len(card["horizontal_content_list"].([]map[string]string)) != 3 {
		t.Fatal(card)
	}
}
func TestNotificationCardFailureAndLengthLimits(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Name: "模拟失败"}, Nodes: map[string]*domain.NodeRun{"fail": {Error: "Run: exit status 1"}}}
	item := domain.NotificationDelivery{NodeID: "fail", EventType: "failed"}
	card := notificationCard(run, item, nil, "### 节点失败")
	if !strings.Contains(card["sub_title_text"].(string), "exit status 1") {
		t.Fatal(card)
	}
	run.Nodes["fail"].GateResult = &domain.GateResult{Reason: strings.Repeat("原因", 100), ActionRequired: strings.Repeat("补充", 100)}
	body := notificationCard(run, item, nil, "")["sub_title_text"].(string)
	if len([]rune(body)) > 112 || !strings.Contains(body, "待处理：") {
		t.Fatal(body)
	}
}

func TestLongCardKeepsContextAndEvidenceInCompanionMessage(t *testing.T) {
	reason := strings.Repeat("回滚验证未完成，当前不能自动批准。\n", 35)
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Name: "后端交付 SOP"}, Nodes: map[string]*domain.NodeRun{"review": {GateResult: &domain.GateResult{Summary: "缺少回滚验证，需要负责人确认", Reason: reason, ActionRequired: "补充回滚演练结果", Findings: []domain.GateFinding{{Title: "回滚测试缺失", Evidence: "未找到 rollback-test.log"}}}}}}
	item := domain.NotificationDelivery{NodeID: "review", SourceName: "上线风险审查", EventType: "waiting"}
	content := "需求：DNS 消息去重与可靠投递优化"
	card := notificationCard(run, item, nil, content)
	if !strings.Contains(card["main_title"].(map[string]string)["title"], "DNS") || !strings.Contains(card["sub_title_text"].(string), "负责人确认") {
		t.Fatal(card)
	}
	detail := notificationCardDetails(run, item, content)
	if !strings.Contains(detail, reason) || !strings.Contains(detail, "rollback-test.log") || !strings.Contains(detail, "上线风险审查") {
		t.Fatal(detail)
	}
}

func TestLongDetailsPreserveRunLinkEvenAtMessageLimit(t *testing.T) {
	run := domain.Run{Nodes: map[string]*domain.NodeRun{"fail": {Error: strings.Repeat("完整失败原因。", 4000)}}}
	link := "https://loopforge-test.example.com/#/runs/run-test"
	text := notificationCardDetails(run, domain.NotificationDelivery{NodeID: "fail"}, "需求：模拟验收", link)
	if !strings.HasSuffix(text, "[查看运行详情]("+link+")") || len(text) > 16000 {
		t.Fatalf("missing or oversized link footer: %d", len(text))
	}
	if strings.Contains(notificationCardDetails(run, domain.NotificationDelivery{NodeID: "fail"}, "", "javascript:bad"), "javascript:") {
		t.Fatal("unsafe link")
	}
}

func TestNormalizeRealNestedTemplateCardCallback(t *testing.T) {
	body := json.RawMessage(`{"msgid":"click-real","aibotid":"external","chattype":"single","from":{"userid":"user"},"msgtype":"event","event":{"eventtype":"template_card_event","template_card_event":{"event_key":"retry","task_id":"delivery_abcdef123456"}}}`)
	f := Frame{Cmd: "aibot_event_callback", Body: body}
	f.Headers.RequestID = "original-request"
	item, kind, chat, ok := normalizeIncoming(domain.NotificationBot{ID: "bot", BotID: "external"}, f)
	if !ok || item.Action != "retry" || item.DeliveryID != "delivery" || item.CardTaskID != "delivery_abcdef123456" || kind != "single" || chat != "user" {
		t.Fatalf("%+v %s %s %t", item, kind, chat, ok)
	}
}

func TestPrivateCardCallbackResolvesExactTaskToFrozenRecipient(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "bot", BotID: "external", ProjectID: "project"}
	delivery := domain.NotificationDelivery{ID: "delivery", BotID: bot.ID, ProjectID: bot.ProjectID, TargetID: "openid-recipient", TargetType: "single"}
	if err = store.DB.Create(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	card := domain.BotInteraction{ID: repo.BotMessageID(bot.ID, "notification:"+delivery.ID), BotID: bot.ID, Action: "notification", CardTaskID: "delivery_randomnonce", Status: "replied"}
	if err = store.DB.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	service := New(store)
	for _, test := range []struct{ id, task string }{{"valid", "delivery_randomnonce"}, {"wrong", "delivery_wrongnonce"}} {
		body, _ := json.Marshal(map[string]any{"msgid": test.id, "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "internal-userid"}, "msgtype": "event", "event": map[string]any{"eventtype": "template_card_event", "template_card_event": map[string]string{"event_key": "retry", "task_id": test.task}}})
		f := Frame{Cmd: "aibot_event_callback", Body: body}
		f.Headers.RequestID = test.id
		service.receive(context.Background(), bot, f, &socket{})
		record, err := store.BotInteraction(repo.BotMessageID(bot.BotID, test.id))
		if err != nil {
			t.Fatal(err)
		}
		conv, err := store.BotConversation(record.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		want := "internal-userid"
		if test.id == "valid" {
			want = "openid-recipient"
		}
		if conv.ChatID != want || record.SenderID != "internal-userid" {
			t.Fatalf("%s: %+v %+v", test.id, conv, record)
		}
	}
}

func TestCardActionLayoutAdaptsToOperationCount(t *testing.T) {
	all := []map[string]any{{"key": "approve", "text": "通过", "style": 1}, {"key": "retry", "text": "重试", "style": 4}, {"key": "reject", "text": "退回", "style": 4}}
	for count := 1; count <= 3; count++ {
		buttons, menu := notificationCardActions(all[:count])
		if len(buttons) != count || menu != nil {
			t.Fatalf("%v %v", buttons, menu)
		}
		if count == 3 {
			for _, button := range buttons {
				if button["key"] != "reject" && len([]rune(button["text"].(string))) > 2 {
					t.Fatal(button)
				}
			}
			if buttons[2]["text"] != "返工" || buttons[2]["key"] != "reject" {
				t.Fatal(buttons)
			}
		} else if buttons[0]["text"] != "通过并继续" {
			t.Fatal(buttons)
		}
		if all[0]["text"] != "通过" {
			t.Fatal("mutated actions")
		}
	}
}
func TestCardIdentifiesReworkDestination(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "prepare", Name: "方案准备"}, {ID: "review", RejectTo: "prepare"}}}}
	card := notificationCard(run, domain.NotificationDelivery{NodeID: "review"}, []map[string]any{{"key": "reject"}}, "")
	rows := card["horizontal_content_list"].([]map[string]string)
	if rows[len(rows)-1]["keyname"] != "返工到" || rows[len(rows)-1]["value"] != "方案准备" {
		t.Fatal(rows)
	}
}
