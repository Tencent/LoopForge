package mcpclient

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// fakeDial records the address the guarded dialer asked the socket layer to
// connect to and never touches the network.
func fakeDial(dialed *[]string) func(context.Context, string, string) (net.Conn, error) {
	return func(_ context.Context, network, address string) (net.Conn, error) {
		*dialed = append(*dialed, network+" "+address)
		return nil, nil
	}
}

func fakeLookup(host string, answers []string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(_ context.Context, asked string) ([]net.IPAddr, error) {
		if asked != host {
			return nil, errors.New("unexpected lookup for " + asked)
		}
		ips := make([]net.IPAddr, 0, len(answers))
		for _, answer := range answers {
			ip := net.ParseIP(answer)
			if ip == nil {
				return nil, errors.New("bad test answer " + answer)
			}
			ips = append(ips, net.IPAddr{IP: ip})
		}
		return ips, nil
	}
}

// A hostname is resolved once and the checked IP is dialed, so a rebinding
// resolver cannot swap the destination between the check and the connect.
func TestGuardedDialDialsResolvedIP(t *testing.T) {
	var dialed []string
	dial := guardedDialContext(fakeLookup("docs.example.com", []string{"93.184.216.34"}), fakeDial(&dialed))
	if _, err := dial(context.Background(), "tcp", "docs.example.com:8443"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "tcp 93.184.216.34:8443" {
		t.Fatalf("dialed %v, want the resolved IP", dialed)
	}
}

// A blocked answer is skipped in favour of an allowed one; every dial target
// stays a checked IP literal.
func TestGuardedDialSkipsBlockedAnswer(t *testing.T) {
	var dialed []string
	dial := guardedDialContext(fakeLookup("mixed.example.com", []string{"169.254.169.254", "100.64.0.1", "93.184.216.34"}), fakeDial(&dialed))
	if _, err := dial(context.Background(), "tcp", "mixed.example.com:80"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "tcp 93.184.216.34:80" {
		t.Fatalf("dialed %v", dialed)
	}
}

func TestGuardedDialBlocksForbiddenAddresses(t *testing.T) {
	blocked := []string{
		"169.254.169.254",
		"fe80::1",
		"ff02::1",
		"100.64.0.1",
		"100.100.100.200",
		"198.18.0.1",
		"64:ff9b::a00:1",
		"0.1.2.3",
		"0.0.0.0",
		"::",
	}
	for _, ip := range blocked {
		t.Run("literal "+ip, func(t *testing.T) {
			var dialed []string
			dial := guardedDialContext(fakeLookup("unused.example.com", nil), fakeDial(&dialed))
			_, err := dial(context.Background(), "tcp", net.JoinHostPort(ip, "443"))
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("literal %s accepted: %v", ip, err)
			}
			if len(dialed) != 0 {
				t.Fatalf("literal %s dialed %v", ip, dialed)
			}
		})
		t.Run("resolved "+ip, func(t *testing.T) {
			var dialed []string
			dial := guardedDialContext(fakeLookup("rebind.example.com", []string{ip}), fakeDial(&dialed))
			_, err := dial(context.Background(), "tcp", "rebind.example.com:443")
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("resolved %s accepted: %v", ip, err)
			}
			if len(dialed) != 0 {
				t.Fatalf("resolved %s dialed %v", ip, dialed)
			}
		})
	}
}

func TestGuardedDialAllowsLoopbackPrivateAndPublicAddresses(t *testing.T) {
	for _, ip := range []string{
		"127.0.0.1",
		"::1",
		"10.1.2.3",
		"172.16.5.4",
		"192.168.1.1",
		"fd00::1",
		"8.8.8.8",
		"93.184.216.34",
		"2606:4700:4700::1111",
	} {
		var dialed []string
		dial := guardedDialContext(fakeLookup("unused.example.com", nil), fakeDial(&dialed))
		if _, err := dial(context.Background(), "tcp", net.JoinHostPort(ip, "443")); err != nil {
			t.Fatalf("allowed address %s refused: %v", ip, err)
		}
		if len(dialed) != 1 {
			t.Fatalf("allowed address %s dial %v", ip, dialed)
		}
	}
}

func TestGuardedDialRejectsUnresolvableAndMalformedAddresses(t *testing.T) {
	var dialed []string
	dial := guardedDialContext(func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("dns down")
	}, fakeDial(&dialed))
	if _, err := dial(context.Background(), "tcp", "gone.example.com:443"); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("unresolvable accepted: %v", err)
	}
	if _, err := dial(context.Background(), "tcp", "missing-port"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("malformed address accepted: %v", err)
	}
	if len(dialed) != 0 {
		t.Fatalf("dialed %v", dialed)
	}
}

// The production transport keeps direct dialing and never picks up HTTP(S)_PROXY.
func TestNewGuardedTransportHasNoProxy(t *testing.T) {
	tr := newGuardedTransport()
	if tr.Proxy != nil || tr.DialContext == nil {
		t.Fatalf("guarded transport must dial directly without a proxy")
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "169.254.169.254:80"); err == nil {
		t.Fatalf("default guarded transport accepted metadata address")
	}
}

func TestGuardedDialFallsBackAcrossAllowedAddressesSkippingBlocked(t *testing.T) {
	var dialed []string
	dial := guardedDialContext(
		fakeLookup("dual.example.com", []string{"fd00::1", "169.254.169.254", "93.184.216.34"}),
		func(_ context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, network+" "+address)
			if address == "[fd00::1]:443" {
				return nil, errors.New("network is unreachable")
			}
			return nil, nil
		},
	)
	if _, err := dial(context.Background(), "tcp", "dual.example.com:443"); err != nil {
		t.Fatalf("expected fallback to second allowed IP, got %v", err)
	}
	if len(dialed) != 2 || dialed[0] != "tcp [fd00::1]:443" || dialed[1] != "tcp 93.184.216.34:443" {
		t.Fatalf("dialed %v, want fallback skipping blocked IP", dialed)
	}
}

func TestGuardedDialReturnsDialErrorWhenAllAllowedFail(t *testing.T) {
	var dialed []string
	dial := guardedDialContext(
		fakeLookup("down.example.com", []string{"169.254.169.254", "93.184.216.34"}),
		func(_ context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, network+" "+address)
			return nil, errors.New("connection refused")
		},
	)
	_, err := dial(context.Background(), "tcp", "down.example.com:443")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected dial error, got %v", err)
	}
	if len(dialed) != 1 || dialed[0] != "tcp 93.184.216.34:443" {
		t.Fatalf("dialed %v", dialed)
	}
}
