package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestResolveApprovalHTTPTargetCoversAllFourProtocols(t *testing.T) {
	user := domain.User{
		ClaudeCodeAPIKey:  "sk-ant",
		ClaudeCodeBaseURL: "https://proxy.example/anthropic",
		CodexAPIKey:       "sk-oai",
		CodexBaseURL:      "https://proxy.example/openai/v1",
		AgentCLICredentials: map[string]domain.AgentCLICredential{
			runtime.AgentCLIOpenCode:    {APIKey: "sk-oc", BaseURL: "https://proxy.example/oc/v1", Protocol: domain.AgentCLIProtocolChat},
			runtime.AgentCLIAntigravity: {APIKey: "sk-gem", BaseURL: "https://gem.example", Protocol: domain.AgentCLIProtocolGemini},
			runtime.AgentCLIDeepSeek:    {APIKey: "sk-ds"},
			runtime.AgentCLICursor:      {APIKey: "crsr_x"},
		},
		CodeBuddyAPIKey: "ck_x",
	}

	cases := []struct {
		model    string
		wantOK   bool
		protocol string
		endpoint string
	}{
		{"claude-code:hy4", true, runtime.AgentCLIProtocolMessages, "https://proxy.example/anthropic/v1/messages"},
		{"codex:hy4", true, runtime.AgentCLIProtocolResponses, "https://proxy.example/openai/v1/responses"},
		{"opencode:hy4", true, runtime.AgentCLIProtocolChat, "https://proxy.example/oc/v1/chat/completions"},
		{"agy:gemini-2.5-flash", true, runtime.AgentCLIProtocolGemini, "https://gem.example/v1beta/models/gemini-2.5-flash:generateContent"},
		{"dsh:deepseek-flash", true, runtime.AgentCLIProtocolChat, "https://api.deepseek.com/v1/chat/completions"},
		{"cursor:auto", false, "", ""},
		{"deepseek-v4.1-flash", false, "", ""},
	}
	for _, tc := range cases {
		got, ok := resolveApprovalHTTPTarget(user, tc.model)
		if ok != tc.wantOK || got.Protocol != tc.protocol || got.Endpoint != tc.endpoint {
			t.Fatalf("%s: got (%+v, %v), want (%s, %s, %v)", tc.model, got, ok, tc.protocol, tc.endpoint, tc.wantOK)
		}
	}

	// Official Gemini endpoint when BaseURL is empty on agy.
	officialGem := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		runtime.AgentCLIAntigravity: {APIKey: "gem-key", Protocol: domain.AgentCLIProtocolGemini},
	}}
	got, ok := resolveApprovalHTTPTarget(officialGem, "agy:gemini-2.5-flash")
	if !ok || got.Endpoint != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Fatalf("official gemini target = %+v ok=%v", got, ok)
	}
}

func TestRunAssistantApprovalHTTPProtocolsAndDowngrades(t *testing.T) {
	allowJSON := `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"已核对"}`

	t.Run("anthropic_messages_with_bearer_fallback_and_thinking_downgrade", func(t *testing.T) {
		attempts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if r.URL.Path != "/v1/messages" || r.Header.Get("anthropic-version") != "2023-06-01" {
				t.Fatalf("bad path/header: %s %v", r.URL.Path, r.Header)
			}
			// Reject x-api-key, accept Bearer.
			if r.Header.Get("Authorization") != "Bearer sk-test" {
				http.Error(w, "need bearer", http.StatusUnauthorized)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, hasThinking := body["thinking"]; hasThinking {
				http.Error(w, "thinking unsupported", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"..."},{"type":"text","text":` + mustJSON(allowJSON) + `}]}`))
		}))
		defer srv.Close()

		target := approvalHTTPTarget{Protocol: runtime.AgentCLIProtocolMessages, Endpoint: srv.URL + "/v1/messages", APIKey: "sk-test", Model: "hy4"}
		res, err := runAssistantApprovalHTTP(context.Background(), target, "policy", "{}")
		if err != nil || !res.allows() || attempts != 4 {
			t.Fatalf("res=%+v err=%v attempts=%d", res, err, attempts)
		}
	})

	t.Run("openai_responses_and_chat", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer sk-oai" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/responses":
				_, _ = w.Write([]byte(`{"output":[{"type":"reasoning","content":[{"type":"text","text":"..."}]},{"type":"message","content":[{"type":"output_text","text":` + mustJSON(allowJSON) + `}]}]}`))
			case "/v1/chat/completions":
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + mustJSON(allowJSON) + `}}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		for _, tc := range []struct{ proto, path string }{
			{runtime.AgentCLIProtocolResponses, "/v1/responses"},
			{runtime.AgentCLIProtocolChat, "/v1/chat/completions"},
		} {
			target := approvalHTTPTarget{Protocol: tc.proto, Endpoint: srv.URL + tc.path, APIKey: "sk-oai", Model: "hy4"}
			res, err := runAssistantApprovalHTTP(context.Background(), target, "policy", "{}")
			if err != nil || !res.allows() {
				t.Fatalf("%s: res=%+v err=%v", tc.proto, res, err)
			}
		}
	})

	t.Run("gemini_generate_content_skips_thought_parts_and_downgrades_thinking", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if !strings.HasSuffix(r.URL.Path, "/v1beta/models/gemini-2.5-flash:generateContent") {
				t.Fatalf("unexpected gemini path: %s", r.URL.Path)
			}
			if r.Header.Get("x-goog-api-key") != "gem-key" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gc, _ := body["generationConfig"].(map[string]any)
			if _, hasThinking := gc["thinkingConfig"]; hasThinking {
				http.Error(w, "thinkingBudget unsupported", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"internal thought","thought":true},{"text":` + mustJSON(allowJSON) + `}]}}]}`))
		}))
		defer srv.Close()

		target := approvalHTTPTarget{
			Protocol: runtime.AgentCLIProtocolGemini,
			Endpoint: srv.URL + "/v1beta/models/gemini-2.5-flash:generateContent",
			APIKey:   "gem-key",
			Model:    "gemini-2.5-flash",
		}
		res, err := runAssistantApprovalHTTP(context.Background(), target, "policy", "{}")
		if err != nil || !res.allows() || calls != 2 {
			t.Fatalf("gemini: res=%+v err=%v calls=%d", res, err, calls)
		}
	})

	t.Run("fatal_404_on_openai_vs_non_fatal_404_on_gemini", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "model not found", http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := runAssistantApprovalHTTP(context.Background(), approvalHTTPTarget{
			Protocol: runtime.AgentCLIProtocolChat, Endpoint: srv.URL + "/chat/completions", APIKey: "k", Model: "missing",
		}, "policy", "{}")
		var fatal errApprovalHTTPFatal
		if !errors.As(err, &fatal) {
			t.Fatalf("chat 404 should be fatal, got %T %v", err, err)
		}

		_, err = runAssistantApprovalHTTP(context.Background(), approvalHTTPTarget{
			Protocol: runtime.AgentCLIProtocolGemini, Endpoint: srv.URL + "/v1beta/models/alias:generateContent", APIKey: "k", Model: "alias",
		}, "policy", "{}")
		if errors.As(err, &fatal) {
			t.Fatalf("gemini 404 should allow CLI fallback, got fatal %v", err)
		}
	})
}

func TestResolveApprovalCustomAPITargetAndFallback(t *testing.T) {
	cases := []struct {
		cfg          domain.ApprovalReviewAPIConfig
		wantOK       bool
		wantEndpoint string
	}{
		{
			cfg:          domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolChat, BaseURL: "https://gateway.example.com/v1", Model: "gpt-4.1-mini", APIKey: "sk-1"},
			wantOK:       true,
			wantEndpoint: "https://gateway.example.com/v1/chat/completions",
		},
		{
			cfg:          domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolChat, BaseURL: "https://gateway.example.com/v1/chat/completions", Model: "gpt-4.1-mini", APIKey: "sk-1"},
			wantOK:       true,
			wantEndpoint: "https://gateway.example.com/v1/chat/completions",
		},
		{
			cfg:          domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolResponses, Model: "gpt-5.4-mini", APIKey: "sk-1"},
			wantOK:       true,
			wantEndpoint: "https://api.openai.com/v1/responses",
		},
		{
			cfg:          domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolMessages, Model: "claude-haiku-4-5", APIKey: "sk-1"},
			wantOK:       true,
			wantEndpoint: "https://api.anthropic.com/v1/messages",
		},
		{
			cfg:          domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolGemini, Model: "gemini-2.5-flash", APIKey: "sk-1"},
			wantOK:       true,
			wantEndpoint: "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent",
		},
		{
			cfg:    domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolChat, Model: "hy4", APIKey: ""},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		got, ok := resolveApprovalCustomAPITarget(tc.cfg)
		if ok != tc.wantOK {
			t.Fatalf("%+v: ok=%v want=%v", tc.cfg, ok, tc.wantOK)
		}
		if ok && got.Endpoint != tc.wantEndpoint {
			t.Fatalf("%+v: endpoint=%q want=%q", tc.cfg, got.Endpoint, tc.wantEndpoint)
		}
	}

	// Candidate selection: follow mode ignores stale saved custom API/CLI;
	// api mode puts custom API first and keeps session CLI+model as fallback.
	user := domain.User{ID: 7, ClaudeCodeAPIKey: "sk-cc"}
	req := assistantRequest{Model: "claude-code:claude-sonnet-4-6"}
	prefs := domain.AssistantPreferences{
		ApprovalReviewMode:  domain.ApprovalReviewModeFollow,
		ApprovalReviewModel: "claude-code:claude-haiku-4-5",
		ApprovalReviewAPI:   &domain.ApprovalReviewAPIConfig{Protocol: runtime.AgentCLIProtocolChat, BaseURL: "https://gw.example/v1", Model: "hy4", APIKey: "sk-api"},
	}
	got := resolveApprovalReviewCandidatesForPrefs(req, user, prefs, true)
	if len(got) != 1 || got[0].Model != "claude-code:claude-sonnet-4-6" || got[0].CustomAPI != nil {
		t.Fatalf("follow mode should ignore stale custom configs, got %+v", got)
	}

	prefs.ApprovalReviewMode = domain.ApprovalReviewModeAPI
	got = resolveApprovalReviewCandidatesForPrefs(req, user, prefs, true)
	if len(got) != 2 || got[0].Model != "api:hy4" || got[0].CustomAPI == nil || !got[0].Custom || got[1].Model != "claude-code:claude-sonnet-4-6" {
		t.Fatalf("api mode should put custom API first + session fallback, got %+v", got)
	}

	// Live mock HTTP execution of custom API mode with secret redaction.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sk-custom-api-secret") {
			t.Errorf("custom api key leaked into review prompt: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + mustJSON(`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"自定义 API 审查通过"}`) + `}}]}`))
	}))
	defer srv.Close()

	review, err := runAssistantApprovalCustomAPI(context.Background(), assistantApprovalInput{
		ActorID: 7, ProjectID: "p1", Tool: "rules_save",
		Messages: []assistantApprovalMessage{{Role: "user", AuthorID: 7, Content: "保存规则 key=sk-custom-api-secret"}},
	}, user, domain.ApprovalReviewAPIConfig{
		Protocol: runtime.AgentCLIProtocolChat,
		BaseURL:  srv.URL,
		Model:    "hy4",
		APIKey:   "sk-custom-api-secret",
	})
	if err != nil || !review.allows() {
		t.Fatalf("custom api review failed: res=%+v err=%v", review, err)
	}
}

func TestProbeApprovalReviewModelsAllProtocolsAndStoredKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer sk-stored-key" && r.Header.Get("x-api-key") != "sk-anth" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini","name":"GPT-4.1 Mini"},{"id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5"}]}`))
		case "/v1beta/models":
			if r.Header.Get("x-goog-api-key") != "sk-gem" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["generateContent"]},{"name":"models/text-embedding-004","displayName":"Embedding","supportedGenerationMethods":["embedContent"]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	h, u := workbenchFixture(t)
	if _, err := h.repo.PutAssistantPreferences(u.ID, domain.AssistantPreferences{
		ApprovalReviewMode: domain.ApprovalReviewModeAPI,
		ApprovalReviewAPI: &domain.ApprovalReviewAPIConfig{
			Protocol: runtime.AgentCLIProtocolChat,
			BaseURL:  srv.URL + "/v1",
			Model:    "gpt-4.1-mini",
			APIKey:   "sk-stored-key",
		},
	}); err != nil {
		t.Fatal(err)
	}

	probe := func(body string) cliModelProbeResult {
		t.Helper()
		c, rec := workbenchContext(u, "POST", "/api/assistant/approval-review/probe", bytes.NewBufferString(body))
		h.probeApprovalReviewModels(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var res cliModelProbeResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		return res
	}

	// 1. OpenAI chat with blank apiKey reuses stored key.
	res := probe(`{"protocol":"chat","baseUrl":"` + srv.URL + `/v1","apiKey":""}`)
	if res.Error != "" || len(res.Models) != 2 || res.Models[0].ID != "gpt-4.1-mini" {
		t.Fatalf("chat probe=%+v", res)
	}

	// 2. Anthropic messages with /v1 suffix avoids /v1/v1/models duplication.
	res = probe(`{"protocol":"messages","baseUrl":"` + srv.URL + `/v1","apiKey":"sk-anth"}`)
	if res.Error != "" || len(res.Models) != 2 || res.Models[1].Name != "Claude Haiku 4.5" {
		t.Fatalf("messages probe=%+v", res)
	}

	// 3. Google Gemini filters out non-generateContent models and strips models/ prefix.
	res = probe(`{"protocol":"gemini","baseUrl":"` + srv.URL + `","apiKey":"sk-gem"}`)
	if res.Error != "" || len(res.Models) != 1 || res.Models[0].ID != "gemini-2.5-flash" || res.Models[0].Name != "Gemini 2.5 Flash" {
		t.Fatalf("gemini probe=%+v", res)
	}

	// 4. Clearing stored key without entering a new one returns friendly error.
	res = probe(`{"protocol":"chat","baseUrl":"` + srv.URL + `/v1","apiKey":"","apiKeyClear":true}`)
	if res.Error == "" || len(res.Models) != 0 {
		t.Fatalf("expected missing key error, got %+v", res)
	}
}

func mustJSON(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
