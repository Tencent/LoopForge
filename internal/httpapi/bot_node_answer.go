package httpapi

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
)

// Node questions in chats (Feishu, DingTalk, WeCom). A node that needs
// supplementary information submits a "human" decision with actionRequired
// and waits at waiting_approval; the message sent to the run's conversation
// carries that question. A chat message answers it when it quotes the
// message or starts with "回答：" ("回答 2：" picks one of several open
// questions). The answer retries the same node of the same run with the
// answer as human context, like "补充信息，重跑当前节点" on the platform.
//
// The node state is the one pending record shared with the platform page:
// once either side answered, the node ended or the run was cancelled, the
// node no longer waits at the notified attempt and the answer is refused.

// botNodeQuestionWindow bounds how far back open questions are looked up.
const botNodeQuestionWindow = 7 * 24 * time.Hour

var botNodeAnswerPrefix = regexp.MustCompile(`^(?:回答|答复)\s*(\d{1,2})?\s*[:：]\s*`)

// botNodeAnswerText splits "回答：…" / "回答 2：…" into the answer and the
// picked question number (0 when none).
func botNodeAnswerText(text string) (answer string, pick int, ok bool) {
	m := botNodeAnswerPrefix.FindStringSubmatchIndex(text)
	if m == nil {
		return "", 0, false
	}
	if m[2] >= 0 {
		pick, _ = strconv.Atoi(text[m[2]:m[3]])
	}
	return strings.TrimSpace(text[m[1]:]), pick, true
}

type botNodeQuestion struct {
	delivery domain.NotificationDelivery
	question string
}

// botDeliveryInChat reports whether a delivery was sent to this
// conversation: a group by its chat ID, a private chat by the chat or the
// sender (Feishu and DingTalk address private messages by user).
func botDeliveryInChat(d domain.NotificationDelivery, conv domain.BotConversation, sender string) bool {
	if d.TargetType != conv.ChatType || d.EventType != "waiting" || d.NodeID == "" {
		return false
	}
	if conv.ChatType == "group" {
		return d.TargetID == conv.ChatID
	}
	return d.TargetID == conv.ChatID || d.TargetID == sender
}

// botOpenNodeQuestions lists questions from this conversation whose nodes
// still wait at the notified attempt, oldest first.
func (h *assistantHandlers) botOpenNodeQuestions(bot domain.NotificationBot, conv domain.BotConversation, sender string) []botNodeQuestion {
	targets := []string{conv.ChatID}
	if conv.ChatType != "group" && sender != conv.ChatID {
		targets = append(targets, sender)
	}
	since := time.Now().UTC().Add(-botNodeQuestionWindow)
	var deliveries []domain.NotificationDelivery
	for _, target := range targets {
		items, err := h.repo.ChatWaitingDeliveries(bot.ID, conv.ChatType, target, since)
		if err != nil {
			log.Printf("bot %s: node questions of %s: %v", bot.ID, conv.ID, err)
			continue
		}
		deliveries = append(deliveries, items...)
	}
	seen := map[string]bool{}
	var open []botNodeQuestion
	for _, d := range deliveries {
		key := fmt.Sprintf("%s/%s/%d/%d", d.RunID, d.NodeID, d.Attempt, d.ReworkCount)
		if seen[key] {
			continue
		}
		seen[key] = true
		run, ok := h.repo.RunState(d.RunID)
		if !ok || run.ExecutionContext.ProjectID != bot.ProjectID {
			continue
		}
		if botNodeQuestionStale(run, d) != "" {
			continue
		}
		if question := run.Nodes[d.NodeID].Question(); question != "" {
			open = append(open, botNodeQuestion{delivery: d, question: question})
		}
	}
	return open
}

// botQuotedNodeQuestion accepts only a platform message ID or a WeCom
// delivery reference as the identity of a quoted question. Old WeCom
// notifications can still prompt for an explicit numbered answer.
func (h *assistantHandlers) botQuotedNodeQuestion(bot domain.NotificationBot, conv domain.BotConversation, item domain.BotInteraction) (d domain.NotificationDelivery, quoted, needsPick bool) {
	if item.ReplyTo != "" {
		d, ok := h.repo.NotificationDeliveryByPlatformMessage(bot.ID, item.ReplyTo)
		if !ok {
			post, found := h.repo.BotInteractionByPlatformMessage(bot.ID, item.ReplyTo)
			if !found || post.Action != "notification" || post.DeliveryID == "" {
				return d, false, false
			}
			var err error
			if d, err = h.repo.NotificationDelivery(post.DeliveryID); err != nil {
				return d, false, false
			}
		}
		return d, d.ProjectID == bot.ProjectID && botDeliveryInChat(d, conv, item.SenderID), false
	}
	quote := strings.TrimSpace(item.Quote)
	if quote == "" {
		return d, false, false
	}
	line, _, _ := strings.Cut(quote, "\n")
	if id, found := strings.CutPrefix(line, botinteraction.WeComNodeQuestionRefPrefix); found {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 64 {
			return d, false, true
		}
		d, err := h.repo.NotificationDelivery(id)
		if err != nil || d.BotID != bot.ID || d.ProjectID != bot.ProjectID || !botDeliveryInChat(d, conv, item.SenderID) {
			return d, false, true
		}
		switch d.Status {
		case "sending", "sent", "send_unknown":
			return d, true, false
		default:
			return d, false, true
		}
	}
	if strings.Contains(quote, domain.NodeAnswerHint(domain.BotChannelWeCom, conv.ChatType)) {
		return d, false, true
	}
	text := strings.TrimSpace(strings.TrimPrefix(quote, "问题："))
	for _, q := range h.botOpenNodeQuestions(bot, conv, item.SenderID) {
		prefix := []rune(q.question)
		if strings.HasPrefix(text, string(prefix[:min(len(prefix), 15)])) {
			return d, false, true
		}
	}
	return d, false, false
}

// botNodeAnswer handles a chat message that answers a node question.
// handled=false leaves the message to clarification and the assistant.
func (h *assistantHandlers) botNodeAnswer(ctx context.Context, bot domain.NotificationBot, conv domain.BotConversation, item domain.BotInteraction) (string, bool) {
	text := botClarifyText(item.Content)
	answer, pick, prefixed := botNodeAnswerText(text)
	delivery, quoted, needsPick := h.botQuotedNodeQuestion(bot, conv, item)
	if !quoted && !prefixed && !needsPick {
		return "", false
	}
	if quoted && !prefixed {
		answer = text
	}
	if !quoted {
		open := h.botOpenNodeQuestions(bot, conv, item.SenderID)
		switch {
		case len(open) == 0:
			return "这个会话没有待回答的节点问题，答案未提交。问题可能已经回答、节点已结束或运行已取消，请在平台查看运行状态。", true
		case pick > 0 && pick <= len(open):
			delivery = open[pick-1].delivery
		case prefixed && pick == 0 && strings.TrimSpace(item.Quote) == "" && len(open) == 1:
			delivery = open[0].delivery
		default:
			return h.botNodeQuestionList(open), true
		}
	}
	if answer == "" {
		return "请在「回答：」后写上答案。", true
	}
	return h.submitBotNodeAnswer(ctx, bot, conv, item, delivery, answer), true
}

func (h *assistantHandlers) botNodeQuestionList(open []botNodeQuestion) string {
	lines := []string{"这个会话有 " + strconv.Itoa(len(open)) + " 个待回答的节点问题，请发送「回答 序号：你的答案」："}
	for i, q := range open {
		lines = append(lines, fmt.Sprintf("%d. 「%s」：%s", i+1, q.delivery.SourceName, truncateRunes(q.question, 80)))
	}
	return strings.Join(lines, "\n")
}

// submitBotNodeAnswer retries the questioned node with the answer, as the
// sender's platform account, once the node still waits at the notified
// attempt.
func (h *assistantHandlers) submitBotNodeAnswer(ctx context.Context, bot domain.NotificationBot, conv domain.BotConversation, item domain.BotInteraction, d domain.NotificationDelivery, answer string) string {
	user, err := h.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return "答案未提交：" + err.Error() + "。"
	}
	run, ok := h.repo.RunState(d.RunID)
	if !ok || run.ExecutionContext.ProjectID != bot.ProjectID {
		return "运行已不存在，答案未提交。"
	}
	if stale := botNodeQuestionStale(run, d); stale != "" {
		return stale
	}
	nr := run.Nodes[d.NodeID]
	if nr.Question() == "" {
		return "「" + d.SourceName + "」在等待人工确认，没有需要回答的问题。请用卡片按钮或到平台处理。"
	}
	if issue, found := h.repo.Issue(run.ExecutionContext.IssueID); found && issue.ExecutionBlockedReason() != "" {
		return issue.ExecutionBlockedReason()
	}
	reason := botChannelName(bot) + "回答节点问题，平台账号：" + user.LoginName + "，发送者：" + item.SenderID
	expected := engine.NodeExpectation{NodeID: d.NodeID, Status: "waiting_approval", Attempt: d.Attempt, ReworkCount: d.ReworkCount}
	if err := h.engine.RetryContext(ctx, d.RunID, d.NodeID, reason, answer, expected); err != nil {
		if current, ok := h.repo.RunState(d.RunID); ok {
			if stale := botNodeQuestionStale(current, d); stale != "" {
				return stale
			}
		}
		log.Printf("bot %s: answer node %s of run %s: %v", bot.ID, d.NodeID, d.RunID, err)
		return "答案未提交，请在平台查看当前运行状态。"
	}
	done := "已把答案提交给「" + d.SourceName + "」，节点会带着答案继续执行。"
	if conv.ChatType == "group" {
		done += "回答人：" + botinteraction.DisplayName(user) + "。"
	}
	return done
}

// botNodeQuestionStale explains why a question can no longer be answered,
// or returns "" while its node still waits at the notified attempt.
func botNodeQuestionStale(run domain.Run, d domain.NotificationDelivery) string {
	if run.Status == "cancelled" || run.CancelRequested {
		return "运行已取消，答案未提交。"
	}
	if domain.TerminalRunStatus(run.Status) {
		return "运行已结束，答案未提交。"
	}
	nr := run.Nodes[d.NodeID]
	if nr == nil || nr.Status != "waiting_approval" || nr.Attempt != d.Attempt || nr.ReworkCount != d.ReworkCount {
		return "这个问题已经回答过，或节点已结束，答案未提交。请在平台查看运行状态。"
	}
	return ""
}
