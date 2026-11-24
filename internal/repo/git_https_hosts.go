package repo

import (
	"context"
	"crypto/rand"
	"errors"
	"net/netip"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"

	"gorm.io/gorm"
)

// GitHTTPHostTrustRepo stores platform authorizations for exact web Git endpoints.
type GitHTTPHostTrustRepo struct {
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

type PlatformGitHTTPHost PlatformGitHTTPSHost

func (PlatformGitHTTPHost) TableName() string { return "platform_git_http_hosts" }

func gitHTTPHostTable(plainHTTP bool) string {
	if plainHTTP {
		return (PlatformGitHTTPHost{}).TableName()
	}
	return (PlatformGitHTTPSHost{}).TableName()
}

type GitHTTPSHostChoice struct {
	Host     string `json:"host"`
	Platform string `json:"platform"`
}

func (r *GitHTTPHostTrustRepo) ListPlatformGitHTTPHosts(ctx context.Context, plainHTTP bool) ([]PlatformGitHTTPSHost, error) {
	hosts := []PlatformGitHTTPSHost{}
	err := r.DB.WithContext(ctx).Table(gitHTTPHostTable(plainHTTP)).Order("host").Find(&hosts).Error
	return hosts, err
}

func (r *GitHTTPHostTrustRepo) GitHTTPHostChoices(ctx context.Context, plainHTTP bool) ([]GitHTTPSHostChoice, error) {
	hosts := []GitHTTPSHostChoice{}
	err := r.DB.WithContext(ctx).Table(gitHTTPHostTable(plainHTTP)).Select("host", "platform").Order("host").Find(&hosts).Error
	if plainHTTP {
		for i := range hosts {
			hosts[i].Host = "http://" + hosts[i].Host
		}
	}
	return hosts, err
}

func (r *GitHTTPHostTrustRepo) CreatePlatformGitHTTPHost(ctx context.Context, plainHTTP bool, entry *PlatformGitHTTPSHost) error {
	entry.Revision = rand.Text()
	return r.DB.WithContext(ctx).Table(gitHTTPHostTable(plainHTTP)).Create(entry).Error
}

func (r *GitHTTPHostTrustRepo) UpdatePlatformGitHTTPHost(ctx context.Context, plainHTTP bool, entry *PlatformGitHTTPSHost, expected string) error {
	next := rand.Text()
	result := r.DB.WithContext(ctx).Table(gitHTTPHostTable(plainHTTP)).
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

func (r *GitHTTPHostTrustRepo) DeletePlatformGitHTTPHost(ctx context.Context, plainHTTP bool, host, expected string) error {
	result := r.DB.WithContext(ctx).Table(gitHTTPHostTable(plainHTTP)).Where(platformGitHostRevisionWhere, host, host, expected).Delete(&PlatformGitHTTPSHost{})
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
func (r *GitHTTPHostTrustRepo) PlatformGitHTTPSAddresses(ctx context.Context, target string) ([]netip.Addr, error) {
	host, err := gitwork.HTTPSHostScope(target)
	if err != nil {
		return nil, err
	}
	return r.gitHTTPHostAddresses(ctx, false, host)
}

// PlatformGitHTTPAddresses looks up the explicitly authorized plaintext
// endpoint. An absent record never authorizes a plaintext credential.
func (r *GitHTTPHostTrustRepo) PlatformGitHTTPAddresses(ctx context.Context, scope string) ([]netip.Addr, error) {
	endpoint, err := domain.ParseGitHTTPEndpoint(scope)
	if err != nil || endpoint.Scheme != "http" {
		return nil, errors.New("an HTTP Git endpoint is required")
	}
	return r.gitHTTPHostAddresses(ctx, true, endpoint.Authority)
}

func (r *GitHTTPHostTrustRepo) gitHTTPHostAddresses(ctx context.Context, plainHTTP bool, host string) ([]netip.Addr, error) {
	var entry PlatformGitHTTPSHost
	err := r.DB.WithContext(ctx).Table(gitHTTPHostTable(plainHTTP)).Select("host", "addresses").Where("host = ?", host).Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && entry.Host != host) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entry.Addresses) == 0 {
		return nil, errors.New("Git host authorization has no addresses")
	}
	return entry.Addresses, nil
}
