package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

const (
	// qoderCNConfigPath is Qoder CN's user-level settings file below HOME. It
	// keeps mcpServers beside model, permissions and hooks, so every writer
	// (MCP, credential, native approval) merges into it instead of replacing
	// the whole document.
	qoderCNConfigPath = ".qoder-cn/settings.json"

	// qoderCNSOPDisallowedTools are the tools that wait for a person.
	qoderCNSOPDisallowedTools = "AskUserQuestion EnterPlanMode ExitPlanMode"
	// qoderCNChatDisallowedTools adds what acts outside the chat's MCP gateway:
	// shell, edits, web, sub-agents and background tasks. Qoder CN is a
	// Claude-Code-family CLI, so the tool names are the capitalized ones
	// (Read, Write, Edit, Bash, WebFetch, Agent, ...).
	qoderCNChatDisallowedTools = qoderCNSOPDisallowedTools + " Bash Edit Write WebFetch WebSearch Agent TaskOutput TaskStop"
)

// applyQoderCNCredential binds the responsible user's personal access token.
// Qoder CN only reaches its official endpoint with a PAT: a custom Base URL is
// rejected, and the model is optional (the CLI picks its own when unset or
// "default"). The key stays in the environment; settings.json only names the
// model, never the token.
func applyQoderCNCredential(home AgentCLIHome, homeEnv, key, baseURL, protocol, model string, argv []string) ([]string, map[string]string, error) {
	if baseURL != "" {
		return nil, nil, errors.New("Qoder CN 仅支持官方端点（个人访问令牌），不支持自定义 Base URL")
	}
	if key == "" {
		return nil, nil, fmt.Errorf("需求责任人未配置 %s 个人访问令牌（PAT），请在个人配置中配置", AgentCLILabel(AgentCLIQoderCN))
	}
	env := map[string]string{
		"QODERCN_PERSONAL_ACCESS_TOKEN": key,
		"QODERCN_CONFIG_DIR":            homeEnv + "/.qoder-cn",
	}
	// An unset or "default" model keeps the CLI's own choice; a named one
	// travels both as QODERCN_MODEL and as settings.json model.name.
	model = strings.TrimSpace(model)
	named := model != "" && model != "default"
	if named {
		env["QODERCN_MODEL"] = model
	}
	if err := mergeQoderCNSettings(home, func(document map[string]any) {
		if !named {
			return
		}
		settings, _ := document["model"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		settings["name"] = model
		document["model"] = settings
	}); err != nil {
		return nil, nil, err
	}
	// AgentCLICommand supplies -p <prompt> and the read-only / --yolo tool
	// policy; guarantee the print format here so a minimal argv still emits
	// Claude-Code-compatible stream-json.
	argv = append([]string{}, argv...)
	if i := slices.Index(argv, "--output-format"); i >= 0 && i+1 < len(argv) {
		argv[i+1] = "stream-json"
	} else {
		argv = append(argv, "--output-format", "stream-json")
	}
	return argv, env, nil
}

// mergeQoderCNSettings reads the existing settings.json (when present), applies
// mutate and writes it back, so MCP servers, the model selection and the native
// approval hooks never clobber one another.
func mergeQoderCNSettings(home AgentCLIHome, mutate func(map[string]any)) error {
	root, err := openAgentCLIHome(home)
	if err != nil {
		return err
	}
	data, err := root.ReadFile(qoderCNConfigPath)
	root.Close()
	document := map[string]any{}
	if err == nil {
		if err = json.Unmarshal(data, &document); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if document == nil {
		document = map[string]any{}
	}
	mutate(document)
	out, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return writeAgentCLIFile(home, qoderCNConfigPath, out)
}