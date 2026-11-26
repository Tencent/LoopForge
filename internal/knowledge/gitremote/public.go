package gitremote

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

// PublicAddresses validates every DNS answer before a caller pins the connection.
func PublicAddresses(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IPAddr, error)) ([]net.IPAddr, error) {
	addresses, err := lookup(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("cannot resolve public host")
	}
	for _, a := range addresses {
		if !IsPublicIP(a.IP) {
			return nil, errors.New("host must resolve only to public addresses")
		}
	}
	return addresses, nil
}

func IsPublicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !reservedPublicIP(ip)
}

// Exclude non-public ranges that net.IP.IsPrivate intentionally does not cover.
func reservedPublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32"} {
		if netip.MustParsePrefix(s).Contains(a) {
			return true
		}
	}
	return false
}
