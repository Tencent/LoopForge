package domain

import "time"

type BotConnection struct {
	BotID      string    `json:"botId" gorm:"primaryKey;size:191"`
	Owner      string    `json:"-" gorm:"size:64"`
	LeaseUntil time.Time `json:"-"`
	State      string    `json:"state" gorm:"size:32"`
	LastError  string    `json:"lastError,omitempty" gorm:"size:255"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
type BotConversation struct {
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	BotID     string    `json:"botId" gorm:"index;size:191"`
	ChatID    string    `json:"chatId" gorm:"size:191"`
	ChatType  string    `json:"chatType" gorm:"size:16"`
	SessionID string    `json:"sessionId" gorm:"size:64"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type BotInteraction struct {
	ID             string    `json:"id" gorm:"primaryKey;size:64"`
	BotID          string    `json:"botId" gorm:"index:idx_bot_queue,priority:1;size:191"`
	ConversationID string    `json:"conversationId" gorm:"index;size:64"`
	SessionID      string    `json:"sessionId" gorm:"index;size:64"`
	SenderID       string    `json:"senderId" gorm:"size:191"`
	RequestID      string    `json:"-" gorm:"size:191"`
	Content        string    `json:"content" gorm:"type:text"`
	Quote          string    `json:"quote,omitempty" gorm:"type:text"`
	Action         string    `json:"action,omitempty" gorm:"size:32"`
	CardTaskID     string    `json:"cardTaskId,omitempty" gorm:"size:128"`
	DeliveryID     string    `json:"deliveryId,omitempty" gorm:"size:64"`
	Status         string    `json:"status" gorm:"index:idx_bot_queue,priority:2;size:32"`
	Reply          string    `json:"reply,omitempty" gorm:"type:text"`
	LastError      string    `json:"lastError,omitempty" gorm:"size:255"`
	CreatedAt      time.Time `json:"createdAt" gorm:"index:idx_bot_queue,priority:3"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
