package httpapi

import (
	"encoding/json"
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

// Task 6: 引擎可用性端点。这里钉的是「选择器只能亮真的能跑的那几项」：可用性按需求责任人
// 判定，不可用的整组给得出原因，而且绝不把任何密钥或 Base URL 带出接口。

func clarifyEnginesFixture(t *testing.T) (*repo.Store, *gin.Engine, *auth.Client) {
	t.Helper()
	root := t.TempDir()
	emptyBin := filepath.Join(root, "emptybin")
	if err := os.MkdirAll(emptyBin, 0o700); err != nil {
		t.Fatal(err)
	}
	// 没有 CLI 装在这台机器上：可控地验证「未安装」这条原因，也让查找梯子落空。
	t.Setenv("HOME", root)
	t.Setenv("PATH", emptyBin)
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	for _, name := range []string{"AGENTFLOW_CODEBUDDY_BIN", "AGENTFLOW_CODEX_BIN", "AGENTFLOW_CLAUDE_BIN", "AGENTFLOW_OPENCODE_BIN", "AGENTFLOW_CURSOR_BIN", "AGENTFLOW_AGY_BIN", "AGENTFLOW_KIMI_BIN", "AGENTFLOW_PI_BIN", "AGENTFLOW_DSH_BIN", "AGENTFLOW_ZCODE_BIN"} {
		t.Setenv(name, "")
	}

	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	client := auth.NewClient(auth.Config{SessionSecret: "clarify-engines-secret"})
	s := &Server{deps: &deps{repo: st, authClient: client}}
	s.stream = &streamHandlers{deps: s.deps}
	g := gin.New()
	g.GET("/api/clarify-engines", s.stream.clarifyEngines)
	return st, g, client
}

func getClarifyEngines(t *testing.T, st *repo.Store, g *gin.Engine, client *auth.Client, login, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/clarify-engines"+query, nil)
	identity, ok := st.UserByLoginName(login)
	if !ok {
		t.Fatalf("missing test identity %s", login)
	}
	token, err := client.SignUser(&auth.User{ID: identity.ID, LoginName: identity.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func decodeClarifyEngineCatalog(t *testing.T, rec *httptest.ResponseRecorder) clarifyEngineCatalog {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var catalog clarifyEngineCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func clarifyEngineOptionOf(t *testing.T, catalog clarifyEngineCatalog, id string) clarifyEngineOption {
	t.Helper()
	for _, option := range catalog.Engines {
		if option.ID == id {
			return option
		}
	}
	t.Fatalf("engine %q missing from %+v", id, catalog.Engines)
	return clarifyEngineOption{}
}

// clarifyEngineProjectFor 建一个由该用户拥有的项目，这样 ?projectId= 解析出的责任人就是
// 测试里配了凭据的那个人。
func clarifyEngineProjectFor(t *testing.T, st *repo.Store, login string) string {
	t.Helper()
	owner, ok := st.UserByLoginName(login)
	if !ok {
		t.Fatalf("missing test identity %s", login)
	}
	id := "eng-proj-" + login
	if err := st.PutProject(domain.Project{ID: id, Name: id, OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClarifyEnginesUsesResponsibleUser(t *testing.T) {
	st, g, client := clarifyEnginesFixture(t)
	if err := st.PutUser(domain.User{LoginName: "viewer", ChineseName: "查看的人", CodexAPIKey: "viewer-codex-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{LoginName: "holder", ChineseName: "责任人丁"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("holder")
	if err := st.PutIssue(domain.Issue{ID: "eng-owner", ProjectID: "platform", AssigneeUserID: owner.ID, Title: "按责任人判定"}); err != nil {
		t.Fatal(err)
	}

	catalog := decodeClarifyEngineCatalog(t, getClarifyEngines(t, st, g, client, "viewer", "?issueId=eng-owner"))
	if catalog.ResponsibleUserID != owner.ID {
		t.Fatalf("responsible user = %d, want the assignee %d", catalog.ResponsibleUserID, owner.ID)
	}
	if catalog.ResponsibleUserName != "责任人丁" {
		t.Fatalf("responsible name = %q", catalog.ResponsibleUserName)
	}
	codex := clarifyEngineOptionOf(t, catalog, "codex")
	if codex.Configured || codex.Available {
		t.Fatalf("责任人没配 Codex Key，却报成可用：%+v", codex)
	}
	if !strings.Contains(codex.Reason, "Codex API Key") {
		t.Fatalf("reason = %q, want the missing-key message", codex.Reason)
	}
	if codex.Credential != "codexApiKey" {
		t.Fatalf("credential field = %q, want the 个人配置 field name", codex.Credential)
	}
	body := strings.ToLower(getClarifyEngines(t, st, g, client, "viewer", "?issueId=eng-owner").Body.String())
	if strings.Contains(body, "viewer-codex-secret") {
		t.Fatal("可用性接口泄露了查看者自己的 Codex Key")
	}
}

func TestClarifyEnginesReasonsForMissingPrerequisites(t *testing.T) {
	st, g, client := clarifyEnginesFixture(t)
	if err := st.PutUser(domain.User{LoginName: "cli-user", AgentCLICredentials: map[string]domain.AgentCLICredential{
		"opencode": {APIKey: "opencode-secret-value", BaseURL: "https://gateway.example.com/v1"},
		"pi":       {APIKey: "pi-secret-value"},
	}}); err != nil {
		t.Fatal(err)
	}
	project := clarifyEngineProjectFor(t, st, "cli-user")
	catalog := decodeClarifyEngineCatalog(t, getClarifyEngines(t, st, g, client, "cli-user", "?projectId="+project))

	// 有 Key 有 Base URL，但这台控制面机器上没有 opencode 可执行文件。
	opencode := clarifyEngineOptionOf(t, catalog, "opencode")
	if !opencode.Configured || opencode.Available || !strings.Contains(opencode.Reason, "OpenCode") {
		t.Fatalf("opencode = %+v, want configured but not installed", opencode)
	}
	// opencode 内置目录为空但允许自定义模型，所以至少给一项可选，且必须带引擎前缀。
	if len(opencode.Models) == 0 {
		t.Fatalf("opencode 允许自定义模型，就该有一项可选：%+v", opencode.Models)
	}
	for _, model := range opencode.Models {
		if !strings.HasPrefix(model.ID, "opencode:") {
			t.Fatalf("opencode 的模型 ID 没有带引擎前缀：%+v", model.ID)
		}
	}
	// pi 必须填 Base URL。
	pi := clarifyEngineOptionOf(t, catalog, "pi")
	if pi.Available || !strings.Contains(pi.Reason, "Base URL") {
		t.Fatalf("pi = %+v, want the missing Base URL reason", pi)
	}
	body := strings.ToLower(getClarifyEngines(t, st, g, client, "cli-user", "?projectId="+project).Body.String())
	for _, secret := range []string{"opencode-secret-value", "pi-secret-value", "gateway.example.com"} {
		if strings.Contains(body, secret) {
			t.Fatalf("响应泄露了 %q", secret)
		}
	}
}

func TestClarifyEnginesListsModelsOfConfiguredEngines(t *testing.T) {
	st, g, client := clarifyEnginesFixture(t)
	// 这台控制面真的装了 codex：它才应该是亮着的那一项。
	fakeBin := filepath.Join(os.Getenv("PATH"), "codex")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{LoginName: "modelled", CodexAPIKey: "sk-codex", ClarifyModel: "codex:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	project := clarifyEngineProjectFor(t, st, "modelled")
	catalog := decodeClarifyEngineCatalog(t, getClarifyEngines(t, st, g, client, "modelled", "?projectId="+project))
	codex := clarifyEngineOptionOf(t, catalog, "codex")
	if !codex.Configured || !codex.Available || codex.Reason != "" {
		t.Fatalf("codex = %+v, want available with no reason", codex)
	}
	prefixed := false
	for _, model := range codex.Models {
		if strings.HasPrefix(model.ID, "codex:") {
			prefixed = true
		}
	}
	if !prefixed {
		t.Fatalf("codex 的模型没有带引擎前缀：%+v", codex.Models)
	}
	if catalog.Default != "codex:gpt-5.5" || catalog.DefaultStale {
		t.Fatalf("default = %q stale=%v, want the personal default honoured", catalog.Default, catalog.DefaultStale)
	}
}

func TestClarifyEnginesMarksStaleDefault(t *testing.T) {
	st, g, client := clarifyEnginesFixture(t)
	// 默认值指向 Codex，但 Key 已经不在了：回退 CodeBuddy，并且把失效状态报出来。
	if err := st.PutUser(domain.User{LoginName: "stale", ClarifyModel: "codex:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	project := clarifyEngineProjectFor(t, st, "stale")
	catalog := decodeClarifyEngineCatalog(t, getClarifyEngines(t, st, g, client, "stale", "?projectId="+project))
	if catalog.Default != clarifyDefaultModel || !catalog.DefaultStale {
		t.Fatalf("default = %q stale=%v, want default + stale flag", catalog.Default, catalog.DefaultStale)
	}
}

func TestClarifyEnginesScopeErrors(t *testing.T) {
	st, g, client := clarifyEnginesFixture(t)
	if err := st.PutUser(domain.User{LoginName: "scope"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ query string }{
		{"?"},
		{"?issueId=nope"},
		{"?projectId=nope"},
	}
	for _, tc := range cases {
		rec := getClarifyEngines(t, st, g, client, "scope", tc.query)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Fatalf("query %s status=%d body=%s", tc.query, rec.Code, rec.Body.String())
		}
	}
	// 每张单都要能问：不存在的 issueId 是 404，不是 200 空目录。
	if rec := getClarifyEngines(t, st, g, client, "scope", "?issueId=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown issue status=%d, want 404", rec.Code)
	}
}

func TestClarifyEnginesIncludeTheFullEngineSet(t *testing.T) {
	st, g, client := clarifyEnginesFixture(t)
	if err := st.PutUser(domain.User{LoginName: "wide", ClaudeCodeAPIKey: "sk-claude"}); err != nil {
		t.Fatal(err)
	}
	project := clarifyEngineProjectFor(t, st, "wide")
	catalog := decodeClarifyEngineCatalog(t, getClarifyEngines(t, st, g, client, "wide", "?projectId="+project))
	want := append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...)
	if len(catalog.Engines) != len(want) {
		t.Fatalf("engines = %d, want every CLI the platform offers (%d): %+v", len(catalog.Engines), len(want), catalog.Engines)
	}
	for index, id := range want {
		if catalog.Engines[index].ID != id {
			t.Fatalf("engine order = %v, want %v", catalog.Engines, want)
		}
	}
	// Claude Code 不在镜像里，由平台首次使用时从 npm 安装，所以本机没有二进制不算不可用。
	if _, err := resolveAssistantEngineBin(assistantEngineClaude); err == nil {
		t.Skip("这台机器真的装了 claude，跳过「未安装」断言")
	}
	claude := clarifyEngineOptionOf(t, catalog, assistantEngineClaude)
	if !claude.Configured || !claude.Available {
		t.Fatalf("claude-code = %+v, want available because the platform installs it on first use", claude)
	}
}
