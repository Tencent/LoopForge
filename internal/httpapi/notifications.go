package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"strings"
	"time"
)

type notificationHandlers struct {
	*deps
	service *notification.Service
}

func (h *notificationHandlers) access(c *gin.Context, projectID string, write bool) bool {
	project, ok := h.repo.Project(projectID)
	if !ok {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return false
	}
	user, authenticated := h.requestUser(c.Request, true)
	if !authenticated || user == nil {
		if !authRequired() {
			return true
		}
		c.JSON(401, gin.H{"error": "请先登录"})
		return false
	}
	if h.isPlatformAdmin(c.Request) || project.OwnerID == user.ID {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == user.ID && (!write || !strings.EqualFold(strings.TrimSpace(member.Role), "viewer")) {
			return true
		}
	}
	c.JSON(403, gin.H{"error": "你没有该项目的通知管理权限"})
	return false
}
func (h *notificationHandlers) managed(c *gin.Context, bot domain.NotificationBot) bool {
	if !h.access(c, bot.ProjectID, true) {
		return false
	}
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok || user == nil {
		return false
	}
	project, _ := h.repo.Project(bot.ProjectID)
	if bot.OwnerID == user.ID || project.OwnerID == user.ID {
		return true
	}
	for _, m := range project.Members {
		if m.UserID == user.ID && strings.EqualFold(strings.TrimSpace(m.Role), "maintainer") {
			return true
		}
	}
	c.JSON(403, gin.H{"error": "只有创建者或项目管理员可以修改机器人和发送测试通知"})
	return false
}
func (h *notificationHandlers) find(c *gin.Context, write bool) (domain.NotificationBot, bool) {
	bot, err := h.repo.NotificationBot(c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": "机器人不存在"})
		return bot, false
	}
	if write {
		return bot, h.managed(c, bot)
	}
	return bot, h.access(c, bot.ProjectID, false)
}
func (h *notificationHandlers) bots(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		projectID := strings.TrimSpace(c.Query("projectId"))
		if !h.access(c, projectID, false) {
			return
		}
		bots, err := h.repo.NotificationBots(projectID)
		if err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, bots)
	case http.MethodPost:
		h.save(c, nil)
	default:
		c.JSON(405, gin.H{"error": "method not allowed"})
	}
}
func (h *notificationHandlers) bot(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodPut:
		bot, ok := h.find(c, true)
		if ok {
			h.save(c, &bot)
		}
	case http.MethodDelete:
		bot, ok := h.find(c, true)
		if !ok {
			return
		}
		respond(c, h.repo.DeleteNotificationBot(bot.ID))
	default:
		c.JSON(405, gin.H{"error": "method not allowed"})
	}
}
func (h *notificationHandlers) save(c *gin.Context, old *domain.NotificationBot) {
	var req struct {
		ProjectID          string  `json:"projectId"`
		Name               string  `json:"name"`
		BotID              string  `json:"botId"`
		Secret             string  `json:"secret"`
		Enabled            bool    `json:"enabled"`
		Model              *string `json:"model"`
		InteractionEnabled *bool   `json:"interactionEnabled"`
		TargetID           *string `json:"targetId"`
	}
	if !decode(c, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BotID = strings.TrimSpace(req.BotID)
	req.Secret = strings.TrimSpace(req.Secret)
	if req.Name == "" || len([]rune(req.Name)) > 80 || req.BotID == "" || len(req.BotID) > 191 || len(req.Secret) > 512 {
		c.JSON(400, gin.H{"error": "请填写有效的名称、Bot ID 和 Secret"})
		return
	}
	bot := domain.NotificationBot{ID: "bot-" + randomID(), ProjectID: strings.TrimSpace(req.ProjectID), Name: req.Name, BotID: req.BotID, Enabled: req.Enabled, Health: "unchecked"}
	if old != nil {
		bot = *old
		bot.Name = req.Name
		bot.Enabled = req.Enabled
		if old.BotID != req.BotID {
			if req.Secret == "" {
				c.JSON(400, gin.H{"error": "修改 Bot ID 时必须同时填写新 Secret"})
				return
			}
			bot.BotID = req.BotID
			bot.Target = nil
			bot.Health = "unchecked"
			bot.LastCheckedAt = nil
		}
	}
	if req.InteractionEnabled != nil {
		bot.InteractionEnabled = *req.InteractionEnabled
	}
	if req.Model != nil {
		model := strings.TrimSpace(*req.Model)
		if model == "" {
			model = domain.DefaultNotificationReportModel
		}
		if assistantAllowedModel(model, assistantKnownCatalog()) != model {
			c.JSON(400, gin.H{"error": "请选择有效的 CodeBuddy 模型"})
			return
		}
		bot.Model = model
	}
	if bot.Model == "" {
		bot.Model = domain.DefaultNotificationReportModel
	}
	if !h.access(c, bot.ProjectID, true) {
		return
	}
	if old == nil {
		if user, ok := h.requestUser(c.Request, true); ok && user != nil {
			bot.OwnerID = user.ID
		}
		if req.Secret == "" {
			c.JSON(400, gin.H{"error": "新建机器人需要 Secret"})
			return
		}
	}
	if req.Secret != "" {
		bot.SecretConfigured = true
		bot.Health = "unchecked"
		bot.LastCheckedAt = nil
	}
	// A target is accepted only after refreshing the real provider list. Never trust a posted name/type.
	if bot.InteractionEnabled {
		if bot.OwnerID == 0 {
			c.JSON(400, gin.H{"error": "请登录后创建支持对话的 Bot，平台需要记录创建者的执行身份"})
			return
		}
		bots, err := h.repo.InteractiveNotificationBots()
		if err != nil {
			respond(c, err)
			return
		}
		for _, existing := range bots {
			if existing.BotID == bot.BotID && existing.ID != bot.ID && bot.Enabled {
				c.JSON(409, gin.H{"error": "这只企业微信 Bot 已在另一个配置中启用对话，请复用原配置"})
				return
			}
		}
	}
	if req.TargetID != nil {
		if *req.TargetID == "" {
			bot.Target = nil
		} else {
			if old == nil || req.Secret != "" || old.BotID != req.BotID {
				c.JSON(400, gin.H{"error": "请先保存凭据，再刷新并选择接收会话"})
				return
			}
			chats, err := h.service.Sessions(c.Request.Context(), bot)
			if err != nil {
				h.providerError(c, err)
				return
			}
			found := false
			for _, chat := range chats {
				if chat.ID == *req.TargetID {
					target := chat
					bot.Target = &target
					found = true
					break
				}
			}
			if !found {
				c.JSON(409, gin.H{"error": "目标会话已不可用，请刷新列表"})
				return
			}
			bot.Health = "ready"
			now := time.Now().UTC()
			bot.LastCheckedAt = &now
		}
	}
	if err := h.repo.SaveNotificationBot(bot, req.Secret); err != nil {
		if errors.Is(err, repo.ErrInteractiveBotAlreadyExists) {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		respond(c, err)
		return
	}
	stored, err := h.repo.NotificationBot(bot.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, stored)
}
func (h *notificationHandlers) sessions(c *gin.Context) {
	bot, ok := h.find(c, true)
	if !ok {
		return
	}
	chats, err := h.service.Sessions(c.Request.Context(), bot)
	if err != nil {
		h.providerError(c, err)
		return
	}
	query := strings.ToLower(strings.TrimSpace(c.Query("q")))
	filtered := make([]domain.BotChat, 0, len(chats))
	for _, chat := range chats {
		if query == "" || strings.Contains(strings.ToLower(chat.Name), query) {
			filtered = append(filtered, chat)
		}
	}
	c.JSON(200, gin.H{"sessions": filtered, "total": len(chats), "checkedAt": time.Now().UTC()})
}
func (h *notificationHandlers) test(c *gin.Context) {
	bot, ok := h.find(c, true)
	if !ok {
		return
	}
	if bot.Target == nil {
		c.JSON(400, gin.H{"error": "请先选择并保存接收会话"})
		return
	}
	content := "### 平台通知测试\n机器人：" + bot.Name + "\n这是一条测试通知，不代表实际任务状态。"
	now := time.Now().UTC()
	item := domain.NotificationDelivery{ID: "test-" + randomID(), ProjectID: bot.ProjectID, BotID: bot.ID, EventType: "test", TargetID: bot.Target.ID, TargetName: bot.Target.Name, Content: content, Status: "sending", Attempts: 1, LeaseToken: randomID(), CreatedAt: now, NextAttemptAt: now}
	until := now.Add(2 * time.Minute)
	item.LeaseUntil = &until
	if err := h.repo.CreateNotificationDelivery(item); err != nil {
		respond(c, err)
		return
	}
	if err := h.service.Send(c.Request.Context(), bot, bot.Target.ID, content); err != nil {
		status := "failed"
		var pe *notification.ProviderError
		if errors.As(err, &pe) {
			switch pe.Code {
			case 850003, 853004:
				status = "awaiting_authorization"
			case 404:
				status = "target_unavailable"
			}
		}
		_ = h.repo.FinishNotification(item, status, err.Error(), now)
		h.providerError(c, err)
		return
	}
	if err := h.repo.FinishNotification(item, "sent", "", now); err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "targetName": bot.Target.Name})
}
func (h *notificationHandlers) providerError(c *gin.Context, err error) {
	code := 0
	var pe *notification.ProviderError
	if errors.As(err, &pe) {
		code = pe.Code
	}
	c.JSON(502, gin.H{"error": err.Error(), "code": code})
}
func (h *notificationHandlers) deliveries(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.access(c, projectID, false) {
		return
	}
	items, err := h.repo.NotificationDeliveries(projectID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, items)
}
func (h *notificationHandlers) retry(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.access(c, projectID, true) {
		return
	}
	// Only a Bot manager may resend to its configured recipient.
	items, err := h.repo.NotificationDeliveries(projectID)
	if err != nil {
		respond(c, err)
		return
	}
	for _, item := range items {
		if item.ID == c.Param("deliveryId") {
			bot, err := h.repo.NotificationBot(item.BotID)
			if err != nil {
				c.JSON(404, gin.H{"error": "机器人已删除"})
				return
			}
			if !h.managed(c, bot) {
				return
			}
			err = h.repo.RetryNotification(projectID, item.ID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(409, gin.H{"error": "当前记录不可重试"})
				return
			}
			respond(c, err)
			return
		}
	}
	c.JSON(404, gin.H{"error": "发送记录不存在"})
}

func (h *notificationHandlers) preview(c *gin.Context) {
	var req struct {
		ProjectID  string                     `json:"projectId"`
		Binding    domain.NotificationBinding `json:"binding"`
		SourceType string                     `json:"sourceType"`
	}
	if !decode(c, &req) {
		return
	}
	if !h.access(c, req.ProjectID, false) {
		return
	}
	project, _ := h.repo.Project(req.ProjectID)
	values := map[string]string{"project": project.Name, "workflow": "需求交付流程", "node": "实现功能", "status": "节点已完成", "issue.title": "登录校验优化", "issue.id": "示例需求", "task.name": "每日进度汇总", "input": "优化登录校验并完成验证", "error": "示例错误（仅失败时可用）"}
	if req.Binding.Scope == "run" {
		values["status"] = "SOP 已完成"
		values["node"] = "全部节点"
	}
	if req.SourceType == "schedule_job" || req.SourceType == "assistant_task" {
		values["status"] = "任务已完成"
	}
	result := "已完成登录校验优化，并通过相关测试。\n\n```notification\n{\"title\":\"登录校验优化进展\",\"summary\":\"已完成校验优化，相关测试通过。\",\"body\":\"已完成校验优化，相关测试通过。后续可进行人工验收。\"}\n```"
	if req.Binding.Scope == "run" {
		result = domain.BuildWorkflowNotificationResult(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Name: "需求交付流程", Nodes: []domain.WorkflowNode{{ID: "plan", Name: "需求分析"}, {ID: "impl", Name: "实现与验证"}}}, Nodes: map[string]*domain.NodeRun{"plan": {Status: "succeeded", Output: "已明确验收标准"}, "impl": {Status: "succeeded", Output: result}}})
	}
	content, source, err := domain.RenderNotification(req.Binding, domain.NotificationContext{Values: values, Result: result, Fallback: "示例执行已完成", SourceType: req.SourceType})
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	link := strings.TrimRight(h.service.PublicURL, "/") + "/#/runs/example"
	content = strings.ReplaceAll(content, domain.NotificationURLMarker, link)
	c.JSON(200, gin.H{"content": content, "contentSource": source, "note": "示例预览，不发送消息；AI 内容在实际执行时生成"})
}
