package runtime

import (
	"context"
	"io"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPromptOnStdinKeepsShortPromptOnArgv(t *testing.T) {
	command := CommandSpec{Argv: []string{"codebuddy", "-p", "--", "hello"}}
	got := PromptOnStdin(command, "hello")
	if strings.Join(got.Argv, " ") != "codebuddy -p -- hello" || got.Stdin != nil {
		t.Fatalf("short prompt should stay on argv: %q stdin=%v", got.Argv, got.Stdin != nil)
	}
}

func TestPromptOnStdinMovesOversizedPrompt(t *testing.T) {
	prompt := strings.Repeat("对话历史", maxArgvPromptBytes/12+100)
	argv := []string{"codebuddy", "-p", "--output-format", "stream-json", "--", prompt}
	got := PromptOnStdin(CommandSpec{Argv: argv, Provider: "codebuddy"}, prompt)
	if strings.Join(got.Argv, " ") != "codebuddy -p --output-format stream-json --" {
		t.Fatalf("oversized prompt must leave argv and keep the -- terminator: %d args, last=%.20q", len(got.Argv), got.Argv[len(got.Argv)-1])
	}
	if got.Stdin == nil {
		t.Fatal("oversized prompt must be sent on stdin")
	}
	data, err := io.ReadAll(got.Stdin)
	if err != nil || string(data) != prompt {
		t.Fatalf("stdin prompt mismatch: %d bytes, err=%v", len(data), err)
	}
	if argv[len(argv)-1] != prompt {
		t.Fatal("the caller's argv must not be modified")
	}
}

func TestPromptOnStdinLeavesOtherShapesAlone(t *testing.T) {
	prompt := strings.Repeat("x", maxArgvPromptBytes+1)
	cases := map[string]CommandSpec{
		"no terminator":    {Argv: []string{"cli", "-p", prompt}},
		"different prompt": {Argv: []string{"cli", "-p", "--", "summary"}},
		"stdin in use":     {Argv: []string{"cli", "-p", "--", prompt}, Stdin: strings.NewReader("protocol")},
	}
	for name, command := range cases {
		got := PromptOnStdin(command, prompt)
		if len(got.Argv) != len(command.Argv) || got.Stdin != command.Stdin {
			t.Fatalf("%s: command should be unchanged", name)
		}
	}
}

// Linux rejects a single argv string above MAX_ARG_STRLEN (128KiB) before the
// process starts. A chat prompt that embeds a long conversation must still
// reach the CLI, so it travels on stdin instead.
func TestOversizedChatPromptReachesCLIOnStdin(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("MAX_ARG_STRLEN is a Linux limit")
	}
	prompt := strings.Repeat("长对话", 200<<10/9) // ~200KiB, above MAX_ARG_STRLEN
	command := CommandSpec{Argv: []string{"sh", "-c", "wc -c", "--", prompt}}

	if _, err := ExecuteLocalCommand(context.Background(), t.TempDir(), command, RunCredentials{}, nil); err == nil || !strings.Contains(err.Error(), "argument list too long") || !strings.Contains(err.Error(), "128KiB") {
		t.Fatalf("argv prompt should fail with an explained E2BIG, got %v", err)
	}

	out, err := ExecuteLocalCommand(context.Background(), t.TempDir(), PromptOnStdin(command, prompt), RunCredentials{}, nil)
	if err != nil {
		t.Fatalf("stdin prompt should start the CLI: %v", err)
	}
	if n, convErr := strconv.Atoi(strings.TrimSpace(out)); convErr != nil || n != len(prompt) {
		t.Fatalf("CLI received %q bytes on stdin, want %d", strings.TrimSpace(out), len(prompt))
	}
}
