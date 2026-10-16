package httpapi

import "github.com/gin-gonic/gin"

type botInteractionHandlers struct {
	*deps
	notifications *notificationHandlers
}

func (h *botInteractionHandlers) records(c *gin.Context) {
	bot, ok := h.notifications.find(c, true)
	if !ok {
		return
	}
	conn, _ := h.repo.BotConnectionState(bot.BotID)
	chats, err := h.repo.BotConversations(bot.ID)
	if err != nil {
		respond(c, err)
		return
	}
	records, err := h.repo.BotInteractionRecords(bot.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"connection": conn, "conversations": chats, "messages": records})
}
