package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestGatewayGitCredentialServesOnlyTheSpeakersConfiguredHost(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	save := func(userID int64, host, platform, token string, expose bool) {
		t.Helper()
		err := f.s.SaveGitHTTPCredential(userID, "", domain.GitHTTPCredentialSecret{
			GitHTTPCredential: domain.GitHTTPCredential{Name: host, Platform: platform, Host: host},
			Token:             token,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(f.user.ID, "github.com", domain.GitPlatformGitHub, "speaker-github", true)
	save(f.user.ID, "gitlab.example.org:8443", domain.GitPlatformGitLab, "speaker-gitlab", true)
	save(f.user.ID, "hidden.example.org", domain.GitPlatformOther, "speaker-hidden", false)
	save(f.user.ID+1, "ghe.example.com", domain.GitPlatformGitHub, "other-user", true)

	gateway := &agentToolGateway{deps: &deps{repo: f.s}}
	s := &AgentToolSession{Token: "git-session", UserID: f.user.ID, RuntimeMode: domain.AssistantModeWorkspace, ChatSessionID: "chat"}
	ask := func(session *AgentToolSession, params map[string]string) gitCredentialResult {
		t.Helper()
		raw, _ := json.Marshal(params)
		response := gateway.handleRPC(withAgentToolToken(context.Background(), session.Token), session, agentToolRPCRequest{
			JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: agentGitCredentialMethod, Params: raw,
		})
		if response == nil {
			t.Fatal("gitCredential RPC returned no response")
		}
		if response.Error != nil {
			return gitCredentialResult{}
		}
		result, ok := response.Result.(gitCredentialResult)
		if !ok {
			t.Fatalf("unexpected result %T", response.Result)
		}
		return result
	}

	got := ask(s, map[string]string{"protocol": "https", "host": "github.com", "caller": "git"})
	if got.Username != domain.GitTokenHTTPUser || got.Password != "speaker-github" || got.Platform != domain.GitPlatformGitHub {
		t.Fatalf("github.com credential = %+v", got)
	}
	if got := ask(s, map[string]string{"protocol": "https", "host": "gitlab.example.org:8443"}); got.Password != "speaker-gitlab" {
		t.Fatalf("host with port = %+v", got)
	}
	if got := ask(s, map[string]string{"protocol": "https", "host": "hidden.example.org"}); got.Password != "speaker-hidden" {
		t.Fatal("all configured hosts must be available")
	}
	for name, params := range map[string]map[string]string{
		"plain http":          {"protocol": "http", "host": "github.com"},
		"different port":      {"protocol": "https", "host": "gitlab.example.org"},
		"another user's host": {"protocol": "https", "host": "ghe.example.com"},
		"unconfigured":        {"protocol": "https", "host": "evil.example.net"},
		"injected attributes": {"protocol": "https", "host": "github.com\nhost=evil.example.net"},
	} {
		if got := ask(s, params); got != (gitCredentialResult{}) {
			t.Errorf("%s must get no credential, got %+v", name, got)
		}
	}
	hosts := s.requestedGitHosts()
	if len(hosts) != 3 || hosts["github.com"] != domain.GitPlatformGitHub || hosts["gitlab.example.org:8443"] != domain.GitPlatformGitLab {
		t.Fatalf("requested hosts = %v", hosts)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(domain.GitTokenHTTPUser + ":speaker-github"))
	leaked := "token speaker-github, url https://x:speaker-gitlab@h, Authorization: Basic " + basic + " other-user"
	for _, text := range []string{redactAssistantSecrets(leaked, domain.User{}), sanitizeAssistantDiagnostic(leaked, domain.User{}, 4096)} {
		if strings.Contains(text, "speaker-") || strings.Contains(text, strings.TrimRight(basic, "=")) || !strings.Contains(text, "other-user") {
			t.Fatalf("issued tokens must be redacted from any output, only those: %s", text)
		}
	}

	platform := &AgentToolSession{Token: "platform-session", UserID: f.user.ID, RuntimeMode: domain.AssistantModePlatform}
	if got := ask(platform, map[string]string{"protocol": "https", "host": "github.com"}); got != (gitCredentialResult{}) {
		t.Fatal("a platform-mode turn must not receive Git tokens")
	}
	closed := &AgentToolSession{Token: "closed-session", UserID: f.user.ID, RuntimeMode: domain.AssistantModeWorkspace}
	closed.closed.Store(true)
	if got := ask(closed, map[string]string{"protocol": "https", "host": "github.com"}); got != (gitCredentialResult{}) {
		t.Fatal("a finished turn must not receive Git tokens")
	}
}

// The workspace command carries the exposed hosts and the session gateway,
// never a token; a runner too old for the helper gets neither.
func TestBindWorkspaceGitCarriesHostsNotTokens(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	err := f.s.SaveGitHTTPCredential(f.user.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitHub", Platform: domain.GitPlatformGitHub, Host: "github.com"},
		Token:             "speaker-github",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []domain.Runner{{ID: "old", RunnerVersion: "0.10.1"}, {ID: "new", RunnerVersion: domain.RunnerVersionGitHTTP}} {
		if err := f.s.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	h := &assistantHandlers{deps: &deps{repo: f.s}}
	for _, tc := range []struct {
		runner string
		bound  bool
	}{{"", true}, {"new", true}, {"old", false}} {
		s := &AgentToolSession{Token: "bind-" + tc.runner, UserID: f.user.ID, User: f.user, ExecutionRunnerID: tc.runner}
		command, credentials := h.bindWorkspaceGit(context.Background(), s, runtime.CommandSpec{}, runtime.RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{"github.com": {Token: "must-not-travel-with-main-bot-command"}}})
		env := runtime.CommandEnvironment(command)
		if credentials.GitHTTPCredentials != nil || !slices.Contains(command.CredentialBindings, runtime.BindGitWorkspace) {
			t.Fatalf("runner %q: credentials %v, bindings %v", tc.runner, credentials.GitHTTPCredentials, command.CredentialBindings)
		}
		if got := credentials.GitHTTPHosts["github.com"] == domain.GitPlatformGitHub && env[runtime.SessionGatewayEnv] == agentToolMCPURL(s.Token); got != tc.bound {
			t.Fatalf("runner %q: hosts %v, gateway %q", tc.runner, credentials.GitHTTPHosts, env[runtime.SessionGatewayEnv])
		}
		for key, value := range env {
			if strings.Contains(value, "speaker-github") {
				t.Fatalf("runner %q: %s carries the token", tc.runner, key)
			}
		}
	}
}
