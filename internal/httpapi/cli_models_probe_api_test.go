package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// probeDiscoverCall is one stubbed assistantEngineDiscover call.
type probeDiscoverCall struct {
	anthropic       bool
	apiKey, baseURL string
}

// TestProbeCLIModelsAPI drives 「获取可用模型」 through the route, so the
// stored credential the handler loads is what cliProbeCredentials merges the
// form over, and every early answer is checked before a remote call.
func TestProbeCLIModelsAPI(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "probe-alice", ClaudeCodeAPIKey: "stored-claude-test-secret", ClaudeCodeBaseURL: "https://stored.example/anthropic"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{LoginName: "probe-bob"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := st.UserByLoginName("probe-alice")
	bob, _ := st.UserByLoginName("probe-bob")
	authClient := auth.NewClient(auth.Config{SessionSecret: "probe-test-secret"})
	s := &Server{deps: &deps{repo: st, authClient: authClient}}
	s.users = &usersHandlers{deps: s.deps}
	g := gin.New()
	g.POST("/api/users/:id/cli-models/probe", s.users.probeCLIModels)

	previous := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = previous })
	var (
		calls        []probeDiscoverCall
		discovered   []assistantDiscoveredModel
		discoverErr  error
		remoteModels = []assistantDiscoveredModel{{ID: "remote-model", Name: "Remote"}}
	)
	assistantEngineDiscover = func(_ context.Context, anthropic bool, apiKey, baseURL string) ([]assistantDiscoveredModel, error) {
		calls = append(calls, probeDiscoverCall{anthropic: anthropic, apiKey: apiKey, baseURL: baseURL})
		return discovered, discoverErr
	}

	cases := []struct {
		name       string
		path       string // overrides the probed user's route
		id         int64  // probed user; alice when zero
		login      string
		body       string
		discovered []assistantDiscoveredModel
		err        error
		code       int
		wantError  string
		wantSource string
		wantCall   *probeDiscoverCall
	}{
		{name: "invalid user id", path: "/api/users/0/cli-models/probe", login: "probe-alice", body: `{"engine":"claude-code"}`, code: 400, wantError: "valid user id is required"},
		{name: "guest", login: "", body: `{"engine":"claude-code"}`, code: 403, wantError: "只能使用当前登录用户的凭据获取模型"},
		{name: "another user's credential", login: "probe-bob", body: `{"engine":"claude-code"}`, code: 403, wantError: "只能使用当前登录用户的凭据获取模型"},
		{name: "malformed body", login: "probe-alice", body: `{"engine":`, code: 400},
		{name: "codebuddy is not probed here", login: "probe-alice", body: `{"engine":"codebuddy"}`, code: 400, wantError: "不支持的 CLI：codebuddy"},
		{name: "unknown cli", login: "probe-alice", body: `{"engine":" nope "}`, code: 400, wantError: "不支持的 CLI：nope"},
		{name: "invalid form base url", login: "probe-alice", body: `{"engine":"claude-code","baseUrl":"file:///tmp/x"}`, code: 200, wantError: errInvalidCLIBaseURL},
		{
			name: "blank form probes with the stored credential", login: "probe-alice", body: `{"engine":"claude-code","apiKey":"  "}`,
			discovered: remoteModels, code: 200, wantSource: "remote",
			wantCall: &probeDiscoverCall{anthropic: true, apiKey: "stored-claude-test-secret", baseURL: "https://stored.example/anthropic"},
		},
		{
			name: "unsaved form values win over the stored ones", login: "probe-alice",
			body:       `{"engine":"claude-code","apiKey":" form-claude-test-secret ","baseUrl":" https://form.example/anthropic "}`,
			discovered: remoteModels, code: 200, wantSource: "remote",
			wantCall: &probeDiscoverCall{anthropic: true, apiKey: "form-claude-test-secret", baseURL: "https://form.example/anthropic"},
		},
		{name: "empty base url lists the builtin catalog", login: "probe-alice", body: `{"engine":"claude-code","baseUrl":""}`, code: 200, wantSource: "builtin"},
		{name: "no stored or form key", id: bob.ID, login: "probe-bob", body: `{"engine":"claude-code","baseUrl":"https://form.example/anthropic"}`, code: 200, wantError: "请先填写 " + assistantEngineLabel(assistantEngineClaude) + " API Key"},
		{
			name: "gemini protocol ignores the base url", id: bob.ID, login: "probe-bob",
			body: fmt.Sprintf(`{"engine":%q,"apiKey":"k","baseUrl":"https://gateway.example/v1","protocol":%q}`, runtime.AgentCLIAntigravity, runtime.AgentCLIProtocolGemini),
			code: 200, wantSource: "builtin",
		},
		{name: "cursor needs a key", id: bob.ID, login: "probe-bob", body: fmt.Sprintf(`{"engine":%q}`, runtime.AgentCLICursor), code: 200, wantError: "请先填写 " + assistantEngineLabel(runtime.AgentCLICursor) + " API Key"},
		{
			name: "discovery failure", login: "probe-alice", body: `{"engine":"claude-code"}`,
			err: errors.New("401 unauthorized"), code: 200, wantError: "获取模型列表失败：401 unauthorized",
			wantCall: &probeDiscoverCall{anthropic: true, apiKey: "stored-claude-test-secret", baseURL: "https://stored.example/anthropic"},
		},
		{
			name: "service lists no models", login: "probe-alice", body: `{"engine":"claude-code"}`,
			discovered: []assistantDiscoveredModel{}, code: 200, wantError: "服务没有返回可用模型，可以手动添加模型",
			wantCall: &probeDiscoverCall{anthropic: true, apiKey: "stored-claude-test-secret", baseURL: "https://stored.example/anthropic"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, discovered, discoverErr = nil, tc.discovered, tc.err
			path := tc.path
			if path == "" {
				id := tc.id
				if id == 0 {
					id = alice.ID
				}
				path = fmt.Sprintf("/api/users/%d/cli-models/probe", id)
			}
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.login != "" {
				identity, ok := st.UserByLoginName(tc.login)
				if !ok {
					t.Fatalf("missing test identity %s", tc.login)
				}
				token, err := authClient.SignUser(&auth.User{ID: identity.ID, LoginName: identity.LoginName, AuthTime: time.Now().UTC()})
				if err != nil {
					t.Fatal(err)
				}
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
			}
			rec := httptest.NewRecorder()
			g.ServeHTTP(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "test-secret") {
				t.Fatalf("probe leaked a key: %s", rec.Body.String())
			}
			if tc.wantCall == nil {
				if len(calls) != 0 {
					t.Fatalf("unexpected discovery: %+v", calls)
				}
			} else if len(calls) != 1 || calls[0] != *tc.wantCall {
				t.Fatalf("discovery calls %+v, want %+v", calls, *tc.wantCall)
			}
			if tc.code != http.StatusOK && tc.wantError == "" {
				return
			}
			var got cliModelProbeResult
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			if got.Error != tc.wantError || got.Source != tc.wantSource {
				t.Fatalf("error %q source %q, want %q %q", got.Error, got.Source, tc.wantError, tc.wantSource)
			}
			if tc.wantSource != "" && len(got.Models) == 0 {
				t.Fatalf("%s catalog is empty", tc.wantSource)
			}
			if tc.wantSource == "remote" && got.Models[0].ID != "remote-model" {
				t.Fatalf("remote models %+v", got.Models)
			}
		})
	}
}
