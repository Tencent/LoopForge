package domain

import (
	"errors"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var (
	httpsHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	allDigits      = regexp.MustCompile(`^[0-9]+$`)
	errHTTPSHost   = errors.New("请输入 HTTPS Git 主机名或 IPv6 地址。IPv6 带端口时请用 [fd00::2]:8443")
)

// HTTPSHostScope is the canonical HTTPS authority for Git credentials and
// delivery links. The default port is omitted and IPv6 addresses are bracketed.
func HTTPSHostScope(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	scheme, rest, hasScheme := strings.Cut(s, "://")
	if hasScheme {
		if scheme != "https" {
			return "", errHTTPSHost
		}
		s = rest
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" || strings.ContainsAny(s, "/@?#*% \t\r\n") {
		return "", errHTTPSHost
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
			return "", errHTTPSHost
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", errHTTPSHost
		}
	}
	if bracketed {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" {
			return "", errHTTPSHost
		}
		host = "[" + ip.String() + "]"
	} else if !validHTTPSHostName(host) {
		return "", errHTTPSHost
	}
	if port == "" || port == "443" {
		return host, nil
	}
	return host + ":" + port, nil
}

func validHTTPSHostName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
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
