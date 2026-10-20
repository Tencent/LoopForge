package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

type claudeProvider struct{ baseProvider }

func (claudeProvider) Kind() string { return "claude-code" }

func (claudeProvider) Materialize(ctx *providerMaterializeContext) error {
	if err := materializeClaudePlugin(ctx); err != nil {
		return err
	}
	ctx.Compiled.Args = append(ctx.Compiled.Args,
		"--mcp-config", filepath.ToSlash(filepath.Join(ctx.AgentRel, "mcp.json")),
		"--setting-sources", "user",
		"--system-prompt-file", filepath.ToSlash(filepath.Join(ctx.AgentRel, "agent-system.md")),
	)
	if len(ctx.Compiled.Hooks) > 0 {
		settingsPath := filepath.Join(ctx.ConfigRoot, "claude-settings.json")
		if err := writeHookSettings(ctx.Workspace, settingsPath, ctx.Compiled.Hooks); err != nil {
			return err
		}
		ctx.Compiled.Args = append(ctx.Compiled.Args, "--settings", filepath.ToSlash(filepath.Join(".agentflow", "config", ctx.AgentID, "claude-settings.json")))
	}
	if len(ctx.Assets.Skills) > 0 {
		ctx.Compiled.Args = append(ctx.Compiled.Args, "--plugin-dir", filepath.ToSlash(ctx.AgentRel))
	}
	return nil
}

func materializeClaudePlugin(ctx *providerMaterializeContext) error {
	if len(ctx.Assets.Skills) == 0 {
		return nil
	}
	manifestDir := filepath.Join(ctx.ConfigRoot, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return err
	}
	manifest, _ := json.MarshalIndent(map[string]any{
		"name":        "agentflow-" + ctx.AgentID,
		"version":     "1.0.0",
		"description": "Agent-scoped AgentFlow skills",
		"author":      map[string]string{"name": "AgentFlow"},
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), manifest, 0o600); err != nil {
		return err
	}
	return replaceRelativeSymlink(filepath.Join(ctx.AgentRoot, ".claude-plugin"), filepath.Join("..", "config", ctx.AgentID, ".claude-plugin"))
}

func (claudeProvider) Prepare(in providerInvocation) (CommandSpec, error) {
	argv := buildProviderArgs(in)
	argv = dropArgWithValue(argv, "--resume")
	argv = isolateClaudeFlags(argv, filepath.ToSlash(filepath.Join(in.Compiled.AgentRoot, "agent-system.md")))
	argv = appendEffortArgs(argv, in.Executor.Effort)
	// Current Claude Code releases require --verbose when print mode emits
	// stream-json. It is harmless for other print-mode output formats and keeps
	// older persisted executor definitions working after a CLI upgrade.
	if (hasArg(argv, "-p") || hasArg(argv, "--print")) && !hasArg(argv, "--verbose") {
		argv = append(argv, "--verbose")
	}
	if in.Driver == "local" && !hasArg(argv, "--strict-mcp-config") {
		argv = append(argv, "--strict-mcp-config")
	}
	if in.Compiled.HasSkills {
		argv = append(argv, "--plugin-dir", in.Compiled.AgentRoot)
	}
	if sessionID := strings.TrimSpace(in.Options.ResumeSessionID); sessionID != "" {
		argv = append(argv, "--resume", sessionID)
	}
	return CommandSpec{Argv: append(argv, positionalPrompt(in)), Provider: "claude-code", CredentialBindings: []CredentialBinding{BindClaudeCode, BindGit, BindMCP}, PrivateHome: true}, nil
}

func (claudeProvider) Protocol(ex domain.ExecutorDefinition, sink EventSink) *protocolAdapter {
	return makeProtocolAdapter(ex, sink, protocolAnthropic, "anthropic-stream-v1")
}

func (claudeProvider) SandboxRequirements() SandboxRequirements {
	return SandboxRequirements{PrivateHome: true, BunFS: true}
}

func isolateClaudeFlags(args []string, systemPromptPath string) []string {
	start := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		start = 1
	}
	head := append([]string{}, args[:start]...)
	rest := make([]string, 0, len(args)-start+4)
	skipNext := false
	for _, arg := range args[start:] {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "--setting-sources" || arg == "--system-prompt-file" || arg == "--plugin-dir" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--setting-sources=") || strings.HasPrefix(arg, "--system-prompt-file=") || strings.HasPrefix(arg, "--plugin-dir=") {
			continue
		}
		rest = append(rest, arg)
	}
	// Claude Code does not accept CodeBuddy's synthetic `none` value. Select
	// only host user settings while excluding repository project/local settings.
	rest = append(rest, "--setting-sources", "user", "--system-prompt-file", systemPromptPath)
	return append(head, rest...)
}
