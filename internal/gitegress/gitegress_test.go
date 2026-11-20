package gitegress

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientRefusesNonPublicAddressesUnlessAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	t.Setenv(AllowCIDRsEnv, "")
	if res, err := Client().Get(srv.URL); err == nil {
		res.Body.Close()
		t.Fatal("loopback must be refused without an allow list")
	}

	t.Setenv(AllowCIDRsEnv, "10.0.0.0/8, 127.0.0.0/8")
	res, err := Client().Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatalf("allowed network: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("redirect must be returned, not followed: HTTP %d", res.StatusCode)
	}
}

func TestPublicAddressClassification(t *testing.T) {
	cases := map[string]bool{
		"140.82.112.3":    true,
		"2606:4700::1111": true,
		"127.0.0.1":       false,
		"10.1.2.3":        false,
		"172.16.0.1":      false,
		"192.168.1.1":     false,
		"169.254.169.254": false,
		"100.64.0.1":      false,
		"0.0.0.0":         false,
		"::1":             false,
		"fd00::1":         false,
		"fe80::1":         false,
		"224.0.0.1":       false,
		"198.18.0.1":      false,
	}
	for raw, want := range cases {
		if got := public(netip.MustParseAddr(raw)); got != want {
			t.Errorf("public(%s) = %v, want %v", raw, got, want)
		}
	}
	if err := checkAddress("[::ffff:127.0.0.1]:443", nil); err == nil {
		t.Error("an IPv4-mapped loopback address must be refused")
	}
}
