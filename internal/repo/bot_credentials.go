package repo

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type botCredentialRow struct {
	BotID  string `gorm:"primaryKey;size:191"`
	Secret string `gorm:"column:ciphertext;type:text;not null"`
}

func (botCredentialRow) TableName() string { return "user_bot_credentials" }
func (r *UsersRepo) BotCredential(botID string) (string, error) {
	var row botCredentialRow
	err := r.DB.Where("bot_id = ?", botID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return "", err
	}
	return row.Secret, err
}

func (r *UsersRepo) putBotCredential(tx *gorm.DB, id, secret string) error {
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&botCredentialRow{BotID: id, Secret: secret}).Error
}
func (r *UsersRepo) deleteBotCredential(tx *gorm.DB, id string) error {
	return tx.Where("bot_id = ?", id).Delete(&botCredentialRow{}).Error
}
