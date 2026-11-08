package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// Agent CLIs beyond CodeBuddy / Codex / Claude Code. Each one runs with the
// responsible user's own key from 个人配置 inside a private HOME that also
// holds the CLI's MCP and model configuration, so neither the host login nor
// the host configuration leaks into a run.
const (
	AgentCLIOpenCode    = "opencode"
	AgentCLICursor      = "cursor"
	AgentCLIAntigravity = "agy"
	AgentCLIKimi        = "kimi"
	AgentCLIPi          = "pi"
	AgentCLIDeepSeek    = "dsh"
)

// AgentCLIKinds is the display order of the agent CLIs.
var AgentCLIKinds = domain.AgentCLIExecutorKinds

func IsAgentCLI(kind string) bool { return domain.IsAgentCLIKind(kind) }

const (
	AgentCLIProtocolChat      = domain.AgentCLIProtocolChat
	AgentCLIProtocolResponses = domain.AgentCLIProtocolResponses
	AgentCLIProtocolMessages  = domain.AgentCLIProtocolMessages
	AgentCLIProtocolGemini    = domain.AgentCLIProtocolGemini
)

func AgentCLIProtocols(kind string) []string { return domain.AgentCLIProtocolOptions[kind] }

// AgentCLIProtocol normalizes a stored protocol choice; unknown values fall
// back to the CLI's default.
func AgentCLIProtocol(kind, protocol string) string {
	return domain.NormalizeAgentCLIProtocol(kind, protocol)
}

// AgentCLINeedsBaseURL reports whether a credential is unusable without a
// Base URL: OpenCode and Pi have no official endpoint of their own, and
// Antigravity only reaches an OpenAI-compatible gateway through one.
func AgentCLINeedsBaseURL(kind, protocol string) bool {
	switch kind {
	case AgentCLIOpenCode, AgentCLIPi:
		return true
	case AgentCLIAntigravity:
		return AgentCLIProtocol(kind, protocol) == AgentCLIProtocolChat
	}
	return false
}

// agentCLIModelOptional reports whether a CLI picks its own model when
// neither the executor nor the chat names one: Cursor and Antigravity always
// can, DeepSeek Harness on the official endpoint.
func agentCLIModelOptional(kind, baseURL string) bool {
	switch kind {
	case AgentCLICursor:
		return true
	case AgentCLIAntigravity, AgentCLIDeepSeek:
		return baseURL == ""
	}
	return false
}

// AgentCLIOptions carries the per-invocation facts the credential binding
// needs to render a CLI's model configuration.
type AgentCLIOptions struct {
	Model string
	// SystemPromptFile is the system prompt path as the process sees it
	// (relative paths resolve against the working directory).
	SystemPromptFile string
	// ReadOnly is AgentCLICommandOptions.Chat.
	ReadOnly bool
}

// agentCLIKeyEnv holds the user's key for CLIs whose configuration names
// the variable instead of the secret.
const agentCLIKeyEnv = "AGENTFLOW_CLI_API_KEY"

// agentCLIHostEnv is cleared for every child: these variables would select
// the host's own login, key, gateway or configuration directory.
var agentCLIHostEnv = []string{
	agentCLIKeyEnv, "CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GEMINI_BASE_URL",
	"AGY_LLM_GATEWAY_URL", "AGY_LLM_GATEWAY_API_KEY", "AGY_LLM_GATEWAY_WIRE_PROTOCOL", "AGY_LLM_GATEWAY_MODELS",
	"DEEPSEEK_API_KEY", "DSH_HOME", "MOONSHOT_API_KEY", "KIMI_API_KEY", "KIMI_BASE_URL", "KIMI_CODE_HOME",
	"KIMI_MODEL_NAME", "KIMI_MODEL_API_KEY", "KIMI_MODEL_BASE_URL", "KIMI_MODEL_PROVIDER_TYPE",
	"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR", "PI_CODING_AGENT_DIR",
}

// agentCLIConfigPaths are the HOME-relative files an agent CLI reads its MCP
// servers from.
var agentCLIMCPPaths = map[string]string{
	AgentCLIOpenCode:    ".config/opencode/opencode.json",
	AgentCLICursor:      ".cursor/mcp.json",
	AgentCLIAntigravity: ".gemini/config/mcp_config.json",
	AgentCLIKimi:        ".kimi-code/mcp.json",
	AgentCLIPi:          ".pi/agent/mcp.json",
	AgentCLIDeepSeek:    ".dsh/agentflow-mcp.json",
}

// AgentCLIMCPOptions shapes the MCP configuration of one invocation.
type AgentCLIMCPOptions struct {
	// ReadOnly denies the CLI's own shell, edit and web tools (AI 对话 runs on
	// the control plane); MCP tools stay available.
	ReadOnly bool
}

var dshServerNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// WriteAgentCLIMCPConfig renders MCP entries (the mcpConfigEntries shape:
// {"type":"http","url","headers"} or {"command","args","cwd","env"}) in the
// CLI's own format under home, replacing whatever an earlier invocation left.
func WriteAgentCLIMCPConfig(kind, home string, entries map[string]any, options AgentCLIMCPOptions) error {
	rel, ok := agentCLIMCPPaths[kind]
	if !ok {
		return fmt.Errorf("unknown agent CLI %q", kind)
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	servers := map[string]any{}
	var dshInserts []any
	for _, id := range ids {
		entry, _ := entries[id].(map[string]any)
		if entry == nil {
			continue
		}
		url, _ := entry["url"].(string)
		command, _ := entry["command"].(string)
		headers := entry["headers"]
		switch {
		case kind == AgentCLIOpenCode && url != "":
			server := map[string]any{"type": "remote", "url": url, "enabled": true, "timeout": 600000}
			if headers != nil {
				server["headers"] = headers
			}
			servers[id] = server
		case kind == AgentCLIOpenCode && command != "":
			argv := []any{command}
			if args, ok := entry["args"].([]string); ok {
				for _, arg := range args {
					argv = append(argv, arg)
				}
			} else if args, ok := entry["args"].([]any); ok {
				argv = append(argv, args...)
			}
			server := map[string]any{"type": "local", "command": argv, "enabled": true, "timeout": 600000}
			if env := entry["env"]; env != nil {
				server["environment"] = env
			}
			servers[id] = server
		case kind == AgentCLIDeepSeek && (url != "" || command != ""):
			name := dshServerNameRe.ReplaceAllString(id, "_")
			if len(name) > 32 {
				name = name[:32]
			}
			config := map[string]any{"serverName": name, "toolCallTimeoutMs": 600000}
			if url != "" {
				config["transport"], config["url"] = "streamable-http", url
				if headers != nil {
					config["headers"] = headers
				}
			} else {
				config["transport"], config["command"] = "stdio", command
				for _, key := range []string{"args", "env", "cwd"} {
					if value := entry[key]; value != nil {
						config[key] = value
					}
				}
			}
			dshInserts = append(dshInserts, map[string]any{"id": "agentflow-mcp-" + name, "name": "@deepseek-ai/dsh-mcp-client", "config": config})
		case url != "":
			key := "url"
			if kind == AgentCLIAntigravity {
				key = "serverUrl"
			}
			server := map[string]any{key: url}
			if headers != nil {
				server["headers"] = headers
			}
			servers[id] = server
		case command != "":
			server := map[string]any{"command": command}
			for _, key := range []string{"args", "env", "cwd"} {
				if value := entry[key]; value != nil && !(key == "cwd" && (kind == AgentCLICursor || kind == AgentCLIAntigravity)) {
					server[key] = value
				}
			}
			servers[id] = server
		}
	}
	var document any
	switch kind {
	case AgentCLIOpenCode:
		permission := map[string]any{"edit": "allow", "bash": "allow", "webfetch": "allow", "external_directory": "allow", "doom_loop": "allow"}
		if options.ReadOnly {
			permission = map[string]any{"edit": "deny", "bash": "deny", "webfetch": "deny", "websearch": "deny", "external_directory": "deny", "task": "deny"}
		}
		document = map[string]any{"$schema": "https://opencode.ai/config.json", "mcp": servers, "permission": permission, "autoupdate": false, "share": "disabled"}
	case AgentCLIDeepSeek:
		if len(dshInserts) == 0 {
			document = []any{}
		} else {
			document = []any{map[string]any{"insert": dshInserts}}
		}
	default:
		document = map[string]any{"mcpServers": servers}
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if kind == AgentCLICursor {
		// --force approves everything these rules do not deny.
		deny := []string{}
		if options.ReadOnly {
			deny = []string{"Shell(**)", "Write(**)", "Delete(**)", "WebFetch(*)"}
		}
		config, _ := json.Marshal(map[string]any{"version": 1, "permissions": map[string]any{"allow": []string{}, "deny": deny}})
		if err := writeAgentCLIFile(home, ".cursor/cli-config.json", config); err != nil {
			return err
		}
	}
	return writeAgentCLIFile(home, rel, data)
}

// agyChatPermissions are the Antigravity deny rules of AI 对话.
var agyChatPermissions = map[string]any{"allow": []string{"mcp(*)"}, "deny": []string{"command(*)", "write_file(*)", "read_url(*)"}}

func writeAgentCLIFile(home, rel string, data []byte) error {
	path := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ApplyAgentCLICredential writes the credential-dependent model configuration of an
// agent CLI into home (host path; homeEnv is the same directory as the
// process sees it) and returns argv and the environment to run it with. The
// key itself stays in the environment; configuration files only name the
// variable.
func ApplyAgentCLICredential(kind string, cred ProviderCredential, options AgentCLIOptions, home, homeEnv string, argv []string) ([]string, map[string]string, error) {
	key := strings.TrimSpace(cred.APIKey)
	if key == "" {
		return nil, nil, fmt.Errorf("需求责任人未配置 %s API Key，请在个人配置中配置", AgentCLILabel(kind))
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cred.BaseURL), "/")
	protocol := AgentCLIProtocol(kind, cred.Protocol)
	if baseURL == "" && AgentCLINeedsBaseURL(kind, protocol) {
		return nil, nil, fmt.Errorf("%s 需要在个人配置中填写 Base URL", AgentCLILabel(kind))
	}
	model := strings.TrimSpace(options.Model)
	if model == "" && !agentCLIModelOptional(kind, baseURL) {
		return nil, nil, fmt.Errorf("%s 需要指定模型", AgentCLILabel(kind))
	}
	homeEnv = filepath.ToSlash(homeEnv)
	env := map[string]string{}
	switch kind {
	case AgentCLIOpenCode:
		npm := map[string]string{AgentCLIProtocolChat: "@ai-sdk/openai-compatible", AgentCLIProtocolResponses: "@ai-sdk/openai", AgentCLIProtocolMessages: "@ai-sdk/anthropic"}[protocol]
		endpoint := baseURL
		if protocol == AgentCLIProtocolMessages && !strings.HasSuffix(endpoint, "/v1") {
			// Base URLs follow the Claude Code convention; the AI SDK wants /v1.
			endpoint += "/v1"
		}
		config := map[string]any{
			"provider": map[string]any{"agentflow": map[string]any{
				"npm":     npm,
				"name":    "AgentFlow",
				"options": map[string]any{"baseURL": endpoint, "apiKey": "{env:" + agentCLIKeyEnv + "}"},
				"models":  map[string]any{model: map[string]any{"name": model}},
			}},
			"model":      "agentflow/" + model,
			"autoupdate": false,
			"share":      "disabled",
		}
		if prompt := strings.TrimSpace(options.SystemPromptFile); prompt != "" {
			config["instructions"] = []string{prompt}
		}
		content, _ := json.Marshal(config)
		env[agentCLIKeyEnv] = key
		env["OPENCODE_CONFIG_CONTENT"] = string(content)
		env["OPENCODE_DISABLE_AUTOUPDATE"] = "1"
	case AgentCLICursor:
		env["CURSOR_API_KEY"] = key
	case AgentCLIAntigravity:
		settings := map[string]any{}
		if protocol == AgentCLIProtocolChat {
			env["AGY_LLM_GATEWAY_URL"] = baseURL
			env["AGY_LLM_GATEWAY_API_KEY"] = key
			env["AGY_LLM_GATEWAY_WIRE_PROTOCOL"] = "openai"
			env["AGY_LLM_GATEWAY_MODELS"] = model
		} else {
			settings["modelProvider"] = "gemini"
			env["GEMINI_API_KEY"] = key
			if baseURL != "" {
				env["GOOGLE_GEMINI_BASE_URL"] = baseURL
				env["GEMINI_BASE_URL"] = baseURL
			}
		}
		data, _ := json.Marshal(settings)
		if err := writeAgentCLIFile(home, ".gemini/antigravity-cli/settings.json", data); err != nil {
			return nil, nil, err
		}
		if options.ReadOnly {
			// Inline settings replace the file's for the session, so they
			// repeat the provider.
			settings["permissions"] = agyChatPermissions
			inline, _ := json.Marshal(settings)
			argv = insertBeforeFlag(argv, "-p", "--settings", string(inline))
		}
	case AgentCLIKimi:
		providerType := map[string]string{AgentCLIProtocolChat: "openai", AgentCLIProtocolResponses: "openai_responses", AgentCLIProtocolMessages: "anthropic"}[protocol]
		lines := []string{`default_model = "agentflow"`, "", "[providers.agentflow]"}
		if baseURL == "" {
			lines = append(lines, `type = "kimi"`)
		} else {
			lines = append(lines, "type = "+strconv.Quote(providerType), "base_url = "+strconv.Quote(baseURL))
		}
		lines = append(lines, "api_key_env = "+strconv.Quote(agentCLIKeyEnv), "",
			"[models.agentflow]", `provider = "agentflow"`, "model = "+strconv.Quote(model),
			"max_context_size = 262144", `capabilities = ["thinking", "tool_use"]`, "")
		if err := writeAgentCLIFile(home, ".kimi-code/config.toml", []byte(strings.Join(lines, "\n"))); err != nil {
			return nil, nil, err
		}
		env[agentCLIKeyEnv] = key
		env["KIMI_CODE_HOME"] = homeEnv + "/.kimi-code"
		env["KIMI_DISABLE_TELEMETRY"] = "1"
	case AgentCLIPi:
		api := map[string]string{AgentCLIProtocolChat: "openai-completions", AgentCLIProtocolResponses: "openai-responses", AgentCLIProtocolMessages: "anthropic-messages"}[protocol]
		models, _ := json.MarshalIndent(map[string]any{"providers": map[string]any{"agentflow": map[string]any{
			"baseUrl": baseURL, "api": api, "apiKey": "$" + agentCLIKeyEnv,
			"models": []map[string]any{{"id": model, "reasoning": true}},
		}}}, "", "  ")
		if err := writeAgentCLIFile(home, ".pi/agent/models.json", models); err != nil {
			return nil, nil, err
		}
		env[agentCLIKeyEnv] = key
		env["PI_CODING_AGENT_DIR"] = homeEnv + "/.pi/agent"
	case AgentCLIDeepSeek:
		patch := []any{}
		if baseURL == "" {
			env["DEEPSEEK_API_KEY"] = key
			if model != "" {
				patch = append(patch, map[string]any{"id": "agent-default-model", "config": map[string]any{"provider": "deepseek-official", "model": model}})
			}
		} else {
			api := map[string]string{AgentCLIProtocolChat: "openai-completions", AgentCLIProtocolResponses: "openai-responses", AgentCLIProtocolMessages: "anthropic-messages"}[protocol]
			env[agentCLIKeyEnv] = key
			patch = []any{
				map[string]any{"id": "llm-pi-ai", "config": map[string]any{"providers": map[string]any{"agentflow": map[string]any{
					"displayName": "AgentFlow", "apiKeyEnv": agentCLIKeyEnv, "api": api, "baseURL": baseURL,
					"models": []map[string]any{{"id": model}},
				}}}},
				map[string]any{"id": "agent-default-model", "config": map[string]any{"provider": "agentflow", "model": model}},
			}
		}
		data, _ := json.MarshalIndent(patch, "", "  ")
		if err := writeAgentCLIFile(home, ".dsh/agentflow-model.json", data); err != nil {
			return nil, nil, err
		}
		env["DSH_HOME"] = homeEnv + "/.dsh"
		// Launcher flags precede the profile's own arguments: dsh --profile <name> --patch … app-args.
		if len(argv) < 3 {
			return nil, nil, errors.New("invalid DeepSeek Harness invocation")
		}
		patches := []string{"--patch", homeEnv + "/.dsh/agentflow-model.json"}
		if _, err := os.Stat(filepath.Join(home, ".dsh", "agentflow-mcp.json")); err == nil {
			patches = append(patches, "--patch", homeEnv+"/.dsh/agentflow-mcp.json")
		}
		head := append([]string{}, argv[:3]...)
		argv = append(append(head, patches...), argv[3:]...)
	default:
		return nil, nil, fmt.Errorf("unknown agent CLI %q", kind)
	}
	return argv, env, nil
}

// insertBeforeFlag inserts values ahead of the last occurrence of flag, or
// appends them when flag is absent.
func insertBeforeFlag(argv []string, flag string, values ...string) []string {
	for i := len(argv) - 1; i > 0; i-- {
		if argv[i] == flag {
			out := append(append([]string{}, argv[:i]...), values...)
			return append(out, argv[i:]...)
		}
	}
	return append(append([]string{}, argv...), values...)
}

// AgentCLIUserCredentials maps a user's saved agent CLI credentials onto the
// form command bindings use.
func AgentCLIUserCredentials(user domain.User) map[string]ProviderCredential {
	if len(user.AgentCLICredentials) == 0 {
		return nil
	}
	out := make(map[string]ProviderCredential, len(user.AgentCLICredentials))
	for kind, cred := range user.AgentCLICredentials {
		out[kind] = ProviderCredential{APIKey: cred.APIKey, BaseURL: cred.BaseURL, Protocol: cred.Protocol}
	}
	return out
}

// AgentCLILabel is the product name shown in errors and settings.
func AgentCLILabel(kind string) string {
	switch kind {
	case AgentCLIOpenCode:
		return "OpenCode"
	case AgentCLICursor:
		return "Cursor CLI"
	case AgentCLIAntigravity:
		return "Antigravity"
	case AgentCLIKimi:
		return "Kimi Code"
	case AgentCLIPi:
		return "Pi"
	case AgentCLIDeepSeek:
		return "DeepSeek Harness"
	}
	return kind
}

// AgentCLIBinary is the executable each agent CLI installs.
func AgentCLIBinary(kind string) string {
	switch kind {
	case AgentCLICursor:
		return "cursor-agent"
	}
	return kind
}
