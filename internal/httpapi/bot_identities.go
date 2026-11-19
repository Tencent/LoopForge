package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/url"
	"strings"
	"time"
)

type botIdentityHandlers struct {
	*deps
	publicURL string
}

func (h *botIdentityHandlers) currentUser(c *gin.Context) (domain.User, bool) {
	// Binding is an interactive login operation, never a Bearer-token operation.
	if _, ok := bearerToken(c.Request); ok {
		c.JSON(401, gin.H{"error": "请使用平台网页登录确认身份"})
		return domain.User{}, false
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current == nil || current.IsGuest || current.ID <= 0 {
		c.JSON(401, gin.H{"error": "请先登录平台"})
		return domain.User{}, false
	}
	user, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(401, gin.H{"error": "平台账号不存在"})
		return user, false
	}
	return user, true
}

// binding serves the status and claim steps of the web confirmation. Both
// re-check that the configuration still exists and is enabled and that the
// signed-in account may use its project, and return what the page displays:
// channel, Bot, project and the masked original sender.
func (h *botIdentityHandlers) binding(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	var input struct {
		Token string `json:"token"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Token) != 64 {
		c.JSON(400, gin.H{"error": "绑定链接无效"})
		return
	}
	status, err := h.repo.BotBindingChallengeStatus(input.Token, user.ID, time.Now().UTC())
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	meta, err := h.bindingMeta(input.Token, user)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	if strings.HasSuffix(c.Request.URL.Path, "/claim") && status != "confirmed" {
		if !botBindingSameOrigin(c) {
			c.JSON(403, gin.H{"error": "请从平台绑定页面操作"})
			return
		}
		code, err := h.repo.ClaimBotBindingChallenge(input.Token, user.ID, time.Now().UTC())
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		meta["status"], meta["loginName"], meta["command"] = "claimed", user.LoginName, "/bind "+code
		c.JSON(200, meta)
		return
	}
	meta["status"], meta["loginName"] = status, user.LoginName
	c.JSON(200, meta)
}

// bindingMeta loads the configuration of a link and verifies it is usable by
// user. Legacy WeCom links without a configuration show the channel only.
func (h *botIdentityHandlers) bindingMeta(token string, user domain.User) (gin.H, error) {
	configID, channel, mask, err := h.repo.BotBindingChallengeInfo(token)
	if err != nil {
		return nil, err
	}
	meta := gin.H{"channel": domain.NotificationBot{Channel: channel}.ChannelKind(), "channelName": botChannelName(domain.NotificationBot{Channel: channel}), "sender": mask}
	if configID == "" {
		return meta, nil
	}
	bot, err := h.repo.NotificationBot(configID)
	if err != nil || !bot.Enabled || !bot.InteractionEnabled {
		return nil, errors.New("该机器人已停用或删除，绑定链接失效")
	}
	project, ok := h.repo.Project(bot.ProjectID)
	if !ok || !botProjectMember(project, user, h.repo.IsPlatformAdmin(user.LoginName)) {
		return nil, errors.New("当前平台账号没有该机器人所属项目的执行权限")
	}
	meta["botName"], meta["projectName"] = bot.Name, project.Name
	return meta, nil
}

// botProjectMember mirrors botExecutionUser: owner, platform admin or a
// non-viewer member.
func botProjectMember(project domain.Project, user domain.User, admin bool) bool {
	if admin || project.OwnerID == user.ID {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == user.ID && !strings.EqualFold(strings.TrimSpace(member.Role), "viewer") {
			return true
		}
	}
	return false
}
func botBindingSameOrigin(c *gin.Context) bool {
	origin, err := url.Parse(c.GetHeader("Origin"))
	return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && origin.Host == c.Request.Host
}
func (h *botIdentityHandlers) list(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	bindings, err := h.repo.BotUserBindings(user.ID)
	if err != nil {
		respond(c, err)
		return
	}
	for i := range bindings {
		if bindings[i].ConfigID != "" {
			if bot, err := h.repo.NotificationBot(bindings[i].ConfigID); err == nil {
				bindings[i].BotName = bot.Name
			}
		}
		if bindings[i].Channel == "" {
			bindings[i].Channel = domain.BotChannelWeCom
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, bindings)
}
func (h *botIdentityHandlers) remove(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	if !botBindingSameOrigin(c) {
		c.JSON(403, gin.H{"error": "请从个人配置解除绑定"})
		return
	}
	if err := h.repo.RevokeBotUserBinding(c.Param("id"), user.ID, time.Now().UTC()); err != nil {
		c.JSON(404, gin.H{"error": "绑定不存在"})
		return
	}
	c.Status(204)
}
func botIdentityCommand(content string) string {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "@") {
		if _, rest, ok := strings.Cut(text, " "); ok {
			text = strings.TrimSpace(rest)
		}
	}
	return text
}

// Only platform login nominates an account. The trusted callback from the
// original sender must submit the browser-only code before it takes effect.
func (h *botIdentityHandlers) message(bot domain.NotificationBot, item domain.BotInteraction) (bool, string, error) {
	text := botIdentityCommand(item.Content)
	channel := botChannelName(bot)
	scope := bot.IdentityScope()
	if text == "/unbind" || text == "解除绑定" {
		binding, err := h.repo.BotUserBinding(scope, item.SenderID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, "当前" + channel + "账号尚未绑定平台账号。发送“绑定账号”可进行关联。", nil
		}
		if err != nil {
			return true, "", err
		}
		if err := h.repo.RevokeBotUserBinding(binding.ID, binding.UserID, time.Now().UTC()); err != nil {
			return true, "", err
		}
		return true, "已解除当前" + channel + "账号与平台账号的绑定，旧确认码已失效。需要重新关联时，请发送“绑定账号”。", nil
	}

	if strings.HasPrefix(text, "/bind ") {
		binding, err := h.repo.ConfirmBotUserBinding(bot, item.SenderID, strings.TrimSpace(strings.TrimPrefix(text, "/bind ")), time.Now().UTC())
		if err != nil {
			return true, err.Error(), nil
		}
		user, ok := h.repo.User(binding.UserID)
		if !ok {
			return true, "平台账号不存在，请在个人配置解除后重新绑定。", nil
		}
		return true, "已绑定平台账号「" + user.LoginName + "」。后续操作将使用你的身份、个人凭据和项目权限。请重新发送刚才的请求。", nil
	}
	explicit := text == "绑定账号" || text == "/bind"
	if explicit {
		if binding, err := h.repo.BotUserBinding(scope, item.SenderID); err == nil {
			if user, ok := h.repo.User(binding.UserID); ok {
				return true, "当前已绑定「" + user.LoginName + "」。发送 /unbind 或在平台个人配置中解除绑定。", nil
			}
		}
	}
	_, err := h.repo.BotUserBinding(scope, item.SenderID)
	if err == nil {
		return false, "", nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return true, "", err
	}
	base, err := url.Parse(strings.TrimRight(h.publicURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return true, "请先关联平台账号；平台外部访问地址尚未配置，请联系管理员。", nil
	}
	token, err := h.repo.NewBotBindingChallenge(bot, item.SenderID, time.Now().UTC(), explicit)
	switch {
	case errors.Is(err, repo.ErrBotBindingPending):
		return true, "请先打开上一条消息中的绑定链接完成确认（15 分钟内有效）。需要新链接时发送“绑定账号”。", nil
	case errors.Is(err, repo.ErrBotBindingRateLimited):
		return true, "获取绑定链接过于频繁，请稍后再试。", nil
	case err != nil:
		return true, "", err
	}
	link := strings.TrimRight(h.publicURL, "/") + "/bot-bind.html#request=" + url.QueryEscape(token)
	if bot.ChannelKind() != domain.BotChannelWeCom {
		// Feishu and DingTalk replies are plain text: Markdown link syntax
		// is shown verbatim and auto-linking swallows the closing bracket,
		// so the URL stands alone on its own line.
		return true, "请先关联你的平台账号（链接 15 分钟内有效）：\n" + link + "\n登录确认后，将页面上的 /bind 确认码发回当前 Bot。其他人点击链接不会直接完成绑定。", nil
	}
	return true, "请先[关联你的平台账号](" + link + ")（15 分钟内有效）。登录确认后，将页面上的 /bind 确认码发回当前 Bot。其他人点击链接不会直接完成绑定。", nil
}
