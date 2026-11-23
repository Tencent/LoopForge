package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

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
		// CLI default & short aliases must skip direct HTTP so the CLI resolves them.
		{"codex:default", false, "", ""},
		{"claude-code:default", false, "", ""},
		{"opencode:default", false, "", ""},
		{"claude-code:sonnet", false, "", ""},
		{"claude-code:haiku", false, "", ""},
		{"claude-code:opus", false, "", ""},
		{"default", false, "", ""},
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
	t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
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
	t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		for _, secret := range []string{"sk-custom-api-secret", "raw-db-password", "unregistered-third-party-key"} {
			if strings.Contains(capturedBody, secret) {
				t.Errorf("secret %q leaked into custom review API request: %s", secret, capturedBody)
			}
		}
		if !strings.Contains(capturedBody, "prod-cluster") {
			t.Errorf("side-effect target missing from review request: %s", capturedBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + mustJSON(`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"自定义 API 审查通过"}`) + `}}]}`))
	}))
	defer srv.Close()

	review, err := runAssistantApprovalCustomAPI(context.Background(), assistantApprovalInput{
		ActorID: 7, ProjectID: "p1", Tool: "mcp_deploy",
		Messages: []assistantApprovalMessage{{Role: "user", AuthorID: 7, Content: "保存规则 key=sk-custom-api-secret"}},
		Arguments: redactApprovalArguments(map[string]any{
			"target": "prod-cluster",
			"auth": map[string]any{
				"password": "raw-db-password",
				"api_key":  "unregistered-third-party-key",
			},
		}, user),
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
	t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
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

	// 5. Cross-origin Base URL change (different host/port) must NOT reuse stored key.
	otherHits := 0
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"leaked"}]}`))
	}))
	defer otherSrv.Close()

	res = probe(`{"protocol":"chat","baseUrl":"` + otherSrv.URL + `/v1","apiKey":""}`)
	if !strings.Contains(res.Error, "跨源") || otherHits != 0 {
		t.Fatalf("cross-origin probe should block stored key reuse: res=%+v otherHits=%d", res, otherHits)
	}

	// 6. Switching to official endpoint (empty baseUrl) is also cross-origin vs custom gateway.
	res = probe(`{"protocol":"chat","baseUrl":"","apiKey":""}`)
	if !strings.Contains(res.Error, "跨源") || len(res.Models) != 0 {
		t.Fatalf("switching from custom gateway to official endpoint must block stored key reuse: %+v", res)
	}

	// 7. Cross-origin probe succeeds once the user supplies a new key explicitly.
	res = probe(`{"protocol":"chat","baseUrl":"` + otherSrv.URL + `/v1","apiKey":"sk-new-origin"}`)
	if res.Error != "" || otherHits != 1 || len(res.Models) != 1 || res.Models[0].ID != "leaked" {
		t.Fatalf("cross-origin probe with new key should succeed: res=%+v hits=%d", res, otherHits)
	}
}

func TestApprovalHTTPEgressAndRedirectProtection(t *testing.T) {
	t.Run("blocks_non_public_addresses_by_default", func(t *testing.T) {
		t.Setenv(approvalEgressAllowCIDRsEnv, "")
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		_, err := runAssistantApprovalHTTP(context.Background(), approvalHTTPTarget{
			Protocol: runtime.AgentCLIProtocolChat,
			Endpoint: srv.URL + "/v1/chat/completions",
			APIKey:   "sk-test",
			Model:    "gpt-4.1-mini",
		}, "policy", "{}")
		if err == nil || !strings.Contains(err.Error(), approvalEgressAllowCIDRsEnv) || hits != 0 {
			t.Fatalf("expected egress block on approval HTTP, err=%v hits=%d", err, hits)
		}

		h, u := workbenchFixture(t)
		c, rec := workbenchContext(u, "POST", "/api/assistant/approval-review/probe",
			bytes.NewBufferString(`{"protocol":"chat","baseUrl":"`+srv.URL+`/v1","apiKey":"sk-test"}`))
		h.probeApprovalReviewModels(c)
		var res cliModelProbeResult
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if !strings.Contains(res.Error, approvalEgressAllowCIDRsEnv) || hits != 0 {
			t.Fatalf("expected egress block on model probe, res=%+v hits=%d", res, hits)
		}
	})

	t.Run("refuses_redirects_on_review_and_probe", func(t *testing.T) {
		t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
		targetHits := 0
		targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetHits++
			w.WriteHeader(http.StatusOK)
		}))
		defer targetSrv.Close()

		redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, targetSrv.URL+r.URL.Path, http.StatusFound)
		}))
		defer redirectSrv.Close()

		_, err := runAssistantApprovalHTTP(context.Background(), approvalHTTPTarget{
			Protocol: runtime.AgentCLIProtocolChat,
			Endpoint: redirectSrv.URL + "/v1/chat/completions",
			APIKey:   "sk-secret",
			Model:    "gpt-4.1-mini",
		}, "policy", "{}")
		if err == nil || !strings.Contains(err.Error(), "重定向") || targetHits != 0 {
			t.Fatalf("approval HTTP must refuse redirect: err=%v targetHits=%d", err, targetHits)
		}

		h, u := workbenchFixture(t)
		for _, proto := range []string{runtime.AgentCLIProtocolChat, runtime.AgentCLIProtocolMessages, runtime.AgentCLIProtocolGemini} {
			c, rec := workbenchContext(u, "POST", "/api/assistant/approval-review/probe",
				bytes.NewBufferString(`{"protocol":"`+proto+`","baseUrl":"`+redirectSrv.URL+`","apiKey":"sk-secret"}`))
			h.probeApprovalReviewModels(c)
			var res cliModelProbeResult
			_ = json.Unmarshal(rec.Body.Bytes(), &res)
			if !strings.Contains(res.Error, "重定向") || targetHits != 0 {
				t.Fatalf("probe %s must refuse redirect: res=%+v targetHits=%d", proto, res, targetHits)
			}
		}
	})

	t.Run("policy_rejection_skips_local_cli_fallback_while_ordinary_error_falls_back", func(t *testing.T) {
		origCLI := approvalReviewCLIRunner
		cliCalls := 0
		approvalReviewCLIRunner = func(ctx context.Context, req assistantCLIRequest, user domain.User, onEvent func(assistantStreamEvent)) (string, string, error) {
			cliCalls++
			return `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"cli fallback"}`, "", nil
		}
		t.Cleanup(func() { approvalReviewCLIRunner = origCLI })

		input := assistantApprovalInput{
			ActorID: 1, ProjectID: "p1", Tool: "rules_save",
			Messages: []assistantApprovalMessage{{Role: "user", AuthorID: 1, Content: "保存规则"}},
		}

		// 1. Egress dial block must abort without spawning local CLI.
		t.Setenv(approvalEgressAllowCIDRsEnv, "")
		blockedHits := 0
		blockedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			blockedHits++
			w.WriteHeader(http.StatusOK)
		}))
		defer blockedSrv.Close()

		blockedUser := domain.User{ID: 1, CodexAPIKey: "sk-codex", CodexBaseURL: blockedSrv.URL + "/v1"}
		_, err := runAssistantApprovalReviewWithEffort(context.Background(), input, blockedUser, "codex:gpt-4.1-mini", "low")
		var policyErr errApprovalPolicyBlocked
		if !errors.As(err, &policyErr) || cliCalls != 0 || blockedHits != 0 {
			t.Fatalf("egress dial block must not fall back to local CLI: err=%v cliCalls=%d hits=%d", err, cliCalls, blockedHits)
		}

		// 2. Redirect refusal must also abort without spawning local CLI.
		t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
		targetHits := 0
		targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetHits++
			w.WriteHeader(http.StatusOK)
		}))
		defer targetSrv.Close()
		redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, targetSrv.URL+r.URL.Path, http.StatusFound)
		}))
		defer redirectSrv.Close()

		redirectUser := domain.User{ID: 1, CodexAPIKey: "sk-codex", CodexBaseURL: redirectSrv.URL + "/v1"}
		_, err = runAssistantApprovalReviewWithEffort(context.Background(), input, redirectUser, "codex:gpt-4.1-mini", "low")
		if !errors.As(err, &policyErr) || cliCalls != 0 || targetHits != 0 {
			t.Fatalf("redirect block must not fall back to local CLI: err=%v cliCalls=%d targetHits=%d", err, cliCalls, targetHits)
		}

		// 2b. Truncated/broken 3xx body must still be treated as policy rejection
		//     before reading the body, never as an ordinary I/O error triggering CLI fallback.
		truncatedRedirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Redirect(w, r, targetSrv.URL+r.URL.Path, http.StatusFound)
				return
			}
			conn, rw, err := hj.Hijack()
			if err != nil {
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 302 Found\r\nLocation: " + targetSrv.URL + "/v1\r\nContent-Length: 100\r\n\r\npartial")
			_ = rw.Flush()
			_ = conn.Close()
		}))
		defer truncatedRedirectSrv.Close()

		truncatedUser := domain.User{ID: 1, CodexAPIKey: "sk-codex", CodexBaseURL: truncatedRedirectSrv.URL + "/v1"}
		_, err = runAssistantApprovalReviewWithEffort(context.Background(), input, truncatedUser, "codex:gpt-4.1-mini", "low")
		if !errors.As(err, &policyErr) || cliCalls != 0 || targetHits != 0 {
			t.Fatalf("truncated 3xx response must still reject as policy error without CLI fallback: err=%v cliCalls=%d targetHits=%d", err, cliCalls, targetHits)
		}

		hProbe, uProbe := workbenchFixture(t)
		cProbe, recProbe := workbenchContext(uProbe, "POST", "/api/assistant/approval-review/probe",
			bytes.NewBufferString(`{"protocol":"chat","baseUrl":"`+truncatedRedirectSrv.URL+`/v1","apiKey":"sk-secret"}`))
		hProbe.probeApprovalReviewModels(cProbe)
		var probeRes cliModelProbeResult
		_ = json.Unmarshal(recProbe.Body.Bytes(), &probeRes)
		if !strings.Contains(probeRes.Error, "重定向") || targetHits != 0 {
			t.Fatalf("probe on truncated 3xx response must reject redirect before body read: res=%+v targetHits=%d", probeRes, targetHits)
		}

		// 3. Approval reviewer loop must terminate immediately on egress block
		//    rather than falling back to the session model's local CLI.
		t.Setenv(approvalEgressAllowCIDRsEnv, "")
		h, u := workbenchFixture(t)
		u.CodexAPIKey = "sk-codex"
		if _, err := h.repo.PutAssistantPreferences(u.ID, domain.AssistantPreferences{
			ApprovalReviewMode: domain.ApprovalReviewModeAPI,
			ApprovalReviewAPI: &domain.ApprovalReviewAPIConfig{
				Protocol: runtime.AgentCLIProtocolChat,
				BaseURL:  blockedSrv.URL + "/v1",
				Model:    "gpt-4.1-mini",
				APIKey:   "sk-custom",
			},
		}); err != nil {
			t.Fatal(err)
		}
		reviewer := h.approvalReviewer(assistantRequest{
			Model:     "codex:default",
			ProjectID: "p1",
			Messages:  []assistantMessage{{Role: "user", AuthorID: u.ID, Content: "保存规则"}},
		}, u, nil)
		res := reviewer(withApprovalActionSource(context.Background(), approvalSourceBuiltinPlatform), "rules_save", "保存规则", map[string]any{"body": map[string]any{"id": "r1"}})
		if res.allows() || res.Outcome != "unavailable" || !strings.Contains(res.Rationale, "出站策略") || cliCalls != 0 {
			t.Fatalf("egress block in custom API must abort candidate fallback: res=%+v cliCalls=%d", res, cliCalls)
		}

		// 4. Ordinary transport/server failure (HTTP 500 on allowed IP) still falls back to CLI.
		t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32,::1/128")
		errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream temporary failure", http.StatusInternalServerError)
		}))
		defer errSrv.Close()

		fallbackUser := domain.User{ID: 1, CodexAPIKey: "sk-codex", CodexBaseURL: errSrv.URL + "/v1"}
		got, err := runAssistantApprovalReviewWithEffort(context.Background(), input, fallbackUser, "codex:gpt-4.1-mini", "low")
		if err != nil || !got.allows() || cliCalls != 1 {
			t.Fatalf("ordinary 500 should fall back to CLI: got=%+v err=%v cliCalls=%d", got, err, cliCalls)
		}
	})

	t.Run("multi_address_dial_preserves_policy_block_over_first_connect_error", func(t *testing.T) {
		origCLI := approvalReviewCLIRunner
		cliCalls := 0
		approvalReviewCLIRunner = func(ctx context.Context, req assistantCLIRequest, user domain.User, onEvent func(assistantStreamEvent)) (string, string, error) {
			cliCalls++
			return `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"cli fallback"}`, "", nil
		}
		t.Cleanup(func() { approvalReviewCLIRunner = origCLI })

		// Reserve a closed port on 127.0.0.1 so the first (allowed) IP fails
		// immediately with ECONNREFUSED, forcing net.Dialer to try the second
		// (blocked) IP. Without recording Control rejections per dial, Go's
		// dialSerial/dialParallel discards the second error and returns only
		// ECONNREFUSED, which would trigger local CLI fallback.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closedPort := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()

		cases := []struct {
			name   string
			addrs  []netip.Addr
			wantIP string
		}{
			{
				name:   "serial_ipv4_refused_then_private_blocked",
				addrs:  []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("10.255.255.1")},
				wantIP: "10.255.255.1",
			},
			{
				name:   "dual_stack_ipv4_refused_and_ula_ipv6_blocked",
				addrs:  []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("fd00::1")},
				wantIP: "fd00::1",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if strings.Contains(tc.wantIP, ":") {
					if ln6, err := net.Listen("tcp6", "[::1]:0"); err != nil {
						t.Skipf("IPv6 sockets unavailable in test environment: %v", err)
					} else {
						_ = ln6.Close()
					}
				}
				cliCalls = 0
				t.Setenv(approvalEgressAllowCIDRsEnv, "127.0.0.1/32")
				resolver := mockMultiAddrDNSResolver(t, tc.addrs)
				origFactory := approvalHTTPClientFactory
				approvalHTTPClientFactory = func(timeout time.Duration) *http.Client {
					return newApprovalEgressClientWithResolver(timeout, approvalAllowedEgressPrefixes(), resolver)
				}
				t.Cleanup(func() { approvalHTTPClientFactory = origFactory })

				user := domain.User{
					ID:           1,
					CodexAPIKey:  "sk-codex",
					CodexBaseURL: fmt.Sprintf("http://multi-egress.invalid:%d/v1", closedPort),
				}
				input := assistantApprovalInput{
					ActorID: 1, ProjectID: "p1", Tool: "rules_save",
					Messages: []assistantApprovalMessage{{Role: "user", AuthorID: 1, Content: "保存规则"}},
				}
				_, err := runAssistantApprovalReviewWithEffort(context.Background(), input, user, "codex:gpt-4.1-mini", "low")
				var policyErr errApprovalPolicyBlocked
				if !errors.As(err, &policyErr) || !strings.Contains(policyErr.Error(), tc.wantIP) || cliCalls != 0 {
					t.Fatalf("multi-address dial must surface policy block for %s without CLI fallback: err=%v cliCalls=%d", tc.wantIP, err, cliCalls)
				}
			})
		}
	})

	t.Run("requires_https_for_public_ips_even_when_private_cidr_allowlisted", func(t *testing.T) {
		allowPrivate := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
		var policyErr errApprovalPolicyBlocked
		// Plaintext HTTP to public IP must be rejected even if an internal CIDR is allowlisted.
		err := checkApprovalEgressAddress("93.184.216.34:80", allowPrivate, true)
		if !errors.As(err, &policyErr) || !strings.Contains(policyErr.Error(), "https://") {
			t.Fatalf("expected plaintext HTTP block on public IP, got %v", err)
		}
		// HTTPS to public IP is allowed.
		if err := checkApprovalEgressAddress("93.184.216.34:443", allowPrivate, false); err != nil {
			t.Fatalf("expected HTTPS to public IP allowed, got %v", err)
		}
		// Plaintext HTTP to explicitly allowlisted internal gateway IP is allowed.
		if err := checkApprovalEgressAddress("10.1.2.3:8080", allowPrivate, true); err != nil {
			t.Fatalf("expected allowlisted internal HTTP allowed, got %v", err)
		}
		// Preferences validation rejects http:// when allowlist env is unset, allows it when set.
		t.Setenv(approvalEgressAllowCIDRsEnv, "")
		if err := validateApprovalReviewBaseURL("http://gateway.example.com/v1"); err == nil || !strings.Contains(err.Error(), "https://") {
			t.Fatalf("expected http:// Base URL rejected without allowlist, got %v", err)
		}
		t.Setenv(approvalEgressAllowCIDRsEnv, "10.0.0.0/8")
		if err := validateApprovalReviewBaseURL("http://internal-gw.local/v1"); err != nil {
			t.Fatalf("expected http:// Base URL allowed with allowlist, got %v", err)
		}
	})
}

func mockMultiAddrDNSResolver(t *testing.T, addrs []netip.Addr) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 17 {
				continue
			}
			qEnd := 12
			for qEnd < n && buf[qEnd] != 0 {
				qEnd += int(buf[qEnd]) + 1
			}
			if qEnd+5 > n {
				continue
			}
			qType := binary.BigEndian.Uint16(buf[qEnd+1 : qEnd+3])
			question := buf[12 : qEnd+5]

			var answers []byte
			var count uint16
			for _, ip := range addrs {
				switch {
				case qType == 1 && ip.Is4():
					a4 := ip.As4()
					answers = append(answers, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x05, 0x00, 0x04)
					answers = append(answers, a4[:]...)
					count++
				case qType == 28 && ip.Is6():
					a16 := ip.As16()
					answers = append(answers, 0xc0, 0x0c, 0x00, 0x1c, 0x00, 0x01, 0x00, 0x00, 0x00, 0x05, 0x00, 0x10)
					answers = append(answers, a16[:]...)
					count++
				}
			}
			resp := make([]byte, 12, 12+len(question)+len(answers))
			copy(resp[0:2], buf[0:2])
			binary.BigEndian.PutUint16(resp[2:4], 0x8180)
			binary.BigEndian.PutUint16(resp[4:6], 1)
			binary.BigEndian.PutUint16(resp[6:8], count)
			resp = append(resp, question...)
			resp = append(resp, answers...)
			_, _ = pc.WriteTo(resp, peer)
		}
	}()
	dnsAddr := pc.LocalAddr().String()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "udp", dnsAddr)
		},
	}
}

func mustJSON(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
