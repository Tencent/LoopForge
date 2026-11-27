package mcpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	_, cgnatRange, _      = net.ParseCIDR("100.64.0.0/10")
	_, benchmarkRange, _  = net.ParseCIDR("198.18.0.0/15")
	_, nat64Range, _      = net.ParseCIDR("64:ff9b::/96")
	_, currentNetRange, _ = net.ParseCIDR("0.0.0.0/8")
)

func newGuardedTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           newGuardedDialContext(15 * time.Second),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// The generic client permits loopback/private MCP servers, but never connects
// to metadata, link-local or other special-use ranges. DNS results are checked
// and dialed directly so a rebinding cannot switch the destination afterwards.
func newGuardedDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return guardedDialContext(net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

// guardedDialContext resolves a hostname once, rejects every disallowed address
// and dials the checked IP instead of the host, so the resolver cannot hand a
// different (unchecked) address to the socket layer.
func guardedDialContext(
	lookupIP func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("mcp address is invalid")
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); ip != nil {
			if isBlockedDialIP(ip) {
				return nil, fmt.Errorf("mcp address is not allowed")
			}
			return dial(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		ips, err := lookupIP(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("mcp address cannot be resolved: %w", err)
		}
		var lastErr error
		for _, candidate := range ips {
			if isBlockedDialIP(candidate.IP) {
				continue
			}
			conn, dialErr := dial(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
			if ctx.Err() != nil {
				return nil, lastErr
			}
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("mcp address is not allowed")
	}
}

func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	return cgnatRange.Contains(ip) || benchmarkRange.Contains(ip) ||
		nat64Range.Contains(ip) || currentNetRange.Contains(ip)
}
