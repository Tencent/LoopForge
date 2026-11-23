package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// prepareRunGitBinding lends SOP commands an authenticated, command-lifetime
// credential relay. Tokens remain in memory, rather than environment variables
// or the files that a workspace snapshot carries to the next node.
func prepareRunGitBinding(ctx context.Context, sandbox Sandbox, command CommandSpec, credentials RunCredentials) (CommandSpec, func(), error) {
	noop := func() {}
	bind := false
	for _, binding := range command.CredentialBindings {
		if binding == BindGit {
			bind = true
		}
	}
	if !bind {
		return command, noop, nil
	}
	if !command.PrivateHome && command.environment["HOME"] == "" {
		command.PrivateHome = true
	}
	hosts := map[string]string{}
	secrets := map[string]domain.GitHTTPExecutionCredential{}
	for host, credential := range credentials.GitHTTPCredentials {
		if credential.Token != "" {
			hosts[host], secrets[host] = credential.Platform, credential
		}
	}
	// Keep BindGit distinct from conversation BindGitWorkspace: SOP SSH
	// credentials are keyed by the frozen remote, rather than by host.
	// The host list is deliberately separate from the secret snapshot.
	credentials.GitHTTPHosts = hosts
	command = WithCommandEnvironment(command, map[string]string{GitHostsEnv: workspaceGitHosts(hosts)})
	if len(secrets) == 0 {
		return command, noop, nil
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return CommandSpec{}, noop, err
	}
	token := hex.EncodeToString(random[:])
	base, closeRelay, err := startConversationRelay(ctx, token, func(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
		var request struct {
			Method string                          `json:"method"`
			Params struct{ Protocol, Host string } `json:"params"`
		}
		result := gitCredentialReply{}
		if len(raw) <= 4096 && json.Unmarshal(raw, &request) == nil && request.Method == gitCredentialMethod && request.Params.Protocol == "https" {
			if host, err := gitwork.HTTPSHostScope(request.Params.Host); err == nil && ctx.Err() == nil {
				if credential, ok := secrets[host]; ok {
					result = gitCredentialReply{Username: credential.GitUsername(), Password: credential.Token, Platform: credential.Platform}
					if !credential.ExpiresAt.IsZero() {
						result.PasswordExpiryUTC = credential.ExpiresAt.Unix()
					}
				}
			}
		}
		encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		return encoded, err
	}, sandboxDriver(sandbox) == "docker")
	if err != nil {
		return CommandSpec{}, noop, err
	}
	command = WithCommandEnvironment(command, map[string]string{SessionGatewayEnv: base + "/internal/agent-tools/" + token + "/mcp"})
	return command, closeRelay, nil
}

// RunGitEnvironment is the same short-lived binding for runner processes
// whose provider credentials have already been prepared by the daemon.
func RunGitEnvironment(ctx context.Context, sandbox Sandbox, workspace string, credentials RunCredentials) (map[string]string, func(), error) {
	command, closeRelay, err := prepareRunGitBinding(ctx, sandbox, CommandSpec{CredentialBindings: []CredentialBinding{BindGit}}, credentials)
	if err != nil {
		return nil, closeRelay, err
	}
	credentials.GitHTTPHosts = runGitHosts(credentials)
	env := CommandEnvironment(command)
	for _, key := range workspaceGitSecretEnvironment {
		env[key] = ""
	}
	if err := bindWorkspaceGitEnvironment(ctx, sandbox, workspace, env, credentials); err != nil {
		closeRelay()
		return nil, func() {}, err
	}
	return env, closeRelay, nil
}

var workspaceGitSecretEnvironment = []string{"AGENTFLOW_GIT_TOKEN", "AGENTFLOW_GIT_HTTP_USER", "AGENTFLOW_GIT_HTTP_PASSWORD", "AGENTFLOW_GIT_HTTP_HOST", "AGENTFLOW_GIT_HTTP_URL_HOST", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "GLAB_TOKEN", "TEA_TOKEN", "CNB_TOKEN", "GIT_ASKPASS", "SSH_ASKPASS", "GIT_CONFIG_PARAMETERS"}

func scrubRunGitCredentials(ctx context.Context, sandbox Sandbox, workspace string, command CommandSpec, credentials RunCredentials) {
	home := command.environment["HOME"]
	if sandboxDriver(sandbox) == "docker" && strings.HasPrefix(home, "/workspace/") {
		home = filepath.Join(workspace, filepath.FromSlash(strings.TrimPrefix(home, "/workspace/")))
	}
	var homes []string
	if rel, err := filepath.Rel(workspace, home); home != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		homes = append(homes, home)
	}
	ScrubWorkspaceGitSecrets(context.WithoutCancel(ctx), homes, workspace)
}

func runGitHosts(credentials RunCredentials) map[string]string {
	hosts := map[string]string{}
	for host, credential := range credentials.GitHTTPCredentials {
		if credential.Token != "" {
			hosts[host] = credential.Platform
		}
	}
	return hosts
}

func redactRunGitSecrets(text string, credentials RunCredentials) string {
	for _, credential := range credentials.GitHTTPCredentials {
		if credential.Token == "" {
			continue
		}
		for _, secret := range []string{credential.Token, url.QueryEscape(credential.Token), url.PathEscape(credential.Token), strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(credential.GitUsername()+":"+credential.Token)), "=")} {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func runGitEventSink(sink EventSink, credentials RunCredentials) EventSink {
	if sink == nil {
		return nil
	}
	return func(event domain.TelemetryEvent) {
		event.Message = redactRunGitSecrets(event.Message, credentials)
		sink(event)
	}
}
