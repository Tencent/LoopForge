package gitwork

import (
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"sync"

	"agentflow/internal/domain"
)

type HTTPConfigEntry struct {
	Key   string
	Value string
}

// PlainHTTPConfig pins a plaintext Git endpoint to its approved addresses and
// keeps credentials on direct requests to that endpoint.
func PlainHTTPConfig(endpoint domain.GitHTTPEndpoint, approved []netip.Addr) []HTTPConfigEntry {
	if endpoint.Scheme != "http" || len(approved) == 0 {
		return nil
	}
	addresses := make([]string, 0, len(approved))
	for _, ip := range approved {
		if !ip.IsGlobalUnicast() || ip.Is4In6() || ip.Zone() != "" {
			return nil
		}
		address := ip.String()
		if ip.Is6() {
			address = "[" + address + "]"
		}
		addresses = append(addresses, address)
	}
	prefix := "http." + endpoint.URL() + "/."
	config := []HTTPConfigEntry{
		{Key: prefix + "followRedirects", Value: "false"},
		{Key: prefix + "proxy"},
		{Key: prefix + "curloptResolve"},
	}
	hostname := endpoint.Hostname()
	if literal, err := netip.ParseAddr(hostname); err == nil {
		if len(approved) != 1 || approved[0] != literal {
			return nil
		}
		return config
	}
	return append(config, HTTPConfigEntry{
		Key: prefix + "curloptResolve", Value: net.JoinHostPort(hostname, endpoint.Port()) + ":" + strings.Join(addresses, ","),
	})
}

// Older Git versions silently ignore unknown config keys. Withhold HTTP
// credentials when the installed Git cannot pin the approved destination.
var SupportsHTTPAddressPinning = sync.OnceValue(func() bool {
	out, err := exec.Command("git", "help", "--config").Output()
	return err == nil && strings.Contains(string(out), "http.curloptResolve\n")
})
