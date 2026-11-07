package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func sponsorFixture(t *testing.T) (*repo.Store, domain.User) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, u := range []domain.User{
		{LoginName: "sponsor", CodeBuddyAPIKey: "sponsor-secret", CodeBuddyInternetEnvironment: "internal"},
		{LoginName: "newcomer", GitToken: "newcomer-git"},
	} {
		if err := st.PutUser(u); err != nil {
			t.Fatal(err)
		}
	}
	newcomer, _ := st.UserByLoginName("newcomer")
	project, _ := st.Project("platform")
	project.Members = append(project.Members, domain.ProjectMember{UserID: newcomer.ID, Role: "member"})
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	return st, newcomer
}

func sponsoredChat(h *assistantHandlers, user domain.User, model string) *httptest.ResponseRecorder {
	body := `{"projectId":"platform","model":"` + model + `","messages":[{"role":"user","content":"你好"}]}`
	req := httptest.NewRequest("POST", "/api/assistant/chat", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.chat(c)
	return rec
}

func TestUsersWithoutKeyChatOnSponsoredModel(t *testing.T) {
	st, newcomer := sponsorFixture(t)
	t.Setenv(assistantSponsorLoginEnv, "sponsor")
	var used domain.User
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(_ context.Context, _ string, u domain.User) (string, error) {
		used = u
		return `{"message":"你好呀","drafts":[]}`, nil
	}}

	for _, model := range []string{"", assistantSponsoredModel} {
		used = domain.User{}
		rec := sponsoredChat(h, newcomer, model)
		if rec.Code != 200 {
			t.Fatalf("model %q: %d %s", model, rec.Code, rec.Body.String())
		}
		if used.ID != newcomer.ID || used.CodeBuddyAPIKey != "sponsor-secret" || used.CodeBuddyInternetEnvironment != "internal" || used.GitToken != "newcomer-git" {
			t.Fatalf("turn must run as the newcomer with only the sponsor's CodeBuddy key: %+v", used)
		}
	}

	rec := sponsoredChat(h, newcomer, "claude-opus-5")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "配置自己的 CodeBuddy API Key") {
		t.Fatalf("other models need an own key: %d %s", rec.Code, rec.Body.String())
	}
	if stored, _ := st.User(newcomer.ID); stored.CodeBuddyAPIKey != "" {
		t.Fatal("sponsor key must never be persisted on the newcomer")
	}

	var catalog assistantModelCatalog
	mrec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(mrec)
	c.Request = httptest.NewRequest("GET", "/api/assistant/models", nil)
	h.models(c)
	if json.Unmarshal(mrec.Body.Bytes(), &catalog) != nil || catalog.SponsoredModel != assistantSponsoredModel || catalog.Default != assistantChatDefaultModel {
		t.Fatalf("catalog = %s", mrec.Body.String())
	}
	if strings.Contains(mrec.Body.String(), `"sponsor"`) || strings.Contains(mrec.Body.String(), "sponsor-secret") {
		t.Fatal("catalog must not reveal the sponsor account or key")
	}
}

func TestOwnKeyKeepsFreeModelChoice(t *testing.T) {
	st, newcomer := sponsorFixture(t)
	t.Setenv(assistantSponsorLoginEnv, "sponsor")
	if _, err := st.UpdateUserCredentials(newcomer.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "own-secret"}); err != nil {
		t.Fatal(err)
	}
	var used domain.User
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(_ context.Context, _ string, u domain.User) (string, error) {
		used = u
		return `{"message":"ok","drafts":[]}`, nil
	}}
	if rec := sponsoredChat(h, newcomer, "claude-opus-5"); rec.Code != 200 || used.CodeBuddyAPIKey != "own-secret" {
		t.Fatalf("own key: %d %s key=%q", rec.Code, rec.Body.String(), used.CodeBuddyAPIKey)
	}
}

// The setup guides read bootstrap to tell new users that chat and SOP runs
// work on the shared model before they configure a key.
func TestBootstrapAdvertisesSponsoredModel(t *testing.T) {
	st, _ := sponsorFixture(t)
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	t.Setenv(assistantSponsorLoginEnv, "sponsor")
	boot := requestEmptyJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", http.StatusOK)
	if boot["sponsoredModel"] != assistantSponsoredModel {
		t.Fatalf("sponsoredModel = %v", boot["sponsoredModel"])
	}
	if err := st.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "platform-secret", Models: []string{"glm-5.1", "kimi-k2.6"}}, 0); err != nil {
		t.Fatal(err)
	}
	boot = requestEmptyJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", http.StatusOK)
	if models, _ := boot["sponsoredModels"].([]any); boot["sponsoredModel"] != "glm-5.1" || len(models) != 2 {
		t.Fatalf("platform shared key = %v %v", boot["sponsoredModel"], boot["sponsoredModels"])
	}
	if err := st.ClearPlatformCodeBuddyKey(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(assistantSponsorLoginEnv, "")
	boot = requestEmptyJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", http.StatusOK)
	if boot["sponsoredModel"] != "" {
		t.Fatalf("no sponsor must advertise nothing, got %v", boot["sponsoredModel"])
	}
}

func TestNoSponsorKeepsRequiringOwnKey(t *testing.T) {
	st, newcomer := sponsorFixture(t)
	for _, login := range []string{"", "missing-user", "newcomer"} {
		t.Setenv(assistantSponsorLoginEnv, login)
		h := &assistantHandlers{deps: &deps{repo: st}, generate: func(context.Context, string, domain.User) (string, error) {
			t.Fatal("must not reach the model")
			return "", nil
		}}
		if rec := sponsoredChat(h, newcomer, ""); rec.Code != 400 || !strings.Contains(rec.Body.String(), "CodeBuddy API Key") {
			t.Fatalf("sponsor %q: %d %s", login, rec.Code, rec.Body.String())
		}
		if _, ok := h.assistantSharedKey(); ok {
			t.Fatalf("sponsor %q without a key must not be available", login)
		}
	}
}

// The key an administrator configures under 平台管理 replaces the legacy
// sponsor account, and limits shared turns to the models they allowed.
func TestPlatformSharedKeyLimitsChatToAllowedModels(t *testing.T) {
	st, newcomer := sponsorFixture(t)
	stubCLIModelDiscovery(t)
	t.Setenv(assistantSponsorLoginEnv, "sponsor")
	if err := st.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "platform-secret", InternetEnvironment: "ioa", Models: []string{"glm-5.1", "kimi-k2.6"}}, 0); err != nil {
		t.Fatal(err)
	}
	var used domain.User
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(_ context.Context, _ string, u domain.User) (string, error) {
		used = u
		return `{"message":"ok","drafts":[]}`, nil
	}}
	for model, want := range map[string]string{"": "glm-5.1", "kimi-k2.6": "kimi-k2.6"} {
		used = domain.User{}
		rec := sponsoredChat(h, newcomer, model)
		if rec.Code != 200 || used.CodeBuddyAPIKey != "platform-secret" || used.CodeBuddyInternetEnvironment != "ioa" {
			t.Fatalf("model %q: %d %s used=%+v", model, rec.Code, rec.Body.String(), used)
		}
		if got := assistantAllowedModel(model, h.chatCatalog(context.Background(), used)); got != want {
			t.Fatalf("model %q resolves to %q, want %q", model, got, want)
		}
	}
	for _, model := range []string{assistantSponsoredModel, "claude-opus-5"} {
		rec := sponsoredChat(h, newcomer, model)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "glm-5.1、kimi-k2.6") {
			t.Fatalf("model %q outside the allow list: %d %s", model, rec.Code, rec.Body.String())
		}
	}

	var catalog assistantModelCatalog
	mrec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(mrec)
	c.Request = httptest.NewRequest("GET", "/api/assistant/models", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(newcomer)))
	h.models(c)
	if json.Unmarshal(mrec.Body.Bytes(), &catalog) != nil || catalog.Default != "glm-5.1" || strings.Join(catalog.SponsoredModels, ",") != "glm-5.1,kimi-k2.6" || len(catalog.Models) != 2 {
		t.Fatalf("shared-key catalog = %s", mrec.Body.String())
	}
	if strings.Contains(mrec.Body.String(), "platform-secret") {
		t.Fatal("catalog must not reveal the shared key")
	}
}

func TestAssistantModelsPerExecutor(t *testing.T) {
	h := &assistantHandlers{deps: &deps{}}
	get := func(executor string) (*httptest.ResponseRecorder, assistantModelCatalog) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/api/assistant/models?executor="+executor, nil)
		h.models(c)
		var catalog assistantModelCatalog
		_ = json.Unmarshal(rec.Body.Bytes(), &catalog)
		return rec, catalog
	}
	for executor, family := range map[string]string{"codex": "gpt", "claude": "claude"} {
		rec, catalog := get(executor)
		if rec.Code != 200 || catalog.Default != "default" || len(catalog.Models) < 2 || catalog.Models[0].ID != "default" {
			t.Fatalf("%s catalog: %d %s", executor, rec.Code, rec.Body.String())
		}
		for _, model := range catalog.Models {
			if model.Family != family {
				t.Fatalf("%s model %s family = %q", executor, model.ID, model.Family)
			}
		}
		if catalog.SponsoredModel != "" {
			t.Fatalf("%s must not offer the CodeBuddy sponsored model", executor)
		}
	}
	if rec, _ := get("bogus"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "不支持的执行器") {
		t.Fatalf("unknown executor: %d %s", rec.Code, rec.Body.String())
	}
}
