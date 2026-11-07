package runtime

import (
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/observability"
)

type codeBuddyProvider struct{ baseProvider }

func (codeBuddyProvider) Kind() string { return "codebuddy" }

func (codeBuddyProvider) Materialize(ctx *providerMaterializeContext) error {
	if err := materializeClaudePlugin(ctx); err != nil {
		return err
	}
	if _, err := resetControlDirectory(ctx.Workspace, "hooks", "codebuddy"); err != nil {
		return err
	}
	if err := writeManagedFile(ctx.Workspace, filepath.Join(ctx.Workspace, ".agentflow", "hooks", "codebuddy-settings.json"), nil, 0o600); err != nil {
		return err
	}
	if err := observability.WriteCodeBuddyHookBridgeAt(ctx.Workspace, filepath.Join(".agentflow", "hooks", "codebuddy")); err != nil {
		return err
	}
	if err := mergeHookSettings(ctx.Workspace, filepath.Join(ctx.Workspace, ".agentflow", "hooks", "codebuddy-settings.json"), ctx.Compiled.Hooks); err != nil {
		return err
	}
	if err := replaceRelativeSymlink(filepath.Join(ctx.AgentRoot, "hooks"), filepath.Join("..", "hooks", "codebuddy")); err != nil {
		return err
	}
	if err := replaceRelativeSymlink(filepath.Join(ctx.AgentRoot, "codebuddy-settings.json"), filepath.Join("..", "hooks", "codebuddy-settings.json")); err != nil {
		return err
	}
	ctx.Compiled.Args = append(ctx.Compiled.Args,
		"--mcp-config", filepath.ToSlash(filepath.Join(".agentflow", "config", ctx.AgentID, "mcp.json")),
		"--system-prompt-file", filepath.ToSlash(filepath.Join(ctx.AgentRel, "agent-system.md")),
		"--settings", filepath.ToSlash(filepath.Join(ctx.AgentRel, "codebuddy-settings.json")),
	)
	return nil
}

func (codeBuddyProvider) Prepare(in providerInvocation) (CommandSpec, error) {
	argv := buildProviderArgs(in)
	argv = dropArgWithValue(argv, "--resume")
	argv = isolateCodeBuddyFlagsWithSource(argv, "none")
	argv = appendEffortArgs(argv, in.Executor.Effort)
	if in.Compiled.HasMCP {
		// --tools is a built-in allowlist. CodeBuddy also applies it to MCP
		// tools, so a Read/Write/Bash-only list hides HTTP MCP servers.
		argv = dropArgWithValue(argv, "--tools")
	}
	if !hasArg(argv, "--strict-mcp-config") {
		argv = append(argv, "--strict-mcp-config")
	}
	if in.Compiled.HasSkills {
		argv = append(argv, "--plugin-dir", in.Compiled.AgentRoot)
	}
	options := in.Options
	if sessionID := strings.TrimSpace(in.Options.ResumeSessionID); sessionID != "" {
		argv = append(argv, "--resume", sessionID)
	}
	return CommandSpec{
		// CodeBuddy declares --plugin-dir as variadic (<dirs...>). Terminate
		// option parsing explicitly so the positional prompt is not consumed as
		// another plugin directory.
		Argv:               append(argv, "--", positionalPrompt(in)),
		Provider:           "codebuddy",
		CredentialBindings: []CredentialBinding{BindCodeBuddy, BindGit, BindMCP},
		PrivateHome:        true,
		HookOutput:         in.Compiled.HookOutput,
		Telemetry:          &options,
	}, nil
}

func (codeBuddyProvider) Protocol(ex domain.ExecutorDefinition, sink EventSink) *protocolAdapter {
	return makeProtocolAdapter(ex, sink, protocolAnthropic, "anthropic-stream-v1")
}

func (codeBuddyProvider) SandboxRequirements() SandboxRequirements {
	return SandboxRequirements{PrivateHome: true, BunFS: true}
}

// isolateCodeBuddyFlags keeps unattended print-mode runs from loading project
// settings and from stalling on permission prompts.
func isolateCodeBuddyFlags(args []string) []string {
	return isolateCodeBuddyFlagsWithSource(args, "user")
}

func isolateCodeBuddyFlagsWithSource(args []string, settingSource string) []string {
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
		if arg == "--setting-sources" || arg == "--permission-mode" || arg == "--plugin-dir" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--setting-sources=") || strings.HasPrefix(arg, "--permission-mode=") || strings.HasPrefix(arg, "--plugin-dir=") {
			continue
		}
		rest = append(rest, arg)
	}
	if !hasArg(rest, "-y") && !hasArg(rest, "--dangerously-skip-permissions") {
		rest = append(rest, "-y")
	}
	rest = append(rest, "--permission-mode", "bypassPermissions")
	rest = append(rest, "--setting-sources", settingSource)
	return append(head, rest...)
}
