package repo

import (
	"crypto/sha256"
	"fmt"
)

type ForgeTokenDescription struct {
	ID       string `json:"id"`
	Instance string `json:"instance"`
}

func forgeTokenID(instance string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(instance)))
}

func (r *UsersRepo) ForgeTokenDescriptions(userID int64) ([]ForgeTokenDescription, error) {
	out := []ForgeTokenDescription{}
	err := r.DB.Model(&forgeTokenRow{}).Select("id", "instance").Where("user_id = ?", userID).Order("instance").Find(&out).Error
	return out, err
}

func (r *UsersRepo) SaveForgeToken(userID int64, instance, token string) error {
	return upsert(r.DB, (forgeTokenRow{}).TableName(), map[string]any{
		"user_id": userID, "id": forgeTokenID(instance), "instance": instance, "token": token,
	})
}

func (r *UsersRepo) DeleteForgeToken(userID int64, id string) error {
	return r.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&forgeTokenRow{}).Error
}

func (r *UsersRepo) ForgeToken(userID int64, instance string) string {
	if userID <= 0 || instance == "" {
		return ""
	}
	var row forgeTokenRow
	if err := r.DB.Select("token").Where("user_id = ? AND id = ?", userID, forgeTokenID(instance)).Take(&row).Error; err != nil {
		return ""
	}
	return row.Token
}
