package gitwork

import "testing"

func TestHTTPSHostScope(t *testing.T) {
	cases := map[string]string{
		"github.com":                      "github.com",
		" GitHub.COM ":                    "github.com",
		"https://gitlab.corp.example/":    "gitlab.corp.example",
		"git.example.org:8443":            "git.example.org:8443",
		"https://git.example.org:443":     "git.example.org",
		"gitlab":                          "gitlab",
		"xn--fiq228c.example":             "xn--fiq228c.example",
		"HTTPS://Code.Example.Org:8443/":  "code.example.org:8443",
		"a-b.example.com":                 "a-b.example.com",
		"ghe.example.com:65535":           "ghe.example.com:65535",
		"ghe.example.com:1":               "ghe.example.com:1",
		"https://ghe.example.com:08443":   "",
		"http://github.com":               "",
		"ssh://git@github.com":            "",
		"github.com/org/repo":             "",
		"user@github.com":                 "",
		"*.github.com":                    "",
		"10.0.0.1":                        "",
		"FD00:0:0:0:0:0:0:2":              "[fd00::2]",
		"[fd00::2]":                       "[fd00::2]",
		"https://[FD00::2]:443/":          "[fd00::2]",
		"[FD00::2]:8443":                  "[fd00::2]:8443",
		"https://fd00::2":                 "",
		"[fd00::2]:0":                     "",
		"[fd00::2]:65536":                 "",
		"[fe80::2%eth0]:8443":             "",
		"[::ffff:10.0.0.1]":               "",
		"[github.com]":                    "",
		"1.2.3":                           "",
		"-bad.example.com":                "",
		"bad-.example.com":                "",
		"github.com:0":                    "",
		"github.com:65536":                "",
		"github.com:":                     "",
		"github.com?x=1":                  "",
		"exa mple.com":                    "",
		"例子.example":                      "",
		"":                                "",
		"https://":                        "",
		"github.com%2fevil":               "",
		"github.com\nhost=evil.example":   "",
		"github..com":                     "",
		"github.com.":                     "",
		"https://github.com:8443/org.git": "",
	}
	for input, want := range cases {
		got, err := HTTPSHostScope(input)
		if want == "" {
			if err == nil {
				t.Errorf("HTTPSHostScope(%q) = %q, want error", input, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("HTTPSHostScope(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestHTTPSRemoteHost(t *testing.T) {
	cases := map[string]string{
		"https://github.com/org/repo.git":                "github.com",
		"https://x-access-token@ghe.example.com/o/r.git": "ghe.example.com",
		"https://gitlab.example.org:8443/g/sub/r.git":    "gitlab.example.org:8443",
		"https://[FD00:0:0:0:0:0:0:2]:8443/g/r.git":      "[fd00::2]:8443",
		"https://[fd00::2]:443/g/r.git":                  "[fd00::2]",
		"https://fd00::2/g/r.git":                        "",
		"http://github.com/org/repo.git":                 "",
		"git@github.com:org/repo.git":                    "",
		"ssh://git@github.com/org/repo.git":              "",
	}
	for remote, want := range cases {
		got, ok := HTTPSRemoteHost(remote)
		if ok != (want != "") || got != want {
			t.Errorf("HTTPSRemoteHost(%q) = %q, %v; want %q", remote, got, ok, want)
		}
	}
}
