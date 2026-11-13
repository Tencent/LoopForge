package gitwork

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
)

var ErrPrivateHTTPSDestination = errors.New("GitLab 地址指向非公网目标，需由部署管理员允许该主机")

// ResolveHTTPSDestination checks every DNS answer before the caller connects
// to one of them. Explicitly trusted private hosts are a deployment setting.
func ResolveHTTPSDestination(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	trusted := slices.ContainsFunc(strings.Split(os.Getenv("AGENTFLOW_GITLAB_PRIVATE_HOSTS"), ","), func(value string) bool {
		return strings.EqualFold(strings.TrimSpace(value), host)
	})
	if !trusted {
		for _, ip := range ips {
			ip = ip.Unmap()
			if !publicHTTPSAddress(ip) {
				return nil, ErrPrivateHTTPSDestination
			}
		}
	}
	return ips, nil
}

// Globally scoped IPv6 unicast is currently 2000::/3. Special-use
// ranges within it and IPv4 ranges not covered by netip are excluded.
var blockedHTTPSNetworks = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}
var globalIPv6 = netip.MustParsePrefix("2000::/3")

func publicHTTPSAddress(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && (!ip.Is6() || globalIPv6.Contains(ip)) &&
		!slices.ContainsFunc(blockedHTTPSNetworks, func(network netip.Prefix) bool { return network.Contains(ip) })
}
