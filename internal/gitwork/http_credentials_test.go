package gitwork

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestPlainHTTPRedirectConfigMatchesIPv6Spellings(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture repository: %s: %v", out, err)
	}
	ctx := WithHTTPCredentials(t.Context(), map[string]HTTPCredential{
		"http://[fd00::2]":      {Token: "fixture-http-token"},
		"http://[fd00::2]:8443": {Token: "fixture-http-token"},
	})
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"http://[FD00:0::0002]/team/app.git", true},
		{"http://[FD00:0:0:0:0:0:0:2]:8443/team/app=repo.git", true},
		{"http://[fd00::2]:8444/team/app.git", false},
		{"https://[fd00::2]:8443/team/app.git", false},
		{"http://localhost:8443/team/app.git", false},
	} {
		if tc.want {
			key := "http." + tc.url + ".followRedirects"
			if out, err := exec.Command("git", "-C", dir, "config", "--local", key, "true").CombinedOutput(); err != nil {
				t.Fatalf("set local redirect policy: %s: %v", out, err)
			}
		}
		cmd := command(ctx, dir, nil, []string{"config", "--get-urlmatch", "http.followRedirects", tc.url})
		out, err := cmd.CombinedOutput()
		if tc.want && (err != nil || strings.TrimSpace(string(out)) != "false") {
			t.Errorf("redirect setting for %s = %q, %v", tc.url, out, err)
		}
		if !tc.want && (err == nil || len(out) != 0) {
			t.Errorf("unexpected redirect lookup result for %s: %q, %v", tc.url, out, err)
		}
	}
}

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
	credential, endpoint, remote := httpCredentialForCommand(ctx, []string{"ls-remote", "https://git.example.org:8443/owner/other"})
	if endpoint.Scope() != "git.example.org:8443" || remote == nil || remote.Host != endpoint.Authority || credential.Token != "fake-other-token" {
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

func TestGitHTTPRepositoryURLDoesNotNeedAPlatform(t *testing.T) {
	for input, want := range map[string]string{
		"https://Unfamiliar.example.org:443/team/code.git": "https://unfamiliar.example.org/team/code.git",
		"http://unfamiliar.example.org:80/team/code.git":   "http://unfamiliar.example.org/team/code.git",
		"http://192.0.2.13:8080/team/code.git":             "http://192.0.2.13:8080/team/code.git",
		"http://[FD00:0::2]/team/code.git":                 "http://[fd00::2]/team/code.git",
	} {
		remote, name, err := GitHTTPRepositoryURL(input)
		if err != nil || remote != want || name != "code" {
			t.Errorf("%q: got %q %q %v, want %q", input, remote, name, err, want)
		}
	}
	for _, raw := range []string{"http://user:token@unfamiliar.example.org/team/code", "https://unfamiliar.example.org/", "https://unfamiliar.example.org/team/../code", "https://unfamiliar.example.org/team/code?token=x", "https://127.0.0.1/team/code", "http://0x7f000001/team/code", "http://127.1/team/code"} {
		if _, _, err := GitHTTPRepositoryURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestPlainHTTPCredentialMatchesOnlyItsEndpoint(t *testing.T) {
	const authority = "git.example.org:443"
	const repoURL = "http://" + authority + "/team/code.git"
	for _, tc := range []struct {
		name, protocol, host string
		configured           bool
		want                 bool
	}{
		{"configured", "http", authority, true, true},
		{"other scheme", "https", authority, true, false},
		{"other port", "http", "git.example.org", true, false},
		{"other host", "http", "elsewhere.example.org:443", true, false},
		{"absent credential", "http", authority, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := map[string]HTTPCredential{
				"git.example.org": {Username: "git", Token: "fake-secure-token"},
			}
			token := ""
			if tc.configured {
				token = "fake-plaintext-token"
				credentials["http://"+authority] = HTTPCredential{Username: "git", Token: token}
			}
			ctx := WithHTTPRemote(WithHTTPCredentials(t.Context(), credentials), repoURL)
			if selected, endpoint, _ := httpCredentialForCommand(ctx, []string{"push", "origin", "HEAD:refs/heads/verification"}); endpoint.URL()+"/team/code.git" != repoURL || selected.Token != token {
				t.Fatal("the push refspec replaced the repository endpoint")
			}
			cmd := command(ctx, t.TempDir(), nil, []string{"credential", "fill"})
			cmd.Stdin = strings.NewReader("protocol=" + tc.protocol + "\nhost=" + tc.host + "\n\n")
			out, err := cmd.CombinedOutput()
			if tc.want && (err != nil || !strings.Contains(string(out), "password=fake-plaintext-token")) {
				t.Fatalf("expected the scoped HTTP credential, git reported %v", err)
			}
			if !tc.want && (err == nil || strings.Contains(string(out), "password=")) {
				t.Fatal("credential escaped its approved endpoint")
			}
		})
	}
}
