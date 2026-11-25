package repo

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"

	"agentflow/internal/gitwork"

	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHHostTrustRepo owns the platform-wide Git SSH host key registry.
type SSHHostTrustRepo struct {
	*deps
}

var ErrPlatformGitSSHHostChanged = errors.New("platform Git SSH host trust changed")

// Use the primary-key lookup and compare both strings byte-for-byte, even
// when the database collation considers distinct legacy values equivalent.
const platformGitHostRevisionWhere = "host = ? AND HEX(host) = HEX(?) AND HEX(revision) = HEX(?)"

type PlatformGitSSHHost struct {
	Host       string `json:"host" gorm:"type:varchar(260);primaryKey"`
	PublicKeys string `json:"publicKeys" gorm:"type:text;not null"`
	Revision   string `json:"revision" gorm:"type:varchar(64);not null"`
}

func (PlatformGitSSHHost) TableName() string { return "platform_git_ssh_hosts" }

func (r *SSHHostTrustRepo) ListPlatformGitSSHHosts(ctx context.Context) ([]PlatformGitSSHHost, error) {
	hosts := []PlatformGitSSHHost{}
	err := r.DB.WithContext(ctx).Order("host").Find(&hosts).Error
	return hosts, err
}

func (r *SSHHostTrustRepo) CreatePlatformGitSSHHost(ctx context.Context, entry *PlatformGitSSHHost) error {
	entry.Revision = rand.Text()
	return r.DB.WithContext(ctx).Create(entry).Error
}

func (r *SSHHostTrustRepo) UpdatePlatformGitSSHHost(ctx context.Context, entry *PlatformGitSSHHost, expectedRevision string) error {
	nextRevision := rand.Text()
	result := r.DB.WithContext(ctx).Model(&PlatformGitSSHHost{}).
		Where(platformGitHostRevisionWhere, entry.Host, entry.Host, expectedRevision).
		Updates(map[string]any{"public_keys": entry.PublicKeys, "revision": nextRevision})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPlatformGitSSHHostChanged
	}
	entry.Revision = nextRevision
	return nil
}

func (r *SSHHostTrustRepo) DeletePlatformGitSSHHost(ctx context.Context, host, expectedRevision string) error {
	result := r.DB.WithContext(ctx).
		Where(platformGitHostRevisionWhere, host, host, expectedRevision).
		Delete(&PlatformGitSSHHost{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPlatformGitSSHHostChanged
	}
	return nil
}

// PlatformSSHKnownHosts returns only the configured records needed for this
// Git operation. The input scopes use the standard host:port representation.
func (r *SSHHostTrustRepo) PlatformSSHKnownHosts(ctx context.Context, scopes []string) ([]byte, error) {
	var entries []PlatformGitSSHHost
	if err := r.DB.WithContext(ctx).Where("host IN ?", scopes).Find(&entries).Error; err != nil {
		return nil, err
	}
	var knownHosts strings.Builder
	for _, entry := range entries {
		// The database collation can also match noncanonical rows. Only exact
		// endpoint matches are allowed to contribute trusted keys.
		if !slices.Contains(scopes, entry.Host) {
			continue
		}
		keys, _, err := gitwork.NormalizeSSHHostPublicKeys(entry.PublicKeys)
		if err != nil {
			continue
		}
		host := knownhosts.Normalize(entry.Host)
		for publicKey := range strings.SplitSeq(keys, "\n") {
			fmt.Fprintf(&knownHosts, "%s %s\n", host, publicKey)
		}
	}
	return []byte(knownHosts.String()), nil
}
