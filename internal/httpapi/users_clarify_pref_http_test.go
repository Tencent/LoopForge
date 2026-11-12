package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// Task 5: 个人默认框架的 API 与选择顺序。这里钉的是「设了什么就用什么，设不了就说清为什么
// 设不了」，以及默认值失效时绝不假装成功。

func newClarifyPrefRouter(t *testing.T) (*repo.Store, *gin.Engine, *auth.Client, domain.User) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "pref-alice"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := st.UserByLoginName("pref-alice")
	authClient := auth.NewClient(auth.Config{SessionSecret: "pref-test-secret"})
	s := &Server{deps: &deps{repo: st, authClient: authClient}}
	s.users = &usersHandlers{deps: s.deps}
	g := gin.New()
	g.PUT("/api/users/:id/credentials", s.users.user)
	return st, g, authClient, alice
}

func putClarifyPref(t *testing.T, st *repo.Store, g *gin.Engine, client *auth.Client, user domain.User, login, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d/credentials", user.ID), strings.NewReader(body))
	if login != "" {
		identity, ok := st.UserByLoginName(login)
		if !ok {
			t.Fatalf("missing test identity %s", login)
		}
		token, err := client.SignUser(&auth.User{ID: identity.ID, LoginName: identity.LoginName, AuthTime: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func TestClarifyModelPreferenceAPI(t *testing.T) {
	st, g, client, alice := newClarifyPrefRouter(t)

	// 框架没配 Key 就设不了默认：报错要说明缺什么，不能收下了却永远用不上。
	rec := putClarifyPref(t, st, g, client, alice, "pref-alice", `{"clarifyModel":"codex:gpt-5.5"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400 without a Codex key", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "个人配置中配置 Codex API Key") {
		t.Fatalf("body=%s, want the missing-key message", rec.Body.String())
	}

	// 平台不认识的框架前缀同样拒绝，并带上原值。
	rec = putClarifyPref(t, st, g, client, alice, "pref-alice", `{"clarifyModel":"foobar:x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "foobar") {
		t.Fatalf("status=%d body=%s, want 400 naming foobar", rec.Code, rec.Body.String())
	}

	// 配好 Key 之后可以设，响应里带着偏好回来，密钥本身不带。
	rec = putClarifyPref(t, st, g, client, alice, "pref-alice", `{"codexApiKey":"codex-pref-secret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = putClarifyPref(t, st, g, client, alice, "pref-alice", `{"clarifyModel":"codex:gpt-5.5"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var public map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public["clarifyModel"] != "codex:gpt-5.5" {
		t.Fatalf("response lost the preference: %v", public)
	}
	if key, ok := public["codexApiKey"].(string); ok && key != "" {
		t.Fatalf("response leaked the API key: %v", public)
	}
	if public["codexApiKeyConfigured"] != true {
		t.Fatalf("response lost the configured state: %v", public)
	}
	stored, _ := st.User(alice.ID)
	if stored.ClarifyModel != "codex:gpt-5.5" || stored.CodexAPIKey != "codex-pref-secret" {
		t.Fatalf("stored user = %+v", stored)
	}

	// 清空默认回到「没得选」，也就是 CodeBuddy。
	rec = putClarifyPref(t, st, g, client, alice, "pref-alice", `{"clarifyModel":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stored, _ = st.User(alice.ID); stored.ClarifyModel != "" {
		t.Fatalf("clearing the preference did not persist: %q", stored.ClarifyModel)
	}
}

// fakeClarifyEngineBin 造一个可执行文件，让「这台机器装了该引擎」成为可控前提。
func fakeClarifyEngineBin(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClarifyChoiceUsesPersonalDefaultBeforeFallingBack(t *testing.T) {
	t.Setenv("AGENTFLOW_CODEX_BIN", fakeClarifyEngineBin(t))
	user := domain.User{CodexAPIKey: "sk-x", ClarifyModel: "codex:gpt-5.5"}
	h := &streamHandlers{}

	engine, err := h.clarifyChoice(chatClarifyRequest{}, domain.Issue{}, user)
	if err != nil {
		t.Fatalf("personal default: %v", err)
	}
	if engine.ID != "codex" || engine.ModelID != "codex:gpt-5.5" {
		t.Fatalf("engine = %+v, want the personal default codex:gpt-5.5", engine)
	}

	// 本次选择的优先级高于个人默认。
	chosen, err := h.clarifyChoice(chatClarifyRequest{Model: "default"}, domain.Issue{}, user)
	if err != nil {
		t.Fatalf("explicit choice: %v", err)
	}
	if chosen.ID != assistantEngineCodeBuddy {
		t.Fatalf("explicit choice = %+v, want CodeBuddy", chosen)
	}

	// 这张单记住的框架优先于个人默认：多轮澄清不能中途换引擎。
	sticky, err := h.clarifyChoice(chatClarifyRequest{}, domain.Issue{
		Clarification: domain.ClarificationAssessment{ModelID: "claude-code:claude-opus-5"},
	}, domain.User{CodexAPIKey: "sk-x", ClaudeCodeAPIKey: "sk-c", ClarifyModel: "codex:gpt-5.5"})
	if err != nil {
		t.Fatalf("sticky choice: %v", err)
	}
	if sticky.ID != assistantEngineClaude {
		t.Fatalf("sticky = %+v, want the engine this item started with", sticky)
	}

	// 个人默认失效（Key 被清空）时回退 CodeBuddy，而不是报错卡死。
	stale, err := h.clarifyChoice(chatClarifyRequest{}, domain.Issue{}, domain.User{ClarifyModel: "codex:gpt-5.5"})
	if err != nil {
		t.Fatalf("stale default must fall back, got %v", err)
	}
	if stale.ID != assistantEngineCodeBuddy || stale.ModelID != clarifyDefaultModel {
		t.Fatalf("stale default = %+v, want codebuddy/default", stale)
	}
}
