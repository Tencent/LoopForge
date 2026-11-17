package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestModelEffortsSurviveProbeSaveAndSelection(t *testing.T) {
	h, owner := workbenchFixture(t)
	stubCLIModelDiscovery(t)
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"gateway-reasoner","supported_reasoning_efforts":["xhigh","low","invalid","xhigh"]},
			{"id":"gateway-chat","reasoningEfforts":[]},
			{"id":"gpt-6.1-sol"}
		]}`)
	}))
	t.Cleanup(service.Close)
	body, _ := json.Marshal(map[string]string{"engine": "codex", "apiKey": "fixture-key", "baseUrl": service.URL})
	c, response := workbenchContext(owner, http.MethodPost, "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(owner.ID, 10)}}
	(&usersHandlers{deps: h.deps}).probeCLIModels(c)
	var result cliModelProbeResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Error != "" || len(result.Models) != 3 {
		t.Fatalf("probe=%s err=%v", response.Body, err)
	}
	for i, want := range [][]string{{"low", "xhigh"}, {}, {"low", "medium", "high", "xhigh"}} {
		if got := result.Models[i].ReasoningEfforts; got == nil || !slices.Equal(*got, want) {
			t.Fatalf("model %s lost efforts: %+v, want %v", result.Models[i].ID, got, want)
		}
	}
	if _, err := h.repo.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{
		CodexAPIKey: "fixture-key", CodexBaseURL: &service.URL, CodexModels: &result.Models,
	}); err != nil {
		t.Fatal(err)
	}
	owner, _ = h.repo.User(owner.ID)
	models := assistantEngineModels(context.Background(), owner, nil)
	for _, model := range models {
		if model.ID == "codex:gateway-reasoner" && !slices.Equal(model.Efforts, []string{"low", "xhigh"}) {
			t.Fatalf("chat catalog lost efforts: %+v", model)
		}
	}
	for _, check := range []struct {
		model, effort string
		valid         bool
	}{
		{"codex:gateway-reasoner", "xhigh", true},
		{"codex:gateway-reasoner", "high", false},
		{"codex:gateway-chat", "high", false},
		{"codex:gateway-chat", "", true},
	} {
		if err := validateAssistantEffort(owner, check.model, check.effort); (err == nil) != check.valid {
			t.Errorf("validate %s/%s: %v", check.model, check.effort, err)
		}
	}
	c, response = workbenchContext(owner, http.MethodGet, "/api/executor-models", bytes.NewBuffer(nil))
	(&catalogHandlers{deps: h.deps}).executorModels(c)
	var catalogs map[string]executorModelCatalog
	if err := json.Unmarshal(response.Body.Bytes(), &catalogs); err != nil {
		t.Fatal(err)
	}
	if got := catalogs["codex"].Models; len(got) != 3 || !slices.EqualFunc(got[0].Efforts, effortOptions([]string{"low", "xhigh"}), func(a, b executorEffortOption) bool { return a == b }) || len(got[1].Efforts) != 0 {
		t.Fatalf("role catalog lost per-model efforts: %+v", got)
	}
}

func TestModelEffortsKeepLegacyCatalogsAndCLIConstraints(t *testing.T) {
	custom := []string{"low", "xhigh", "ultra"}
	for _, check := range []struct {
		model string
		entry domain.CLIModel
		want  []string
	}{
		{"codex:custom", domain.CLIModel{ID: "custom"}, []string{"low", "medium", "high"}},
		{"codex:custom", domain.CLIModel{ID: "custom", ReasoningEfforts: &custom}, custom},
		{"opencode:custom", domain.CLIModel{ID: "custom", ReasoningEfforts: &custom}, []string{"low"}},
		{"agy:claude-custom", domain.CLIModel{ID: "claude-custom", ReasoningEfforts: &custom}, nil},
		{"cursor:unknown", domain.CLIModel{ID: "unknown", ReasoningEfforts: &custom}, nil},
		{"cursor:widget", domain.CLIModel{ID: "widget", Efforts: map[string]string{"low": "widget-low", "xhigh": "widget-extra-high"}, ReasoningEfforts: &custom}, []string{"low", "xhigh"}},
	} {
		if got := assistantModelEffortsIn(check.model, []domain.CLIModel{check.entry}); !slices.Equal(got, check.want) {
			t.Errorf("%s: %v, want %v", check.model, got, check.want)
		}
	}
}

func TestAutomaticDiscoveryUsesSameEffortsForValidation(t *testing.T) {
	levels := []string{"xhigh"}
	user := domain.User{CodexAPIKey: "fixture-key", CodexBaseURL: "https://effort-catalog.example/v1"}
	key := assistantDiscoveryKey(false, user.CodexAPIKey, user.CodexBaseURL)
	assistantDiscoveryCache.Store(key, assistantDiscoveryEntry{models: []assistantDiscoveredModel{{ID: "remote", ReasoningEfforts: &levels}}})
	t.Cleanup(func() { assistantDiscoveryCache.Delete(key) })
	if err := validateAssistantEffort(user, "codex:remote", "xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := validateAssistantEffort(user, "codex:remote", "high"); err == nil {
		t.Fatal("validation ignored the discovered model's capabilities")
	}
	if err := validateAssistantEffort(domain.User{}, "codex:remote", "xhigh"); err == nil {
		t.Fatal("another user's discovered capabilities were used")
	}
}
