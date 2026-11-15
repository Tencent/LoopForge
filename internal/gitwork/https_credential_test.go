package gitwork

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	fakeGitHubPAT = "ghp_FAKEGLOBALPAT"
	fakeCNBToken  = "cnb-fake-token-0000"
)

// cnbServer answers every request with 401 and records the credentials git
// offered. cnbBaseURL points at it for the duration of the test.
type cnbServer struct {
	mu    sync.Mutex
	auths []string
	url   string
}

func (s *cnbServer) authorizations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...)
}

func (s *cnbServer) basic(user, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

func (s *cnbServer) remote() string { return s.url + "/org/repo.git" }

func newCNBServer(t *testing.T) *cnbServer {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	server := &cnbServer{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.auths = append(server.auths, r.Header.Get("Authorization"))
		server.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="cnb"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	server.url = srv.URL
	previous := cnbBaseURL
	cnbBaseURL = srv.URL
	t.Cleanup(func() { cnbBaseURL = previous })

	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	// A user-level helper that answers any host with the GitHub PAT.
	userHelper := "[credential]\n\thelper = !f() { test \"$1\" = get && printf 'username=private\\npassword=" + fakeGitHubPAT + "\\n'; }; f\n"
	if err := os.WriteFile(global, []byte(userHelper), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	return server
}

func requireNoGitHubPAT(t *testing.T, s *cnbServer) {
	t.Helper()
	for _, auth := range s.authorizations() {
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if strings.Contains(auth, fakeGitHubPAT) || strings.Contains(string(raw), fakeGitHubPAT) {
			t.Fatalf("GitHub PAT reached the CNB host: %q", raw)
		}
	}
}

func TestRunNeverSendsGlobalTokenToCNB(t *testing.T) {
	server := newCNBServer(t)
	ctx := WithToken(context.Background(), fakeGitHubPAT)

	_, err := Run(ctx, t.TempDir(), "ls-remote", server.remote())
	if err == nil {
		t.Fatal("ls-remote without a CNB token must fail")
	}
	requireNoGitHubPAT(t, server)
	for _, auth := range server.authorizations() {
		if auth != "" {
			t.Fatalf("an unconfigured CNB host must receive no credentials, got %q", auth)
		}
	}
}

func TestRunSendsOnlyCNBTokenToCNB(t *testing.T) {
	server := newCNBServer(t)
	ctx := WithToken(context.Background(), fakeGitHubPAT)
	ctx = WithHTTPSCredentials(ctx, []HTTPSCredential{CNBCredential(fakeCNBToken)})

	_, err := Run(ctx, t.TempDir(), "ls-remote", server.remote())
	if err == nil {
		t.Fatal("the fake CNB server rejects every credential")
	}
	requireNoGitHubPAT(t, server)
	want := server.basic(CNBHTTPUser, fakeCNBToken)
	for _, auth := range server.authorizations() {
		if auth == want {
			return
		}
	}
	t.Fatalf("CNB token was not offered; server saw %q", server.authorizations())
}

// TestSandboxEnvironmentFailsClosedForCNB drives git the way a sandboxed
// command sees it: a global GIT_ASKPASS that answers with the GitHub PAT and
// the managed-host config from BindManagedHosts.
func TestSandboxEnvironmentFailsClosedForCNB(t *testing.T) {
	server := newCNBServer(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "askpass.calls")
	askpass := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\necho \"$1\" >> '" + calls + "'\nprintf '%s\\n' '" + fakeGitHubPAT + "'\n"
	if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// An empty GIT_CONFIG_GLOBAL makes the user-level helper of newCNBServer
	// inert so only GIT_ASKPASS could leak here.
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)

	run := func(managed bool, credentials []HTTPSCredential) {
		t.Helper()
		environment := map[string]string{
			"GIT_TERMINAL_PROMPT": "0",
			"GIT_ASKPASS":         askpass,
			"AGENTFLOW_GIT_TOKEN": fakeGitHubPAT,
			"AGENTFLOW_GIT_USER":  "private",
		}
		if managed {
			BindManagedHosts(environment, credentials)
		}
		cmd := exec.Command("git", "ls-remote", server.remote())
		cmd.Dir = dir
		cmd.Env = os.Environ()
		for k, v := range environment {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("ls-remote must fail: %s", out)
		}
	}

	// Control: without the managed block the askpass does answer, so this
	// harness can see a leak.
	run(false, nil)
	if _, err := os.Stat(calls); err != nil {
		t.Fatal("control run never reached GIT_ASKPASS; the test cannot detect a leak")
	}
	if got := server.authorizations(); !strings.Contains(strings.Join(got, "\n"), "Basic ") {
		t.Fatalf("control run sent no credentials: %q", got)
	}
	_ = os.Remove(calls)
	server.mu.Lock()
	server.auths = nil
	server.mu.Unlock()

	run(true, nil)
	if _, err := os.Stat(calls); err == nil {
		t.Fatal("GIT_ASKPASS was consulted for a CNB remote; quit=1 did not stop git")
	}
	requireNoGitHubPAT(t, server)
	for _, auth := range server.authorizations() {
		if auth != "" {
			t.Fatalf("an unconfigured CNB host must receive no credentials, got %q", auth)
		}
	}

	run(true, []HTTPSCredential{CNBCredential(fakeCNBToken)})
	if _, err := os.Stat(calls); err == nil {
		t.Fatal("GIT_ASKPASS was consulted although a CNB token was supplied")
	}
	requireNoGitHubPAT(t, server)
	want := server.basic(CNBHTTPUser, fakeCNBToken)
	for _, auth := range server.authorizations() {
		if auth == want {
			return
		}
	}
	t.Fatalf("CNB token was not offered; server saw %q", server.authorizations())
}

func TestRewriteRemoteForTokenSkipsManagedHost(t *testing.T) {
	for _, remote := range []string{"https://cnb.cool/org/repo.git", "git@cnb.cool:org/repo.git", "https://CNB.cool/a/b/c.git"} {
		if got := RewriteRemoteForToken(remote, fakeGitHubPAT); got != remote {
			t.Errorf("RewriteRemoteForToken(%q) = %q, want it unchanged", remote, got)
		}
	}
	got := RewriteRemoteForToken("https://github.com/org/repo.git", fakeGitHubPAT)
	if !strings.Contains(got, fakeGitHubPAT+"@github.com") {
		t.Fatalf("other hosts must still be rewritten, got %q", got)
	}
}

// A port, http or an upper-case host is still cnb.cool: it must be managed and
// must never get the global token written into its URL.
func TestManagedRemoteIgnoresPortSchemeAndCase(t *testing.T) {
	for _, remote := range []string{
		"https://cnb.cool:443/a/b.git", "https://cnb.cool:8443/a/b.git",
		"http://cnb.cool/a/b.git", "https://CNB.cool/a/b.git", "https://cnb.cool/a/b.git",
	} {
		if !IsManagedRemote(remote) {
			t.Errorf("IsManagedRemote(%q) = false", remote)
		}
		if got := RewriteRemoteForToken(remote, fakeGitHubPAT); got != remote {
			t.Errorf("RewriteRemoteForToken(%q) = %q, want it unchanged", remote, got)
		}
	}
	if IsManagedRemote("https://cnb.cool.example.org/a/b.git") {
		t.Error("a look-alike host must not be managed")
	}
}

func TestRunRedactsCNBToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := WithHTTPSCredentials(context.Background(), []HTTPSCredential{CNBCredential(fakeCNBToken)})
	out, err := Run(ctx, t.TempDir(), "-c", "alias.leak=!echo $"+scopedTokenEnvPrefix+"0", "leak")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, fakeCNBToken) || out != "***" {
		t.Fatalf("token was not redacted: %q", out)
	}
	if got := Redact("password="+fakeCNBToken, fakeCNBToken); strings.Contains(got, fakeCNBToken) {
		t.Fatalf("Redact left the token: %q", got)
	}
}

func TestEnvDropsInheritedScopedTokens(t *testing.T) {
	t.Setenv(scopedTokenEnvPrefix+"0", fakeGitHubPAT)
	t.Setenv(scopedUserEnvPrefix+"0", "x")
	for _, kv := range Env("") {
		if strings.HasPrefix(kv, "AGENTFLOW_SCOPED_GIT_") {
			t.Fatalf("inherited scoped credential survived: %s", kv)
		}
	}
}

func TestBindManagedHostsAppendsAfterExistingConfig(t *testing.T) {
	environment := map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "a.b", "GIT_CONFIG_VALUE_0": "c"}
	BindManagedHosts(environment, []HTTPSCredential{CNBCredential(fakeCNBToken)})
	if environment["GIT_CONFIG_COUNT"] != "4" || environment["GIT_CONFIG_KEY_0"] != "a.b" {
		t.Fatalf("existing entry moved or count wrong: %v", environment)
	}
	if environment["GIT_CONFIG_VALUE_1"] != "" || !strings.HasSuffix(environment["GIT_CONFIG_KEY_1"], ".helper") || !strings.Contains(environment["GIT_CONFIG_VALUE_2"], "quit=1") {
		t.Fatalf("managed block must reset the helper first, then define it: %v", environment)
	}
	if strings.Contains(environment["GIT_CONFIG_VALUE_2"], fakeCNBToken) || environment[scopedTokenEnvPrefix+"0"] != fakeCNBToken {
		t.Fatalf("token must live only in the environment: %v", environment)
	}
}
