package repo

import (
	"context"
	"crypto/rand"
	"errors"
	"net/netip"

	"agentflow/internal/gitwork"

	"gorm.io/gorm"
)

// HTTPSHostTrustRepo stores platform authorizations for exact HTTPS endpoints.
type HTTPSHostTrustRepo struct {
	*deps
}

var ErrGitHTTPSHostChanged = errors.New("platform Git HTTPS host authorization changed")

type PlatformGitHTTPSHost struct {
	Host      string       `json:"host" gorm:"type:varchar(260);primaryKey"`
	Platform  string       `json:"platform" gorm:"type:varchar(16);not null"`
	Addresses []netip.Addr `json:"addresses" gorm:"serializer:json;type:text;not null"`
	Revision  string       `json:"revision" gorm:"type:varchar(64);not null"`
}

func (PlatformGitHTTPSHost) TableName() string { return "platform_git_https_hosts" }

type GitHTTPSHostChoice struct {
	Host     string `json:"host"`
	Platform string `json:"platform"`
}

func (r *HTTPSHostTrustRepo) ListPlatformGitHTTPSHosts(ctx context.Context) ([]PlatformGitHTTPSHost, error) {
	hosts := []PlatformGitHTTPSHost{}
	err := r.DB.WithContext(ctx).Order("host").Find(&hosts).Error
	return hosts, err
}

func (r *HTTPSHostTrustRepo) GitHTTPSHostChoices(ctx context.Context) ([]GitHTTPSHostChoice, error) {
	hosts := []GitHTTPSHostChoice{}
	err := r.DB.WithContext(ctx).Model(&PlatformGitHTTPSHost{}).Select("host", "platform").Order("host").Find(&hosts).Error
	return hosts, err
}

func (r *HTTPSHostTrustRepo) CreatePlatformGitHTTPSHost(ctx context.Context, entry *PlatformGitHTTPSHost) error {
	entry.Revision = rand.Text()
	return r.DB.WithContext(ctx).Create(entry).Error
}

func (r *HTTPSHostTrustRepo) UpdatePlatformGitHTTPSHost(ctx context.Context, entry *PlatformGitHTTPSHost, expected string) error {
	next := rand.Text()
	result := r.DB.WithContext(ctx).Model(&PlatformGitHTTPSHost{}).
		Where(platformGitHostRevisionWhere, entry.Host, entry.Host, expected).
		Select("platform", "addresses", "revision").
		Updates(PlatformGitHTTPSHost{Platform: entry.Platform, Addresses: entry.Addresses, Revision: next})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHTTPSHostChanged
	}
	entry.Revision = next
	return nil
}

func (r *HTTPSHostTrustRepo) DeletePlatformGitHTTPSHost(ctx context.Context, host, expected string) error {
	result := r.DB.WithContext(ctx).Where(platformGitHostRevisionWhere, host, host, expected).Delete(&PlatformGitHTTPSHost{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHTTPSHostChanged
	}
	return nil
}

// PlatformGitHTTPSAddresses looks up the authorization for the actual dial target.
// Only an absent record permits the client to fall back to the deployment policy.
func (r *HTTPSHostTrustRepo) PlatformGitHTTPSAddresses(ctx context.Context, target string) ([]netip.Addr, error) {
	host, err := gitwork.HTTPSHostScope(target)
	if err != nil {
		return nil, err
	}
	var entry PlatformGitHTTPSHost
	err = r.DB.WithContext(ctx).Select("host", "addresses").Where("host = ?", host).Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && entry.Host != host) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entry.Addresses) == 0 {
		return nil, errors.New("Git HTTPS host authorization has no addresses")
	}
	return entry.Addresses, nil
}
