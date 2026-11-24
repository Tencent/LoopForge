package domain

import (
	"errors"
	"testing"
)

// The rules mirror scripts/install-address.py so the installer and the
// platform management page accept exactly the same values.
func TestNormalizePublicURL(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain":           {"https://loopforge.example.com", "https://loopforge.example.com"},
		"trailing_slash":  {"https://loopforge.example.com/", "https://loopforge.example.com"},
		"port":            {"http://203.0.113.10:8090", "http://203.0.113.10:8090"},
		"padded":          {"  https://loopforge.example.com  ", "https://loopforge.example.com"},
		"empty":           {"", ""},
		"blank":           {"   ", ""},
		"ipv6_with_port":  {"http://[fd00::2]:8443", "http://[fd00::2]:8443"},
		"public_ipv6":     {"http://[2001:db8::1]", "http://[2001:db8::1]"},
		"uppercase_proto": {"HTTPS://loopforge.example.com", "https://loopforge.example.com"},
	} {
		got, err := NormalizePublicURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: NormalizePublicURL(%q) = %q, %v; want %q", name, tc.in, got, err, tc.want)
		}
	}

	for name, in := range map[string]string{
		"no_scheme":     "loopforge.example.com",
		"wrong_proto":   "ftp://loopforge.example.com",
		"credentials":   "https://user:pass@loopforge.example.com",
		"password_only": "https://:pass@loopforge.example.com",
		"query":         "https://loopforge.example.com?a=1",
		"fragment":      "https://loopforge.example.com#x",
		"path":          "https://loopforge.example.com/platform",
		"no_host":       "https://",
		"listen_v4":     "http://0.0.0.0:8090",
		"listen_v6":     "http://[::]:8090",
		"link_local":    "http://169.254.1.2:8090",
	} {
		got, err := NormalizePublicURL(in)
		if err == nil || got != "" {
			t.Errorf("%s: NormalizePublicURL(%q) = %q, %v; want a refusal", name, in, got, err)
		}
	}
}

// A loopback address is usable for a local trial but can never work in a link
// sent to somebody's phone, so it comes back with a warning rather than a
// refusal: the caller saves it and says why it may not work.
func TestNormalizePublicURLWarnsAboutLoopback(t *testing.T) {
	for _, in := range []string{"http://localhost:8090", "http://LOCALHOST:8090", "http://127.0.0.1:8090", "http://[::1]:8090"} {
		got, err := NormalizePublicURL(in)
		if !errors.Is(err, ErrPublicURLLoopback) {
			t.Errorf("NormalizePublicURL(%q) error = %v; want ErrPublicURLLoopback", in, err)
		}
		if got == "" {
			t.Errorf("NormalizePublicURL(%q) refused the value instead of warning", in)
		}
	}
}

// UsablePublicURL is what the link builders share: anything it rejects is
// treated as no address at all.
func TestUsablePublicURL(t *testing.T) {
	for _, in := range []string{"https://loopforge.example.com", "http://203.0.113.10:8090", "http://localhost:8090"} {
		if !UsablePublicURL(in) {
			t.Errorf("UsablePublicURL(%q) = false", in)
		}
	}
	for _, in := range []string{"", "   ", "bad", "loopforge.example.com", "ftp://loopforge.example.com", "https://"} {
		if UsablePublicURL(in) {
			t.Errorf("UsablePublicURL(%q) = true", in)
		}
	}
}

// Over-long input is refused before it reaches the column.
func TestNormalizePublicURLLengthLimit(t *testing.T) {
	long := "https://"
	for len(long) <= PublicURLMaxLen {
		long += "a"
	}
	if _, err := NormalizePublicURL(long); err == nil {
		t.Fatal("an over-long address was accepted")
	}
}
