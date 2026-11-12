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
	// The hook bridge is static and shared by every CodeBuddy Agent in the
	// checkout, and a parallel branch may be running it: keep the directory
	// and rewrite only changed files. The settings, which merge this Agent's
	// own hooks, are per Agent so parallel roles do not overwrite each other.
	if _, err := EnsureControlDirectory(ctx.Workspace, "hooks", "codebuddy"); err != nil {
		return err
	}
	if err := observability.WriteCodeBuddyHookBridgeAt(ctx.Workspace, filepath.Join(".agentflow", "hooks", "codebuddy")); err != nil {
		return err
	}
	base, err := readWorkspaceFile(ctx.Workspace, filepath.Join(ctx.Workspace, ".agentflow", "hooks", "codebuddy-settings.json"))
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(ctx.ConfigRoot, "codebuddy-settings.json")
	if err := writeManagedFile(ctx.Workspace, settingsPath, base, 0o600); err != nil {
		return err
	}
	if err := mergeHookSettings(ctx.Workspace, settingsPath, ctx.Compiled.Hooks); err != nil {
		return err
	}
	if err := replaceRelativeSymlink(filepath.Join(ctx.AgentRoot, "hooks"), filepath.Join("..", "hooks", "codebuddy")); err != nil {
		return err
	}
	if err := replaceRelativeSymlink(filepath.Join(ctx.AgentRoot, "codebuddy-settings.json"), filepath.Join("..", "config", ctx.AgentID, "codebuddy-settings.json")); err != nil {
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
	out := dropArgWithValue(args, "--setting-sources", "--permission-mode", "--plugin-dir")
	if !hasArg(out, "-y") && !hasArg(out, "--dangerously-skip-permissions") {
		out = append(out, "-y")
	}
	out = append(out, "--permission-mode", "bypassPermissions")
	return append(out, "--setting-sources", settingSource)
}
