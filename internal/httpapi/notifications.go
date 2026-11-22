package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"errors"
	"fmt"
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
		if !notification.BotWorkersEnabled() {
			c.JSON(503, gin.H{"error": "机器人配置维护中，请稍后再试", "code": "bot_maintenance"})
			return
		}
		respond(c, h.repo.DeleteNotificationBot(bot.ID))
	default:
		c.JSON(405, gin.H{"error": "method not allowed"})
	}
}
func (h *notificationHandlers) save(c *gin.Context, old *domain.NotificationBot) {
	if !notification.BotWorkersEnabled() {
		c.JSON(503, gin.H{"error": "机器人配置维护中，请稍后再试", "code": "bot_maintenance"})
		return
	}
	var req struct {
		ProjectID          string  `json:"projectId"`
		Name               string  `json:"name"`
		Channel            string  `json:"channel"`
		BotID              string  `json:"botId"`
		Secret             string  `json:"secret"`
		Enabled            bool    `json:"enabled"`
		Model              *string `json:"model"`
		InteractionEnabled *bool   `json:"interactionEnabled"`
		TargetID           *string `json:"targetId"`
		WriteApproval      *string `json:"writeApproval"`
		ConfigVersion      *int64  `json:"configVersion"`
		// TenantKey is the DingTalk CorpId entered by the administrator.
		TenantKey *string `json:"tenantKey"`
		// CardTemplateID is the DingTalk interactive card template for
		// action cards; empty sends plain-text notifications.
		CardTemplateID *string `json:"cardTemplateId"`
		// PendingTargetAction decides what happens to queued Feishu/DingTalk
		// notifications of the previous target: "cancel" or "migrate".
		PendingTargetAction string `json:"pendingTargetAction"`
	}
	if !decode(c, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BotID = strings.TrimSpace(req.BotID)
	req.Secret = strings.TrimSpace(req.Secret)
	if req.Name == "" || len([]rune(req.Name)) > 80 || req.BotID == "" || len(req.BotID) > 191 || len(req.Secret) > 512 {
		c.JSON(400, gin.H{"error": "请填写有效的名称、应用标识和 Secret"})
		return
	}
	// Clients without a channel field only ever managed WeCom Bots.
	channel := domain.NotificationBot{Channel: strings.TrimSpace(req.Channel)}.ChannelKind()
	if req.Channel != "" && req.Channel != channel {
		c.JSON(400, gin.H{"error": "不支持的渠道"})
		return
	}
	bot := domain.NotificationBot{ID: "bot-" + randomID(), ProjectID: strings.TrimSpace(req.ProjectID), Name: req.Name, Channel: channel, BotID: req.BotID, Enabled: req.Enabled, Health: "unchecked", WriteApproval: domain.BotWriteReadOnly}
	if old != nil {
		bot = *old
		bot.Name = req.Name
		bot.Enabled = req.Enabled
		if req.Channel != "" && channel != old.ChannelKind() {
			c.JSON(400, gin.H{"error": repo.ErrBotImmutableField.Error(), "code": "immutable_field"})
			return
		}
		channel = old.ChannelKind()
		if old.BotID != req.BotID {
			if channel != domain.BotChannelWeCom {
				c.JSON(400, gin.H{"error": repo.ErrBotImmutableField.Error(), "code": "immutable_field"})
				return
			}
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
	if !botChannelOpen(channel) {
		c.JSON(409, gin.H{"error": "该渠道尚未开放", "code": "channel_not_ready"})
		return
	}
	if req.WriteApproval != nil {
		switch mode := strings.TrimSpace(*req.WriteApproval); {
		case mode == domain.BotWriteReadOnly, mode == domain.BotWriteConfirm:
			// Web confirmation works on every channel: drafts run only
			// after the bound user confirms them on the platform page.
			bot.WriteApproval = mode
		case mode == domain.BotWriteAuto && channel == domain.BotChannelWeCom:
			bot.WriteApproval = mode
		default:
			c.JSON(400, gin.H{"error": "该渠道只支持只读查询或网页确认"})
			return
		}
	}
	if req.InteractionEnabled != nil {
		bot.InteractionEnabled = *req.InteractionEnabled
	}
	if channel == domain.BotChannelDingTalk {
		corp := ""
		if req.TenantKey != nil {
			corp = strings.TrimSpace(*req.TenantKey)
		}
		switch {
		case corp == "" && old == nil:
			c.JSON(400, gin.H{"error": "请填写钉钉企业 CorpId", "code": "tenant_required"})
			return
		case len(corp) > 191 || strings.ContainsAny(corp, " \t\r\n"):
			c.JSON(400, gin.H{"error": "钉钉企业 CorpId 格式无效"})
			return
		case corp != "" && old != nil && old.TenantState == domain.BotTenantLocked && corp != old.TenantKey:
			c.JSON(400, gin.H{"error": "企业已确认，更换企业请新建配置", "code": "immutable_field"})
			return
		case corp != "":
			bot.TenantExpected = corp
			if user, ok := h.requestUser(c.Request, true); ok && user != nil {
				bot.TenantExpectedBy = user.ID
			}
		}
		if req.CardTemplateID != nil {
			tpl := strings.TrimSpace(*req.CardTemplateID)
			if len(tpl) > 128 || strings.ContainsAny(tpl, " \t\r\n/?#") {
				c.JSON(400, gin.H{"error": "钉钉互动卡片模板 ID 格式无效"})
				return
			}
			bot.CardTemplateID = tpl
		}
	}
	if req.Model != nil {
		model, err := normalizeFeatureModel(*req.Model)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		// Preserve validation on the legacy, unqualified CodeBuddy field.
		// Explicit CLI IDs are checked by their own model service at runtime.
		if !assistantModelHasEnginePrefix(model) && !codeBuddyIsDefaultModel(model) &&
			(old == nil || old.Model != model) && !assistantCatalogLists(assistantKnownCatalog(), model) {
			c.JSON(400, gin.H{"error": errFeatureModel.Error()})
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
		// New channels verify the credential before anything is stored, outside
		// the database transaction; a failure keeps the previous configuration.
		if channel != domain.BotChannelWeCom {
			adapter, err := h.service.Interactions.Channel(bot)
			if err != nil {
				c.JSON(409, gin.H{"error": "该渠道尚未开放", "code": "channel_not_ready"})
				return
			}
			verified, err := adapter.Verify(c.Request.Context(), bot, botinteraction.Credential{Secret: req.Secret})
			if err != nil {
				c.JSON(400, gin.H{"error": "凭据校验失败：" + err.Error(), "code": "credential_invalid"})
				return
			}
			bot.Health = "ready"
			now := time.Now().UTC()
			bot.LastCheckedAt = &now
			if verified.TenantName != "" {
				bot.TenantName = verified.TenantName
			}
		}
	}
	// A target is accepted only after refreshing the real provider list. Never trust a posted name/type.
	if bot.InteractionEnabled {
		if bot.OwnerID == 0 {
			c.JSON(400, gin.H{"error": "请登录后创建支持对话的 Bot，平台需要记录创建者的执行身份"})
			return
		}
	}
	var switchedTarget *domain.BotChat
	if req.TargetID != nil {
		if *req.TargetID == "" {
			bot.Target = nil
		} else if channel != domain.BotChannelWeCom {
			// Feishu/DingTalk: a bound private-chat sender of this
			// configuration, accepted only after a trial send succeeded.
			if old == nil || req.Secret != "" {
				c.JSON(400, gin.H{"error": "请先保存凭据，再选择通知接收人"})
				return
			}
			unchanged := old.Target != nil && old.Target.ID == *req.TargetID && old.Target.VerifiedAt != nil
			if !unchanged {
				if !bot.Enabled {
					c.JSON(409, gin.H{"error": "请先启用此 Bot，再试发并选择通知接收人"})
					return
				}
				if pending, err := h.repo.PendingNotificationsForOtherTargets(bot.ID, *req.TargetID); err != nil {
					respond(c, err)
					return
				} else if pending > 0 && req.PendingTargetAction != "cancel" && req.PendingTargetAction != "migrate" {
					c.JSON(409, gin.H{"error": fmt.Sprintf("原接收人还有 %d 条待发通知，请选择取消或改发到新接收人", pending), "code": "pending_deliveries", "pending": pending})
					return
				}
				target, err := h.service.VerifyPushTarget(c.Request.Context(), bot, *req.TargetID)
				if err != nil {
					var failed *notification.PushTargetError
					switch {
					case errors.As(err, &failed):
						c.JSON(502, gin.H{"error": "试发未成功，接收人未保存：" + failed.Error(), "code": "push_trial_failed", "outcome": failed.Result.Outcome})
					case errors.Is(err, notification.ErrPushTargetUnverified):
						c.JSON(409, gin.H{"error": "只能选择已在本 Bot 单聊中绑定平台账号的成员", "code": "push_target_unverified"})
					default:
						c.JSON(409, gin.H{"error": err.Error(), "code": "push_target_unavailable"})
					}
					return
				}
				bot.Target = &target
				switchedTarget = &target
			}
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
	saved, err := h.repo.SaveNotificationBotChecked(bot, req.Secret, req.ConfigVersion)
	if err != nil {
		botSaveError(c, err)
		return
	}
	if switchedTarget != nil && (req.PendingTargetAction == "cancel" || req.PendingTargetAction == "migrate") {
		if _, err := h.repo.SwitchNotificationTarget(saved.ID, *switchedTarget, req.PendingTargetAction); err != nil {
			respond(c, err)
			return
		}
	}
	stored, err := h.repo.NotificationBot(saved.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, stored)
}

// botChannelOpen reports the channels configurable in this release.
func botChannelOpen(channel string) bool {
	return channel == domain.BotChannelWeCom || channel == domain.BotChannelFeishu || channel == domain.BotChannelDingTalk
}

func botSaveError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repo.ErrInteractiveBotAlreadyExists):
		c.JSON(409, gin.H{"error": "这个应用已在另一个配置中启用对话，请复用原配置", "code": "receive_slot_taken"})
	case errors.Is(err, repo.ErrBotConfigChanged):
		c.JSON(409, gin.H{"error": "配置已被其他人修改，请刷新后重试", "code": "config_changed"})
	case errors.Is(err, repo.ErrBotImmutableField):
		c.JSON(400, gin.H{"error": err.Error(), "code": "immutable_field"})
	case errors.Is(err, repo.ErrCredentialKeyMissing):
		c.JSON(503, gin.H{"error": "平台未配置凭据加密密钥，暂时无法保存 Secret，请联系管理员", "code": "credential_key_missing"})
	default:
		respond(c, err)
	}
}

// tenant reads the enterprise information the channel reports for the
// stored credential, shown next to the candidate on the confirmation page.
func (h *notificationHandlers) tenant(c *gin.Context) {
	bot, ok := h.find(c, true)
	if !ok {
		return
	}
	adapter, err := h.service.Interactions.Channel(bot)
	if err != nil || bot.ChannelKind() == domain.BotChannelWeCom {
		c.JSON(409, gin.H{"error": "该渠道没有企业确认", "code": "channel_not_ready"})
		return
	}
	secret, err := h.repo.BotCredential(bot.ID)
	if err != nil {
		c.JSON(409, gin.H{"error": "机器人凭据不可用"})
		return
	}
	info := gin.H{"tenantState": bot.TenantState, "candidate": bot.TenantCandidate, "tenantKey": bot.TenantKey, "configVersion": bot.ConfigVersion}
	if verified, err := adapter.Verify(c.Request.Context(), bot, botinteraction.Credential{Secret: secret}); err == nil {
		info["queried"] = gin.H{"tenantKey": verified.TenantKey, "tenantName": verified.TenantName}
		if verified.TenantName != "" && verified.TenantName != bot.TenantName {
			_ = h.repo.SetBotTenantName(bot.ID, verified.TenantName)
		}
	} else {
		info["queryError"] = err.Error()
	}
	c.JSON(200, info)
}

// tenantConfirm locks the candidate enterprise scope (Bot managers only).
func (h *notificationHandlers) tenantConfirm(c *gin.Context) {
	bot, ok := h.find(c, true)
	if !ok {
		return
	}
	if !botBindingSameOrigin(c) {
		c.JSON(403, gin.H{"error": "请从平台页面操作"})
		return
	}
	var req struct {
		TenantKey     string `json:"tenantKey"`
		ConfigVersion int64  `json:"configVersion"`
		Rediscover    bool   `json:"rediscover"`
	}
	if !decode(c, &req) {
		return
	}
	var saved domain.NotificationBot
	var err error
	if req.Rediscover {
		saved, err = h.repo.RediscoverBotTenant(bot.ID, req.ConfigVersion)
	} else {
		actor := int64(0)
		if user, ok := h.requestUser(c.Request, true); ok && user != nil {
			actor = user.ID
		}
		saved, err = h.repo.ConfirmBotTenant(bot.ID, strings.TrimSpace(req.TenantKey), req.ConfigVersion, actor)
	}
	switch {
	case errors.Is(err, repo.ErrBotTenantCandidate):
		c.JSON(409, gin.H{"error": "企业候选已变化或与当前凭据不一致，请刷新后重新确认", "code": "tenant_candidate_mismatch"})
		return
	case err != nil:
		botSaveError(c, err)
		return
	}
	stored, err := h.repo.NotificationBot(saved.ID)
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
	if err != nil && bot.ChannelKind() != domain.BotChannelWeCom {
		c.JSON(409, gin.H{"error": err.Error(), "code": "push_target_unavailable"})
		return
	}
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
	if bot.ChannelKind() != domain.BotChannelWeCom {
		// The test re-runs the trial send and refreshes the verification.
		target, err := h.service.VerifyPushTarget(c.Request.Context(), bot, bot.Target.ID)
		var failed *notification.PushTargetError
		switch {
		case errors.As(err, &failed):
			c.JSON(502, gin.H{"error": failed.Error(), "code": "push_trial_failed", "outcome": failed.Result.Outcome})
		case err != nil:
			c.JSON(409, gin.H{"error": err.Error(), "code": "push_target_unavailable"})
		default:
			c.JSON(200, gin.H{"success": true, "targetName": target.Name})
		}
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

// verifyTarget sends a trial proactive message to a bound member without
// changing the notification target, so the member becomes a verified push
// target (for example before switching an expired DingTalk reply).
func (h *notificationHandlers) verifyTarget(c *gin.Context) {
	bot, ok := h.find(c, true)
	if !ok {
		return
	}
	if bot.ChannelKind() == domain.BotChannelWeCom {
		c.JSON(409, gin.H{"error": "企业微信不需要试发验证", "code": "state_conflict"})
		return
	}
	if !botBindingSameOrigin(c) {
		c.JSON(403, gin.H{"error": "请从平台页面操作"})
		return
	}
	var req struct {
		TargetID string `json:"targetId"`
	}
	if !decode(c, &req) {
		return
	}
	target, err := h.service.VerifyPushTarget(c.Request.Context(), bot, strings.TrimSpace(req.TargetID))
	var failed *notification.PushTargetError
	switch {
	case errors.As(err, &failed):
		c.JSON(502, gin.H{"error": "试发未成功：" + failed.Error(), "code": "push_trial_failed", "outcome": failed.Result.Outcome})
	case errors.Is(err, notification.ErrPushTargetUnverified):
		c.JSON(409, gin.H{"error": "只能试发给已在本 Bot 单聊中绑定平台账号的成员", "code": "push_target_unverified"})
	case err != nil:
		c.JSON(409, gin.H{"error": err.Error(), "code": "push_target_unavailable"})
	default:
		c.JSON(200, target)
	}
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
			if c.Request.URL.Path == "/api/notification-deliveries/"+item.ID+"/confirm" {
				actor := int64(0)
				if user, ok := h.requestUser(c.Request, true); ok && user != nil {
					actor = user.ID
				}
				err = h.repo.ConfirmNotificationReceived(projectID, item.ID, actor)
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(409, gin.H{"error": "只有送达结果未知的记录可以确认收到"})
					return
				}
				respond(c, err)
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
