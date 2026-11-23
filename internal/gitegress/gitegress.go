// Package gitegress bounds the control plane's connections to Git hosts,
// including token verification, PR discovery and host key discovery. A user can
// name any host, so a request only connects to public addresses unless an
// operator allows a private network. Workspace traffic never passes here.
package gitegress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// AllowCIDRsEnv lists comma-separated networks the control plane may reach
// even though they are not public, for example an internal GitLab.
const AllowCIDRsEnv = "AGENTFLOW_GIT_EGRESS_ALLOW_CIDRS"

const timeout = 30 * time.Second

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Client returns an HTTP client for user-configured Git hosts. Each
// connection checks the address it actually dials, after DNS resolution, so
// a name cannot resolve to a vetted address and then connect elsewhere.
// Redirects are returned to the caller instead of followed, and no proxy is
// used because a proxy would connect on the client's behalf. Clients do not
// keep connections, so each call follows the current allow list.
func Client() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           newDialer().DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// DialContext applies the Git egress policy to the address selected by DNS.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return newDialer().DialContext(ctx, network, address)
}

func newDialer() *net.Dialer {
	allowed := allowedPrefixes()
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkAddress(address, allowed)
		},
	}
}

func checkAddress(address string, allowed []netip.Prefix) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("git egress: unexpected address %q", address)
	}
	ip := ap.Addr().Unmap()
	for _, prefix := range allowed {
		if prefix.Contains(ip) {
			return nil
		}
	}
	if !public(ip) {
		return fmt.Errorf("git egress: %s is not a public address; allow it in %s", ip, AllowCIDRsEnv)
	}
	return nil
}

func public(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func allowedPrefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, raw := range strings.Split(os.Getenv(AllowCIDRsEnv), ",") {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil {
			out = append(out, prefix.Masked())
		}
	}
	return out
}
