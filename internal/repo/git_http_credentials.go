package repo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/gongfeng"

	"gorm.io/gorm"
)

var (
	ErrGitHTTPCredentialExists  = errors.New("该主机已有访问令牌，请编辑现有凭据")
	ErrGitHTTPTokenRequired     = errors.New("添加凭据时请填写访问令牌")
	ErrGitHTTPCredentialManaged = errors.New("内部托管凭据由平台维护，不能在这里修改或删除")
)

func gitHTTPCredentialID(host string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(host)))
}

func (row gitHTTPCredentialRow) secret() domain.GitHTTPCredentialSecret {
	return domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{
			ID: row.ID, Name: row.Name, Platform: row.Platform, Host: row.Host,
			Username: row.Username},
		Token: row.Token,
	}
}

// managedGitHost is the HTTPS host of the internal adapter's Git server.
func managedGitHost() string {
	host, _ := gitwork.HTTPSRemoteHost(gongfeng.ConfigFromEnv().APIBase)
	return host
}

// managedGitHTTPCredential presents the internal adapter's OAuth or
// "private:" token kept in user_configs as a read-only credential, without
// refreshing it. A plain pasted token is a personal credential instead.
func managedGitHTTPCredential(user domain.User) (domain.GitHTTPCredentialSecret, bool) {
	raw := formatManagedGitCredential(user)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, gitwork.OAuthHTTPUser+":") && !strings.HasPrefix(lower, gitwork.HTTPUser+":") {
		return domain.GitHTTPCredentialSecret{}, false
	}
	host := managedGitHost()
	username, token := gitwork.SplitToken(raw)
	if host == "" || token == "" {
		return domain.GitHTTPCredentialSecret{}, false
	}
	secret := domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{
			ID: domain.ManagedGitHTTPCredentialID, Name: "内部托管凭据", Platform: domain.GitPlatformGitLab,
			Host: host, Username: username, Managed: true,
		},
		Token: token,
	}
	if strings.TrimSpace(user.GongfengRefreshToken) != "" {
		secret.ExpiresAt = user.GongfengTokenExpiresAt
	}
	return secret, true
}

// GitHTTPCredentials describes the user's HTTPS Git credentials, followed by
// the managed internal credential unless a personal one covers its host.
// Tokens are never read into the description.
func (r *UsersRepo) GitHTTPCredentials(userID int64) ([]domain.GitHTTPCredential, error) {
	var rows []gitHTTPCredentialRow
	err := r.DB.Select("id", "name", "platform", "host", "username").
		Where("user_id = ?", userID).Order("name, id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]domain.GitHTTPCredential, 0, len(rows)+1)
	hosts := make(map[string]bool, len(rows))
	for _, row := range rows {
		out = append(out, row.secret().GitHTTPCredential)
		hosts[row.Host] = true
	}
	if user, ok := r.User(userID); ok {
		if managed, ok := managedGitHTTPCredential(user); ok && !hosts[managed.Host] {
			out = append(out, managed.GitHTTPCredential)
		}
	}
	return out, nil
}

// SaveGitHTTPCredential creates the credential for input.Host when existingID
// is empty, otherwise updates that credential; an empty token keeps the
// stored one. The host of an existing credential cannot change.
func (r *UsersRepo) SaveGitHTTPCredential(userID int64, existingID string, input domain.GitHTTPCredentialSecret) error {
	if existingID == domain.ManagedGitHTTPCredentialID {
		return ErrGitHTTPCredentialManaged
	}
	host, err := gitwork.HTTPSHostScope(input.Host)
	if err != nil {
		return err
	}
	id := gitHTTPCredentialID(host)
	now := time.Now().UTC()
	if existingID == "" {
		if input.Token == "" {
			return ErrGitHTTPTokenRequired
		}
		row := gitHTTPCredentialRow{
			UserID: userID, ID: id, Name: input.Name, Platform: input.Platform, Host: host,
			Username: input.Username, Token: input.Token, Created: &now, Updated: &now,
		}
		err := r.DB.Create(&row).Error
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrGitHTTPCredentialExists
		}
		return err
	}
	if existingID != id {
		return gorm.ErrRecordNotFound
	}
	row := r.DB.Model(&gitHTTPCredentialRow{}).Where("user_id = ? AND id = ?", userID, id)
	updates := map[string]any{
		"name": input.Name, "platform": input.Platform, "username": input.Username,
		"updated_at": now,
	}
	if input.Token != "" {
		updates["token"] = input.Token
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

func (r *UsersRepo) DeleteGitHTTPCredential(userID int64, id string) error {
	if id == domain.ManagedGitHTTPCredentialID {
		return ErrGitHTTPCredentialManaged
	}
	return r.DB.Where("user_id = ? AND id = ?", userID, id).Delete(&gitHTTPCredentialRow{}).Error
}

// GitHTTPCredentialForHost returns the user's credential for exactly one
// host[:port], refreshing a managed OAuth token first.
func (r *UsersRepo) GitHTTPCredentialForHost(ctx context.Context, userID int64, host string) (domain.GitHTTPCredentialSecret, bool) {
	host, err := gitwork.HTTPSHostScope(host)
	if err != nil || userID <= 0 {
		return domain.GitHTTPCredentialSecret{}, false
	}
	var row gitHTTPCredentialRow
	if err := r.DB.Where("user_id = ? AND id = ?", userID, gitHTTPCredentialID(host)).Limit(1).Find(&row).Error; err != nil {
		return domain.GitHTTPCredentialSecret{}, false
	}
	if row.ID != "" && row.Host == host && row.Token != "" {
		return row.secret(), true
	}
	if host != managedGitHost() {
		return domain.GitHTTPCredentialSecret{}, false
	}
	return r.refreshedManagedGitHTTPCredential(ctx, userID)
}

// GitHTTPCredentialSecrets is used only by authenticated execution dispatch.
// Public settings and run snapshots continue to use credential descriptions.
func (r *UsersRepo) GitHTTPCredentialSecrets(ctx context.Context, userID int64) map[string]domain.GitHTTPExecutionCredential {
	out := map[string]domain.GitHTTPExecutionCredential{}
	credentials, err := r.GitHTTPCredentials(userID)
	if err != nil {
		return out
	}
	for _, credential := range credentials {
		if secret, ok := r.GitHTTPCredentialForHost(ctx, userID, credential.Host); ok {
			out[credential.Host] = domain.GitHTTPExecutionCredential{GitHTTPCredential: secret.GitHTTPCredential, Token: secret.Token, ExpiresAt: secret.ExpiresAt}
		}
	}
	return out
}

// GitHTTPCredentialByID resolves one listed credential for verification.
func (r *UsersRepo) GitHTTPCredentialByID(ctx context.Context, userID int64, id string) (domain.GitHTTPCredentialSecret, bool) {
	if id == domain.ManagedGitHTTPCredentialID {
		return r.refreshedManagedGitHTTPCredential(ctx, userID)
	}
	var row gitHTTPCredentialRow
	if err := r.DB.Where("user_id = ? AND id = ?", userID, id).Limit(1).Find(&row).Error; err != nil || row.ID == "" {
		return domain.GitHTTPCredentialSecret{}, false
	}
	return row.secret(), true
}

func (r *UsersRepo) refreshedManagedGitHTTPCredential(ctx context.Context, userID int64) (domain.GitHTTPCredentialSecret, bool) {
	if r.managedGitCredential(ctx, userID) == "" {
		return domain.GitHTTPCredentialSecret{}, false
	}
	user, ok := r.User(userID)
	if !ok {
		return domain.GitHTTPCredentialSecret{}, false
	}
	return managedGitHTTPCredential(user)
}

// GitHTTPWorkspaceHosts maps each host whose credential the user hands to
// workspace turns onto its platform. It carries no tokens: a turn requests
// the token for one host when it needs it.
func (r *UsersRepo) GitHTTPWorkspaceHosts(userID int64) map[string]string {
	if userID <= 0 {
		return nil
	}
	creds, err := r.GitHTTPCredentials(userID)
	if err != nil {
		return nil
	}
	hosts := make(map[string]string, len(creds))
	for _, cred := range creds {
		hosts[cred.Host] = cred.Platform
	}
	return hosts
}
