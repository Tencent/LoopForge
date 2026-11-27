package domain

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	httpsHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	allDigits      = regexp.MustCompile(`^[0-9]+$`)
	hexNumber      = regexp.MustCompile(`^0x[0-9a-f]+$`)
	errHTTPSHost   = errors.New("请输入 HTTPS Git 主机名或 IPv6 地址。IPv6 带端口时请用 [fd00::2]:8443")
	errHTTPHost    = errors.New("请输入有效的 HTTP Git 主机或 IP 地址。IPv6 请用方括号")
)

type GitHTTPEndpoint struct {
	Scheme    string
	Authority string
}

// Scope keeps existing HTTPS identifiers and includes the scheme for HTTP.
func (e GitHTTPEndpoint) Scope() string {
	if e.Scheme == "http" {
		return e.URL()
	}
	return e.Authority
}

func (e GitHTTPEndpoint) URL() string { return e.Scheme + "://" + e.Authority }

func (e GitHTTPEndpoint) Hostname() string { return (&url.URL{Host: e.Authority}).Hostname() }

func (e GitHTTPEndpoint) Port() string {
	if port := (&url.URL{Host: e.Authority}).Port(); port != "" {
		return port
	}
	return gitHTTPDefaultPort(e.Scheme)
}

func gitHTTPDefaultPort(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// HTTPSHostScope is the canonical HTTPS authority for Git credentials and
// delivery links. The default port is omitted and IPv6 addresses are bracketed.
func HTTPSHostScope(raw string) (string, error) {
	endpoint, err := ParseGitHTTPEndpoint(raw)
	if err != nil || endpoint.Scheme != "https" {
		return "", errHTTPSHost
	}
	return endpoint.Authority, nil
}

// ParseGitHTTPEndpoint parses an explicit HTTP(S) endpoint or a legacy HTTPS
// authority. Repository paths and URL credentials are excluded.
func ParseGitHTTPEndpoint(raw string) (GitHTTPEndpoint, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	scheme, rest, hasScheme := strings.Cut(s, "://")
	invalid := errHTTPSHost
	if hasScheme {
		switch scheme {
		case "http":
			invalid = errHTTPHost
		case "https":
		default:
			return GitHTTPEndpoint{}, invalid
		}
		s = rest
	} else {
		scheme = "https"
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" || strings.ContainsAny(s, "/@?#*% \t\r\n") {
		return GitHTTPEndpoint{}, invalid
	}
	if !hasScheme && strings.Count(s, ":") > 1 && !strings.HasPrefix(s, "[") {
		// A bare IPv6 address uses the default port. An explicit port uses brackets.
		s = "[" + s + "]"
	}

	host, port := s, ""
	bracketed := strings.HasPrefix(s, "[")
	switch {
	case bracketed && strings.HasSuffix(s, "]"):
		host = s[1 : len(s)-1]
	case strings.Contains(s, ":"):
		var err error
		host, port, err = net.SplitHostPort(s)
		if err != nil {
			return GitHTTPEndpoint{}, invalid
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return GitHTTPEndpoint{}, invalid
		}
	}
	if bracketed {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" {
			return GitHTTPEndpoint{}, invalid
		}
		host = "[" + ip.String() + "]"
	} else if ip, err := netip.ParseAddr(host); err == nil {
		if scheme != "http" || !ip.Is4() {
			return GitHTTPEndpoint{}, invalid
		}
	} else if !validHTTPSHostName(host) || (scheme == "http" && hexNumber.MatchString(host[strings.LastIndexByte(host, '.')+1:])) {
		return GitHTTPEndpoint{}, invalid
	}
	if port != "" && port != gitHTTPDefaultPort(scheme) {
		host += ":" + port
	}
	return GitHTTPEndpoint{Scheme: scheme, Authority: host}, nil
}

func validHTTPSHostName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) > 63 || !httpsHostLabel.MatchString(label) {
			return false
		}
	}
	return !allDigits.MatchString(labels[len(labels)-1])
}
