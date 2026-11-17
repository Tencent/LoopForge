package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func openAITestStream(w http.ResponseWriter, output any) {
	w.Header().Set("Content-Type", "text/event-stream")
	data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}}})
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
}

func TestOpenAIDirectToolsUseGatewayWithoutCLIOrProviderState(t *testing.T) {
	toolCalls, rounds := 0, 0
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("provider credential forwarded to tool gateway")
		}
		var request struct {
			ID     any
			Method string
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "read_example", "description": "read only", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			if request.Params.Name != "read_example" {
				t.Error("wrong gateway tool")
			}
			toolCalls++
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "gateway evidence"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer mcp.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test-openai-private" {
			t.Error("wrong endpoint/auth")
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["store"] != false || request["previous_response_id"] != nil || request["conversation"] != nil {
			t.Error("provider-side session/state requested")
		}
		if rounds == 0 {
			if len(request["tools"].([]any)) != 1 {
				t.Error("gateway catalog missing")
			}
			openAITestStream(w, []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_example", "arguments": "{}"}})
		} else {
			input, _ := json.Marshal(request["input"])
			if !strings.Contains(string(input), "gateway evidence") || !strings.Contains(string(input), "full conversation") {
				t.Error("tool result or history missing")
			}
			openAITestStream(w, []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "API_OK"}}}})
		}
		rounds++
	}))
	defer api.Close()
	config := filepath.Join(t.TempDir(), "mcp.json")
	raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{agentToolSessionMCPServerID: map[string]any{"url": mcp.URL}}})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "unused-cli-root")
	var total domain.TokenUsage
	out, _, err := runAssistantModel(context.Background(), assistantCLIRequest{Model: "openai:gpt-4.1-mini", Prompt: "full conversation", MCPConfig: config, SessionRoot: root, Resume: true, OnUsage: func(u domain.TokenUsage) { total.TotalTokens += u.TotalTokens }, Execute: func(context.Context, string, runtime.CommandSpec, runtime.RunCredentials, runtime.EventSink) (string, error) {
		t.Fatal("CLI started for API provider")
		return "", nil
	}}, domain.User{OpenAIAPIKey: "test-openai-private", OpenAIBaseURL: api.URL}, nil)
	if err != nil || out != "API_OK" || rounds != 2 || toolCalls != 1 || total.TotalTokens != 26 {
		t.Fatalf("out=%q rounds=%d calls=%d usage=%d err=%v", out, rounds, toolCalls, total.TotalTokens, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("API provider created CLI state")
	}
}

func TestOpenAIDirectRejectsIncompleteStreamAndRedactsErrors(t *testing.T) {
	for _, failure := range []string{"incomplete", "unauthorized"} {
		t.Run(failure, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "unauthorized" {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":{"code":"invalid_api_key","message":"bad test-private-key"}}`)
					return
				}
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
			}))
			defer api.Close()
			_, _, err := runAssistantModel(context.Background(), assistantCLIRequest{Model: "openai:gpt-4.1-mini", Prompt: "hello"}, domain.User{OpenAIAPIKey: "test-private-key", OpenAIBaseURL: api.URL}, nil)
			if err == nil || strings.Contains(err.Error(), "test-private-key") {
				t.Fatalf("unsafe success/error: %v", err)
			}
		})
	}
}

func TestOpenAIPersonalCredentialPersistsAndIsNotPublic(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := "https://api.openai.com/v1"
	models := []domain.CLIModel{{ID: "gpt-4.1-mini", Name: "OpenAI API"}}
	u, err := store.UpdateUserCredentials(1, repo.UserCredentialUpdate{OpenAIAPIKey: "test-private-key", OpenAIBaseURL: &base, OpenAIModels: &models})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(domain.User{ID: 1, LoginName: u.LoginName, ChineseName: "Updated"}); err != nil {
		t.Fatal(err)
	}
	u, _ = store.User(1)
	if u.OpenAIAPIKey != "test-private-key" || len(u.OpenAIModels) != 1 {
		t.Fatal("credential lost on profile refresh")
	}
	public := domain.PublicUser(u)
	raw, _ := json.Marshal(public)
	if !public.OpenAIAPIKeyConfigured || strings.Contains(string(raw), "test-private-key") {
		t.Fatal("credential leaked or configured flag missing")
	}
	model, err := clarifyExecutionModel(u, "openai:gpt-4.1-mini")
	if err != nil || model != "openai:gpt-4.1-mini" {
		t.Fatalf("API clarification requires CLI: %s %v", model, err)
	}
	if err := clarifyModelEngineError(model); err != nil {
		t.Fatal(err)
	}
	if _, err = clarifyExecutionModel(domain.User{}, model); err == nil {
		t.Fatal("borrowed a credential")
	}
}

func TestOpenAIModelPickerLoadsItsPersonalCatalog(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	models := []domain.CLIModel{{ID: "gpt-4.1-mini", Name: "OpenAI API"}}
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{OpenAIAPIKey: "test-private-key", OpenAIModels: &models}); err != nil {
		t.Fatal(err)
	}
	r := cvBRequest(t, f, http.MethodGet, "/api/assistant/models?executor=openai", nil, true)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "openai:gpt-4.1-mini") || strings.Contains(r.Body.String(), "test-private-key") {
		t.Fatalf("catalog status=%d body=%s", r.Code, r.Body.String())
	}
}

func TestOpenAIWebChatAndClarificationWithoutCodeBuddy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", t.TempDir())
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", filepath.Join(t.TempDir(), "missing-codebuddy"))
	f := cvBNewHTTPFixture(t)
	gateway := httptest.NewServer(f.h)
	defer gateway.Close()
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = gateway.URL
	config.SetCurrent(endpoints)
	defer config.SetCurrent(previous)
	clarifying := false
	readFile := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage       `json:"input"`
			Tools []struct{ Name string } `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !clarifying {
			openAITestStream(w, []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "API_CHAT_OK"}}}})
			return
		}
		catalog, _ := json.Marshal(request.Tools)
		if !strings.Contains(string(catalog), "clarify_read_file") || !strings.Contains(string(catalog), submitClarificationTool) {
			t.Error("clarification gateway tools missing")
		}
		if !readFile {
			openAITestStream(w, []any{map[string]any{"type": "function_call", "call_id": "read", "name": "clarify_read_file", "arguments": `{"path":"source.go"}`}})
			readFile = true
			return
		}
		input, _ := json.Marshal(request.Input)
		if !strings.Contains(string(input), "package sample") {
			t.Error("API did not receive scoped file evidence")
		}
		openAITestStream(w, []any{map[string]any{"type": "function_call", "call_id": "submit", "name": submitClarificationTool, "arguments": `{"summary":"Checked the source; implementation scope is clear.","questions":[],"ready":true}`}})
	}))
	defer api.Close()
	base := api.URL
	models := []domain.CLIModel{{ID: "gpt-4.1-mini"}}
	user, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{OpenAIAPIKey: "test-private-key", OpenAIBaseURL: &base, OpenAIModels: &models})
	if err != nil || user.CodeBuddyAPIKey != "" {
		t.Fatalf("API-only user setup failed: %v", err)
	}
	chat := cvBRequest(t, f, http.MethodPost, "/api/assistant/chat", map[string]any{
		"sessionId": "api-only-chat", "executor": "openai", "model": "openai:gpt-4.1-mini",
		"messages": []any{map[string]string{"role": "user", "content": "Say API_CHAT_OK"}},
	}, true)
	if chat.Code != http.StatusOK || !strings.Contains(chat.Body.String(), "API_CHAT_OK") {
		t.Fatalf("API chat: status=%d body=%s", chat.Code, chat.Body.String())
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutProject(domain.Project{ID: "api-project", Name: "API project", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRepository(domain.Repository{ID: "api-source", Name: "API source", ProjectID: "api-project", LocalPath: root}); err != nil {
		t.Fatal(err)
	}
	clarifying = true
	h := &streamHandlers{deps: &deps{repo: f.s}}
	parsed, _, _, err := h.executeClarify(context.Background(), domain.Issue{Title: "Check source", ProjectID: "api-project", AssigneeUserID: user.ID, RepositoryID: "api-source"}, chatClarifyRequest{Model: "openai:gpt-4.1-mini"}, nil)
	if err != nil || parsed == nil || !readFile {
		t.Fatalf("API clarification: parsed=%v read=%t err=%v", parsed, readFile, err)
	}
}
