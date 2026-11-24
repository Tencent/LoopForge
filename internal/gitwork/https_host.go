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
	endpoint, ok := GitHTTPRemoteEndpoint(remote)
	if !ok || endpoint.Scheme != "https" {
		return "", false
	}
	return endpoint.Authority, true
}

func GitHTTPRemoteEndpoint(remote string) (domain.GitHTTPEndpoint, bool) {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return domain.GitHTTPEndpoint{}, false
	}
	endpoint, err := domain.ParseGitHTTPEndpoint(u.Scheme + "://" + u.Host)
	return endpoint, err == nil
}

// GitCredentialScope interprets Git's separate protocol and host attributes.
func GitCredentialScope(protocol, host string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "https":
		scope, err := HTTPSHostScope(host)
		return scope, err == nil
	case "http":
		endpoint, err := domain.ParseGitHTTPEndpoint("http://" + host)
		if err == nil && endpoint.Scheme == "http" {
			return endpoint.Scope(), true
		}
	}
	return "", false
}

// PlainHTTPRedirectConfig is the Git config key for one canonical HTTP scope.
func PlainHTTPRedirectConfig(scope string) string {
	return "http." + scope + "/.followRedirects"
}
