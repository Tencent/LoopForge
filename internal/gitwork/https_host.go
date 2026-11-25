package gitwork

import (
	"net/url"
	"strconv"
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

// WithPlainHTTPRedirectsDisabled applies the rule to the full remote URL.
// Git prefers longer URL path matches even when the host rule is command scoped.
func WithPlainHTTPRedirectsDisabled(env []string, remote *url.URL) []string {
	urlMatch := *remote
	if urlMatch.User != nil {
		urlMatch.User = url.User(urlMatch.User.Username())
	}
	count := 0
	for _, entry := range env {
		if raw, ok := strings.CutPrefix(entry, "GIT_CONFIG_COUNT="); ok {
			count, _ = strconv.Atoi(raw)
		}
	}
	index := strconv.Itoa(count)
	env = WithEnvValue(env, "GIT_CONFIG_COUNT", strconv.Itoa(count+1))
	env = WithEnvValue(env, "GIT_CONFIG_KEY_"+index, "http."+urlMatch.String()+".followRedirects")
	return WithEnvValue(env, "GIT_CONFIG_VALUE_"+index, "false")
}
