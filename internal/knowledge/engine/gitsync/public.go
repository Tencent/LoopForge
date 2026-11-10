package gitsync

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
)

type publicGitOptions struct{}

// SyncPublicHTTPS pins DNS to validated public addresses and disables redirects
// and proxies, so Git cannot re-resolve a checked hostname to a private target.
func SyncPublicHTTPS(ctx context.Context, dir, remote, branch string, old map[string]string) (string, string, []FileInfo, []string, []string, error) {
	capabilities, err := exec.CommandContext(ctx, "git", "help", "--config").Output()
	if err != nil || !strings.Contains(strings.ToLower(string(capabilities)), "http.curloptresolve") {
		return "", "", nil, nil, nil, errors.New("Git with http.curloptResolve support is required for public HTTPS sync")
	}
	options, err := publicOptions(ctx, remote, net.DefaultResolver.LookupIPAddr)
	if err != nil {
		return "", "", nil, nil, nil, err
	}
	return Sync(context.WithValue(ctx, publicGitOptions{}, options), dir, remote, branch, old)
}
func publicOptions(ctx context.Context, remote string, lookup func(context.Context, string) ([]net.IPAddr, error)) ([]string, error) {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("public HTTPS URL required")
	}
	addresses, err := lookup(ctx, u.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("cannot resolve public Git host")
	}
	ips := []string{}
	for _, a := range addresses {
		ip := a.IP
		if reservedPublicIP(ip) || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return nil, errors.New("Git host must resolve only to public addresses")
		}
		v := ip.String()
		if strings.Contains(v, ":") {
			v = "[" + v + "]"
		}
		ips = append(ips, v)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return []string{"http.followRedirects=false", "http.proxy=", "http.sslVerify=true", "protocol.allow=never", "protocol.https.allow=always", "http.curloptResolve=" + u.Hostname() + ":" + port + ":" + strings.Join(ips, ",")}, nil
}

// Exclude non-public ranges that net.IP.IsPrivate intentionally does not cover.
func reservedPublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(s).Contains(a) {
			return true
		}
	}
	return false
}
