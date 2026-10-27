package runtime

import (
	"agentflow/internal/observability"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !found {
				out = append(out, prefix+value)
				found = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, prefix+value)
	}
	return out
}

func bindCommand(sandbox Sandbox, workspace string, command CommandSpec, credentials RunCredentials) (CommandSpec, error) {
	bound := command
	bound.environment = make(map[string]string, len(command.environment)+16)
	for key, value := range command.environment {
		bound.environment[key] = value
	}
	// Known host credentials are cleared for every child by default. A provider
	// receives them only by explicitly requesting a controlled binding.
	bound.environment["CODEBUDDY_API_KEY"] = ""
	bound.environment["CODEBUDDY_INTERNET_ENVIRONMENT"] = ""
	bound.environment["AGENTFLOW_GIT_TOKEN"] = ""
	bound.environment["AGENTFLOW_GITLAB_TOKEN"] = ""
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		bound.environment[key] = ""
	}
	for _, binding := range command.CredentialBindings {
		switch binding {
		case BindCodeBuddy:
			// Empty values are intentional: a responsible user without credentials must
			// not inherit either variable from the AgentFlow host process.
			bound.environment["CODEBUDDY_API_KEY"] = strings.TrimSpace(credentials.CodeBuddy.APIKey)
			if bound.environment["CODEBUDDY_API_KEY"] == "" {
				return CommandSpec{}, fmt.Errorf("需求责任人未配置 CodeBuddy API Key，请在个人配置中配置")
			}
			bound.environment["CODEBUDDY_INTERNET_ENVIRONMENT"] = strings.TrimSpace(credentials.CodeBuddy.InternetEnvironment)
		case BindGit:
			if err := bindGitEnvironment(sandbox, workspace, bound.environment, credentials.GitToken); err != nil {
				return CommandSpec{}, err
			}
			if err := BindGitLabEnvironment(bound.environment, credentials.GitLabBaseURL, credentials.GitLabToken); err != nil {
				return CommandSpec{}, err
			}
		case BindMCP:
			if err := bindMCPCallEnvironment(sandbox, workspace, bound.environment, credentials.MCPServers); err != nil {
				return CommandSpec{}, err
			}
			bound.Argv = BindMCPCallArgv(sandbox, bound.Argv)
		case BindCodex:
			bound.environment["CODEX_API_KEY"] = strings.TrimSpace(credentials.Codex.APIKey)
			if bound.environment["CODEX_API_KEY"] == "" {
				return CommandSpec{}, fmt.Errorf("需求责任人未配置 Codex API Key，请在个人配置中配置")
			}
			baseURL := strings.TrimSpace(credentials.Codex.BaseURL)
			if baseURL == "" {
				baseURL = "https://api.openai.com/v1"
			}
			// The key stays in the process environment; only its variable name is
			// written to argv. A dedicated provider prevents host login fallback.
			config := []string{
				"-c", `model_provider="agentflow_user"`,
				"-c", `model_providers.agentflow_user.name="User API"`,
				"-c", "model_providers.agentflow_user.base_url=" + strconv.Quote(baseURL),
				"-c", `model_providers.agentflow_user.env_key="CODEX_API_KEY"`,
				"-c", `model_providers.agentflow_user.wire_api="responses"`,
				"-c", "model_providers.agentflow_user.requires_openai_auth=false",
			}
			if len(bound.Argv) < 2 {
				return CommandSpec{}, fmt.Errorf("invalid Codex invocation")
			}
			args := append([]string{}, bound.Argv[:len(bound.Argv)-1]...)
			args = append(args, config...)
			bound.Argv = append(args, bound.Argv[len(bound.Argv)-1])
		case BindClaudeCode:
			bound.environment["ANTHROPIC_API_KEY"] = strings.TrimSpace(credentials.ClaudeCode.APIKey)
			if bound.environment["ANTHROPIC_API_KEY"] == "" {
				return CommandSpec{}, fmt.Errorf("需求责任人未配置 Claude Code API Key，请在个人配置中配置")
			}
			baseURL := strings.TrimSpace(credentials.ClaudeCode.BaseURL)
			if baseURL == "" {
				baseURL = "https://api.anthropic.com"
			}
			bound.environment["ANTHROPIC_BASE_URL"] = baseURL
		case BindAgentCLI:
			home, homeEnv := privateHomePaths(sandbox, workspace, command)
			if !command.PrivateHome {
				home = command.environment["HOME"]
				homeEnv = home
			}
			if strings.TrimSpace(home) == "" {
				return CommandSpec{}, fmt.Errorf("%s 需要独立的 HOME 目录", AgentCLILabel(command.Provider))
			}
			argv, env, err := ApplyAgentCLICredential(command.Provider, credentials.AgentCLI[command.Provider], command.AgentCLI, home, homeEnv, bound.Argv)
			if err != nil {
				return CommandSpec{}, err
			}
			bound.Argv = argv
			for _, key := range agentCLIHostEnv {
				bound.environment[key] = ""
			}
			for key, value := range env {
				bound.environment[key] = value
			}
		default:
			return CommandSpec{}, fmt.Errorf("unsupported credential binding %q", binding)
		}
	}
	if command.HookOutput != "" {
		bound.environment["AGENTFLOW_HOOK_OUTPUT"] = command.HookOutput
	}
	if command.Telemetry != nil {
		runID, nodeID := command.Telemetry.RunID, command.Telemetry.NodeID
		bound.environment["AGENTFLOW_RUN_ID"] = runID
		bound.environment["AGENTFLOW_NODE_ID"] = nodeID
		bound.environment["CODEBUDDY_CODE_ENABLE_TELEMETRY"] = "1"
		bound.environment["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
		bound.environment["OTEL_TRACES_EXPORTER"] = "otlp"
		bound.environment["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/protobuf"
		bound.environment["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = observability.CollectorTracesURL()
		bound.environment["OTEL_LOG_TOOL_DETAILS"] = "1"
		bound.environment["OTEL_RESOURCE_ATTRIBUTES"] = fmt.Sprintf("agentflow.run_id=%s,agentflow.node_id=%s,service.name=agentflow-codebuddy", runID, nodeID)
	}
	if command.PrivateHome {
		provider := PrivateHomeName(command)
		hostHome := filepath.Join(workspace, ".agentflow", "private-home", provider)
		hostTmp := filepath.Join(workspace, ".agentflow", "tmp", provider)
		if err := os.MkdirAll(hostHome, 0o700); err != nil {
			return CommandSpec{}, fmt.Errorf("create private home: %w", err)
		}
		if err := os.MkdirAll(hostTmp, 0o700); err != nil {
			return CommandSpec{}, fmt.Errorf("create private temp: %w", err)
		}
		if sandboxDriver(sandbox) == "docker" {
			bound.environment["HOME"] = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "private-home", provider))
			bound.environment["TMPDIR"] = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "tmp", provider))
		} else {
			bound.environment["HOME"] = hostHome
			bound.environment["TMPDIR"] = hostTmp
		}
		if command.Provider == "codex" || command.Provider == "claude-code" {
			dir, key := ".codex", "CODEX_HOME"
			if command.Provider == "claude-code" {
				dir, key = ".claude", "CLAUDE_CONFIG_DIR"
			}
			if err := os.MkdirAll(filepath.Join(hostHome, dir), 0o700); err != nil {
				return CommandSpec{}, err
			}
			bound.environment[key] = filepath.ToSlash(filepath.Join(bound.environment["HOME"], dir))
		}
	}
	return bound, nil
}

// PrivateHomeName is the directory under .agentflow/private-home that a
// PrivateHome command runs with as HOME.
func PrivateHomeName(command CommandSpec) string {
	name := safeID(command.PrivateHomeKey)
	if name == "" {
		name = safeID(command.Provider)
	}
	if name == "" {
		name = "process"
	}
	return name
}

// AgentCLIPrivateHomeKey is the PrivateHomeKey of an agent CLI invocation:
// one HOME per CLI and Agent, so concurrent Agents keep their own MCP
// configuration and sessions.
func AgentCLIPrivateHomeKey(kind, agentID string) string {
	return safeID(kind + "-" + agentID)
}

// BindAgentCLICommand is the runner-side BindAgentCLI: it renders the agent
// CLI's configuration into the command's private HOME and returns argv with
// the environment (HOME and TMPDIR included) to run it with. Agent CLIs always
// run in that HOME, sandboxed or not, because their MCP configuration is there.
func BindAgentCLICommand(sandbox Sandbox, workspace string, command CommandSpec, cred ProviderCredential, argv []string) ([]string, map[string]string, error) {
	name := PrivateHomeName(command)
	home, homeEnv := privateHomePaths(sandbox, workspace, command)
	tmp := filepath.Join(workspace, ".agentflow", "tmp", name)
	for _, dir := range []string{home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create private home: %w", err)
		}
	}
	argv, bound, err := ApplyAgentCLICredential(command.Provider, cred, command.AgentCLI, home, homeEnv, argv)
	if err != nil {
		return nil, nil, err
	}
	env := map[string]string{}
	for _, key := range agentCLIHostEnv {
		env[key] = ""
	}
	for key, value := range bound {
		env[key] = value
	}
	env["HOME"] = homeEnv
	env["TMPDIR"] = tmp
	if sandboxDriver(sandbox) == "docker" {
		env["TMPDIR"] = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "tmp", name))
	}
	return argv, env, nil
}

// privateHomePaths returns a PrivateHome command's HOME on the host and as
// the process sees it.
func privateHomePaths(sandbox Sandbox, workspace string, command CommandSpec) (host, env string) {
	name := PrivateHomeName(command)
	host = filepath.Join(workspace, ".agentflow", "private-home", name)
	if sandboxDriver(sandbox) == "docker" {
		return host, filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "private-home", name))
	}
	return host, host
}

// GitEnvironment is the git environment a child process needs to reach
// private remotes with token without the token touching any git config.
func GitEnvironment(sandbox Sandbox, workspace, token string) (map[string]string, error) {
	environment := map[string]string{}
	if err := bindGitEnvironment(sandbox, workspace, environment, token); err != nil {
		return nil, err
	}
	return environment, nil
}

func bindGitEnvironment(sandbox Sandbox, workspace string, environment map[string]string, token string) error {
	environment["GIT_TERMINAL_PROMPT"] = "0"
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	user := "private"
	if strings.HasPrefix(strings.ToLower(token), "oauth2:") {
		if stripped := strings.TrimSpace(token[len("oauth2:"):]); stripped != "" {
			user = "oauth2"
			token = stripped
		}
	} else if strings.HasPrefix(strings.ToLower(token), "private:") {
		if stripped := strings.TrimSpace(token[len("private:"):]); stripped != "" {
			token = stripped
		}
	}
	prefixes := []string{"git@git.example.com:", "ssh://git@git.example.com/", "https://git.example.com/", "http://git.example.com/"}
	authed := "https://" + url.UserPassword(user, token).String() + "@git.example.com/"
	environment["GIT_CONFIG_COUNT"] = strconv.Itoa(len(prefixes))
	for index, prefix := range prefixes {
		environment[fmt.Sprintf("GIT_CONFIG_KEY_%d", index)] = "url." + authed + ".insteadof"
		environment[fmt.Sprintf("GIT_CONFIG_VALUE_%d", index)] = prefix
	}
	// Verification/command nodes keep the host HOME, so ~/.gitconfig insteadOf
	// rewrites git@ to https://git.example.com/ without credentials. insteadOf does
	// not chain; GIT_ASKPASS answers the resulting username/password prompt.
	askpassHost := filepath.Join(workspace, ".agentflow", "runtime", "git-askpass")
	if err := writeGitAskPass(workspace, askpassHost); err != nil {
		return err
	}
	askpass := askpassHost
	if sandboxDriver(sandbox) == "docker" {
		askpass = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "runtime", "git-askpass"))
	}
	environment["AGENTFLOW_GIT_TOKEN"] = token
	environment["AGENTFLOW_GIT_USER"] = user
	environment["GIT_ASKPASS"] = askpass
	environment["SSH_ASKPASS"] = askpass
	return nil
}

const gitAskPassScript = `#!/bin/sh
case "$1" in
  *[Uu]sername*) printf '%s\n' "${AGENTFLOW_GIT_USER:-private}" ;;
  *) printf '%s\n' "${AGENTFLOW_GIT_TOKEN-}" ;;
esac
`

func writeGitAskPass(workspace, path string) error {
	if _, err := EnsureControlDirectory(workspace, "runtime"); err != nil {
		return fmt.Errorf("create git askpass dir: %w", err)
	}
	if err := writeManagedFile(workspace, path, []byte(gitAskPassScript), 0o700); err != nil {
		return fmt.Errorf("write git askpass: %w", err)
	}
	return nil
}

// BindGitLabEnvironment configures a helper scoped to one GitLab origin; the token remains in the process environment.
func BindGitLabEnvironment(env map[string]string, base, token string) error {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	token = strings.TrimSpace(token)
	if base == "" {
		return nil
	}
	if token == "" {
		return fmt.Errorf("需求责任人未配置该 GitLab 的 Token")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("GitLab 地址无效")
	}
	n, _ := strconv.Atoi(env["GIT_CONFIG_COUNT"])
	helper := `!f() { test "$1" = get || exit 0; printf 'username=oauth2\npassword=%s\n' "$AGENTFLOW_GITLAB_TOKEN"; }; f`
	env[fmt.Sprintf("GIT_CONFIG_KEY_%d", n)] = "credential." + base + ".helper"
	env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", n)] = helper
	n++
	env[fmt.Sprintf("GIT_CONFIG_KEY_%d", n)] = "http." + base + ".followRedirects"
	env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", n)] = "false"
	n++
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(n)
	env["AGENTFLOW_GITLAB_TOKEN"] = token
	return nil
}
