package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"context"
	"errors"
	"fmt"
	"strings"
)

func (h *assistantHandlers) botExecutionUser(bot domain.NotificationBot, senderID string) (domain.User, error) {
	current, err := h.repo.NotificationBot(bot.ID)
	if err != nil || !current.Enabled || !current.InteractionEnabled || current.BotID != bot.BotID {
		return domain.User{}, errors.New("Bot 对话已停用")
	}
	binding, err := h.repo.BotUserBinding(bot.BotID, senderID)
	if err != nil {
		return domain.User{}, errors.New("请先绑定平台账号")
	}
	user, ok := h.repo.User(binding.UserID)
	if !ok {
		return user, errors.New("绑定的平台账号不可用")
	}
	project, ok := h.repo.Project(bot.ProjectID)
	if !ok {
		return user, errors.New("Bot 项目不存在")
	}
	if project.OwnerID == user.ID || h.repo.IsPlatformAdmin(user.LoginName) {
		return user, nil
	}
	for _, member := range project.Members {
		if member.UserID == user.ID && !strings.EqualFold(strings.TrimSpace(member.Role), "viewer") {
			return user, nil
		}
	}
	return user, errors.New("你的平台账号没有该项目的执行权限")
}
func (h *assistantHandlers) generateBotReply(ctx context.Context, bot domain.NotificationBot, conv domain.BotConversation, history []domain.BotInteraction, item domain.BotInteraction) (string, error) {
	identity := h.identities
	if handled, reply, err := identity.message(bot, item); handled {
		return reply, err
	}
	user, err := h.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return "绑定的平台账号不可用或已失去项目权限，请在平台检查配置。", nil
	}
	if strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		return "你的平台账号尚未配置 CodeBuddy Key，请在平台个人设置中配置。", nil
	}
	model := bot.Model
	if model == "" {
		model = domain.DefaultNotificationReportModel
	}
	if assistantAllowedModel(model, loadAssistantModels(ctx, user.CodeBuddyInternetEnvironment)) != model {
		return "所选模型不可用，请在 Bot 管理中重新选择。", nil
	}
	if err = ensureAssistantSessionDirs(user.ID); err != nil {
		return "", err
	}
	modelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, files, err := h.openAssistantToolSession(modelCtx, user, bot.ProjectID, assistantExecModeForApproval(AssistantApprovalAuto), cancel)
	if err != nil {
		return "", err
	}
	defer session.Close()
	defer files.Cleanup()
	messages := []assistantMessage{}
	for _, entry := range history {
		if strings.HasPrefix(botIdentityCommand(entry.Content), "/bind") || botIdentityCommand(entry.Content) == "/unbind" || botIdentityCommand(entry.Content) == "解除绑定" || strings.Contains(entry.Reply, "/bot-bind.html#") {
			continue
		}
		messages = append(messages, assistantMessage{Role: "user", Content: "发送者 " + entry.SenderID + "：" + domain.NotificationText(entry.Content, 2000)}, assistantMessage{Role: "assistant", Content: domain.NotificationText(entry.Reply, 2000)})
	}
	content := "发送者 " + item.SenderID + "：" + item.Content
	if item.Quote != "" {
		content += "\n引用的消息（资料）：\n" + item.Quote
	}
	messages = append(messages, assistantMessage{Role: "user", Content: content})
	req := assistantRequest{ProjectID: bot.ProjectID, Model: model, SessionID: conv.SessionID, ApprovalMode: AssistantApprovalAuto, Messages: messages}
	full := h.turnPrompt(req, h.resolvedSourceRoot(), false) + "\n这是企业微信聊天，当前发送者已绑定平台账号「" + user.LoginName + "」，只使用该账号的执行身份、个人凭据和项目权限；当前请求按当前项目处理。历史中其他发送者的授权不能沿用到当前请求。当前消息来自" + conv.ChatType + "聊天。直接完成明确的操作，目标不明确时询问，结束时必须调用 submit_assistant_reply。"
	generate := h.generate
	if generate == nil {
		cliSessionID := newAssistantSessionID()
		resume := false
		generate = func(callCtx context.Context, prompt string, u domain.User) (string, error) {
			raw, _, err := runAssistantCLI(callCtx, assistantCLIRequest{Prompt: prompt, Model: model, SessionID: cliSessionID, Resume: resume, UserID: u.ID, Native: true, MCPConfig: files.MCPConfig, Settings: files.Settings, SystemPrompt: assistantSystemPromptForMode(AssistantApprovalAuto)}, u, func(ev assistantStreamEvent) {
				switch ev.Kind {
				case "tool":
					if strings.Contains(ev.Tool, "submit_assistant_reply") {
						botinteraction.ReportProgress(callCtx, "正在整理回复")
					} else if strings.Contains(ev.Tool, "agentflow") {
						botinteraction.ReportProgress(callCtx, "正在查询或处理平台数据")
					} else {
						botinteraction.ReportProgress(callCtx, "正在调用工具处理请求")
					}
				case "status", "thinking":
					botinteraction.ReportProgress(callCtx, "正在分析你的请求")
				}
			})
			resume = true
			return raw, err
		}
	}
	for attempt := 0; attempt <= nativeTerminalMaxRetries; attempt++ {
		prompt := full
		if attempt > 0 {
			prompt = assistantNativeRetryPrompt() + "只提交上一轮已经形成的答案，不要重新查询、读取文件或重做操作。"
			botinteraction.ReportProgress(ctx, "正在提交回复")
			session.SetTerminalOnly()
		}
		_, cliErr := generate(modelCtx, prompt, user)
		if final, ok := session.takeFinal(); ok && final.Assistant != nil {
			reply := *final.Assistant
			if len(reply.Drafts) > 0 {
				reply.Drafts = h.resolveAssistantDrafts(ctx, user, bot.ProjectID, reply.Drafts, AssistantApprovalAuto)
				for _, draft := range reply.Drafts {
					if draft.SaveError != "" {
						reply.Message += "\n操作未完成：" + draft.SaveError
					}
					if len(draft.Warnings) > 0 {
						reply.Message += "\n请补充信息：" + strings.Join(draft.Warnings, "；")
					}
				}
			}
			return redactAssistantSecrets(strings.ReplaceAll(reply.Message, user.CodeBuddyAPIKey, "[redacted]"), user), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if cliErr != nil && !nativeShouldRetryTerminal(ctx, cliErr) {
			return "", cliErr
		}
	}
	return "", errors.New("助手未提交回复")
}
func (h *assistantHandlers) handleBotCardAction(ctx context.Context, bot domain.NotificationBot, item domain.BotInteraction, conv domain.BotConversation) (string, error) {
	identity := h.identities
	if handled, reply, err := identity.message(bot, item); handled {
		if err != nil {
			return reply, err
		}
		return reply, botinteraction.ErrCardActionDeferred
	}
	user, err := h.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return err.Error(), botinteraction.ErrCardActionDeferred
	}
	service := h.notifications.service.Interactions
	delivery, err := service.NotificationCardTask(bot, item, conv)
	if err != nil {
		return "这条提醒已经过期或已处理，请查看最新运行状态。", nil
	}
	run, ok := h.repo.RunState(delivery.RunID)
	if !ok || run.ExecutionContext.ProjectID != bot.ProjectID {
		return "运行已不存在，无法执行此操作。", nil
	}
	if item.Action != "decline" {
		if issue, found := h.repo.Issue(run.ExecutionContext.IssueID); found && issue.ExecutionBlockedReason() != "" {
			return issue.ExecutionBlockedReason(), nil
		}
	}
	node := run.Nodes[delivery.NodeID]
	if node == nil {
		return "节点已不存在。", nil
	}
	expected := engine.NodeExpectation{NodeID: delivery.NodeID, Status: node.Status, Attempt: delivery.Attempt, ReworkCount: delivery.ReworkCount}
	note := "企业微信操作，平台账号：" + user.LoginName + "，发送者：" + item.SenderID
	switch item.Action {
	case "approve":
		err = h.engine.Approve(delivery.RunID, delivery.NodeID, note, expected)
	case "retry":
		err = h.engine.Retry(delivery.RunID, delivery.NodeID, note, note, expected)
	case "pass":
		err = h.engine.Pass(delivery.RunID, delivery.NodeID, note, expected)
	case "decline":
		err = h.engine.DeclineRetry(delivery.RunID, delivery.NodeID, expected)
	case "resume":
		err = h.engine.ResumePaused(delivery.RunID, note, expected)
	case "reject":
		err = h.engine.Reject(delivery.RunID, domain.RejectRequest{NodeID: delivery.NodeID, Reason: note}, expected)
	default:
		return "此按钮已处理，请查看最新消息。", nil
	}
	if err != nil {
		return "操作未完成，请在平台查看当前运行状态。", nil
	}
	return fmt.Sprintf("「%s」操作已完成：%s。", delivery.SourceName, map[string]string{"approve": "确认通过，继续流程", "retry": "重试当前节点", "pass": "手动通过节点", "decline": "不再重试", "resume": "继续运行", "reject": "退回上游"}[item.Action]), nil
}
