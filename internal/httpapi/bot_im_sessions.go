package httpapi

import (
	"agentflow/internal/domain"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type imConversationSummary struct {
	ID            string    `json:"id"`
	BotID         string    `json:"botId"`
	BotName       string    `json:"botName"`
	Channel       string    `json:"channel"`
	ChannelName   string    `json:"channelName"`
	ProjectID     string    `json:"projectId"`
	ProjectName   string    `json:"projectName"`
	ChatID        string    `json:"chatId"`
	ChatType      string    `json:"chatType"`
	WriteApproval string    `json:"writeApproval"`
	SessionID     string    `json:"sessionId"`
	SenderID      string    `json:"senderId,omitempty"`
	Preview       string    `json:"preview,omitempty"`
	Status        string    `json:"status,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type imConversationMessage struct {
	ID        string    `json:"id"`
	SenderID  string    `json:"senderId"`
	Content   string    `json:"content"`
	Quote     string    `json:"quote,omitempty"`
	Reply     string    `json:"reply,omitempty"`
	Status    string    `json:"status"`
	LastError string    `json:"lastError,omitempty"`
	Action    string    `json:"action,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type imConversationDetail struct {
	Conversation imConversationSummary   `json:"conversation"`
	Messages     []imConversationMessage `json:"messages"`
}

// conversations lists recent IM (WeCom / Feishu / DingTalk) Bot conversations
// across all projects the caller may read, for the workbench IM sidebar tab.
// 路由: g.GET("/api/bot-interactions/conversations", api.botInteractions.conversations)
func (h *botInteractionHandlers) conversations(c *gin.Context) {
	projects, ok := h.readableIMProjects(c)
	if !ok {
		return
	}
	if len(projects) == 0 {
		c.JSON(http.StatusOK, []imConversationSummary{})
		return
	}
	projectFilter := strings.TrimSpace(c.Query("projectId"))
	projectNames := make(map[string]string, len(projects))
	projectIDs := make([]string, 0, len(projects))
	for _, p := range projects {
		if projectFilter != "" && p.ID != projectFilter {
			continue
		}
		projectNames[p.ID] = p.Name
		projectIDs = append(projectIDs, p.ID)
	}
	if len(projectIDs) == 0 {
		c.JSON(http.StatusOK, []imConversationSummary{})
		return
	}
	bots, err := h.repo.NotificationBotsByProjects(projectIDs)
	if err != nil {
		respond(c, err)
		return
	}
	if len(bots) == 0 {
		c.JSON(http.StatusOK, []imConversationSummary{})
		return
	}
	botByID := make(map[string]domain.NotificationBot, len(bots))
	botIDs := make([]string, 0, len(bots))
	for _, b := range bots {
		botByID[b.ID] = b
		botIDs = append(botIDs, b.ID)
	}
	chats, err := h.repo.BotConversationsByBots(botIDs, 100)
	if err != nil {
		respond(c, err)
		return
	}
	if len(chats) == 0 {
		c.JSON(http.StatusOK, []imConversationSummary{})
		return
	}
	convIDs := make([]string, 0, len(chats))
	for _, ch := range chats {
		convIDs = append(convIDs, ch.ID)
	}
	recentRows, err := h.repo.RecentBotInteractionsByConversations(convIDs)
	if err != nil {
		respond(c, err)
		return
	}
	latest := make(map[string]domain.BotInteraction, len(convIDs))
	for _, row := range recentRows {
		if isBotBindingExchange(row) {
			continue
		}
		if _, seen := latest[row.ConversationID]; !seen {
			latest[row.ConversationID] = row
		}
	}
	out := make([]imConversationSummary, 0, len(chats))
	for _, ch := range chats {
		bot, found := botByID[ch.BotID]
		if !found {
			continue
		}
		item := buildIMConversationSummary(ch, bot, projectNames[bot.ProjectID], latest[ch.ID])
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// conversationDetail returns the read-only transcript of one IM conversation
// after verifying read access to its owning project.
// 路由: g.GET("/api/bot-interactions/conversations/:conversationId", api.botInteractions.conversationDetail)
func (h *botInteractionHandlers) conversationDetail(c *gin.Context) {
	conv, bot, ok := h.resolveReadableIMConversation(c)
	if !ok {
		return
	}
	rows, err := h.repo.BotConversationInteractions(conv.ID, 100)
	if err != nil {
		respond(c, err)
		return
	}
	messages := make([]imConversationMessage, 0, len(rows))
	var latest domain.BotInteraction
	for _, row := range rows {
		if isBotBindingExchange(row) {
			continue
		}
		latest = row
		messages = append(messages, imConversationMessage{
			ID:        row.ID,
			SenderID:  row.SenderID,
			Content:   row.Content,
			Quote:     row.Quote,
			Reply:     row.Reply,
			Status:    row.Status,
			LastError: row.LastError,
			Action:    row.Action,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	projName := ""
	if p, found := h.repo.Project(bot.ProjectID); found {
		projName = p.Name
	}
	c.JSON(http.StatusOK, imConversationDetail{
		Conversation: buildIMConversationSummary(conv, bot, projName, latest),
		Messages:     messages,
	})
}

// continueInWorkbench creates a brand-new personal workbench conversation
// seeded with the IM transcript so the user can continue working in Studio
// without accidentally pushing messages back to the IM group or chat.
// 路由: g.POST("/api/bot-interactions/conversations/:conversationId/continue", api.botInteractions.continueInWorkbench)
func (h *botInteractionHandlers) continueInWorkbench(c *gin.Context) {
	user, authenticated := h.requestUser(c.Request, true)
	var userID int64
	var loginName string
	if authenticated && user != nil {
		userID = user.ID
		loginName = user.LoginName
	} else if authRequired() {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return
	}
	conv, bot, ok := h.resolveReadableIMConversation(c)
	if !ok {
		return
	}
	rows, err := h.repo.BotConversationInteractions(conv.ID, 40)
	if err != nil {
		respond(c, err)
		return
	}
	channelName := botChannelName(bot)
	chatKind := "单聊"
	if conv.ChatType == "group" {
		chatKind = "群聊"
	}
	now := time.Now().UTC()
	var messages []domain.AssistantMessage
	var firstSnippet string
	for _, row := range rows {
		if isBotBindingExchange(row) {
			continue
		}
		content := strings.TrimSpace(row.Content)
		if content == "" && strings.TrimSpace(row.Reply) == "" {
			continue
		}
		if firstSnippet == "" && content != "" {
			firstSnippet = truncateRunes(content, 28)
		}
		senderLabel := strings.TrimSpace(row.SenderID)
		if senderLabel == "" {
			senderLabel = "IM 用户"
		}
		userText := "[" + channelName + " · " + chatKind + " · " + senderLabel + "] " + content
		if q := strings.TrimSpace(row.Quote); q != "" {
			userText += "\n> 引用：" + q
		}
		created := row.CreatedAt
		if created.IsZero() {
			created = now
		}
		messages = append(messages, domain.AssistantMessage{
			Role:       "user",
			Content:    userText,
			AuthorID:   userID,
			AuthorName: loginName,
			CreatedAt:  created,
		})
		if reply := strings.TrimSpace(row.Reply); reply != "" {
			repliedAt := row.UpdatedAt
			if repliedAt.IsZero() {
				repliedAt = created
			}
			messages = append(messages, domain.AssistantMessage{
				Role:      "assistant",
				Content:   reply,
				CreatedAt: repliedAt,
			})
		}
	}
	title := channelName + " · " + firstNonEmpty(firstSnippet, bot.Name, chatKind)
	session := domain.AssistantSession{
		ID:                    newAssistantSessionID(),
		UserID:                userID,
		ProjectID:             bot.ProjectID,
		Title:                 truncateRunes(title, 64),
		TitleEdited:           true,
		Source:                domain.AssistantSourceChat,
		Messages:              messages,
		ExecutionTargetLocked: len(messages) > 0,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := h.repo.PutAssistantSession(userID, session); err != nil {
		respond(c, err)
		return
	}
	saved, _ := h.repo.AssistantSession(userID, session.ID)
	if saved.ID == "" {
		saved = session
	}
	c.JSON(http.StatusCreated, saved)
}

func (h *botInteractionHandlers) readableIMProjects(c *gin.Context) ([]domain.Project, bool) {
	user, authenticated := h.requestUser(c.Request, true)
	if !authenticated || user == nil {
		if authRequired() {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
			return nil, false
		}
		return h.repo.Projects(), true
	}
	all := h.repo.Projects()
	if h.isPlatformAdmin(c.Request) {
		return all, true
	}
	out := make([]domain.Project, 0, len(all))
	for _, p := range all {
		if p.OwnerID == user.ID {
			out = append(out, p)
			continue
		}
		for _, m := range p.Members {
			if m.UserID == user.ID && !strings.EqualFold(strings.TrimSpace(m.Role), "viewer") {
				out = append(out, p)
				break
			}
		}
	}
	return out, true
}

func (h *botInteractionHandlers) resolveReadableIMConversation(c *gin.Context) (domain.BotConversation, domain.NotificationBot, bool) {
	conv, err := h.repo.BotConversation(strings.TrimSpace(c.Param("conversationId")))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "IM 会话不存在"})
		return domain.BotConversation{}, domain.NotificationBot{}, false
	}
	bot, err := h.repo.NotificationBot(conv.BotID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "对应的机器人不存在"})
		return domain.BotConversation{}, domain.NotificationBot{}, false
	}
	if !h.notifications.access(c, bot.ProjectID, true) {
		return domain.BotConversation{}, domain.NotificationBot{}, false
	}
	return conv, bot, true
}

func buildIMConversationSummary(conv domain.BotConversation, bot domain.NotificationBot, projectName string, latest domain.BotInteraction) imConversationSummary {
	channel := bot.ChannelKind()
	if channel == "" {
		channel = domain.BotChannelWeCom
	}
	preview := truncateRunes(strings.TrimSpace(latest.Content), 80)
	if preview == "" {
		preview = truncateRunes(strings.TrimSpace(latest.Reply), 80)
	}
	updatedAt := conv.UpdatedAt
	if latest.UpdatedAt.After(updatedAt) {
		updatedAt = latest.UpdatedAt
	}
	return imConversationSummary{
		ID:            conv.ID,
		BotID:         bot.ID,
		BotName:       bot.Name,
		Channel:       channel,
		ChannelName:   botChannelName(bot),
		ProjectID:     bot.ProjectID,
		ProjectName:   projectName,
		ChatID:        conv.ChatID,
		ChatType:      firstNonEmpty(conv.ChatType, "single"),
		WriteApproval: bot.EffectiveWriteApproval(),
		SessionID:     conv.SessionID,
		SenderID:      latest.SenderID,
		Preview:       preview,
		Status:        latest.Status,
		UpdatedAt:     updatedAt,
	}
}
