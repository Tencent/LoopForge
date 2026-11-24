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
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestFeatureModelPreservesExplicitChoice(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "deployment-default")
	codexUser := domain.User{CodexAPIKey: "test-only", CodexModels: []domain.CLIModel{{ID: "personal-model"}}}
	codeBuddyUser := domain.User{CodeBuddyAPIKey: "test-only", CodeBuddyInternetEnvironment: "ioa"}
	multiCLI := codeBuddyUser
	multiCLI.CodexAPIKey = codexUser.CodexAPIKey
	multiCLI.CodeBuddyModels = []domain.CLIModel{{ID: "personal-codebuddy-model"}}
	preferredCLI := multiCLI
	preferredCLI.AssistantDefaults = domain.AssistantDefaults{Executor: "codex", Model: "preferred-model"}
	for _, tc := range []struct {
		user domain.User
		in   string
		want string
	}{
		{codexUser, "", "codex:personal-model"},
		{codexUser, "codex:default", "codex:personal-model"},
		{codexUser, "codex:custom-model", "codex:custom-model"},
		{multiCLI, "", "codebuddy:deployment-default"},
		{multiCLI, "default", "codebuddy:deployment-default"},
		{multiCLI, "codex:custom-model", "codex:custom-model"},
		{preferredCLI, "default", "codex:preferred-model"},
		{preferredCLI, "codex:default", "codex:preferred-model"},
		{preferredCLI, "codebuddy:default", "codebuddy:deployment-default"},
		{preferredCLI, "deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{codeBuddyUser, "deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{codeBuddyUser, "codebuddy:deepseek-v4.1-flash", "codebuddy:deepseek-v4.1-flash"},
	} {
		if got, err := resolveFeatureModel(tc.user, tc.in); err != nil || got != tc.want {
			t.Errorf("model %q resolved to %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := resolveFeatureModel(codeBuddyUser, "codex:custom-model"); err == nil {
		t.Fatal("accepted an explicit CLI without its own credential")
	}
	h := &assistantHandlers{}
	if got := h.assistantChatRequestModel(assistantRequest{}, domain.AssistantSession{}, preferredCLI); got != "codex:preferred-model" {
		t.Errorf("new chat default = %q", got)
	}
	if got := h.assistantChatRequestModel(assistantRequest{}, domain.AssistantSession{Executor: "codebuddy", Model: "deepseek-v4.1-flash"}, preferredCLI); got != "deepseek-v4.1-flash" {
		t.Errorf("existing chat choice = %q", got)
	}
}

// The platform workflow installs Codex before running this test. Its only
// model endpoint is a loopback Responses fixture and no CodeBuddy CLI or key
// is present. MCP calls go through the actual session gateway.
func TestFeatureModelsWithoutCodeBuddy(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_NATIVE_PROVIDERS") != "1" {
		t.Skip("set AGENTFLOW_TEST_NATIVE_PROVIDERS=1 with Codex installed")
	}
	if _, err := resolveCodeBuddyBin(); err == nil {
		t.Fatal("this acceptance test requires a machine without CodeBuddy")
	}
	const selected = "codex:fixture-model"
	const key = "fixture-model-key"
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	d := &deps{repo: store}
	gateway := &agentToolGateway{deps: d}
	router := gin.New()
	router.Any("/internal/agent-tools/:token/mcp", gateway.ServeHTTP)
	mcp := httptest.NewServer(router)
	t.Cleanup(mcp.Close)
	t.Setenv("AGENTFLOW_AGENT_TOOL_GATEWAY_URL", mcp.URL)
	t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
	assistant := &assistantHandlers{deps: d, identities: &botIdentityHandlers{deps: d}}
	stream := &streamHandlers{deps: d}
	const project = "cli-acceptance"
	const userID = int64(123)
	must(store.PutUser(domain.User{ID: userID, LoginName: "codex-only"}))
	must(store.PutProject(domain.Project{ID: project, Name: "CLI acceptance", OwnerID: userID}))
	root := t.TempDir()
	must(os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture-source"), 0o600))
	must(store.PutRepository(domain.Repository{ID: "fixture", Name: "fixture", ProjectID: project, LocalPath: root}))
	issue := domain.Issue{ProjectID: project, Title: "Document fixture", RepositoryID: "fixture"}
	run := domain.Run{ID: "fixture-run", ExecutionContext: domain.TelemetryContext{ProjectID: project, ResponsibleUserID: userID}}

	for _, name := range []string{"eval", "approval", "report", "bot", "clarification"} {
		t.Run(name, func(t *testing.T) {
			var calls, clarifyStep, clarifyRound atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string          `json:"model"`
					Tools json.RawMessage `json:"tools"`
					Input json.RawMessage `json:"input"`
				}
				if r.Method != "POST" || r.URL.Path != "/v1/responses" {
					http.NotFound(w, r)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "invalid fixture request", http.StatusBadRequest)
					return
				}
				if body.Model != "fixture-model" || r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("Codex received the wrong model or credential")
				}
				step := calls.Add(1)
				switch name {
				case "eval":
					writeFeatureResponses(w, step, featureResponseText(`{"summary":"evaluated","dimensions":{"control":90,"latency":90,"tools":90,"tokens":90,"recovery":90,"delivery":90}}`))
				case "approval":
					writeFeatureResponses(w, step, featureResponseText(`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"approved fixture"}`))
				case "report":
					if step > 1 {
						writeFeatureResponses(w, step, featureResponseText("Report submitted."))
						return
					}
					if strings.Contains(string(body.Tools), "clarify_") || strings.Contains(string(body.Tools), "issues_list") {
						t.Error("report can see tools outside its submission contract")
					}
					writeFeatureResponses(w, step, featureResponseTool(t, step, body.Tools, submitNotificationReportTool, `{"body":"report accepted"}`))
				case "bot":
					writeFeatureResponses(w, step, featureResponseText("bot accepted"))
				case "clarification":
					switch clarifyStep.Add(1) {
					case 1:
						if clarifyRound.Load() == 1 && !strings.Contains(string(body.Input), "previous answer") {
							t.Error("next clarification turn lost the transcript")
						}
						writeFeatureResponses(w, step, featureResponseTool(t, step, body.Tools, "clarify_list_files", `{"repository":"fixture"}`))
					case 2:
						if !strings.Contains(string(body.Input), "README.md") {
							t.Error("repository listing was not returned")
						}
						writeFeatureResponses(w, step, featureResponseTool(t, step, body.Tools, "clarify_read_file", `{"repository":"fixture","path":"README.md"}`))
					case 3:
						if !strings.Contains(string(body.Input), "fixture-source") {
							t.Error("repository text was not returned")
						}
						writeFeatureResponses(w, step, featureResponseTool(t, step, body.Tools, submitClarificationTool, `{"ready":true,"summary":"Clarification accepted","questions":[]}`))
					default:
						writeFeatureResponses(w, step, featureResponseText("Clarification submitted."))
					}
				}
			}))
			t.Cleanup(model.Close)
			base := model.URL + "/v1"
			user, err := store.UpdateUserCredentials(userID, repo.UserCredentialUpdate{CodexAPIKey: key, CodexBaseURL: &base})
			must(err)
			ctx, stop := context.WithTimeout(context.Background(), 24*time.Second)
			defer stop()
			switch name {
			case "eval":
				result, err := (&runsHandlers{deps: d}).executeRunEval(ctx, domain.RunEvalRecord{ID: "eval-fixture", Model: selected}, run, user, func(string, any) {})
				if err != nil || result.Summary != "evaluated" {
					t.Fatalf("eval result=%+v err=%v", result, err)
				}
			case "approval":
				review := assistant.approvalReviewer(assistantRequest{Model: selected}, user, nil)(ctx, "fixture", "Review fixture", nil)
				if !review.allows() || review.Model != selected {
					t.Fatalf("review=%+v", review)
				}
			case "report":
				report, err := assistant.generateSOPReport(ctx, run, selected, "Report fixture.")
				if err != nil || report.Body != "report accepted" {
					t.Fatalf("report=%+v err=%v", report, err)
				}
			case "bot":
				bot := domain.NotificationBot{ID: "fixture-bot", BotID: "fixture-bot-account", ProjectID: project, Model: selected, Enabled: true, InteractionEnabled: true}
				must(store.SaveNotificationBot(bot, "fixture-secret"))
				now := time.Now()
				token, err := store.NewBotBindingChallenge(bot.BotID, "sender", now)
				must(err)
				code, err := store.ClaimBotBindingChallenge(token, user.ID, now)
				must(err)
				_, err = store.ConfirmBotUserBinding(bot.BotID, "sender", code, now)
				must(err)
				reply, err := assistant.generateBotReply(ctx, bot, domain.BotConversation{}, nil, domain.BotInteraction{SenderID: "sender", Content: "Hello from the fixture."})
				if err != nil || reply != "bot accepted" {
					t.Fatalf("reply=%q err=%v", reply, err)
				}
			case "clarification":
				req := chatClarifyRequest{Model: selected, Message: "Document fixture"}
				for round := 0; round < 2; round++ {
					clarifyRound.Store(int32(round))
					clarifyStep.Store(0)
					out, _, info, err := stream.executeClarify(ctx, issue, req, nil)
					if err != nil || !strings.Contains(fmt.Sprint(out), "Clarification accepted") || info["analyzed"] != true {
						t.Fatalf("clarification=%v, info=%v err=%v", out, info, err)
					}
					req.SessionID, _ = info["sessionId"].(string)
					must(json.Unmarshal([]byte(`[{"role":"assistant","content":"What changed?"},{"role":"user","content":"previous answer"}]`), &req.History))
				}
			}
			want := int32(1)
			if name == "clarification" {
				want = 6
			}
			// Terminal submission may already have completed when the CLI
			// requests a final acknowledgement. Only require the intended calls.
			if calls.Load() < want {
				t.Fatalf("model requests=%d want at least %d", calls.Load(), want)
			}
		})
	}
}

func featureResponseText(text string) map[string]any {
	return map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func featureResponseTool(t *testing.T, step int32, tools json.RawMessage, name, arguments string) map[string]any {
	t.Helper()
	if !strings.Contains(string(tools), `"name":"`+name+`"`) {
		t.Errorf("session MCP tool %s is missing", name)
	}
	return map[string]any{"id": fmt.Sprintf("fc_%d", step), "type": "function_call", "call_id": fmt.Sprintf("call_%d", step),
		"name": name, "namespace": "mcp__agentflow_session", "arguments": arguments, "status": "completed"}
}

func writeFeatureResponses(w http.ResponseWriter, step int32, item map[string]any) {
	id := fmt.Sprintf("resp_%d", step)
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(event string, fields map[string]any) {
		fields["type"] = event
		raw, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	}
	emit("response.created", map[string]any{"response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}})
	emit("response.output_item.added", map[string]any{"output_index": 0, "item": item})
	emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	emit("response.completed", map[string]any{"response": map[string]any{
		"id": id, "object": "response", "status": "completed", "model": "fixture-model", "output": []any{item},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13},
	}})
}
