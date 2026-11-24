package domain

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PlatformSettings holds the platform-wide settings a platform administrator
// maintains on the platform management page, for values that must be
// changeable without a redeploy. It is stored as one row and read on use, so
// a change applies to the next operation rather than the next restart.
type PlatformSettings struct {
	// PublicURL is the address users reach this platform on. It is the one
	// the platform puts into links it sends elsewhere: the account binding
	// link and the write confirmation link of chat Bots, and the run detail
	// link of notifications. Empty falls back to AGENTFLOW_PUBLIC_URL.
	PublicURL string    `json:"publicUrl,omitempty"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// PublicURLMaxLen bounds the stored address.
const PublicURLMaxLen = 512

// ErrPublicURLLoopback reports an address only this machine can open. It is
// returned alongside a usable value: a loopback address is accepted for local
// trials but cannot work in a link sent to somebody's phone, so the caller
// warns instead of refusing.
var ErrPublicURLLoopback = errors.New("回环地址只有本机能打开，发到 IM 里点不开，仅适合本机联调")

// NormalizePublicURL validates an external platform address and returns it
// without a trailing slash.
//
// A loopback address returns the normalized value together with
// ErrPublicURLLoopback; every other error returns an empty value.
func NormalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > PublicURLMaxLen {
		return "", errors.New("地址过长")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("地址格式无法识别，请填写 http(s)://主机 形式")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("地址必须以 http:// 或 https:// 开头")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("地址缺少主机名")
	}
	if parsed.User != nil {
		return "", errors.New("地址不能包含用户名或密码")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("地址不能包含查询参数或锚点")
	}
	if path := strings.TrimSuffix(parsed.Path, "/"); path != "" {
		return "", errors.New("地址不能包含路径，只填到主机和端口")
	}
	if parsed.Port() != "" || strings.HasSuffix(parsed.Host, ":") {
		_, port, err := net.SplitHostPort(parsed.Host)
		if err != nil {
			return "", errors.New("端口格式不正确")
		}
		if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
			return "", errors.New("端口必须为 1 到 65535")
		}
	}
	host := parsed.Hostname()
	// Rebuild from the parsed parts rather than echoing the input: the value
	// is concatenated into links, so the scheme and host must come out in one
	// canonical lower-case form whatever case was typed.
	normalized := parsed.Scheme + "://" + strings.ToLower(parsed.Host)
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsUnspecified():
			// 0.0.0.0 / :: is what the platform listens on, never an address
			// anybody can open.
			return "", errors.New("这是监听地址，不是访问地址，请填服务器的 IP 或域名")
		case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
			return "", errors.New("链路本地地址无法作为访问地址")
		case ip.IsLoopback():
			return normalized, ErrPublicURLLoopback
		}
	}
	if strings.EqualFold(host, "localhost") {
		return normalized, ErrPublicURLLoopback
	}
	return normalized, nil
}

// UsablePublicURL reports whether an address can be put into a link. It is the
// check the link builders share, so a stored or configured value that cannot
// work is treated the same as none at all.
func UsablePublicURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
