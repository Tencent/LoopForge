package gitwork

import (
	"net/url"
	"strings"

	"agentflow/internal/domain"
)

// HTTPSHostScope returns the canonical host used by the Git credential helper.
func HTTPSHostScope(raw string) (string, error) {
	return domain.HTTPSHostScope(raw)
}

// HTTPSRemoteHost returns the credential host scope of an https:// remote URL.
func HTTPSRemoteHost(remote string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "", false
	}
	host, err := HTTPSHostScope("https://" + u.Host)
	if err != nil {
		return "", false
	}
	return host, true
}
