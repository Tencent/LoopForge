package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// APITokens returns every token owned by a user, including revoked tokens so
// the owner can audit old credentials.
// The returned slice is always non-nil so JSON callers receive [] instead of
// null for an owner that has no tokens yet.
func (r *TokensRepo) APITokens(userID int64) []domain.APIToken {
	out := make([]domain.APIToken, 0)
	if userID <= 0 {
		return out
	}
	var rows []apiTokenRow
	if err := r.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out
}

func (r *TokensRepo) PutAPIToken(token domain.APIToken) error {
	if strings.TrimSpace(token.ID) == "" || token.UserID <= 0 || strings.TrimSpace(token.TokenHash) == "" {
		return errors.New("api token id, userId, and tokenHash are required")
	}
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	return r.DB.Create(&apiTokenRow{
		ID: token.ID, UserID: token.UserID, Name: token.Name, Prefix: token.Prefix,
		TokenHash: token.TokenHash, LastUsedAt: token.LastUsedAt, ExpiresAt: token.ExpiresAt,
		Created: token.CreatedAt, RevokedAt: token.RevokedAt,
	}).Error
}

// ActiveAPITokenByHash validates a token hash and updates last_used_at at most
// once every five minutes to avoid turning every API call into a database write.
func (r *TokensRepo) ActiveAPITokenByHash(hash string, now time.Time) (domain.APIToken, bool) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return domain.APIToken{}, false
	}
	var row apiTokenRow
	if err := r.DB.Where("token_hash = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", hash, now).
		Take(&row).Error; err != nil {
		return domain.APIToken{}, false
	}
	token := row.toDomain()
	if token.LastUsedAt == nil || now.Sub(*token.LastUsedAt) >= 5*time.Minute {
		if err := r.DB.Model(&apiTokenRow{}).
			Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", token.ID, now.Add(-5*time.Minute)).
			Update("last_used_at", now).Error; err == nil {
			used := now
			token.LastUsedAt = &used
		}
	}
	return token, true
}

func (r *TokensRepo) RevokeAPIToken(id string, userID int64, now time.Time) bool {
	if strings.TrimSpace(id) == "" || userID <= 0 {
		return false
	}
	res := r.DB.Model(&apiTokenRow{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", now)
	if res.Error != nil {
		return false
	}
	return res.RowsAffected > 0
}

func (r apiTokenRow) toDomain() domain.APIToken {
	token := domain.APIToken{ID: r.ID, UserID: r.UserID, Name: r.Name, Prefix: r.Prefix, TokenHash: r.TokenHash, CreatedAt: r.Created}
	if r.LastUsedAt != nil {
		t := *r.LastUsedAt
		token.LastUsedAt = &t
	}
	if r.ExpiresAt != nil {
		t := *r.ExpiresAt
		token.ExpiresAt = &t
	}
	if r.RevokedAt != nil {
		t := *r.RevokedAt
		token.RevokedAt = &t
	}
	return token
}
