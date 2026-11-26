package gitsync

import (
	"context"
	"net"
	"slices"
	"testing"
)

func TestPublicHTTPSBoundary(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1", "0.0.0.0"} {
		_, err := publicOptions(context.Background(), "https://example.com/repo.git", func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
		})
		if err == nil {
			t.Errorf("accepted private destination %s", address)
		}
	}
	opts, err := publicOptions(context.Background(), "https://example.com/repo.git", func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"http.followRedirects=false", "http.proxy=", "http.curloptResolve=example.com:443:93.184.216.34"} {
		if !slices.Contains(opts, required) {
			t.Fatalf("missing connection protection %s", required)
		}
	}
}

func TestSSHRejectsPrivateAndMixedDNS(t *testing.T) {
	for _, remote := range []string{"ssh://git@example.com/repo.git", "git@example.com:repo.git"} {
		for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1", "100.64.0.1", "198.18.0.1", "2001:db8::1"} {
			_, err := publicOptions(context.Background(), remote, func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP(address)}}, nil
			})
			if err == nil {
				t.Fatalf("SSH accepted private DNS %s", address)
			}
		}
		options, err := publicOptions(context.Background(), remote, func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		})
		if err != nil || !slices.Contains(options, "ssh.pinnedIP=93.184.216.34") {
			t.Fatalf("SSH public address not pinned: %v %v", options, err)
		}
	}
}
