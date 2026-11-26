// Package gitegress bounds the control plane's connections to Git hosts,
// including token verification, PR discovery and host key discovery. A user can
// name any host, so a request only connects to public addresses unless an
// administrator authorizes an HTTPS host or an operator allows a network.
// Workspace traffic never passes here.
package gitegress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"
)

// AllowCIDRsEnv lists comma-separated networks the control plane may reach
// even though they are not public, for example an internal GitLab.
const AllowCIDRsEnv = "AGENTFLOW_GIT_EGRESS_ALLOW_CIDRS"

const (
	timeout        = 30 * time.Second
	connectTimeout = 10 * time.Second
)

var (
	ErrNonPublicAddress    = errors.New("git egress: address is not public")
	ErrHTTPSAddressChanged = errors.New("git egress: HTTPS address is outside the host authorization")
	ErrHTTPSHostLookup     = errors.New("git egress: could not load HTTPS host authorization")
)

// HTTPSHostLookup resolves a host:port to its approved addresses. A nil result
// means the endpoint uses the default egress policy.
type HTTPSHostLookup func(context.Context, string) ([]netip.Addr, error)

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
func Client(lookup HTTPSHostLookup) *http.Client {
	dial := DialContext
	if lookup != nil {
		dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			approved, err := lookup(ctx, address)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrHTTPSHostLookup, err)
			}
			return newDialer(approved).DialContext(ctx, network, address)
		}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
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
	return newDialer(nil).DialContext(ctx, network, address)
}

func newDialer(approved []netip.Addr) *net.Dialer {
	allowed := allowedPrefixes()
	return &net.Dialer{
		Timeout: connectTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkAddress(address, allowed, approved)
		},
	}
}

func checkAddress(address string, allowed []netip.Prefix, approved []netip.Addr) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("git egress: unexpected address %q", address)
	}
	ip := ap.Addr().Unmap()
	if approved != nil {
		if slices.Contains(approved, ip) {
			return nil
		}
		return ErrHTTPSAddressChanged
	}
	for _, prefix := range allowed {
		if prefix.Contains(ip) {
			return nil
		}
	}
	if !public(ip) {
		return fmt.Errorf("%w: %s; allow it in %s", ErrNonPublicAddress, ip, AllowCIDRsEnv)
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
