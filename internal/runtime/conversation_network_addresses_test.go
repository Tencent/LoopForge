package runtime

import (
	"net"
	"testing"
)

func TestDaemonIPv4AddressesKeepsOnlyGlobalUnicastIPv4(t *testing.T) {
	addresses, err := daemonIPv4Addresses()
	if err != nil {
		t.Fatalf("daemonIPv4Addresses: %v", err)
	}
	if addresses == nil {
		t.Fatal("addresses map is nil")
	}
	for address, ok := range addresses {
		ip := net.ParseIP(address)
		if !ok || ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
			t.Errorf("unexpected daemon address %q", address)
		}
	}
	if addresses["127.0.0.1"] {
		t.Fatal("loopback address collected")
	}
}
