package repo

import (
	"context"
	"crypto/rand"
	"errors"

	"gorm.io/gorm"
)

// GitHTTPHostTrustRepo stores administrator approval for plaintext Git endpoints.
type GitHTTPHostTrustRepo struct {
	*deps
}

var ErrGitHTTPHostChanged = errors.New("platform Git HTTP host authorization changed")

type PlatformGitHTTPHost struct {
	Host     string `json:"host" gorm:"type:varchar(260);primaryKey"`
	Revision string `json:"revision" gorm:"type:varchar(64);not null"`
}

func (PlatformGitHTTPHost) TableName() string { return "platform_git_http_hosts" }

func (r *GitHTTPHostTrustRepo) ListPlatformGitHTTPHosts(ctx context.Context) ([]PlatformGitHTTPHost, error) {
	hosts := []PlatformGitHTTPHost{}
	err := r.DB.WithContext(ctx).Order("host").Find(&hosts).Error
	return hosts, err
}

func (r *GitHTTPHostTrustRepo) CreatePlatformGitHTTPHost(ctx context.Context, entry *PlatformGitHTTPHost) error {
	entry.Revision = rand.Text()
	return r.DB.WithContext(ctx).Create(entry).Error
}

func (r *GitHTTPHostTrustRepo) DeletePlatformGitHTTPHost(ctx context.Context, host, expected string) error {
	result := r.DB.WithContext(ctx).Where(platformGitHostRevisionWhere, host, host, expected).Delete(&PlatformGitHTTPHost{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHTTPHostChanged
	}
	return nil
}

// PlatformGitHTTPHostAuthorized checks a canonical HTTP host and port.
func (r *GitHTTPHostTrustRepo) PlatformGitHTTPHostAuthorized(ctx context.Context, authority string) (bool, error) {
	var entry PlatformGitHTTPHost
	err := r.DB.WithContext(ctx).Select("host").Where("host = ?", authority).Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil && entry.Host == authority, err
}
