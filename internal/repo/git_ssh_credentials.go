package repo

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"

	"gorm.io/gorm"
)

func gitSSHKeyID(host string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(host)))
}

// GitSSHKeyDescriptions never reads private keys into the public response.
func (r *UsersRepo) GitSSHKeyDescriptions(userID int64) ([]domain.GitSSHKey, error) {
	keys := make([]domain.GitSSHKey, 0)
	err := r.DB.Model(&gitSSHKeyRow{}).Select("id", "name", "host").Where("user_id = ?", userID).Order("name, id").Find(&keys).Error
	return keys, err
}

var ErrGitSSHKeyExists = errors.New("该 SSH 主机已有密钥，请编辑现有密钥")

func (r *UsersRepo) SaveGitSSHKey(userID int64, existingID, host, name, privateKey string) error {
	host, err := gitwork.SSHHostScope(host)
	if err != nil {
		return err
	}
	id := gitSSHKeyID(host)
	if existingID == "" {
		if privateKey == "" {
			return gitwork.ErrSSHKeyRequired
		}
		row := gitSSHKeyRow{UserID: userID, ID: id, Host: host, Name: name, PrivateKey: privateKey}
		err := r.DB.Create(&row).Error
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrGitSSHKeyExists
		}
		return err
	}
	if existingID != id {
		return gorm.ErrRecordNotFound
	}
	row := r.DB.Model(&gitSSHKeyRow{}).Where("user_id = ? AND id = ?", userID, id)
	updates := map[string]any{"name": name}
	if privateKey != "" {
		updates["private_key"] = privateKey
	}
	result := row.Session(&gorm.Session{}).Updates(updates)
	if result.Error != nil || result.RowsAffected != 0 {
		return result.Error
	}
	// MySQL can report zero affected rows for an unchanged value.
	var count int64
	if err := row.Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *UsersRepo) DeleteGitSSHKey(userID int64, id string) error {
	return r.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&gitSSHKeyRow{}).Error
}

// GitSSHKeysForRemotes matches only the user's key for each SSH host and port.
// Unconfigured repositories retain empty values so they cannot borrow another
// host's identity or fall back to the execution machine's credentials.
func (r *UsersRepo) GitSSHKeysForRemotes(userID int64, urls []string) map[string]string {
	if len(urls) == 0 {
		return nil
	}
	keys := make(map[string]string, len(urls))
	ids := make([]string, 0, len(urls))
	targets := make(map[string]string, len(urls))
	for _, raw := range urls {
		keys[raw] = ""
		remote, err := gitwork.ParseSSHRemote(raw)
		if err != nil {
			continue
		}
		id := gitSSHKeyID(remote.Endpoint())
		targets[raw] = id
		ids = append(ids, id)
	}
	if userID <= 0 || len(ids) == 0 {
		return keys
	}
	var matches []gitSSHKeyRow
	if err := r.DB.Select("id", "private_key").Where("user_id = ? AND id IN ?", userID, ids).Find(&matches).Error; err != nil {
		return keys
	}
	byID := make(map[string]string, len(matches))
	for _, key := range matches {
		byID[key.ID] = key.PrivateKey
	}
	for remote, id := range targets {
		keys[remote] = byID[id]
	}
	return keys
}
