package gitlab

import (
	"context"
	"net"
	"net/http"
	"time"

	"agentflow/internal/gitwork"
)

// Connect to a validated IP while preserving the original hostname for TLS.
// Redirects and environment proxies must not choose another destination.
var gitLabTransport = &http.Transport{
	DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := gitwork.ResolveHTTPSDestination(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}
		return nil, err
	},
	IdleConnTimeout:     30 * time.Second,
	TLSHandshakeTimeout: 10 * time.Second,
}
