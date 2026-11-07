package domain

import "time"

// BotUserBinding is scoped to the external Bot ID; do not assume user IDs
// obtained from different enterprises or Bot namespaces are interchangeable.
type BotUserBinding struct {
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	BotID     string    `json:"botId" gorm:"size:191"`
	SenderID  string    `json:"senderId" gorm:"size:191"`
	UserID    int64     `json:"userId" gorm:"index"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type BotBindingChallenge struct {
	ID        string    `json:"-" gorm:"primaryKey;size:64"`
	BindingID string    `json:"-" gorm:"index;size:64"`
	UserID    int64     `json:"-"`
	CodeHash  string    `json:"-" gorm:"size:64"`
	Status    string    `json:"-" gorm:"size:16"`
	Attempts  int       `json:"-"`
	ExpiresAt time.Time `json:"-" gorm:"index"`
	CreatedAt time.Time `json:"-"`
}
