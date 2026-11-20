package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestAssistantExecutorModelsUseTheCallersPersonalCatalog(t *testing.T) {
	h, owner := workbenchFixture(t)
	configuredEfforts := []string{"low", "high"}
	discoveredEfforts := []string{"medium"}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a saved model catalog must take precedence over gateway discovery")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gateway.Close()
	for _, user := range []domain.User{
		{
			LoginName:    "personal-model-viewer",
			CodexAPIKey:  "viewer-catalog-secret",
			CodexBaseURL: gateway.URL + "/v1",
			CodexModels: []domain.CLIModel{
				{ID: "deepseek-v4-pro", Name: "Personal DeepSeek", ReasoningEfforts: &configuredEfforts},
				{ID: "glm-4.7", DiscoveredReasoningEfforts: &discoveredEfforts},
			},
		},
		{
			LoginName:   "another-model-viewer",
			CodexAPIKey: "other-catalog-secret",
			CodexModels: []domain.CLIModel{{ID: "other-private-model"}},
		},
	} {
		if err := h.repo.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	user, _ := h.repo.UserByLoginName("personal-model-viewer")
	c, rec := workbenchContext(user, http.MethodGet, "/api/assistant/models?executor=codex", bytes.NewBuffer(nil))
	h.models(c)
	var catalog assistantModelCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &catalog) != nil {
		t.Fatalf("personal executor catalog: %d %s", rec.Code, rec.Body)
	}
	var ids []string
	for _, model := range catalog.Models {
		ids = append(ids, model.ID)
	}
	if catalog.Default != "default" || !slices.Equal(ids, []string{"default", "deepseek-v4-pro", "glm-4.7"}) {
		t.Fatalf("personal executor catalog = default %q, models %v", catalog.Default, ids)
	}
	if catalog.Models[1].Name != "Personal DeepSeek" || catalog.Models[2].Name != "glm-4.7" {
		t.Fatalf("personal model names = %+v", catalog.Models)
	}
	if !slices.Equal(catalog.Models[1].Efforts, []string{"low", "high"}) || !slices.Equal(catalog.Models[2].Efforts, []string{"medium"}) || len(catalog.Models[0].Efforts) == 0 {
		t.Fatalf("personal and CLI default model reasoning efforts = %+v", catalog.Models)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(rec.Body.String(), "catalog-secret") {
		t.Fatal("personal executor catalogs must be private and must not expose credentials")
	}

	// Another caller without a personal Codex catalog keeps the CLI default
	// and built-in models; the previous caller's gateway models stay private.
	c, rec = workbenchContext(owner, http.MethodGet, "/api/assistant/models?executor=codex", bytes.NewBuffer(nil))
	h.models(c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gpt-6.1-sol") || strings.Contains(rec.Body.String(), "deepseek-v4-pro") || strings.Contains(rec.Body.String(), "other-private-model") {
		t.Fatalf("another caller's executor catalog: %d %s", rec.Code, rec.Body)
	}
}

func TestAssistantExecutorModelsDiscoverOnlyTheRequestedCLI(t *testing.T) {
	h, user := workbenchFixture(t)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/v1/models" {
			t.Errorf("a Codex catalog request queried another CLI gateway: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer codex-discovery-secret" {
			t.Error("the requested CLI gateway did not receive its own credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-pro","name":"Discovered DeepSeek","supported_reasoning_efforts":["high"]}]}`))
	}))
	defer gateway.Close()
	user = domain.User{
		LoginName:         "scoped-discovery-user",
		CodexAPIKey:       "codex-discovery-secret",
		CodexBaseURL:      gateway.URL + "/codex/v1",
		ClaudeCodeAPIKey:  "claude-discovery-secret",
		ClaudeCodeBaseURL: gateway.URL + "/claude",
	}
	if err := h.repo.PutUser(user); err != nil {
		t.Fatal(err)
	}
	user, _ = h.repo.UserByLoginName(user.LoginName)
	c, rec := workbenchContext(user, http.MethodGet, "/api/assistant/models?executor=codex", bytes.NewBuffer(nil))
	h.models(c)
	var catalog assistantModelCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &catalog) != nil {
		t.Fatalf("discovered executor catalog: %d %s", rec.Code, rec.Body)
	}
	if catalog.Default != "default" || len(catalog.Models) != 2 || catalog.Models[1].ID != "deepseek-v4-pro" || catalog.Models[1].Name != "Discovered DeepSeek" || catalog.Models[1].Family != "deepseek" || !slices.Equal(catalog.Models[1].Efforts, []string{"high"}) {
		t.Fatalf("discovered executor catalog = %+v", catalog)
	}
}
