package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// openCodeChatTurn builds the AI chat command for the opencode agent CLI the
// same way runAssistantCLI does: a session root with the chat's MCP
// configuration and the system prompt as a file OpenCode loads itself. A
// resumed turn reuses a native session the platform recorded earlier.
func openCodeChatTurn(t *testing.T, prompt string, resume bool) runtime.CommandSpec {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if resume {
		if err := writeAgentCLISession(root, runtime.AgentCLIOpenCode, "s1", "ses_native"); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := prepareAssistantAgentCLIInvocation(runtime.AgentCLIOpenCode, assistantEngineInvocationOpts{
		Bin: "opencode", Root: root, Config: config, Prompt: prompt, Model: "m", Effort: "high",
		SessionID: "s1", Resume: resume, SystemPrompt: "You are the LoopForge assistant.",
	})
	if err != nil {
		t.Fatalf("prepare opencode chat invocation: %v", err)
	}
	if inv.resumeMissing {
		t.Fatal("fixture native session was not recorded")
	}
	return inv.command
}

// Exercise the conversation write/EOF transport, not just cmd.Stdin: a CLI
// reading until EOF must finish and receive every byte, including newlines.
func TestAssistantOpenCodePromptThroughConversationProcess(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("fake CLI needs a POSIX shell")
	}
	h, user := workbenchFixture(t)
	dir := t.TempDir()
	fake, messageFile := filepath.Join(dir, "fake-opencode"), filepath.Join(dir, "message")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat > \"$OC_MESSAGE_FILE\"\nprintf 'read-to-eof\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "--message\n" + strings.Repeat("长对话\n", 24<<10) + "\n"
	command := runtime.AgentCLICommand(runtime.AgentCLIOpenCode, runtime.AgentCLICommandOptions{
		Binary: fake, Model: "m", Prompt: prompt, Chat: true,
	})
	command = runtime.WithCommandEnvironment(command, map[string]string{"OC_MESSAGE_FILE": messageFile})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session := &AgentToolSession{ctx: ctx, ChatOwnerID: user.ID, ChatSessionID: "opencode-stdin", User: user}
	var output strings.Builder
	_, err := h.conversationExecutor(session, t.TempDir())(ctx, "", command, runtime.RunCredentials{}, func(event domain.TelemetryEvent) {
		if event.Name == "process.stdout" {
			output.WriteString(event.Message)
		}
	})
	if err != nil {
		t.Fatalf("conversation write/EOF failed: %v", err)
	}
	got, err := os.ReadFile(messageFile)
	if err != nil || !bytes.Equal(got, []byte(prompt)) {
		t.Fatalf("conversation delivered %d bytes (err=%v), want %d unchanged bytes", len(got), err, len(prompt))
	}
	if !strings.Contains(output.String(), "read-to-eof") {
		t.Fatalf("CLI did not finish reading stdin: %q", output.String())
	}
}

func openCodeOption(argv []string, name, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == name && argv[i+1] == value {
			return true
		}
	}
	return false
}

// A chat turn embeds the whole conversation in the prompt, and
// `opencode run [message..]` takes it as a positional message. Past Linux
// MAX_ARG_STRLEN (128KiB) fork/exec fails with E2BIG before the CLI starts.
// OpenCode reads a non-TTY stdin to EOF and uses it as the message when no
// positional message follows `--` (packages/opencode/src/cli/cmd/run.ts,
// resolveRunInput at v1.19.6), so an oversized prompt can travel on stdin.
func TestAssistantOpenCodeChatOversizedPromptUsesStdin(t *testing.T) {
	prompt := strings.Repeat("长对话", 200<<10/9+64)
	if len(prompt) <= testMaxArgStrlen {
		t.Fatalf("fixture is %d bytes, want more than MAX_ARG_STRLEN", len(prompt))
	}
	for _, resume := range []bool{false, true} {
		command := openCodeChatTurn(t, prompt, resume)
		for _, arg := range command.Argv {
			if len(arg) >= testMaxArgStrlen {
				t.Fatalf("resume=%v: argv still carries a %d-byte argument", resume, len(arg))
			}
		}
		if command.Stdin == nil {
			t.Fatalf("resume=%v: oversized prompt was not moved to stdin", resume)
		}
		data, err := io.ReadAll(command.Stdin)
		if err != nil || string(data) != prompt {
			t.Fatalf("resume=%v: stdin carries %d bytes (err=%v), want %d", resume, len(data), err, len(prompt))
		}
		// The prompt is the only moved part: format, model, variant and the
		// CLI's own system prompt file must survive the handoff.
		for _, want := range [][2]string{
			{"--format", "json"}, {"-m", "agentflow/m"}, {"--variant", "high"},
		} {
			if !openCodeOption(command.Argv, want[0], want[1]) {
				t.Fatalf("resume=%v: argv lost %s %s: %q", resume, want[0], want[1], command.Argv)
			}
		}
		if resume && !openCodeOption(command.Argv, "-s", "ses_native") {
			t.Fatalf("resume=true: argv lost the native session: %q", command.Argv)
		}
		if command.AgentCLI.SystemPromptFile == "" {
			t.Fatalf("resume=%v: OpenCode lost its system prompt file", resume)
		}
		// The conversation pipeline relays stdin only for an interactive command.
		if !runtime.ConversationCommandFor(command, runtime.RunCredentials{}).Interactive {
			t.Fatalf("resume=%v: chat pipeline would drop the stdin prompt", resume)
		}
	}
}

// Short prompts keep the existing positional form.
func TestAssistantOpenCodeChatSmallPromptStaysOnArgv(t *testing.T) {
	prompt := "看看这个仓库"
	command := openCodeChatTurn(t, prompt, false)
	if command.Stdin != nil {
		t.Fatal("short prompt must not use stdin")
	}
	if command.Argv[len(command.Argv)-1] != prompt {
		t.Fatalf("short prompt should stay positional: %q", command.Argv)
	}
	if runtime.ConversationCommandFor(command, runtime.RunCredentials{}).Interactive {
		t.Fatal("a positional prompt must not turn the CLI interactive")
	}
}

// fakeOpenCodeCLI follows the documented `opencode run [message..]` contract:
// the message is the positional argument (yargs puts everything after `--`
// there), piped stdin is read to EOF, and resolveRunInput joins the two.
const fakeOpenCodeCLI = `#!/bin/sh
printf '%s\n' "$@" > "$OC_ARGV_FILE"
msg=""
dd=0
for a in "$@"; do
  if [ "$dd" = 1 ]; then msg="$a"; fi
  if [ "$a" = "--" ]; then dd=1; fi
done
piped=$(cat)
if [ -z "$msg" ]; then msg="$piped"; else msg="$msg
$piped"; fi
bytes=$(printf '%s' "$msg" | wc -c | tr -d ' ')
printf '{"type":"result","message_bytes":%s}\n' "$bytes"
`

// The kernel limit is real: the pre-fix argv shape cannot be started, and the
// stdin shape delivers the complete prompt as the CLI's message.
func TestAssistantOpenCodeChatPromptReachesFakeCLI(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("MAX_ARG_STRLEN is a Linux limit")
	}
	prompt := strings.Repeat("长对话", 200<<10/9+64)
	command := openCodeChatTurn(t, prompt, true)
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(fake, []byte(fakeOpenCodeCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(dir, "argv.txt")
	run := func(argv []string, stdin io.Reader) ([]byte, error) {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Stdin = stdin
		cmd.Env = append(os.Environ(), "OC_ARGV_FILE="+argvFile)
		return cmd.Output()
	}

	// The pre-fix shape carried the prompt as the trailing argument. Keeping
	// it here proves this container enforces MAX_ARG_STRLEN, so the passing
	// shape below cannot pass for the wrong reason.
	legacy := append(append([]string{fake}, command.Argv[1:]...), prompt)
	if _, err := run(legacy, nil); err == nil || !strings.Contains(err.Error(), "argument list too long") {
		t.Fatalf("argv prompt should fail with E2BIG, got %v", err)
	}

	out, err := run(append([]string{fake}, command.Argv[1:]...), command.Stdin)
	if err != nil {
		t.Fatalf("opencode did not start with the prompt on stdin: %v", err)
	}
	var event struct {
		MessageBytes int `json:"message_bytes"`
	}
	if err := json.Unmarshal(out, &event); err != nil {
		t.Fatalf("fake CLI output %q: %v", out, err)
	}
	if event.MessageBytes != len(prompt) {
		t.Fatalf("opencode built a %d-byte message, want %d", event.MessageBytes, len(prompt))
	}
	recorded, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	for _, arg := range argv {
		if len(arg) >= testMaxArgStrlen {
			t.Fatalf("the started CLI still received a %d-byte argument", len(arg))
		}
	}
	for _, want := range [][2]string{{"--format", "json"}, {"-m", "agentflow/m"}, {"--variant", "high"}, {"-s", "ses_native"}} {
		if !openCodeOption(argv, want[0], want[1]) {
			t.Fatalf("the started CLI lost %s %s: %q", want[0], want[1], argv)
		}
	}
}
