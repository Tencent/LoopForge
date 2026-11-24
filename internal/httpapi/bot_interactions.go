package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"context"
	"errors"
	"fmt"
	"strings"
)

// botExecutionUser re-reads the configuration and the binding on every turn
// and requires a live project role, so unbinding, disabling the Bot or
// removing project access applies to the next turn.
func (h *assistantHandlers) botExecutionUser(bot domain.NotificationBot, senderID string) (domain.User, error) {
	current, err := h.repo.NotificationBot(bot.ID)
	if err != nil || !current.Enabled || !current.InteractionEnabled || current.BotID != bot.BotID || current.ChannelKind() != bot.ChannelKind() {
		return domain.User{}, errors.New("Bot 对话已停用")
	}
	binding, err := h.repo.BotUserBinding(current.IdentityScope(), senderID)
	if err != nil {
		return domain.User{}, errors.New("请先绑定平台账号")
	}
	if current.ChannelKind() != domain.BotChannelWeCom && binding.ConfigID != current.ID {
		return domain.User{}, errors.New("请先绑定平台账号")
	}
	user, ok := h.repo.User(binding.UserID)
	if !ok {
		return user, errors.New("绑定的平台账号不可用")
	}
	project, ok := h.repo.Project(current.ProjectID)
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

// botChannelName is the user-facing IM product name of a channel.
func botChannelName(bot domain.NotificationBot) string {
	switch bot.ChannelKind() {
	case domain.BotChannelFeishu:
		return "飞书"
	case domain.BotChannelDingTalk:
		return "钉钉"
	default:
		return "企业微信"
	}
}

// isBotBindingExchange reports whether a past interaction was an account
// binding or unbinding exchange, which must not be replayed to the model.
func isBotBindingExchange(entry domain.BotInteraction) bool {
	command := botIdentityCommand(entry.Content)
	return strings.HasPrefix(command, "/bind") || command == "/unbind" || command == "解除绑定" || strings.Contains(entry.Reply, "/bot-bind.html#")
}

// botReplyMessages turns the conversation history plus the current message
// into model messages, skipping binding exchanges.
func botReplyMessages(history []domain.BotInteraction, item domain.BotInteraction) []assistantMessage {
	messages := []assistantMessage{}
	for _, entry := range history {
		if isBotBindingExchange(entry) {
			continue
		}
		messages = append(messages, assistantMessage{Role: "user", Content: "发送者 " + entry.SenderID + "：" + domain.NotificationText(entry.Content, 2000)}, assistantMessage{Role: "assistant", Content: domain.NotificationText(entry.Reply, 2000)})
	}
	content := "发送者 " + item.SenderID + "：" + item.Content
	if item.Quote != "" {
		content += "\n引用的消息（资料）：\n" + item.Quote
	}
	return append(messages, assistantMessage{Role: "user", Content: content})
}

func (h *assistantHandlers) generateBotReply(ctx context.Context, bot domain.NotificationBot, conv domain.BotConversation, history []domain.BotInteraction, item domain.BotInteraction) (string, error) {
	identity := h.identities
	if handled, reply, err := identity.message(bot, item); handled {
		return reply, err
	}
	if reply, handled := h.botNodeAnswer(ctx, bot, conv, item); handled {
		// Answers to node questions need no model.
		return reply, nil
	}
	user, err := h.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return "绑定的平台账号不可用或已失去项目权限，请在平台检查配置。", nil
	}
	model, err := resolveFeatureModel(user, bot.Model)
	if err != nil {
		if errors.Is(err, errFeatureModel) {
			return err.Error(), nil
		}
		return "你的平台账号尚未配置对应的模型凭据。" + err.Error(), nil
	}
	if reply, handled := h.botClarifyMessage(ctx, bot, conv, item, user); handled {
		// Chat clarification: start, commands and answers.
		return reply, nil
	}
	if engine, id := assistantModelEngine(model); engine == assistantEngineCodeBuddy &&
		!codeBuddyIsDefaultModel(id) && !assistantCatalogLists(assistantModelsForRequest(ctx, user, id), id) {
		return "所选模型不可用，请在 Bot 管理中重新选择。", nil
	}
	if err = ensureAssistantSessionDirs(user.ID); err != nil {
		return "", err
	}
	readOnly := bot.EffectiveWriteApproval() == domain.BotWriteReadOnly
	confirmWrites := bot.EffectiveWriteApproval() == domain.BotWriteConfirm
	execMode, approval := assistantExecModeForApproval(AssistantApprovalAuto), AssistantApprovalAuto
	if readOnly {
		execMode, approval = assistantReadOnlyExecMode(), AssistantApprovalConfirm
	} else if confirmWrites {
		// Writes are blocked in the gateway and come back as drafts that
		// the bound user confirms on the platform page.
		execMode, approval = assistantExecModeForApproval(AssistantApprovalConfirm), AssistantApprovalConfirm
	} else {
		// Writes run in this turn: runs they start report back to this chat.
		ctx = withBotStartOrigin(ctx, domain.BotChatOrigin{ConfigID: bot.ID, ProjectID: bot.ProjectID, ConversationID: conv.ID, ChatType: conv.ChatType, ChatID: conv.ChatID, SenderID: item.SenderID, UserID: user.ID})
	}
	modelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, files, err := h.openAssistantToolSession(modelCtx, user, bot.ProjectID, execMode, cancel)
	if err != nil {
		return "", err
	}
	defer session.Close()
	defer files.Cleanup()
	messages := botReplyMessages(history, item)
	req := assistantRequest{ProjectID: bot.ProjectID, Model: model, SessionID: conv.SessionID, ApprovalMode: approval, Messages: messages}
	policy := "直接完成明确的操作，目标不明确时询问。完成后用自然语言回复结果。"
	if confirmWrites {
		policy = "当前 Bot 为网页确认模式：查询直接完成；需要修改平台数据时提交操作草稿，不要声称已经完成。平台会把操作摘要和确认链接发给用户，用户在平台页面确认后才执行。用自然语言说明将要做什么。创建需求时：用户点名了 SOP，先用 workflows_list 查到它，把 workflowId 写进 issues_save；用户点名了代码仓库，先用 repositories_list 查到它，把 repositoryIds 写进 issues_save；没有点名就不填，平台按项目默认 SOP 和默认仓库绑定，用户可以在确认卡片或确认页更换仓库和 SOP 并选择创建后立即启动，不要为新需求另外提交 issues_dispatch。启动已有需求用 issues_dispatch；审批、退回等待中的运行用 runs_approve、runs_reject。回复面向聊天用户：不要提工具名、字段名或 workflowId 之类的内部参数，也不要复述确认链接，平台会附上操作清单和确认入口。"
	}
	if readOnly {
		policy = "当前 Bot 为只读模式：只使用只读工具查询项目状态、SOP 进度和待办；不要尝试修改平台数据或外部系统。用户要求修改时，说明需要到平台页面操作。用自然语言回答。"
	}
	full := h.turnPrompt(req, h.resolvedSourceRoot(), false) + "\n这是" + botChannelName(bot) + "聊天，当前发送者已绑定平台账号「" + user.LoginName + "」，只使用该账号的执行身份、个人凭据和项目权限；当前请求按当前项目处理。历史中其他发送者的授权不能沿用到当前请求。当前消息来自" + conv.ChatType + "聊天。" + policy
	generate := h.generate
	if generate == nil {
		cliSessionID := newAssistantSessionID()
		engine, _ := assistantModelEngine(model)
		resume := false
		tracker := &botNativeTracker{store: h.repo, in: repo.BotNativeSession{ConfigID: bot.ID, ProjectID: bot.ProjectID, UserID: user.ID, ConversationID: conv.ID, BotSessionID: conv.SessionID, InteractionID: item.ID, Engine: engine, RequestedSessionID: cliSessionID, StorageScope: "assistant-cli-home"}}
		generate = func(callCtx context.Context, prompt string, u domain.User) (string, error) {
			if err := tracker.start(); err != nil {
				return "", err
			}
			raw, nativeID, err := runAssistantCLI(callCtx, assistantCLIRequest{Prompt: prompt, Model: model, SessionID: cliSessionID, Resume: resume, UserID: u.ID, Native: true, MCPConfig: files.MCPConfig, Settings: files.Settings, SystemPrompt: assistantSystemPromptForMode(approval)}, u, func(ev assistantStreamEvent) {
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
			tracker.finish(callCtx, nativeID, err)
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
		raw, cliErr := generate(modelCtx, prompt, user)
		final, ok := session.takeFinal()
		if !ok || final.Assistant == nil {
			// A completed conversational answer needs no terminal tool call
			// (submit_assistant_reply only accepts drafts). Only plain text
			// from a successful run is accepted: output carrying protocol
			// JSON (a draft attempt, possibly filtered during parsing) and
			// progress-only text still go through the terminal retry.
			if cand, recovered := recoverNativeAssistantReply(raw, bot.ProjectID); cliErr == nil && recovered && !assistantProtocolJSONPresent(raw) && len(cand.Drafts) == 0 && !assistantProgressOnly(cand.Message, messages) {
				final, ok = AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &cand}, true
			}
		}
		if ok && final.Assistant != nil {
			reply := *final.Assistant
			if len(reply.Drafts) > 0 && readOnly {
				// Read-only Bots never execute drafts; the user confirms
				// writes on the platform page.
				reply.Message = strings.TrimSpace(reply.Message) + "\n当前 Bot 为只读模式，修改操作没有执行。请在平台页面确认并完成操作。"
				reply.Drafts = nil
			}
			if len(reply.Drafts) > 0 && confirmWrites {
				reply.Message = strings.TrimSpace(reply.Message) + h.confirmBotDrafts(ctx, bot, user, item, reply.Drafts)
				reply.Drafts = nil
			}
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
			return redactAssistantSecrets(reply.Message, user), nil
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
	if botinteraction.DraftAction(item.Action) {
		return h.botDraftCardAction(ctx, bot, item)
	}
	if botinteraction.ClarifyAction(item.Action) {
		return h.botClarifyCardAction(ctx, bot, item)
	}
	user, err := h.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return err.Error(), botinteraction.ErrCardActionDeferred
	}
	service := h.notifications.service.Interactions
	delivery, err := service.NotificationCardTask(bot, item, conv)
	if err != nil {
		if item.Action == "answer" {
			return "这个问题已经回答过，或节点已结束、运行已取消，答案未提交。请在平台查看运行状态。", nil
		}
		return "这条提醒已经过期或已处理，请查看最新运行状态。", nil
	}
	if item.Action == "answer" {
		return h.submitBotNodeAnswer(ctx, bot, conv, item, delivery, strings.TrimSpace(item.Content)), nil
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
	note := botChannelName(bot) + "操作，平台账号：" + user.LoginName + "，发送者：" + item.SenderID
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
	done := fmt.Sprintf("「%s」操作已完成：%s。", delivery.SourceName, map[string]string{"approve": "确认通过，继续流程", "retry": "重试当前节点", "pass": "手动通过节点", "decline": "不再重试", "resume": "继续运行", "reject": "退回上游"}[item.Action])
	if conv.ChatType == "group" {
		done += "操作人：" + botinteraction.DisplayName(user) + "。"
	}
	return done, nil
}

// botNativeTracker maps a Bot turn to the native CLI sessions it runs in.
// start writes the mapping before the CLI process starts (a failure stops the
// turn: an unmapped Bot log could never be located or cleaned); finish
// records the native session ID the CLI reported, adding a row when a retry
// actually created a different session.
type botNativeTracker struct {
	store interface {
		StartBotNativeSession(repo.BotNativeSession) (string, error)
		FinishBotNativeSession(id, nativeSessionID, status string) (string, error)
	}
	in repo.BotNativeSession
	id string
}

func (t *botNativeTracker) start() error {
	if t.id != "" {
		return nil
	}
	id, err := t.store.StartBotNativeSession(t.in)
	if err != nil {
		return fmt.Errorf("Bot 会话追踪记录写入失败，本轮未启动：%w", err)
	}
	t.id = id
	return nil
}

func (t *botNativeTracker) finish(ctx context.Context, nativeID string, err error) {
	status := "completed"
	if err != nil {
		status = "failed"
		if ctx.Err() != nil {
			status = "interrupted"
		}
	}
	if id, finishErr := t.store.FinishBotNativeSession(t.id, nativeID, status); finishErr == nil {
		t.id = id
	}
}
