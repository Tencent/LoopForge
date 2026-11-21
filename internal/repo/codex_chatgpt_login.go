package repo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"agentflow/internal/codexoauth"
	"agentflow/internal/domain"
)

// OpenAI rotates the refresh token of a ChatGPT login on every refresh, so at
// most one refresh per login may run across all instances. The lease column
// serializes them, and a result is saved only over the login it started from:
// a sign-in that lands meanwhile always wins.
const (
	codexChatGPTRefreshLead   = 96 * time.Hour
	codexChatGPTStaleRefresh  = 6 * 24 * time.Hour
	codexChatGPTDispatchLead  = time.Hour
	codexChatGPTLeaseTTL      = 2 * time.Minute
	codexChatGPTCheckInterval = 10 * time.Minute
	maxCodexChatGPTLogin      = 64 << 10
)

// Variables so tests can shorten them.
var (
	codexChatGPTRefreshTimeout = 30 * time.Second
	codexChatGPTLeaseWait      = 30 * time.Second
	codexChatGPTLeasePoll      = 500 * time.Millisecond
)

type codexRefresher interface {
	Refresh(ctx context.Context, refreshToken string) (codexoauth.Tokens, error)
}

var codexOAuthClient = func() codexRefresher {
	return codexoauth.NewClient(os.Getenv("AGENTFLOW_CODEX_AUTH_BASE_URL"))
}

var (
	ErrCodexChatGPTRelogin = errors.New("Codex ChatGPT 登录已失效，请在个人配置中重新登录")
	errCodexChatGPTBusy    = errors.New("Codex ChatGPT 登录正在其他实例续期，请稍后重试")
)

// ValidCodexChatGPTLogin normalizes a ChatGPT-mode auth.json. The platform
// refreshes the login itself, so the refresh token is required.
func ValidCodexChatGPTLogin(raw string) (string, error) {
	if len(raw) > maxCodexChatGPTLogin {
		return "", errors.New("auth.json 过大")
	}
	auth, err := codexoauth.ParseAuthFile(raw)
	if err != nil {
		return "", errors.New("auth.json 不是有效的 JSON 对象")
	}
	if auth.AccessToken() == "" || auth.RefreshToken() == "" {
		return "", errors.New("auth.json 缺少 access_token 或 refresh_token，请使用 ChatGPT 登录生成的完整文件")
	}
	return auth.Encode()
}

// SaveCodexChatGPTLogin stores a new sign-in and clears any relogin mark.
func (r *UsersRepo) SaveCodexChatGPTLogin(id int64, raw string) (domain.User, error) {
	content, err := ValidCodexChatGPTLogin(raw)
	if err != nil {
		return domain.User{}, err
	}
	if _, ok := r.User(id); !ok {
		return domain.User{}, errors.New("user not found")
	}
	now := time.Now().UTC()
	err = upsertColumns(r.DB, "user_configs", map[string]any{
		"user_id": id, "codex_chatgpt_login": content, "codex_chatgpt_login_status": "",
		"codex_chatgpt_refresh_lease": nil, "created_at": now, "updated_at": now,
	}, []string{"codex_chatgpt_login", "codex_chatgpt_login_status", "codex_chatgpt_refresh_lease", "updated_at"})
	if err != nil {
		return domain.User{}, err
	}
	user, _ := r.User(id)
	return user, nil
}

// ClearCodexChatGPTLogin signs the user out of ChatGPT on the platform.
func (r *UsersRepo) ClearCodexChatGPTLogin(id int64) (domain.User, error) {
	err := r.DB.Table("user_configs").Where("user_id = ?", id).Updates(map[string]any{
		"codex_chatgpt_login": nil, "codex_chatgpt_login_status": "",
		"codex_chatgpt_refresh_lease": nil, "updated_at": time.Now().UTC(),
	}).Error
	if err != nil {
		return domain.User{}, err
	}
	user, ok := r.User(id)
	if !ok {
		return domain.User{}, errors.New("user not found")
	}
	return user, nil
}

// CodexChatGPTAuth returns the auth.json an execution of this user's Codex
// receives: the refresh token is blanked, so the CLI can call the model but
// never rotate the login. "" means the user has no ChatGPT login. A login
// close to expiry is refreshed first.
func (r *UsersRepo) CodexChatGPTAuth(ctx context.Context, id int64) (string, error) {
	user, ok := r.User(id)
	if !ok || user.CodexChatGPTLogin == "" {
		return "", nil
	}
	if user.CodexChatGPTLoginStatus == domain.CodexChatGPTNeedsRelogin {
		return "", ErrCodexChatGPTRelogin
	}
	auth, err := codexoauth.ParseAuthFile(user.CodexChatGPTLogin)
	if err != nil {
		return "", ErrCodexChatGPTRelogin
	}
	now := time.Now()
	if codexChatGPTDue(auth, now, codexChatGPTDispatchLead) {
		refreshed, err := r.refreshCodexChatGPT(ctx, id, user.CodexChatGPTLogin)
		switch {
		case err == nil:
			auth = refreshed
		case errors.Is(err, ErrCodexChatGPTRelogin):
			return "", err
		default:
			if exp, ok := auth.AccessExpiry(); ok && !exp.After(now) {
				return "", fmt.Errorf("Codex ChatGPT 登录续期失败：%w", err)
			}
			log.Printf("codex chatgpt login of user %d: refresh before dispatch failed, using the current token: %v", id, err)
		}
	}
	return auth.SandboxCopy()
}

// RunCodexChatGPTRefresh keeps every ChatGPT login well ahead of expiry so
// a dispatch rarely has to refresh. It returns when ctx ends.
func (r *UsersRepo) RunCodexChatGPTRefresh(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.refreshDueCodexChatGPTLogins(ctx, time.Now())
		timer.Reset(codexChatGPTCheckInterval)
	}
}

func (r *UsersRepo) refreshDueCodexChatGPTLogins(ctx context.Context, now time.Time) {
	var rows []struct {
		UserID int64  `gorm:"column:user_id"`
		Login  string `gorm:"column:codex_chatgpt_login"`
	}
	err := r.DB.Table("user_configs").Select("user_id, codex_chatgpt_login").
		Where("codex_chatgpt_login IS NOT NULL AND COALESCE(codex_chatgpt_login_status, '') = ''").Scan(&rows).Error
	if err != nil {
		log.Printf("codex chatgpt login: list logins: %v", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		auth, err := codexoauth.ParseAuthFile(row.Login)
		if err != nil || !codexChatGPTDue(auth, now, codexChatGPTRefreshLead) {
			continue
		}
		if _, err := r.refreshCodexChatGPT(ctx, row.UserID, row.Login); err != nil && !errors.Is(err, errCodexChatGPTBusy) {
			log.Printf("codex chatgpt login of user %d: refresh: %v", row.UserID, err)
		}
	}
}

// codexChatGPTDue reports whether the access token expires within lead. A
// token without exp falls back to the age of last_refresh.
func codexChatGPTDue(auth *codexoauth.AuthFile, now time.Time, lead time.Duration) bool {
	if exp, ok := auth.AccessExpiry(); ok {
		return exp.Before(now.Add(lead))
	}
	if last, ok := auth.LastRefresh(); ok {
		return now.Sub(last) > codexChatGPTStaleRefresh
	}
	return true
}

// refreshCodexChatGPT refreshes the login stored as expected and returns the
// login current afterwards, which is the stored one when another instance
// refreshed or the user signed in again meanwhile.
func (r *UsersRepo) refreshCodexChatGPT(ctx context.Context, id int64, expected string) (*codexoauth.AuthFile, error) {
	deadline := time.Now().Add(codexChatGPTLeaseWait)
	for {
		acquired, err := r.acquireCodexChatGPTLease(id)
		if err != nil {
			return nil, err
		}
		if acquired {
			break
		}
		if changed, auth, err := r.codexChatGPTChanged(id, expected); changed {
			return auth, err
		}
		if !time.Now().Before(deadline) {
			return nil, errCodexChatGPTBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(codexChatGPTLeasePoll):
		}
	}
	if changed, auth, err := r.codexChatGPTChanged(id, expected); changed {
		r.releaseCodexChatGPTLease(id)
		return auth, err
	}
	auth, err := codexoauth.ParseAuthFile(expected)
	if err != nil {
		r.releaseCodexChatGPTLease(id)
		return nil, err
	}
	// Once the request is sent the old refresh token may be consumed, so a
	// cancelled dispatch must not discard the answer.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexChatGPTRefreshTimeout)
	tokens, err := codexOAuthClient().Refresh(refreshCtx, auth.RefreshToken())
	cancel()
	if err != nil {
		if codexoauth.IsTerminal(err) {
			if _, markErr := r.finishCodexChatGPTRefresh(id, expected, expected, domain.CodexChatGPTNeedsRelogin); markErr != nil {
				r.releaseCodexChatGPTLease(id)
			}
			return nil, ErrCodexChatGPTRelogin
		}
		r.releaseCodexChatGPTLease(id)
		return nil, err
	}
	auth.ApplyTokens(tokens, time.Now())
	content, err := auth.Encode()
	if err != nil {
		r.releaseCodexChatGPTLease(id)
		return nil, err
	}
	saved, err := r.finishCodexChatGPTRefresh(id, expected, content, "")
	if err != nil {
		// The old refresh token is spent; without this write the login needs a
		// new sign-in once the access token expires.
		log.Printf("ERROR codex chatgpt login of user %d: refreshed login could not be saved: %v", id, err)
		r.releaseCodexChatGPTLease(id)
		return auth, nil
	}
	if !saved {
		r.releaseCodexChatGPTLease(id)
		_, current, err := r.codexChatGPTChanged(id, expected)
		return current, err
	}
	return auth, nil
}

// codexChatGPTChanged reports whether the stored login is no longer expected
// and, if so, what it is now.
func (r *UsersRepo) codexChatGPTChanged(id int64, expected string) (bool, *codexoauth.AuthFile, error) {
	user, ok := r.User(id)
	if !ok || user.CodexChatGPTLogin == "" {
		return true, nil, ErrCodexChatGPTRelogin
	}
	if user.CodexChatGPTLoginStatus == domain.CodexChatGPTNeedsRelogin {
		return true, nil, ErrCodexChatGPTRelogin
	}
	if user.CodexChatGPTLogin == expected {
		return false, nil, nil
	}
	auth, err := codexoauth.ParseAuthFile(user.CodexChatGPTLogin)
	return true, auth, err
}

func (r *UsersRepo) acquireCodexChatGPTLease(id int64) (bool, error) {
	now := time.Now().UTC()
	result := r.DB.Table("user_configs").
		Where("user_id = ? AND codex_chatgpt_login IS NOT NULL AND (codex_chatgpt_refresh_lease IS NULL OR codex_chatgpt_refresh_lease < ?)", id, now).
		Update("codex_chatgpt_refresh_lease", now.Add(codexChatGPTLeaseTTL))
	return result.RowsAffected == 1, result.Error
}

func (r *UsersRepo) releaseCodexChatGPTLease(id int64) {
	if err := r.DB.Table("user_configs").Where("user_id = ?", id).Update("codex_chatgpt_refresh_lease", nil).Error; err != nil {
		log.Printf("codex chatgpt login of user %d: release refresh lease: %v", id, err)
	}
}

// finishCodexChatGPTRefresh saves next and releases the lease only while the
// stored login is still expected.
func (r *UsersRepo) finishCodexChatGPTRefresh(id int64, expected, next, status string) (bool, error) {
	result := r.DB.Table("user_configs").Where("user_id = ? AND codex_chatgpt_login = ?", id, expected).Updates(map[string]any{
		"codex_chatgpt_login": next, "codex_chatgpt_login_status": strings.TrimSpace(status),
		"codex_chatgpt_refresh_lease": nil, "updated_at": time.Now().UTC(),
	})
	return result.RowsAffected == 1, result.Error
}
