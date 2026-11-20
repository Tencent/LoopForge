package runtime

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/observability"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func bindCommand(ctx context.Context, sandbox Sandbox, workspace string, command CommandSpec, credentials RunCredentials) (CommandSpec, error) {
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
	bound.environment["GH_TOKEN"] = ""
	bound.environment["GITHUB_TOKEN"] = ""
	for _, key := range workspaceGitSecretEnvironment {
		bound.environment[key] = ""
	}
	gitBinding := false
	for _, binding := range command.CredentialBindings {
		if binding == BindGit || binding == BindGitWorkspace {
			gitBinding = true
		}
	}
	if !gitBinding {
		bound.environment["GIT_CONFIG_COUNT"] = "1"
		bound.environment["GIT_CONFIG_KEY_0"] = "credential.helper"
		bound.environment["GIT_CONFIG_VALUE_0"] = ""
		bound.environment["GIT_CONFIG_GLOBAL"] = os.DevNull
		bound.environment["GIT_CONFIG_SYSTEM"] = os.DevNull
	}
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
			if err := bindWorkspaceGitEnvironment(ctx, sandbox, workspace, bound.environment, credentials); err != nil {
				return CommandSpec{}, err
			}
		case BindGitWorkspace:
			if err := bindWorkspaceGitEnvironment(ctx, sandbox, workspace, bound.environment, credentials); err != nil {
				return CommandSpec{}, err
			}
		case BindExtra:
			// Missing credentials export as empty strings on purpose: a child
			// must not inherit the AgentFlow host process's own value for a
			// variable the deployment routes through Extra credentials.
			for envName, credName := range command.ExtraCredentialEnv {
				bound.environment[envName] = credentials.Extra[credName]
				bound.credentialEnvKeys = append(bound.credentialEnvKeys, envName)
			}
		case BindMCP:
			if err := bindMCPCallEnvironment(ctx, sandbox, workspace, bound.environment, credentials.MCPServers); err != nil {
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
	bindTelemetryEnvironment(bound.environment, command.Telemetry)
	return bound, nil
}

// bindTelemetryEnvironment points the child CLI's OpenTelemetry exporter at
// the AgentFlow collector and tags its spans with the run and node. A nil
// telemetry leaves env untouched.
func bindTelemetryEnvironment(env map[string]string, telemetry *InvocationOptions) {
	if telemetry == nil {
		return
	}
	runID, nodeID := telemetry.RunID, telemetry.NodeID
	env["AGENTFLOW_RUN_ID"] = runID
	env["AGENTFLOW_NODE_ID"] = nodeID
	env["CODEBUDDY_CODE_ENABLE_TELEMETRY"] = "1"
	env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
	env["OTEL_TRACES_EXPORTER"] = "otlp"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/protobuf"
	env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = observability.CollectorTracesURL()
	env["OTEL_LOG_TOOL_DETAILS"] = "1"
	env["OTEL_RESOURCE_ATTRIBUTES"] = fmt.Sprintf("agentflow.run_id=%s,agentflow.node_id=%s,service.name=agentflow-codebuddy", runID, nodeID)
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

// bindWorkspaceGitEnvironment installs the standard host-scoped helper.
// It requests credentials through a live relay rather than writing tokens.
func bindWorkspaceGitEnvironment(ctx context.Context, sandbox Sandbox, workspace string, environment map[string]string, credentials RunCredentials) error {
	environment["GIT_TERMINAL_PROMPT"] = "0"
	if _, err := EnsureControlDirectory(workspace, "runtime"); err != nil {
		return fmt.Errorf("create git runtime dir: %w", err)
	}
	runtimeHost := mcpCallHostDir(workspace)
	runtimePath := func(rel string) string {
		if sandboxDriver(sandbox) == "docker" {
			return mcpCallGuestPath(rel)
		}
		return filepath.Join(runtimeHost, filepath.FromSlash(rel))
	}
	// The requester's identity is the lowest-precedence (system) default, so
	// the agent can still set its own with git config --global or --local.
	if identity := workspaceGitIdentity(credentials.GitUserName, credentials.GitUserEmail); identity != "" {
		if err := writeManagedFile(workspace, filepath.Join(runtimeHost, "gitconfig-system"), []byte(identity), 0o600); err != nil {
			return fmt.Errorf("write git identity: %w", err)
		}
		environment["GIT_CONFIG_SYSTEM"] = runtimePath("gitconfig-system")
		environment["GIT_CONFIG_NOSYSTEM"] = "0"
	}
	// gh and glab keep logins of a previous speaker in their config
	// directories; each turn starts with empty ones.
	for key, dir := range map[string]string{"GH_CONFIG_DIR": "gh-config", "GLAB_CONFIG_DIR": "glab-config"} {
		if _, err := resetControlDirectory(workspace, "runtime", dir); err != nil {
			return fmt.Errorf("reset %s: %w", dir, err)
		}
		environment[key] = runtimePath(dir)
	}
	gateway := strings.TrimSpace(environment[SessionGatewayEnv])
	hosts := workspaceGitHosts(credentials.GitHTTPHosts)
	environment[GitHostsEnv] = hosts
	environment["GIT_CONFIG_COUNT"] = "1"
	environment["GIT_CONFIG_KEY_0"] = "credential.helper"
	environment["GIT_CONFIG_VALUE_0"] = ""
	if gateway == "" || hosts == "" {
		delete(environment, SessionGatewayEnv)
		environment[GitHostsEnv] = ""
		return nil
	}
	hostBin := filepath.Join(runtimeHost, "bin")
	if _, err := EnsureControlDirectory(workspace, "runtime", "bin"); err != nil {
		return fmt.Errorf("create git helper dir: %w", err)
	}
	if err := installMCPCallHelperForSandbox(ctx, sandbox, workspace, filepath.Join(hostBin, "agentflow-helper")); err != nil {
		return fmt.Errorf("install git credential helper: %w", err)
	}
	helper := runtimePath("bin/agentflow-helper")
	for _, name := range []string{"gh", "glab"} {
		script := "#!/bin/sh\nexec " + shellSingleQuote(helper) + " cli-shim " + name + " \"$@\"\n"
		if err := writeManagedFile(workspace, filepath.Join(hostBin, name), []byte(script), 0o700); err != nil {
			return fmt.Errorf("write %s wrapper: %w", name, err)
		}
	}
	// The empty helper entry drops every helper configured before it (system,
	// global, repository), so a stored credential never answers instead.
	environment["GIT_CONFIG_COUNT"] = "2"
	environment["GIT_CONFIG_KEY_0"] = "credential.helper"
	environment["GIT_CONFIG_VALUE_0"] = ""
	environment["GIT_CONFIG_KEY_1"] = "credential.helper"
	environment["GIT_CONFIG_VALUE_1"] = "!" + shellSingleQuote(helper) + " git-credential"
	environment[GitHostsEnv] = hosts
	if sandboxDriver(sandbox) != "docker" {
		base := environment["PATH"]
		if base == "" {
			base = sandboxBasePath(sandbox)
		}
		environment["PATH"] = hostBin + string(os.PathListSeparator) + base
	}
	return nil
}

// workspaceGitHosts renders host→platform as the sorted "host=platform" list
// of GitHostsEnv, skipping anything that is not a plain HTTPS host.
func workspaceGitHosts(hosts map[string]string) string {
	pairs := make([]string, 0, len(hosts))
	for host, platform := range hosts {
		scope, err := gitwork.HTTPSHostScope(host)
		if err != nil || scope != host {
			continue
		}
		var ok bool
		if platform, ok = domain.NormalizeGitPlatform(platform); !ok {
			platform = domain.GitPlatformOther
		}
		pairs = append(pairs, host+"="+platform)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// sandboxBasePath is the PATH a process in sandbox starts with: the profile's
// (which carries an activated environment's tools) or the host's.
func sandboxBasePath(sandbox Sandbox) string {
	switch sb := unwrapSandbox(sandbox).(type) {
	case *processSandbox:
		if path := sb.profile.Environment["PATH"]; path != "" {
			return path
		}
	case *localSandbox:
		if path := sb.environment["PATH"]; path != "" {
			return path
		}
	}
	return os.Getenv("PATH")
}

func workspaceGitIdentity(name, email string) string {
	clean := strings.NewReplacer("\n", " ", "\r", " ", "\"", "", "\\", "")
	name, email = strings.TrimSpace(clean.Replace(name)), strings.TrimSpace(clean.Replace(email))
	if name == "" && email == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("[user]\n")
	if name != "" {
		fmt.Fprintf(&b, "\tname = \"%s\"\n", name)
	}
	if email != "" {
		fmt.Fprintf(&b, "\temail = \"%s\"\n", email)
	}
	return b.String()
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
