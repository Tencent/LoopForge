package repo

import (
	"fmt"
	"time"

	"agentflow/internal/gitwork"
)

func (r *UsersRepo) SaveGitLabToken(userID int64, baseURL, token string) error {
	now := time.Now().UTC()
	err := upsertColumns(r.DB, "user_configs", map[string]any{
		"user_id": userID, "gitlab_url": nullableString(baseURL), "gitlab_token": nullableString(token),
		"created_at": now, "updated_at": now,
	}, []string{"gitlab_url", "gitlab_token", "updated_at"})
	if err != nil {
		return fmt.Errorf("无法保存 GitLab 个人凭证")
	}
	return nil
}

func (r *UsersRepo) GitLabCredential(userID int64) gitwork.HTTPSCredential {
	user, _ := r.User(userID)
	return gitwork.HTTPSCredential{BaseURL: user.GitLabURL, Token: user.GitLabToken}
}
