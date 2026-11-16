package domain

import (
	"net/url"
	"strings"
)

// StripURLCredentials removes secrets embedded in a repository URL. HTTP(S)
// and git:// userinfo is always a credential (a token is often sent as the
// user name), so it is dropped. An ssh:// user name such as git@ only names
// the account and is kept; any password after it is dropped. scp-like
// `user@host:path` remotes and local paths cannot carry a password and are
// returned unchanged.
func StripURLCredentials(raw string) string {
	s := strings.TrimSpace(raw)
	i := strings.Index(s, "://")
	if i <= 0 || !strings.Contains(s[i+3:], "@") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	switch strings.ToLower(u.Scheme) {
	case "ssh", "git+ssh", "ssh+git":
		if _, hasPassword := u.User.Password(); !hasPassword {
			return s
		}
		u.User = url.User(u.User.Username())
	default:
		u.User = nil
	}
	return u.String()
}

// URLHasCredentials reports whether StripURLCredentials would change raw.
func URLHasCredentials(raw string) bool {
	return StripURLCredentials(raw) != strings.TrimSpace(raw)
}
