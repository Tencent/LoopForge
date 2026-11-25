package httpapi

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/runtime"
)

// Linux rejects any single argv string above MAX_ARG_STRLEN (128KiB).
const testMaxArgStrlen = 128 << 10

// A fresh chat session embeds the whole conversation in the prompt. Once it
// passes MAX_ARG_STRLEN the CLI failed with "fork/exec ...: argument list too
// long" before starting, and every retry failed the same way.
func oversizedAssistantPrompt() string {
	return strings.Repeat(`{"role":"user","content":"很长的对话历史"}`, 200<<10/48)
}

func assertPromptOnStdin(t *testing.T, name string, command runtime.CommandSpec, prompt string) {
	t.Helper()
	for _, arg := range command.Argv {
		if len(arg) >= testMaxArgStrlen {
			t.Fatalf("%s: argv still carries a %d-byte argument", name, len(arg))
		}
	}
	if command.Argv[len(command.Argv)-1] != "--" {
		t.Fatalf("%s: argv should end with the -- terminator, got %q", name, command.Argv[len(command.Argv)-1])
	}
	if command.Stdin == nil {
		t.Fatalf("%s: oversized prompt was not moved to stdin", name)
	}
	data, err := io.ReadAll(command.Stdin)
	if err != nil || string(data) != prompt {
		t.Fatalf("%s: stdin carries %d bytes (err=%v), want %d", name, len(data), err, len(prompt))
	}
}

func TestAssistantCodeBuddyOversizedPromptUsesStdin(t *testing.T) {
	prompt := oversizedAssistantPrompt()
	cases := map[string]assistantCommandOpts{
		"platform":  {Bin: "codebuddy", Config: "/tmp/mcp.json", Prompt: prompt, SessionID: "s1", Native: true},
		"workspace": {Bin: "codebuddy", Config: "/tmp/mcp.json", Prompt: prompt, SessionID: "s1", Native: true, Workspace: true},
		"resume":    {Bin: "codebuddy", Config: "/tmp/mcp.json", Prompt: prompt, SessionID: "s1", Native: true, Resume: true},
		"report":    {Bin: "codebuddy", Config: "/tmp/mcp.json", Prompt: prompt, ReportOnly: true},
		"review":    {Bin: "codebuddy", Config: "/tmp/mcp.json", Prompt: prompt, ReviewOnly: true},
	}
	for name, opts := range cases {
		command, err := prepareAssistantCommand(opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertPromptOnStdin(t, name, command, prompt)
		if !runtime.ConversationCommandFor(command, runtime.RunCredentials{}).Interactive {
			t.Fatalf("%s: remote conversation must forward stdin", name)
		}
	}
}

func TestAssistantCodeBuddyShortPromptStaysOnArgv(t *testing.T) {
	command, err := prepareAssistantCommand(assistantCommandOpts{Bin: "codebuddy", Config: "/tmp/mcp.json", Prompt: "你好", SessionID: "s1", Native: true})
	if err != nil {
		t.Fatal(err)
	}
	if command.Argv[len(command.Argv)-1] != "你好" || command.Stdin != nil {
		t.Fatalf("short prompt should stay positional: %q", command.Argv[len(command.Argv)-2:])
	}
}

func TestAssistantClaudeOversizedPromptUsesStdin(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := oversizedAssistantPrompt()
	inv, err := prepareAssistantClaudeInvocation(assistantEngineInvocationOpts{Bin: "claude", Root: root, Config: config, Prompt: prompt, Model: "m", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	assertPromptOnStdin(t, "claude", inv.command, prompt)
}

func TestClarifyOversizedPromptUsesStdin(t *testing.T) {
	prompt := oversizedAssistantPrompt()
	command, err := prepareClarifyCommandWith(clarifyCommandOpts{Bin: "codebuddy", Prompt: prompt, SessionID: "s", Native: true, MCPConfig: "/tmp/mcp.json", SubmitOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	assertPromptOnStdin(t, "clarify", command, prompt)
}
