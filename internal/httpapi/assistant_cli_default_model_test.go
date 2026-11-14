package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// A resumed Codex thread must also leave the model to Codex when none is
// chosen, and keep an explicit one (#461).
func TestAssistantCodexResumeOmitsEmptyModel(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  any
	}{{"", nil}, {"  ", nil}, {"gpt-5.5", "gpt-5.5"}} {
		dir := t.TempDir()
		config := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeCodexThread(dir, "s1", "thread-1"); err != nil {
			t.Fatal(err)
		}
		inv, err := prepareAssistantCodexInvocation(assistantEngineInvocationOpts{Bin: "codex", Root: dir, Config: config, Prompt: "hi", Model: tc.model, SessionID: "s1", Resume: true})
		if err != nil || inv.resumeMissing {
			t.Fatalf("resume invocation: missing=%v err=%v", inv.resumeMissing, err)
		}
		inv.translate(`{"id":1,"result":{}}`)
		buf := make([]byte, 1<<16)
		n, err := inv.command.Stdin.(*os.File).Read(buf)
		inv.finish(false)
		if err != nil {
			t.Fatal(err)
		}
		var resume map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(buf[:n])), "\n") {
			var message struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal([]byte(line), &message) == nil {
				if message.Method == "thread/start" {
					t.Fatalf("resume started a new thread: %s", line)
				}
				if message.Method == "thread/resume" {
					resume = message.Params
				}
			}
		}
		if resume == nil || resume["threadId"] != "thread-1" {
			t.Fatalf("thread/resume params = %v in %q", resume, buf[:n])
		}
		if got, ok := resume["model"]; tc.want == nil && ok || tc.want != nil && got != tc.want {
			t.Fatalf("model %q: thread/resume params = %v", tc.model, resume)
		}
	}
}

// runAssistantCLI is where the chat's "CLI default" choice reaches a
// non-CodeBuddy CLI: the placeholder must not become `--model default`,
// while an explicit model is still passed through (#461).
func TestRunAssistantCLIClaudeDefaultKeepsCLIModel(t *testing.T) {
	for _, tc := range []struct {
		model, want string
	}{{assistantDefaultModel, ""}, {" Default ", ""}, {"claude-haiku-5.5", "claude-haiku-5.5"}} {
		dir := t.TempDir()
		argvFile := filepath.Join(dir, "argv")
		bin := filepath.Join(dir, "claude")
		script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argvFile + "\n" +
			"cat <<'RESULT'\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"OK\"}\nRESULT\n"
		if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTFLOW_CLAUDE_BIN", bin)
		out, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{Executor: "claude", Model: tc.model, Prompt: "hi", Native: true, SessionRoot: dir}, domain.User{ID: 1, ClaudeCodeAPIKey: "k"}, nil)
		if err != nil || !strings.Contains(out, "OK") {
			t.Fatalf("model %q: out=%q err=%v", tc.model, out, err)
		}
		raw, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
		got := ""
		for i, arg := range argv {
			if arg == "--model" && i+1 < len(argv) {
				got = argv[i+1]
			}
		}
		if got != tc.want {
			t.Fatalf("model %q: --model = %q, want %q (argv %q)", tc.model, got, tc.want, argv)
		}
	}
}
