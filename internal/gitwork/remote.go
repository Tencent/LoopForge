package gitwork

import (
	"fmt"
	"net/url"
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
// executing host, which only platform administrators may choose.
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
		return nil
	}
	if host, ok := scpHost(s); ok {
		if host == "" || strings.HasPrefix(host, "-") {
			return fmt.Errorf("仓库地址主机名无效")
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
