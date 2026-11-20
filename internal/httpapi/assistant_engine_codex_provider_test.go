package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestCodexAppServerSelectsTurnProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resume   bool
		personal bool
	}{
		{name: "start with personal credentials", personal: true},
		{name: "resume with personal credentials", resume: true, personal: true},
		{name: "start with runner login"},
		{name: "resume with runner login", resume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "mcp.json")
			if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.resume {
				if err := writeCodexThread(root, "shared-session", "019a-old"); err != nil {
					t.Fatal(err)
				}
			}
			var credential runtime.ProviderCredential
			if tc.personal {
				credential = runtime.ProviderCredential{APIKey: "fixture-personal-key", BaseURL: "https://models.example.test/v1"}
			}
			inv, err := prepareAssistantCodexInvocation(assistantEngineInvocationOpts{
				Bin: "codex", Root: root, Config: config, SessionID: "shared-session",
				Model: "member-model", Resume: tc.resume, User: credential,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { inv.finish(false) })
			stdin := bufio.NewReader(inv.command.Stdin)
			readCodexTestRPC(t, stdin)
			inv.translate(`{"id":1,"result":{}}`)
			readCodexTestRPC(t, stdin)
			request := readCodexTestRPC(t, stdin)
			if tc.resume && !tc.personal {
				if request.Method != "config/read" || request.Params["includeLayers"] != false {
					t.Fatalf("did not resolve this runner's current provider: %+v", request)
				}
				inv.translate(`{"id":2,"result":{"config":{"model_provider":"runner-provider"}}}`)
				request = readCodexTestRPC(t, stdin)
			}
			wantMethod := "thread/start"
			if tc.resume {
				wantMethod = "thread/resume"
				if request.Params["threadId"] != "019a-old" {
					t.Fatalf("resumed a different thread: %+v", request)
				}
			}
			if request.Method != wantMethod || request.Params["model"] != "member-model" {
				t.Fatalf("lost this turn's model: %+v", request)
			}
			if provider, exists := request.Params["modelProvider"]; tc.personal {
				if provider != "agentflow_user" {
					t.Fatalf("personal credentials can resume the previous provider: %+v", request)
				}
			} else if tc.resume {
				if provider != "runner-provider" {
					t.Fatalf("runner login lost its current provider: %+v", request)
				}
			} else if exists {
				t.Fatalf("runner login start must preserve its own provider: %+v", request)
			}
		})
	}
}

func TestCodexProviderChangeCanUseResumeRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		previous   string
		current    string
		credential runtime.ProviderCredential
	}{
		{name: "runner login to personal key", previous: "runner-provider", current: "agentflow_user",
			credential: runtime.ProviderCredential{APIKey: "fixture-personal-key", BaseURL: "https://models.example.test/v1"}},
		{name: "personal key back to runner login", previous: "agentflow_user", current: "runner-provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := writeCodexThread(root, "shared-session", "019a-old"); err != nil {
				t.Fatal(err)
			}
			user := domain.User{CodexAPIKey: tc.credential.APIKey, CodexBaseURL: tc.credential.BaseURL}
			progressed := false
			_, _, err := runAssistantCLI(context.Background(), assistantCLIRequest{
				Executor: "codex", Model: "codex:member-model", SessionID: "shared-session", SessionRoot: root,
				Prompt: "Only this turn's incremental input.", Native: true, RemoteBinary: true, Resume: true,
				RunnerLogin: tc.credential.APIKey == "",
				Execute: func(_ context.Context, _ string, command runtime.CommandSpec, _ runtime.RunCredentials, sink runtime.EventSink) (string, error) {
					stdin := bufio.NewReader(command.Stdin)
					readCodexTestRPC(t, stdin)
					sink(domain.TelemetryEvent{Name: "process.stdout", Message: `{"id":1,"result":{}}`})
					readCodexTestRPC(t, stdin)
					request := readCodexTestRPC(t, stdin)
					if tc.credential.APIKey == "" {
						if request.Method != "config/read" {
							t.Fatalf("runner provider was not resolved: %+v", request)
						}
						sink(domain.TelemetryEvent{Name: "process.stdout", Message: codexLine(map[string]any{
							"id": request.ID, "result": map[string]any{"config": map[string]any{"model_provider": tc.current}},
						})})
						request = readCodexTestRPC(t, stdin)
					}
					if request.Method != "thread/resume" || request.Params["modelProvider"] != tc.current || request.Params["model"] != "member-model" {
						t.Fatalf("resumed with another actor's provider or model: %+v", request)
					}
					// Codex 0.170 refuses a provider change before starting a turn.
					sink(domain.TelemetryEvent{Name: "process.stdout", Message: codexLine(map[string]any{
						"id": request.ID, "error": map[string]any{"code": -32600, "message": "Cannot change this thread's model provider from `" + tc.previous + "` to `" + tc.current + "` on resume."},
					})})
					if _, err := stdin.ReadByte(); err != io.EOF {
						t.Fatalf("provider rejection started more work: %v", err)
					}
					return "", nil
				},
			}, user, func(event assistantStreamEvent) {
				progressed = progressed || assistantPhaseProgress(event)
			})
			if !errors.Is(err, errAssistantSessionMissing) || progressed || !assistantResumeRecoverable(context.Background(), err) {
				t.Fatalf("provider change cannot safely rebuild from platform history: progressed=%v err=%v", progressed, err)
			}
		})
	}
}

func TestCodexResumeConfigRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		provider string
	}{
		{name: "default provider", response: `{"id":2,"result":{"config":{"model_provider":null}}}`, provider: "openai"},
		{name: "read failure", response: `{"id":2,"error":{"code":-32603,"message":"configuration unavailable"}}`},
		{name: "invalid response", response: `{"id":2,"result":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, stdin, _ := codexTestTurn(t, true)
			readCodexTestRPC(t, stdin)
			inv.translate(`{"id":1,"result":{}}`)
			readCodexTestRPC(t, stdin)
			if request := readCodexTestRPC(t, stdin); request.Method != "config/read" {
				t.Fatalf("did not read current provider: %+v", request)
			}
			out := inv.translate(tc.response)
			if tc.provider != "" {
				request := readCodexTestRPC(t, stdin)
				if request.Method != "thread/resume" || request.Params["modelProvider"] != tc.provider {
					t.Fatalf("default provider was not used: %+v", request)
				}
			} else {
				if len(out) != 1 || strings.Contains(out[0], "No conversation found") {
					t.Fatalf("configuration failure was treated as a missing session: %v", out)
				}
				if _, err := stdin.ReadByte(); err != io.EOF {
					t.Fatalf("configuration failure continued to a turn: %v", err)
				}
			}
		})
	}
}
