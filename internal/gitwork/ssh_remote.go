package gitwork

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type SSHRemote struct {
	URL, User, Host, Path string
	Port                  string
}

func (r SSHRemote) Endpoint() string { return net.JoinHostPort(r.Host, r.Port) }

func (r SSHRemote) aliasURL(alias string) string {
	if strings.HasPrefix(r.URL, "ssh://") {
		return "ssh://" + r.User + "@" + net.JoinHostPort(alias, r.Port) + r.Path
	}
	return r.User + "@" + alias + ":" + r.Path
}

// SSHHostScope normalizes a hostname with an optional port, or an SSH URL.
func SSHHostScope(raw string) (string, error) {
	if remote, err := ParseSSHRemote(raw); err == nil {
		return remote.Endpoint(), nil
	}
	address := strings.TrimSpace(raw)
	if ip, err := netip.ParseAddr(address); err == nil && ip.Zone() == "" {
		return net.JoinHostPort(ip.String(), "22"), nil
	}
	if !strings.ContainsAny(address, "/@") {
		if remote, err := ParseSSHRemote("ssh://git@" + address + "/repo"); err == nil {
			return remote.Endpoint(), nil
		}
	}
	return "", fmt.Errorf("请输入 SSH 主机名，可附带端口，例如 gitlab.com 或 git.example.org:2222")
}

var (
	sshUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	sshHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	sshPath = regexp.MustCompile(`^[A-Za-z0-9_.~/-]+$`)
	sshSCP  = regexp.MustCompile(`^([^@]+)@(\[[^\]]+\]|[^:/]+):(.+)$`)
)

// IsSSHReference distinguishes an explicit SSH clone address from a web or
// legacy project link, including malformed addresses that must be rejected.
func IsSSHReference(raw string) bool {
	s := trimCloneReference(raw)
	lower := strings.ToLower(s)
	if scheme, _, ok := strings.Cut(lower, "://"); ok {
		return scheme == "ssh" || scheme == "git+ssh" || scheme == "ssh+git"
	}
	if i := strings.Index(s, "::"); i >= 0 && isTransportName(s[:i]) {
		return false
	}
	_, scp := scpHost(s)
	return scp
}

func trimCloneReference(raw string) string {
	s := strings.Trim(strings.TrimSpace(raw), `"'`)
	return strings.Trim(strings.TrimSpace(strings.TrimPrefix(s, "git clone ")), `"'`)
}

// ParseSSHRemote accepts a Git SSH URL or an scp-style clone address, without
// credentials, shell metacharacters or implicit use of the host account.
func ParseSSHRemote(raw string) (SSHRemote, error) {
	s := trimCloneReference(raw)
	invalid := fmt.Errorf("请使用 Git SSH 地址，例如 git@gitlab.com:group/repo.git 或 ssh://git@git.example.org:2222/group/repo.git")
	if len(s) > 2048 || strings.ContainsAny(s, " \t\r\n%?#\\") {
		return SSHRemote{}, invalid
	}
	var user, host, path string
	port := "22"
	if strings.HasPrefix(s, "ssh://") {
		u, err := url.Parse(s)
		if err != nil || u.User == nil || strings.HasSuffix(u.Host, ":") {
			return SSHRemote{}, invalid
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return SSHRemote{}, invalid
		}
		user, host, path = u.User.Username(), u.Hostname(), u.Path
		if u.Port() != "" {
			p, err := strconv.Atoi(u.Port())
			if err != nil || p < 1 || p > 65535 {
				return SSHRemote{}, invalid
			}
			port = strconv.Itoa(p)
		}
	} else if m := sshSCP.FindStringSubmatch(s); m != nil {
		user, host, path = m[1], strings.Trim(m[2], "[]"), m[3]
	} else {
		return SSHRemote{}, invalid
	}
	host = strings.ToLower(host)
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else if !sshHost.MatchString(host) || strings.Contains(host, "..") {
		return SSHRemote{}, invalid
	}
	if !sshUser.MatchString(user) || !sshPath.MatchString(path) {
		return SSHRemote{}, invalid
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return SSHRemote{}, invalid
		}
	}
	if strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".git") == "" {
		return SSHRemote{}, invalid
	}
	// Preserve the distinction between an absolute SSH URL and a path
	// relative to the account's home directory (scp form).
	if strings.HasPrefix(s, "ssh://") {
		s = "ssh://" + user + "@" + net.JoinHostPort(host, port) + path
	} else {
		hostPart := host
		if strings.Contains(host, ":") {
			hostPart = "[" + host + "]"
		}
		s = user + "@" + hostPart + ":" + path
	}
	return SSHRemote{URL: s, User: user, Host: host, Path: path, Port: port}, nil
}
