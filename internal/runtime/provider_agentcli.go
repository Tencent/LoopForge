package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

// agentCLIProvider runs one of the agent CLIs (AgentCLIKinds) in its native
// headless mode. The role's MCP servers go into the CLI's own configuration
// inside a per-Agent private HOME; ApplyAgentCLICredential adds the model configuration
// and the user's key there at launch.
type agentCLIProvider struct{ baseProvider }

func newAgentCLIProvider(kind string) agentCLIProvider {
	return agentCLIProvider{baseProvider{kind: kind, parser: protocolTranslated, adapter: "agent-cli-stream-v1"}}
}

func (p agentCLIProvider) Materialize(ctx *providerMaterializeContext) error {
	entries, err := mcpConfigEntries(ctx.MCPServers)
	if err != nil {
		return err
	}
	name := AgentCLIPrivateHomeKey(p.kind, ctx.AgentID)
	if _, err := EnsureControlDirectory(ctx.Workspace, "private-home", name); err != nil {
		return err
	}
	if err := WriteAgentCLIMCPConfig(p.kind, WorkspaceAgentCLIHome(ctx.Workspace, name), entries, AgentCLIMCPOptions{}); err != nil {
		return err
	}
	ctx.Compiled.SystemPrompt = string(ctx.Content)
	return nil
}

// agentCLINativeSystemPrompt marks CLIs that load the system prompt file
// themselves; the others receive it ahead of the task.
func agentCLINativeSystemPrompt(kind string) bool {
	return kind == AgentCLIOpenCode || kind == AgentCLIPi
}

// AgentCLITaskPrompt prefixes the system prompt for CLIs without a system
// prompt option. An oversized prompt is referenced by path instead.
func AgentCLITaskPrompt(system, systemFile, task string) string {
	system = strings.TrimSpace(system)
	if system == "" {
		return task
	}
	if len(system)+len(task) > maxArgvPromptBytes && systemFile != "" {
		return "先完整阅读并严格遵守 `" + systemFile + "` 中的系统指令，然后完成下面的任务。\n\n" + task
	}
	return "<system_instructions>\n" + system + "\n</system_instructions>\n\n" + task
}

func newAgentCLISessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (p agentCLIProvider) Prepare(in providerInvocation) (CommandSpec, error) {
	systemFile := ""
	if in.Compiled.AgentRoot != "" {
		systemFile = filepath.ToSlash(filepath.Join(in.Compiled.AgentRoot, "agent-system.md"))
	}
	command := AgentCLICommand(p.kind, AgentCLICommandOptions{
		Binary: in.Executor.Binary, Model: in.Executor.Model, Effort: in.Executor.Effort,
		Resume: in.Options.ResumeSessionID, Prompt: positionalPrompt(in),
		SystemPrompt: in.Compiled.SystemPrompt, SystemPromptFile: systemFile, Extra: in.Executor.Args,
	})
	command.CredentialBindings = []CredentialBinding{BindAgentCLI, BindGit, BindMCP}
	command.PrivateHome = true
	command.PrivateHomeKey = AgentCLIPrivateHomeKey(p.kind, in.Compiled.AgentID)
	return command, nil
}

// AgentCLICommandOptions describes one headless agent CLI turn.
type AgentCLICommandOptions struct {
	Binary, Model, Effort string
	// Resume is the CLI's own session ID to continue; empty starts one.
	Resume string
	Prompt string
	// SystemPrompt is the system prompt body; SystemPromptFile is the same
	// text as a path the process can read.
	SystemPrompt, SystemPromptFile string
	Extra                          []string
	// Chat runs the CLI for AI 对话 on the control plane: MCP tools stay
	// available, shell, file writes and web access do not. The MCP
	// configuration (WriteAgentCLIMCPConfig ReadOnly) completes the policy.
	Chat      bool
	Workspace bool
	// AgentFile is Kimi Code's chat agent definition (WriteKimiChatAgent).
	AgentFile string
}

// AgentCLICommand builds the argv of an agent CLI turn; the caller adds
// credential bindings and the HOME policy.
func AgentCLICommand(kind string, o AgentCLICommandOptions) CommandSpec {
	binary := strings.TrimSpace(o.Binary)
	if binary == "" {
		binary = AgentCLIBinary(kind)
	}
	model := strings.TrimSpace(o.Model)
	effort := strings.TrimSpace(o.Effort)
	resume := strings.TrimSpace(o.Resume)
	prompt := o.Prompt
	// A chat session keeps the prompt of its first turn; SOP invocations
	// are independent processes that each receive it.
	inline := !agentCLINativeSystemPrompt(kind) && !(o.Chat && kind == AgentCLIKimi)
	if inline && !(o.Chat && resume != "") {
		prompt = AgentCLITaskPrompt(o.SystemPrompt, o.SystemPromptFile, prompt)
	}
	extra := append([]string{}, o.Extra...)
	var argv []string
	readOnly := o.Chat && !o.Workspace
	options := AgentCLIOptions{Model: model, ReadOnly: readOnly}
	switch kind {
	case AgentCLIOpenCode:
		argv = []string{binary, "run", "--format", "json", "--thinking"}
		if model != "" {
			argv = append(argv, "-m", "agentflow/"+model)
		}
		if resume != "" {
			argv = append(argv, "-s", resume)
		}
		if effort != "" {
			argv = append(argv, "--variant", effort)
		}
		options.SystemPromptFile = o.SystemPromptFile
		argv = append(append(argv, extra...), prompt)
	case AgentCLICursor:
		// Chat relies on the deny rules of .cursor/cli-config.json; --force
		// still approves the MCP tools.
		argv = []string{binary, "-p", "--output-format", "stream-json", "--stream-partial-output", "--trust", "--force", "--approve-mcps"}
		if model != "" {
			// ApplyAgentCLICredential re-resolves this against the owner's
			// saved catalog, which only the credential carries.
			argv = append(argv, "--model", CursorModelID(model, effort, nil))
			options.Effort = effort
		}
		if resume != "" {
			argv = append(argv, "--resume", resume)
		}
		argv = append(append(argv, extra...), prompt)
	case AgentCLIAntigravity:
		// Chat deny rules travel as --settings (ApplyAgentCLICredential);
		// they hold even with --dangerously-skip-permissions.
		argv = []string{binary, "--output-format", "stream-json", "--dangerously-skip-permissions", "--disable-slash-commands"}
		if model != "" && AgyModelEfforts(model) == nil {
			effort = ""
		}
		if model != "" {
			argv = append(argv, "--model", agyModelForEffort(model, effort))
		}
		if effort != "" {
			argv = append(argv, "--effort", effort)
		}
		if resume != "" {
			argv = append(argv, "--conversation", resume)
		}
		argv = append(append(argv, extra...), "-p", prompt)
	case AgentCLIKimi:
		argv = []string{binary, "--output-format", "stream-json"}
		if resume != "" {
			argv = append(argv, "-S", resume)
		}
		if o.AgentFile != "" && resume == "" {
			// The session keeps the agent, so resumed turns cannot pass it again.
			argv = append(argv, "--agent-file", o.AgentFile)
		}
		argv = append(append(argv, extra...), "-p", prompt)
	case AgentCLIPi:
		session := resume
		if session == "" {
			session = newAgentCLISessionID()
		}
		argv = []string{binary, "--mode", "json", "--session-id", session}
		if model != "" {
			argv = append(argv, "--model", "agentflow/"+model)
		}
		if effort != "" {
			argv = append(argv, "--thinking", effort)
		}
		if o.SystemPromptFile != "" {
			argv = append(argv, "--append-system-prompt", o.SystemPromptFile)
		}
		if o.Chat {
			// Pi restores its last declared tool loadout from the journal when
			// --tools is omitted. Explicitly reset it on every mode handoff.
			tools := "read,codemode"
			if o.Workspace {
				tools = "read,bash,edit,write,codemode"
			}
			argv = append(argv, "--tools", tools)
		}
		if readOnly {
			argv = append(argv, "--exclude-tools", "bash,edit,write")
		}
		argv = append(append(argv, extra...), prompt)
	case AgentCLIDeepSeek:
		mode := "danger-full-access"
		if readOnly {
			mode = "read-only"
		}
		argv = []string{binary, "--profile", "headless", "--json", "--permission-mode", mode, "--approval-policy", "never"}
		if resume != "" {
			argv = append(argv, "--session-id", resume)
		}
		argv = append(append(argv, extra...), prompt)
	case AgentCLIZCode:
		disallowed := zcodeSOPDisallowedTools
		if readOnly {
			disallowed = zcodeChatDisallowedTools
		}
		argv = []string{binary, "--prompt", prompt, "--output-format", "stream-json", "--mode", "yolo", "--no-color", "--disallowed-tools", disallowed}
		if resume != "" {
			argv = append(argv, "--resume", resume)
		}
		argv = append(argv, extra...)
	}
	return CommandSpec{Argv: argv, Provider: kind, AgentCLI: options}
}

// AgyModelEfforts lists the efforts agy accepts for a model, weakest first.
// Only its Gemini models take --effort; agy rejects the flag for any other
// model a Base URL serves ("--effort is not supported for model …").
func AgyModelEfforts(model string) []string {
	if strings.HasPrefix(strings.TrimSpace(model), "gemini-") {
		return domain.ExecutorEffortLevels[AgentCLIAntigravity]
	}
	return nil
}

// agyModelForEffort replaces the effort suffix of an Antigravity model ID
// (gemini-3.8-flash-medium) with the requested effort: agy rejects a model
// whose suffix differs from --effort.
func agyModelForEffort(model, effort string) string {
	if effort == "" {
		return model
	}
	for _, level := range domain.ExecutorEffortLevels[AgentCLIAntigravity] {
		if base, ok := strings.CutSuffix(model, "-"+level); ok {
			return base + "-" + effort
		}
	}
	return model
}

// kimiChatDisallowedTools are Kimi Code's tools that act outside the chat's
// MCP gateway or spawn further agents.
var kimiChatDisallowedTools = []string{"Bash", "Edit", "Write", "FetchURL", "WebSearch", "Agent", "AgentSwarm", "CronCreate", "CronDelete", "CronList", "TaskOutput", "TaskStop", "WaitFor"}

// WriteKimiChatAgent writes the Kimi Code agent definition for AI 对话: the
// system prompt as its body and the chat's tool restrictions.
func WriteKimiChatAgent(path, systemPrompt string) error {
	return WriteKimiConversationAgent(path, systemPrompt, true)
}

func WriteKimiConversationAgent(path, systemPrompt string, readOnly bool) error {
	body := strings.TrimSpace(systemPrompt)
	if body == "" {
		body = "You are the AgentFlow assistant."
	}
	denied := ""
	if readOnly {
		denied = strings.Join(kimiChatDisallowedTools, ", ")
	}
	content := "---\nname: agentflow-chat\ndescription: LoopForge AI 对话\ndisallowedTools: [" + denied + "]\n---\n" + body + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func (agentCLIProvider) SandboxRequirements() SandboxRequirements {
	return SandboxRequirements{PrivateHome: true}
}
