package runtime

import (
	"io"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// openCodeChatCommand is the argv the AI chat builds for the opencode agent
// CLI: chat's read-only policy, the owner's model and variant, a resumed
// native session, and the turn prompt.
func openCodeChatCommand(prompt string) CommandSpec {
	return AgentCLICommand(AgentCLIOpenCode, AgentCLICommandOptions{
		Binary: "opencode", Model: "m", Effort: "high", Resume: "ses_native", Prompt: prompt,
		SystemPrompt: "You are the LoopForge assistant.", SystemPromptFile: "/root/home/opencode-system.md",
		Chat: true,
	})
}

func openCodeArg(argv []string, name, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == name && argv[i+1] == value {
			return true
		}
	}
	return false
}

// `opencode run [message..]` takes the prompt as a positional message, so an
// oversized chat prompt would fail fork/exec with E2BIG on Linux
// (MAX_ARG_STRLEN, 128KiB per argument). OpenCode reads a non-TTY stdin to EOF
// and, when no positional message follows `--`, uses that text as the message
// (packages/opencode/src/cli/cmd/run.ts: resolveRunInput at v1.19.6), so the
// prompt can travel on stdin without changing any option.
func TestOpenCodeChatOversizedPromptUsesStdin(t *testing.T) {
	prompt := strings.Repeat("长对话", 200<<10/9+64)
	command := openCodeChatCommand(prompt)
	for _, arg := range command.Argv {
		if len(arg) >= 128<<10 {
			t.Fatalf("argv still carries a %d-byte argument", len(arg))
		}
	}
	if n := len(command.Argv); n < 2 || command.Argv[n-1] != "--" {
		t.Fatalf("argv must keep the -- terminator before any later flags: %q", command.Argv)
	}
	if command.Stdin == nil {
		t.Fatal("oversized chat prompt was not moved to stdin")
	}
	data, err := io.ReadAll(command.Stdin)
	if err != nil || string(data) != prompt {
		t.Fatalf("stdin carries %d bytes (err=%v), want %d", len(data), err, len(prompt))
	}
	for _, want := range [][2]string{
		{"--format", "json"}, {"-m", "agentflow/m"}, {"--variant", "high"}, {"-s", "ses_native"},
	} {
		if !openCodeArg(command.Argv, want[0], want[1]) {
			t.Fatalf("argv lost %s %s: %q", want[0], want[1], command.Argv)
		}
	}
	if command.AgentCLI.SystemPromptFile != "/root/home/opencode-system.md" {
		t.Fatalf("OpenCode lost its system prompt file: %q", command.AgentCLI.SystemPromptFile)
	}
	if !command.AgentCLI.ReadOnly {
		t.Fatal("chat turn lost its read-only tool policy")
	}
	if !ConversationCommandFor(command, RunCredentials{}).Interactive {
		t.Fatal("conversation pipeline would drop the stdin prompt")
	}
}

// Ordinary chat turns keep the existing positional form.
func TestOpenCodeChatSmallPromptStaysOnArgv(t *testing.T) {
	prompt := "看看这个仓库"
	command := openCodeChatCommand(prompt)
	if command.Stdin != nil {
		t.Fatal("short chat prompt must not use stdin")
	}
	if command.Argv[len(command.Argv)-1] != prompt {
		t.Fatalf("short chat prompt should stay positional: %q", command.Argv)
	}
	if ConversationCommandFor(command, RunCredentials{}).Interactive {
		t.Fatal("a positional prompt must not turn the CLI interactive")
	}
}

// SOP invocations write the task prompt to a file and pass a short pointer
// when it is oversized, so their argv shape must not change.
func TestOpenCodeSOPOversizedPromptKeepsPointer(t *testing.T) {
	prompt := strings.Repeat("任务", 200<<10/6+64)
	command, err := newAgentCLIProvider(AgentCLIOpenCode).Prepare(providerInvocation{
		Executor: domain.ExecutorDefinition{Kind: AgentCLIOpenCode, Binary: "opencode"},
		Compiled: compiledConfig{AgentRoot: "agent/role", SystemPrompt: "sys"},
		Prompt:   prompt, PromptFile: "runtime/task-prompt.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Stdin != nil {
		t.Fatal("SOP must not move its prompt to stdin")
	}
	last := command.Argv[len(command.Argv)-1]
	if last == prompt || !strings.Contains(last, "runtime/task-prompt.md") {
		t.Fatalf("SOP argv should point at the task prompt file: %q", last)
	}
}
