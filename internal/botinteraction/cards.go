package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrCardExpired = errors.New("该节点已不再等待本次操作")

func ActionsForDelivery(store *repo.Store, item domain.NotificationDelivery) []map[string]any {
	run, ok := store.RunState(item.RunID)
	if !ok {
		return nil
	}
	node := run.Nodes[item.NodeID]
	if node == nil || node.Attempt != item.Attempt || node.ReworkCount != item.ReworkCount || (item.EventType == "waiting" && node.Status != "waiting_approval") || (item.EventType == "failed" && node.Status != "failed") {
		return nil
	}
	actions := []map[string]any{}
	button := func(text, key string) {
		style := 1
		if key == "pass" || key == "decline" || key == "reject" || (key == "retry" && node.Status == "waiting_approval") {
			style = 4
		}
		actions = append(actions, map[string]any{"text": text, "key": key, "style": style})
	}
	switch node.Status {
	case "waiting_approval":
		if domain.TerminalRunStatus(run.Status) {
			return nil
		}
		if strings.Contains(node.Activity, "用户暂停") {
			button("继续", "resume")
		} else if strings.Contains(node.Activity, "轮次用尽") || strings.Contains(node.Activity, "是否重试") || strings.Contains(strings.ToLower(node.Error), "max turns") {
			button("重试", "retry")
			button("不重试", "decline")
		} else {
			button("通过", "approve")
			if run.Status != "running" {
				button("重试", "retry")
			}
			for _, n := range run.WorkflowSnapshot.Nodes {
				if n.ID == item.NodeID && n.RejectTo != "" {
					button("退回", "reject")
				}
			}
		}
	case "failed":
		button("重试", "retry")
		for _, n := range run.WorkflowSnapshot.Nodes {
			if n.ID == item.NodeID && n.Type == "script" {
				button("手动通过", "pass")
			}
		}
	default:
		return nil
	}
	return actions
}
func handledCard(id, text string) map[string]any {
	return map[string]any{"card_type": "button_interaction", "task_id": id, "main_title": map[string]string{"title": text}, "button_list": []map[string]any{{"text": "已收到", "key": "handled", "style": 3}}}
}
func (s *Service) QueueNotificationCard(ctx context.Context, bot domain.NotificationBot, item domain.NotificationDelivery, content string, detailsURL ...string) error {
	actions := ActionsForDelivery(s.Store, item)
	if len(actions) == 0 {
		return ErrCardExpired
	}
	run, ok := s.Store.RunState(item.RunID)
	if !ok {
		return ErrCardExpired
	}
	card := notificationCard(run, item, actions, content)
	if detail := notificationCardDetails(run, item, content, detailsURL...); detail != "" {
		_, err := s.Store.EnqueueBotInteraction(domain.BotInteraction{ID: repo.BotMessageID(bot.ID, "notification-details:"+item.ID), BotID: bot.ID, Action: "notification_details", DeliveryID: item.ID, Content: detail}, item.TargetType, item.TargetID)
		if err != nil {
			return err
		}
	}
	if len(detailsURL) > 0 && detailsURL[0] != "" {
		card["jump_list"] = []map[string]any{{"type": 1, "title": "查看详情", "url": detailsURL[0]}}
	}
	data, _ := json.Marshal(card)
	id := repo.BotMessageID(bot.ID, "notification:"+item.ID)
	_, err := s.Store.EnqueueBotInteraction(domain.BotInteraction{ID: id, BotID: bot.ID, Action: "notification", DeliveryID: item.ID, Content: content, Quote: string(data)}, item.TargetType, item.TargetID)
	if err != nil {
		return err
	}
	if err = s.Store.RequeueBotCard(id); err != nil {
		return err
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := s.Store.BotInteraction(id)
		if err != nil {
			return err
		}
		if record.Status == "reply_failed" {
			return errors.New("企业微信卡片发送失败，请检查会话和 Bot 授权")
		}
		if record.Status == "replied" {
			if record.LastError != "" {
				return ErrCardExpired
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (s *Service) NotificationCardTask(bot domain.NotificationBot, item domain.BotInteraction, conv domain.BotConversation) (domain.NotificationDelivery, error) {
	delivery, err := s.Store.NotificationDelivery(item.DeliveryID)
	if err != nil || delivery.BotID != bot.ID || delivery.ProjectID != bot.ProjectID {
		return delivery, ErrCardExpired
	}
	if item.CardTaskID != "" {
		card, cardErr := s.Store.BotInteraction(repo.BotMessageID(bot.ID, "notification:"+delivery.ID))
		if cardErr != nil || card.CardTaskID != item.CardTaskID {
			return delivery, ErrCardExpired
		}
	}
	if delivery.TargetID != conv.ChatID || delivery.TargetType != conv.ChatType || time.Since(delivery.CreatedAt) > 24*time.Hour {
		return delivery, ErrCardExpired
	}
	if delivery.Status != "sent" && delivery.Status != "sending" && delivery.Status != "pending" {
		return delivery, ErrCardExpired
	}
	allowed := ActionsForDelivery(s.Store, delivery)
	for _, a := range allowed {
		if a["key"] == item.Action {
			return delivery, nil
		}
	}
	return delivery, ErrCardExpired
}
func actionLabel(action string) string {
	return map[string]string{"approve": "通过并继续", "retry": "重试节点", "pass": "手动通过"}[strings.TrimSpace(action)]
}

// Card text fields are plain text. Keep the reason visible above context rows,
// instead of embedding a long Markdown notification in the clipped subtitle.
func notificationCard(run domain.Run, item domain.NotificationDelivery, actions []map[string]any, content string) map[string]any {
	reason, required := "", ""
	node := run.Nodes[item.NodeID]
	if node != nil {
		if node.GateResult != nil {
			reason = node.GateResult.Summary
			if strings.TrimSpace(reason) == "" {
				reason = node.GateResult.Reason
			}
			required = node.GateResult.ActionRequired
		}
		if strings.TrimSpace(reason) == "" {
			reason = node.Error
		}
	}
	if strings.TrimSpace(reason) == "" {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "需求：") || strings.HasPrefix(line, "SOP：") || strings.HasPrefix(line, "节点：") || strings.HasPrefix(line, "查看详情：") {
				continue
			}
			reason = line
			break
		}
	}
	if strings.TrimSpace(reason) == "" {
		if item.EventType == "waiting" {
			reason = "流程在此设置了人工确认，请确认后继续。"
		} else {
			reason = "节点执行失败，请查看运行详情。"
		}
	}
	body := "原因：" + cardText(reason, 76)
	if required != "" {
		body += "\n待处理：" + cardText(required, 27)
	}
	issue := notificationCardIssue(content)
	title := issue
	if title == "未关联需求" {
		title = run.WorkflowSnapshot.Name
	}
	rows := []map[string]string{
		{"keyname": "需求", "value": cardText(issue, 26)},
		{"keyname": "节点", "value": cardText(item.SourceName, 26)},
		{"keyname": "SOP", "value": cardText(run.WorkflowSnapshot.Name, 26)},
	}
	for _, action := range actions {
		if action["key"] == "reject" {
			if target := cardReworkTarget(run, item.NodeID); target != "" {
				rows = append(rows, map[string]string{"keyname": "返工到", "value": cardText(target, 26)})
			}
			break
		}
	}
	buttons, menu := notificationCardActions(actions)
	card := map[string]any{"card_type": "button_interaction", "task_id": item.ID,
		"source":         map[string]any{"desc": map[string]string{"waiting": "待人工确认", "failed": "执行失败"}[item.EventType], "desc_color": 2},
		"main_title":     map[string]string{"title": cardText(title, 26), "desc": "节点：" + cardText(item.SourceName, 26)},
		"sub_title_text": body, "horizontal_content_list": rows, "button_list": buttons}
	if menu != nil {
		card["action_menu"] = menu
	}
	return card
}
func notificationCardIssue(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if issue, ok := strings.CutPrefix(strings.TrimSpace(line), "需求："); ok && strings.TrimSpace(issue) != "" {
			return strings.TrimSpace(issue)
		}
	}
	return "未关联需求"
}

// Keep long evidence intact in a companion Markdown message; the enterprise
// card fields are designed for short summaries and do not support expansion.
func notificationCardDetails(run domain.Run, item domain.NotificationDelivery, content string, detailsURL ...string) string {
	node := run.Nodes[item.NodeID]
	if node == nil {
		return ""
	}
	reason, required := node.Error, ""
	findings := []domain.GateFinding{}
	if node.GateResult != nil {
		if strings.TrimSpace(node.GateResult.Reason) != "" {
			reason = node.GateResult.Reason
		}
		required = node.GateResult.ActionRequired
		findings = node.GateResult.Findings
	}
	if len([]rune(reason)) <= 76 && len([]rune(required)) <= 28 && len(findings) == 0 {
		return ""
	}
	text := "### " + notificationCardIssue(content) + "\n节点：" + item.SourceName + "\nSOP：" + run.WorkflowSnapshot.Name + "\n\n**原因**\n" + reason
	if required != "" {
		text += "\n\n**需要处理**\n" + required
	}
	for _, finding := range findings {
		text += "\n\n- " + finding.Title + "\n" + finding.Evidence
	}
	text += "\n\n请使用随后发送的操作卡片处理。"
	footer := ""
	if len(detailsURL) > 0 {
		if link, err := url.Parse(detailsURL[0]); err == nil && (link.Scheme == "http" || link.Scheme == "https") && link.Host != "" {
			footer = "\n\n[查看运行详情](" + link.String() + ")"
		}
	}
	return domain.NotificationText(text, 16000-len(footer)) + footer
}

func cardText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.NewReplacer("**", "", "`", "").Replace(text)
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func (s *socket) updateActionCard(ctx context.Context, item domain.BotInteraction, result string, finish bool) error {
	state := s.streamState(item.ID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finished {
		return nil
	}
	card := handledCard(item.CardTaskID, "正在处理，请稍候")
	if finish {
		title := "操作未完成"
		if strings.Contains(result, "操作已完成") {
			title = "操作已完成"
		} else if strings.Contains(result, "过期") {
			title = "提醒已失效"
		}
		card["main_title"] = map[string]string{"title": title, "desc": cardText(result, 30)}
		card["sub_title_text"] = cardText(result, 112)
		card["button_list"] = []map[string]any{{"text": title, "key": "handled", "style": 4}}
	}
	err := s.request(ctx, "aibot_respond_update_msg", item.RequestID, map[string]any{"response_type": "update_template_card", "template_card": card})
	if err == nil {
		state.finished = finish
	}
	return err
}

// Keep every available operation visible. Three-column cards need short
// labels; the card's context row explicitly identifies the rework target.
func notificationCardActions(actions []map[string]any) ([]map[string]any, map[string]any) {
	labels := map[string]string{"approve": "通过并继续", "retry": "重新执行", "reject": "返工", "pass": "手动通过", "resume": "继续运行", "decline": "不再重试"}
	if len(actions) > 2 {
		labels = map[string]string{"approve": "通过", "retry": "重试", "reject": "返工", "pass": "放行", "resume": "继续", "decline": "不重试"}
	}
	buttons := []map[string]any{}
	for _, action := range actions {
		key, _ := action["key"].(string)
		text := labels[key]
		if text == "" {
			text, _ = action["text"].(string)
		}
		button := map[string]any{}
		for k, v := range action {
			button[k] = v
		}
		button["text"] = text
		buttons = append(buttons, button)
	}
	return buttons, nil
}
func cardReworkTarget(run domain.Run, nodeID string) string {
	target := ""
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID == nodeID {
			target = node.RejectTo
			break
		}
	}
	if target != "" {
		for _, node := range run.WorkflowSnapshot.Nodes {
			if node.ID == target {
				return node.Name
			}
		}
	}
	return ""
}
