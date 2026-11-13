package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestDeepSeekChatRebuildsLongHistory(t *testing.T) {
	for _, mode := range []string{"new", "branch", "missing-native-session", "project", "project-branch", "project-missing-native-session"} {
		t.Run(mode, func(t *testing.T) {
			h, user := workbenchFixture(t)
			updated, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{"dsh": {APIKey: "test-only"}}})
			if err != nil {
				t.Fatal(err)
			}
			user = updated
			dir := t.TempDir()
			capture := filepath.Join(dir, "calls")
			bin := filepath.Join(dir, "dsh")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '0.2.2-rc.1\\n'; exit 0; fi\ncat >> " + quote(capture) + "\nprintf '\\036' >> " + quote(capture) + `
previous=''
for arg in "$@"; do
  if [ "$previous" = '--session-id' ] && [ "$arg" = 'missing_native' ]; then
    printf 'No conversation found with session ID: missing_native\n' >&2
    exit 1
  fi
  previous="$arg"
done
printf '%s\n' '{"type":"session","sessionId":"native_test"}' '{"type":"final","sessionId":"native_test","text":"ok","reason":{"kind":"completed"}}'
`
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_DSH_BIN", bin)
			history := []assistantMessage{{Role: "user", Content: "history-start " + strings.Repeat("历史正文", 10000) + " HISTORY-MIDDLE-MARKER " + strings.Repeat("历史正文", 10000) + " history-end"}, {Role: "assistant", Content: "previous answer"}}
			session := domain.AssistantSession{ID: "long_history", Executor: "dsh", Model: "dsh:deepseek-v4-flash", Messages: history}
			if strings.HasPrefix(mode, "project") {
				session.ProjectID = "workbench"
			}
			if mode == "branch" || mode == "project-branch" {
				session.ParentSessionID = "parent_chat"
			}
			if err := h.repo.PutAssistantSession(user.ID, session); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-native-session" {
				root := assistantConversationRoot(user.ID, session.ID)
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := writeAssistantEngineState(root, assistantEngineState{Engine: "dsh", SessionID: "resume_seed"}); err != nil {
					t.Fatal(err)
				}
				if err := writeAgentCLISession(root, "dsh", "resume_seed", "missing_native"); err != nil {
					t.Fatal(err)
				}
			}
			messages := append(append([]assistantMessage(nil), history...), assistantMessage{Role: "user", Content: "latest question"})
			if mode == "project-missing-native-session" {
				// Establish a real shared native checkpoint, then lose only the
				// provider's transcript. h.chat must retry with the full history.
				body, _ := json.Marshal(assistantRequest{SessionID: session.ID, ProjectID: session.ProjectID, Executor: "dsh", Model: session.Model, Messages: messages})
				c, w := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
				h.chat(c)
				if w.Code != 200 {
					t.Fatalf("seed status=%d body=%s", w.Code, w.Body)
				}
				saved, _ := h.repo.AssistantSession(user.ID, session.ID)
				messages = append(append([]assistantMessage(nil), saved.Messages...), assistantMessage{Role: "user", Content: "latest question after loss"})
				root := assistantSharedSessionRoot(session.ID)
				data, err := os.ReadFile(filepath.Join(root, "session.json"))
				if err != nil {
					t.Fatal(err)
				}
				var checkpoint assistantNativeCheckpoint
				if err := json.Unmarshal(data, &checkpoint); err != nil || !checkpoint.Ready {
					t.Fatalf("invalid checkpoint: %v", err)
				}
				if err := writeAgentCLISession(root, "dsh", checkpoint.SessionID, "missing_native"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(capture, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			body, _ := json.Marshal(assistantRequest{SessionID: session.ID, ProjectID: session.ProjectID, Executor: "dsh", Model: session.Model, Messages: messages})
			c, w := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
			h.chat(c)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSuffix(string(data), "\x1e"), "\x1e")
			wantCalls := 1
			if strings.Contains(mode, "missing-native-session") {
				wantCalls = 2
			}
			if len(calls) != wantCalls {
				t.Fatalf("calls=%d want=%d", len(calls), wantCalls)
			}
			rebuilt := calls[len(calls)-1]
			if len(rebuilt) <= 128<<10 || !strings.Contains(rebuilt, "history-start") || !strings.Contains(rebuilt, "HISTORY-MIDDLE-MARKER") || !strings.Contains(rebuilt, "history-end") || !strings.Contains(rebuilt, "latest question") {
				t.Fatal("rebuild lost history or did not exercise a large input")
			}
			if strings.Contains(mode, "missing-native-session") && strings.Contains(calls[0], "history-start") {
				t.Fatal("resume unexpectedly replayed history before recovery")
			}
		})
	}
}

// Run the real CLI/credential/exec pipeline, with a model-free headless probe.
// Unlike argv inspection alone, this catches MAX_ARG_STRLEN failures on Linux.
func TestDeepSeekChatLongPromptStdin(t *testing.T) {
	for _, mode := range []string{"new", "branch", "missing-native-session"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "dsh")
			capture := filepath.Join(root, "input")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '0.2.2-rc.1\\n'; exit 0; fi\ncat > " + quote(capture) + "\nprintf '%s\\n' '{\"type\":\"session\",\"sessionId\":\"native_test\"}' '{\"type\":\"final\",\"sessionId\":\"native_test\",\"text\":\"ok\",\"reason\":{\"kind\":\"completed\"}}'\n"
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENTFLOW_DSH_BIN", bin)
			h := &assistantHandlers{}
			longQuestion := "latest-start " + strings.Repeat("最近问题", 16000) + " LATEST-MIDDLE-MARKER " + strings.Repeat("最近问题", 16000) + " latest-end"
			req := assistantRequest{SessionID: "chat_test", Model: "dsh:deepseek-v4-flash", Messages: []assistantMessage{
				{Role: "user", Content: "history-start " + strings.Repeat("历史正文", 20000) + " history-end"},
				{Role: "assistant", Content: "previous answer"},
				{Role: "user", Content: longQuestion},
			}}
			prompt := h.turnPrompt(req, "", false)
			if len(prompt) <= 128<<10 {
				t.Fatal("fixture does not exceed Linux argument limit")
			}
			user := domain.User{ID: 1, AgentCLICredentials: map[string]domain.AgentCLICredential{
				runtime.AgentCLIDeepSeek: {APIKey: "test-only"},
			}}
			system := "SYSTEM " + strings.Repeat("system instruction\n", 9000)
			cliReq := assistantCLIRequest{Model: req.Model, Prompt: prompt, SystemPrompt: system, SessionRoot: root, SessionID: req.SessionID, Native: true}
			if mode == "missing-native-session" {
				cliReq.Resume = true
				if _, _, err := runAssistantCLI(context.Background(), cliReq, user, nil); err != errAssistantSessionMissing {
					t.Fatalf("missing continuation: %v", err)
				}
				cliReq.Resume = false // h.chat's full-history recovery path
			}
			if mode == "branch" {
				cliReq.SessionID = "branch_test"
			}
			out, _, err := runAssistantCLI(context.Background(), cliReq, user, nil)
			if err != nil || out != "ok" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			want := runtime.AgentCLITaskPrompt(system, "", prompt)
			if string(data) != want {
				t.Fatalf("stdin changed: got %d bytes, want %d", len(data), len(want))
			}
			if !strings.Contains(string(data), "LATEST-MIDDLE-MARKER") {
				t.Fatal("the middle of the latest user message was dropped")
			}
			// A following turn must still resume the native session normally.
			cliReq.Resume, cliReq.Prompt = true, "follow-up"
			if _, _, err := runAssistantCLI(context.Background(), cliReq, user, nil); err != nil {
				t.Fatal(err)
			}
			data, _ = os.ReadFile(capture)
			if string(data) != "follow-up" {
				t.Fatal("resumed turn repeats system prompt or loses task")
			}
		})
	}
}

func TestAssistantE2BIGIsRecognizableAndRedacted(t *testing.T) {
	err := fmt.Errorf("launch: %w", &os.PathError{Op: "fork/exec", Path: "/private/secret/dsh", Err: syscall.E2BIG})
	status, message := assistantGenerationFailure(context.Background(), err)
	if status != 502 || !strings.Contains(message, "ASSISTANT_ARGV_TOO_LARGE") || strings.Contains(message, "/private") {
		t.Fatalf("status=%d message=%q", status, message)
	}
}
