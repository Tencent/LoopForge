package repo

import (
	"context"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// Issuer and subject are case-sensitive, including on MySQL.
type oidcIdentityRow struct {
	Issuer  string `gorm:"primaryKey;type:varbinary(512)"`
	Subject string `gorm:"primaryKey;type:varbinary(255)"`
	UserID  int64  `gorm:"not null;index:idx_oidc_identities_user"`
}

func (oidcIdentityRow) TableName() string { return "oidc_identities" }

type OIDCLogin struct {
	State      string    `gorm:"primaryKey;size:64"`
	Browser    string    `gorm:"size:64;not null"`
	Nonce      string    `gorm:"size:64;not null"`
	Verifier   string    `gorm:"size:64;not null"`
	ReturnPath string    `gorm:"type:text;not null"`
	ExpiresAt  time.Time `gorm:"not null;index:idx_oidc_logins_expires"`
}

func (OIDCLogin) TableName() string { return "oidc_logins" }

func (r *UsersRepo) UserByOIDCIdentity(ctx context.Context, issuer, subject string) (domain.User, error) {
	var identity oidcIdentityRow
	if err := r.DB.WithContext(ctx).Take(&identity, "issuer = ? AND subject = ?", issuer, subject).Error; err != nil {
		return domain.User{}, err
	}
	user, ok := r.User(identity.UserID)
	if !ok {
		return domain.User{}, gorm.ErrRecordNotFound
	}
	return user, nil
}

func (r *UsersRepo) CreateOIDCLogin(ctx context.Context, login OIDCLogin, now time.Time) error {
	db := r.DB.WithContext(ctx)
	if err := db.Where("expires_at <= ?", now).Delete(&OIDCLogin{}).Error; err != nil {
		return err
	}
	return db.Create(&login).Error
}

func (r *UsersRepo) ConsumeOIDCLogin(ctx context.Context, state, browser string, now time.Time) (OIDCLogin, error) {
	var login OIDCLogin
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.locking(tx).Take(&login, "state = ? AND browser = ? AND expires_at > ?", state, browser, now).Error; err != nil {
			return err
		}
		deleted := tx.Delete(&login)
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	return login, err
}
