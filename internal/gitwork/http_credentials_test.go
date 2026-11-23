package gitwork

import (
	"context"
	"strings"
	"testing"
)

func TestHTTPSCredentialMatchesOnlyItsHostAndProtocol(t *testing.T) {
	ctx := WithHTTPRemote(WithHTTPCredentials(WithToken(context.Background(), "legacy-must-not-answer"), map[string]HTTPCredential{
		"cnb.cool":             {Username: "cnb", Token: "fake-cnb-token"},
		"git.example.org:8443": {Username: "oauth2", Token: "fake-other-token"},
		"[fd00::2]:8443":       {Username: "oauth2", Token: "fake-ipv6-token"},
	}), "https://cnb.cool/owner/repository")
	for _, tc := range []struct{ protocol, host, want string }{
		{"https", "cnb.cool", "fake-cnb-token"},
		{"https", "CNB.cool:443", "fake-cnb-token"},
		{"https", "unconfigured.example.org", ""},
		{"https", "cnb.cool:444", ""},
		{"http", "cnb.cool", ""},
	} {
		cmd := command(ctx, t.TempDir(), nil, []string{"credential", "fill"})
		cmd.Stdin = strings.NewReader("protocol=" + tc.protocol + "\nhost=" + tc.host + "\n\n")
		out, err := cmd.CombinedOutput()
		if tc.want == "" {
			if err == nil || strings.Contains(string(out), "password=") {
				t.Fatalf("unexpected credentials for %s %s", tc.protocol, tc.host)
			}
		} else if err != nil || !strings.Contains(string(out), "username=cnb\npassword="+tc.want) {
			t.Fatalf("matching host: %v %s", err, out)
		}
		if strings.Contains(strings.Join(cmd.Args, " "), "fake-cnb-token") {
			t.Fatal("token was put in argv")
		}
	}
	credential, host, urlHost := httpCredentialForCommand(ctx, []string{"ls-remote", "https://git.example.org:8443/owner/other"})
	if host != "git.example.org:8443" || urlHost != host || credential.Token != "fake-other-token" {
		t.Fatal("explicit URL borrowed the bound repository credential")
	}

	ipv6 := WithHTTPRemote(ctx, "https://[FD00:0:0:0:0:0:0:2]:8443/team/repo.git")
	for _, tc := range []struct {
		host       string
		authorized bool
	}{
		{"[FD00:0:0:0:0:0:0:2]:8443", true},
		{"[fd00::2]:8443", true},
		{"[fd00:0:0:0:0:0:0:2]:8444", false},
		{"[fd00:0:0:0:0:0:0:3]:8443", false},
	} {
		cmd := command(ipv6, t.TempDir(), nil, []string{"credential", "fill"})
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\n\n")
		out, err := cmd.CombinedOutput()
		if !tc.authorized {
			if err == nil || strings.Contains(string(out), "password=") {
				t.Fatalf("unexpected credential for %s", tc.host)
			}
		} else if err != nil || !strings.Contains(string(out), "password=fake-ipv6-token") {
			t.Fatalf("IPv6 credential selection for %s: %v", tc.host, err)
		}
	}
}

func TestHTTPSRepositoryURLDoesNotNeedAPlatform(t *testing.T) {
	remote, name, err := HTTPSRepositoryURL("https://Unfamiliar.example.org:443/team/code.git")
	if err != nil || remote != "https://unfamiliar.example.org/team/code.git" || name != "code" {
		t.Fatalf("%q %q %v", remote, name, err)
	}
	for _, raw := range []string{"http://unfamiliar.example.org/team/code", "https://user:token@unfamiliar.example.org/team/code", "https://unfamiliar.example.org/", "https://unfamiliar.example.org/team/../code", "https://unfamiliar.example.org/team/code?token=x", "https://127.0.0.1/team/code"} {
		if _, _, err := HTTPSRepositoryURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}
