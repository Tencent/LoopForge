package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
)

// Local accounts are an optional identity adapter. User IDs, permissions and
// API tokens continue to use the upstream UsersRepo and TokensRepo.
type localAccountRow struct {
	UserID       int64  `gorm:"primaryKey;autoIncrement:false"`
	PasswordHash string `gorm:"size:128;not null"`
}

func (localAccountRow) TableName() string { return "local_accounts" }

// bcrypt only uses the first 72 bytes.
const (
	MinPasswordLength = 6
	MaxPasswordLength = 72
)

var errPasswordLength = fmt.Errorf("password must contain %d–%d bytes", MinPasswordLength, MaxPasswordLength)

func (r *UsersRepo) SetLocalPassword(userID int64, password string) error {
	if _, ok := r.User(userID); !ok {
		return errors.New("user not found")
	}
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return errPasswordLength
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return upsert(r.DB, "local_accounts", map[string]any{"user_id": userID, "password_hash": string(hash)})
}

var ErrLoginTaken = errors.New("login already exists")

func (r *UsersRepo) LocalAccountCount() int64 {
	var n int64
	r.DB.Model(&localAccountRow{}).Count(&n)
	return n
}

// CreateLocalAccount registers a new password account; it never takes over an
// existing user row.
func (r *UsersRepo) CreateLocalAccount(login, password string, admin bool) (domain.User, error) {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return domain.User{}, errPasswordLength
	}
	if _, exists := r.UserByLoginName(login); exists {
		return domain.User{}, ErrLoginTaken
	}
	if err := r.PutUser(domain.User{LoginName: login, ChineseName: login}); err != nil {
		return domain.User{}, err
	}
	u, ok := r.UserByLoginName(login)
	if !ok {
		return domain.User{}, errors.New("could not read account")
	}
	if err := r.SetLocalPassword(u.ID, password); err != nil {
		return domain.User{}, err
	}
	if admin {
		if err := r.SetUserPlatformRole(u.ID, domain.PlatformRoleAdmin); err != nil {
			return domain.User{}, err
		}
	}
	return u, nil
}

// Always perform bcrypt work, including for unknown users, to avoid exposing
// the account list through response timing.
var dummyPasswordHash, _ = bcrypt.GenerateFromPassword([]byte("unused-account-password"), bcrypt.DefaultCost)

func (r *UsersRepo) CheckLocalPassword(userID int64, password string) bool {
	var row localAccountRow
	err := r.DB.Where("user_id = ?", userID).First(&row).Error
	hash := []byte(row.PasswordHash)
	if err != nil {
		hash = dummyPasswordHash
	}
	valid := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	return err == nil && valid
}
