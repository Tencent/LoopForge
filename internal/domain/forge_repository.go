package domain

import (
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	RepositoryProviderForgejo = "forgejo"
	forgeProjectPrefix        = "forge:"
)

func IsForgeRepositoryProvider(provider string) bool {
	return provider == RepositoryProviderForgejo
}

var (
	forgeURLSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	forgeHostname   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
)

// ForgeRepository identifies a same-instance Forgejo repository.
// Instance is the HTTPS installation root, which may include a path prefix.
type ForgeRepository struct {
	Instance string
	Owner    string
	Name     string
}

func (r ForgeRepository) Path() string   { return r.Owner + "/" + r.Name }
func (r ForgeRepository) WebURL() string { return r.Instance + "/" + r.Path() }
func (r ForgeRepository) Project() string {
	return forgeProjectPrefix + r.WebURL()
}
func (r ForgeRepository) PullURL(number int) string {
	return r.WebURL() + "/pulls/" + strconv.Itoa(number)
}

// NormalizeForgeInstance rejects ambiguous URLs and normalizes the authority
// before it is compared against the deployment's exact instance allowlist.
func NormalizeForgeInstance(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if len(raw) > 255 || strings.ContainsAny(raw, "%?#\\") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else if !forgeHostname.MatchString(host) || strings.Contains(host, "..") || strings.HasPrefix(u.Host, "[") {
		return "", false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		if n != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(n))
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if u.Path != "" {
		for _, part := range strings.Split(strings.TrimPrefix(u.Path, "/"), "/") {
			if !forgeURLSegment.MatchString(part) {
				return "", false
			}
		}
	}
	return "https://" + host + u.Path, true
}

// ParseForgeRepositoryURL takes a web URL (or HTTPS clone URL) and separates
// the final owner/name pair from the installation root.
func ParseForgeRepositoryURL(raw string) (ForgeRepository, bool) {
	raw = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(raw), "/"), ".git")
	normalized, ok := NormalizeForgeInstance(raw)
	if !ok || len(normalized)+len(forgeProjectPrefix) > 255 {
		return ForgeRepository{}, false
	}
	u, _ := url.Parse(normalized)
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ForgeRepository{}, false
	}
	path := strings.Join(parts[len(parts)-2:], "/")
	return ForgeRepository{
		Instance: strings.TrimSuffix(normalized, "/"+path),
		Owner:    strings.ToLower(parts[len(parts)-2]),
		Name:     strings.ToLower(parts[len(parts)-1]),
	}, true
}

func ParseForgeProject(project string) (ForgeRepository, bool) {
	raw, ok := strings.CutPrefix(project, forgeProjectPrefix)
	if !ok {
		return ForgeRepository{}, false
	}
	r, ok := ParseForgeRepositoryURL(raw)
	return r, ok && r.Project() == project
}

// ParseForgePullURL parses an exact, credential-free web URL. Free-text
// artifact discovery does not infer a forge or an API destination from a link.
func ParseForgePullURL(raw string) (ForgeRepository, int, bool) {
	raw = strings.TrimSpace(raw)
	index := strings.LastIndex(raw, "/pulls/")
	if index < 0 {
		return ForgeRepository{}, 0, false
	}
	repository, suffix := raw[:index], raw[index+len("/pulls/"):]
	number, err := strconv.Atoi(suffix)
	if err != nil || number < 1 || strconv.Itoa(number) != suffix {
		return ForgeRepository{}, 0, false
	}
	r, ok := ParseForgeRepositoryURL(repository)
	return r, number, ok
}
