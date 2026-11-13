package runtime

import (
	"agentflow/internal/observability"
	"fmt"
	"net/url"
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
	if command.bindingsPrepared {
		return command, nil
	}
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
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		bound.environment[key] = ""
	}
	if command.Provider == "codebuddy" {
		// Every conversation and workspace gets its own CLI home, and CodeBuddy
		// downloads its built-in plugin marketplaces (about 71 MB) into each one.
		// LoopForge writes skills and agents itself and installs no plugins.
		bound.environment["CODEBUDDY_SKIP_BUILTIN_MARKETPLACE"] = "1"
	}
	// Validate every private-home component before any credential binding can
	// write configuration. The sandbox may have replaced a parent with a symlink.
	if command.PrivateHome {
		provider := PrivateHomeName(command)
		hostHome, err := EnsureControlDirectory(workspace, "private-home", provider)
		if err != nil {
			return CommandSpec{}, fmt.Errorf("create private home: %w", err)
		}
		hostTmp, err := EnsureControlDirectory(workspace, "tmp", provider)
		if err != nil {
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
			if _, err := EnsureControlDirectory(workspace, "private-home", provider, dir); err != nil {
				return CommandSpec{}, err
			}
			bound.environment[key] = filepath.ToSlash(filepath.Join(bound.environment["HOME"], dir))
		}
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
			argv, err := CodexUserProviderArgv(bound.Argv, credentials.Codex.BaseURL)
			if err != nil {
				return CommandSpec{}, err
			}
			bound.Argv = argv
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
			var home AgentCLIHome
			homeEnv := bound.environment["HOME"]
			if command.PrivateHome {
				home = WorkspaceAgentCLIHome(workspace, PrivateHomeName(command))
			} else {
				homeEnv = command.environment["HOME"]
				if strings.TrimSpace(homeEnv) == "" {
					return CommandSpec{}, fmt.Errorf("%s 需要独立的 HOME 目录", AgentCLILabel(command.Provider))
				}
				home = HostAgentCLIHome(homeEnv)
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
	return bound, nil
}

// CodexUserProviderArgv inserts a dedicated "agentflow_user" model provider
// before the final argv element (the prompt). An empty baseURL falls back to
// the OpenAI API. The key stays in the process environment as CODEX_API_KEY;
// only its variable name is written to argv, and the dedicated provider
// prevents a fallback to the host login. The input slice is not modified.
func CodexUserProviderArgv(argv []string, baseURL string) ([]string, error) {
	if len(argv) < 2 {
		return nil, fmt.Errorf("invalid Codex invocation")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	config := []string{
		"-c", `model_provider="agentflow_user"`,
		"-c", `model_providers.agentflow_user.name="User API"`,
		"-c", "model_providers.agentflow_user.base_url=" + strconv.Quote(baseURL),
		"-c", `model_providers.agentflow_user.env_key="CODEX_API_KEY"`,
		"-c", `model_providers.agentflow_user.wire_api="responses"`,
		"-c", "model_providers.agentflow_user.requires_openai_auth=false",
	}
	out := make([]string, 0, len(argv)+len(config))
	out = append(out, argv[:len(argv)-1]...)
	out = append(out, config...)
	return append(out, argv[len(argv)-1]), nil
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
	_, homeEnv := privateHomePaths(sandbox, workspace, command)
	// Every level is Lstat-checked: the workspace is writable from the sandbox,
	// so .agentflow, private-home or the home itself may have become a symlink.
	if _, err := EnsureControlDirectory(workspace, "private-home", name); err != nil {
		return nil, nil, fmt.Errorf("create private home: %w", err)
	}
	tmp, err := EnsureControlDirectory(workspace, "tmp", name)
	if err != nil {
		return nil, nil, fmt.Errorf("create private temp: %w", err)
	}
	argv, bound, err := ApplyAgentCLICredential(command.Provider, cred, command.AgentCLI, WorkspaceAgentCLIHome(workspace, name), homeEnv, argv)
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
