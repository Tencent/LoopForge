package gitwork

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// AllowedProtocols is the GIT_ALLOW_PROTOCOL list for every git process the
// platform starts. Remote helpers such as ext:: and fd:: run arbitrary
// commands, so they stay disabled even when a git config enables them.
const AllowedProtocols = "https:http:ssh:git:file"

var networkSchemes = map[string]bool{"https": true, "http": true, "ssh": true, "git": true, "git+ssh": true, "ssh+git": true}

// ValidateRemoteURL rejects repository URLs git could treat as an option, a
// remote helper (`<transport>::<address>`) or an unsupported scheme. Local
// paths and file:// URLs are accepted only with allowLocal: they read from the
// executing host, which only platform administrators may choose. Without
// allowLocal, hosts that name the executing machine or its link-local network
// (loopback, link-local such as cloud metadata, unspecified, multicast) are
// rejected too; private ranges stay allowed for self-hosted Git servers.
func ValidateRemoteURL(raw string, allowLocal bool) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("仓库地址为空")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("仓库地址不能以 - 开头")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("仓库地址包含控制字符")
		}
	}
	if i := strings.Index(s, "::"); i >= 0 && isTransportName(s[:i]) {
		return fmt.Errorf("不支持的仓库地址协议 %q", s[:i])
	}
	if i := strings.Index(s, "://"); i >= 0 {
		scheme := strings.ToLower(s[:i])
		if scheme == "file" {
			if !allowLocal {
				return fmt.Errorf("仅平台管理员可以使用本地仓库路径")
			}
			return nil
		}
		if !networkSchemes[scheme] {
			return fmt.Errorf("不支持的仓库地址协议 %q，请使用 https 或 ssh", scheme)
		}
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return fmt.Errorf("仓库地址无法解析")
		}
		if strings.HasPrefix(u.Hostname(), "-") {
			return fmt.Errorf("仓库地址主机名不能以 - 开头")
		}
		if !allowLocal {
			return checkRemoteHost(u.Hostname())
		}
		return nil
	}
	if host, ok := scpHost(s); ok {
		if host == "" || strings.HasPrefix(host, "-") {
			return fmt.Errorf("仓库地址主机名无效")
		}
		if !allowLocal {
			return checkRemoteHost(host)
		}
		return nil
	}
	if !allowLocal {
		return fmt.Errorf("仅平台管理员可以使用本地仓库路径")
	}
	return nil
}

// isTransportName mirrors git's check for `<transport>::<address>`: the
// prefix consists only of RFC 3986 unreserved characters.
func isTransportName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' || r == '~' || r == '+') {
			return false
		}
	}
	return true
}

// scpHost returns the host of an scp-like `[user@]host:path` remote. Git
// treats such a string as ssh when a colon comes before the first slash.
func scpHost(s string) (string, bool) {
	colon := strings.Index(s, ":")
	if colon < 0 {
		return "", false
	}
	// `user@[v6]:path`: the address's own colons come before the separator.
	if at := strings.Index(s, "@["); at >= 0 && at < colon {
		if end := strings.Index(s, "]:"); end > at && !strings.Contains(s[:at], "/") {
			return s[at+2 : end], true
		}
	}
	if slash := strings.Index(s, "/"); slash >= 0 && slash < colon {
		return "", false
	}
	hostPart := s[:colon]
	if strings.HasPrefix(hostPart, "[") {
		end := strings.Index(s, "]:")
		if end < 0 {
			return "", false
		}
		hostPart = s[1:end]
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	return hostPart, true
}

// checkRemoteHost rejects hosts that point git at the executing machine or
// its link-local network. It looks only at the literal host: names other than
// localhost are not resolved here, so a DNS name pointing at loopback is not
// caught, and neither is an HTTP redirect followed by git.
func checkRemoteHost(host string) error {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return fmt.Errorf("仓库地址不能指向本机（%s），仅平台管理员可以使用", host)
	}
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	ip := net.ParseIP(h)
	if ip == nil {
		ip = parseLegacyIPv4(h)
	}
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback(), ip.IsUnspecified():
		return fmt.Errorf("仓库地址不能指向本机（%s），仅平台管理员可以使用", host)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return fmt.Errorf("仓库地址不能指向链路本地或组播地址（%s）", host)
	case len(ip) == net.IPv4len && ip[0] == 0:
		return fmt.Errorf("仓库地址不能指向本机（%s），仅平台管理员可以使用", host)
	}
	return nil
}

// parseLegacyIPv4 parses the inet_aton forms curl and git still accept, such
// as 127.1, 0x7f.0.0.1, 0177.0.0.1 and 2130706433, which net.ParseIP rejects.
func parseLegacyIPv4(s string) net.IP {
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return nil
	}
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return nil
		}
		base, digits := 10, p
		switch {
		case len(p) > 2 && (p[:2] == "0x" || p[:2] == "0X"):
			base, digits = 16, p[2:]
		case len(p) > 1 && p[0] == '0':
			base, digits = 8, p[1:]
		}
		v, err := strconv.ParseUint(digits, base, 32)
		if err != nil || strings.ContainsAny(digits, "+-_") {
			return nil
		}
		vals[i] = v
	}
	var n uint64
	last := len(vals) - 1
	for i := 0; i < last; i++ {
		if vals[i] > 0xff {
			return nil
		}
		n |= vals[i] << (8 * (3 - i))
	}
	if vals[last] >= 1<<(8*(4-last)) {
		return nil
	}
	n |= vals[last]
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}
