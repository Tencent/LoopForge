// Package gitremote validates source addresses before any network or Git use.
package gitremote

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var sshUser = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

// Parse normalizes scp-like SSH addresses to ssh URLs. HTTPS never accepts
// userinfo. SSH accepts a username only, with its secret configured separately.
func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	fail := errors.New("invalid Git address")
	if strings.ContainsAny(raw, "\r\n\x00") {
		return nil, fail
	}
	if !strings.Contains(raw, "://") {
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if at <= 0 || colon < at {
			return nil, fail
		}
		raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || strings.HasPrefix(u.Hostname(), "-") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path == "" || u.Path == "/" {
		return nil, fail
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			return nil, fail
		}
	case "ssh":
		if u.User != nil {
			if _, password := u.User.Password(); password || !sshUser.MatchString(u.User.Username()) {
				return nil, fail
			}
		}
	default:
		return nil, fail
	}
	return u, nil
}
