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
	"sync/atomic"
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
			{"id":"gpt-5.5"}
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
		if got := result.Models[i].DiscoveredReasoningEfforts; got == nil || !slices.Equal(*got, want) {
			t.Fatalf("model %s lost efforts: %+v, want %v", result.Models[i].ID, got, want)
		}
		if result.Models[i].ReasoningEfforts != nil {
			t.Fatalf("model %s returned an override instead of discovered capabilities", result.Models[i].ID)
		}
	}
	if _, err := h.repo.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{
		CodexAPIKey: "fixture-key", CodexBaseURL: &service.URL, CodexModels: &result.Models,
	}); err != nil {
		t.Fatal(err)
	}
	owner, _ = h.repo.User(owner.ID)
	models, _ := assistantEngineModels(context.Background(), owner, nil)
	for id, efforts := range map[string][]string{
		"codex:gateway-reasoner": {"low", "xhigh"},
		"codex:gpt-5.5":          {"low", "medium", "high", "xhigh"},
	} {
		if !slices.ContainsFunc(models, func(model assistantModel) bool {
			return model.ID == id && slices.Equal(model.Efforts, efforts)
		}) {
			t.Errorf("chat catalog missing %s with efforts %v: %+v", id, efforts, models)
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
		{"codex:gpt-5.5", "xhigh", true},
		{"codex:gpt-5.5", "max", false},
	} {
		if err := validateAssistantEffort(context.Background(), owner, check.model, check.effort); (err == nil) != check.valid {
			t.Errorf("validate %s/%s: %v", check.model, check.effort, err)
		}
	}
	c, response = workbenchContext(owner, http.MethodGet, "/api/executor-models", bytes.NewBuffer(nil))
	(&catalogHandlers{deps: h.deps}).executorModels(c)
	var catalogs map[string]executorModelCatalog
	if err := json.Unmarshal(response.Body.Bytes(), &catalogs); err != nil {
		t.Fatal(err)
	}
	if got := catalogs["codex"].Models; len(got) != 3 ||
		!slices.Equal(got[0].Efforts, effortOptions([]string{"low", "xhigh"})) || len(got[1].Efforts) != 0 ||
		!slices.Equal(got[2].Efforts, effortOptions([]string{"low", "medium", "high", "xhigh"})) {
		t.Fatalf("role catalog lost per-model efforts: %+v", got)
	}
}

func TestModelEffortsKeepLegacyCatalogsAndCLIConstraints(t *testing.T) {
	custom := []string{"low", "xhigh", "ultra"}
	discovered := []string{"high"}
	explicitDefault := []string{}
	switchLevels := []string{"off", "on"}
	deepseekLevels := []string{"off", "max"}
	for _, check := range []struct {
		model string
		entry domain.CLIModel
		want  []string
	}{
		{"codex:custom", domain.CLIModel{ID: "custom"}, []string{"low", "medium", "high"}},
		{"kimi:unknown", domain.CLIModel{ID: "unknown"}, nil},
		{"kimi:kimi-for-coding-highspeed", domain.CLIModel{ID: "kimi-for-coding-highspeed", DiscoveredReasoningEfforts: &switchLevels}, []string{"none", "thinking"}},
		{"kimi:k3.1-pro", domain.CLIModel{ID: "k3.1-pro", DiscoveredReasoningEfforts: &deepseekLevels}, []string{"max"}},
		{"dsh:deepseek-flash", domain.CLIModel{ID: "deepseek-flash", DiscoveredReasoningEfforts: &deepseekLevels}, []string{"none", "max"}},
		{"dsh:deepseek-flash", domain.CLIModel{ID: "deepseek-flash", ReasoningEfforts: &explicitDefault, DiscoveredReasoningEfforts: &deepseekLevels}, nil},
		{"zcode:glm-5.3", domain.CLIModel{ID: "glm-5.3", DiscoveredReasoningEfforts: &discovered}, discovered},
		{"codex:custom", domain.CLIModel{ID: "custom", DiscoveredReasoningEfforts: &discovered}, discovered},
		{"codex:custom", domain.CLIModel{ID: "custom", ReasoningEfforts: &custom, DiscoveredReasoningEfforts: &discovered}, custom},
		{"codex:custom", domain.CLIModel{ID: "custom", ReasoningEfforts: &custom}, custom},
		{"codex:gpt-5.5", domain.CLIModel{ID: "gpt-5.5"}, []string{"low", "medium", "high", "xhigh"}},
		{"codex:gpt-5.5", domain.CLIModel{ID: "gpt-5.5", DiscoveredReasoningEfforts: &discovered}, discovered},
		{"codex:gpt-5.5", domain.CLIModel{ID: "gpt-5.5", ReasoningEfforts: &custom, DiscoveredReasoningEfforts: &discovered}, custom},
		{"codex:gpt-5.5", domain.CLIModel{ID: "gpt-5.5", ReasoningEfforts: &explicitDefault, DiscoveredReasoningEfforts: &discovered}, nil},
		{"claude-code:claude-fable-5-2", domain.CLIModel{ID: "claude-fable-5-2"}, []string{"low", "medium", "high", "xhigh", "max"}},
		{"claude-code:claude-fable-5-2", domain.CLIModel{ID: "claude-fable-5-2", DiscoveredReasoningEfforts: &discovered}, discovered},
		{"claude-code:claude-fable-5-2", domain.CLIModel{ID: "claude-fable-5-2", ReasoningEfforts: &custom, DiscoveredReasoningEfforts: &discovered}, []string{"low", "xhigh"}},
		{"claude-code:claude-fable-5-2", domain.CLIModel{ID: "claude-fable-5-2", ReasoningEfforts: &explicitDefault, DiscoveredReasoningEfforts: &discovered}, nil},
		{"claude-code:claude-opus-5.5", domain.CLIModel{ID: "claude-opus-5.5"}, []string{"low", "medium", "high", "xhigh", "max"}},
		{"claude-fable-5-2", domain.CLIModel{ID: "claude-fable-5-2"}, []string{"low", "medium", "high"}},
		{"gpt-5.5", domain.CLIModel{ID: "gpt-5.5"}, []string{"low", "medium", "high"}},
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

func TestAutomaticDiscoveryUsesSameEffortsForColdValidation(t *testing.T) {
	var requests atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("unexpected model discovery request")
		}
		requests.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"remote","supported_reasoning_efforts":["xhigh"]}]}`)
	}))
	t.Cleanup(gateway.Close)
	ctx := context.Background()
	user := domain.User{CodexAPIKey: "fixture-key", CodexBaseURL: gateway.URL}
	key := assistantDiscoveryKey("", user.CodexAPIKey, user.CodexBaseURL)
	t.Cleanup(func() { assistantDiscoveryCache.Delete(key) })
	catalog, _ := assistantEngineModels(ctx, user, nil)
	if !slices.ContainsFunc(catalog, func(model assistantModel) bool {
		return model.ID == "codex:remote" && slices.Equal(model.Efforts, []string{"xhigh"})
	}) {
		t.Fatalf("the picker did not expose the gateway capability: %+v", catalog)
	}

	assistantDiscoveryCache.Delete(key)
	if err := validateAssistantEffort(ctx, user, "codex:remote", "xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := validateAssistantEffort(ctx, user, "codex:remote", "high"); err == nil {
		t.Fatal("validation ignored the discovered model's capabilities")
	}
	if err := validateAssistantEffort(ctx, domain.User{}, "codex:remote", "xhigh"); err == nil {
		t.Fatal("another user's discovered capabilities were used")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected one discovery per process cache, got %d", got)
	}
}

func TestKimiThinkingSwitchSurvivesSavedCatalog(t *testing.T) {
	h, owner := workbenchFixture(t)
	levels := []string{"none", "thinking"}
	models := []domain.CLIModel{{ID: "kimi-for-coding-highspeed", ReasoningEfforts: &levels}}
	if _, err := h.repo.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{
		AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
			"kimi": {APIKey: "fixture-key", Models: &models},
		},
	}); err != nil {
		t.Fatal(err)
	}
	owner, _ = h.repo.User(owner.ID)
	chat, _ := assistantEngineModels(context.Background(), owner, nil)
	if !slices.ContainsFunc(chat, func(model assistantModel) bool {
		return model.ID == "kimi:kimi-for-coding-highspeed" && slices.Equal(model.Efforts, levels)
	}) {
		t.Fatalf("saved Kimi thinking options not shown: %+v", chat)
	}

	baseURL := "https://gateway.example/v1"
	if _, err := h.repo.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{
		AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{"kimi": {BaseURL: &baseURL, Models: &models}},
	}); err != nil {
		t.Fatal(err)
	}
	owner, _ = h.repo.User(owner.ID)
	chat, _ = assistantEngineModels(context.Background(), owner, nil)
	if !slices.ContainsFunc(chat, func(model assistantModel) bool {
		return model.ID == "kimi:kimi-for-coding-highspeed" && len(model.Efforts) == 0
	}) {
		t.Fatalf("gateway catalog advertised a Kimi override: %+v", chat)
	}
	if err := validateAssistantEffort(context.Background(), owner, "kimi:kimi-for-coding-highspeed", "thinking"); err == nil {
		t.Fatal("Kimi gateway accepted an unsupported per-turn override")
	}
}

func TestBuiltinModelEffortsMatchTheCatalogAndTurnValidation(t *testing.T) {
	h, owner := workbenchFixture(t)
	chatCatalogs := make(map[string]assistantModelCatalog)
	for _, engine := range []string{"codex", "claude-code", "kimi", "dsh", "zcode"} {
		c, response := workbenchContext(owner, http.MethodGet, "/api/assistant/models?executor="+engine, &bytes.Buffer{})
		h.models(c)
		var chat assistantModelCatalog
		if err := json.Unmarshal(response.Body.Bytes(), &chat); response.Code != http.StatusOK || err != nil {
			t.Fatalf("%s chat catalog: %d %s", engine, response.Code, response.Body)
		}
		chatCatalogs[engine] = chat
	}
	c, response := workbenchContext(owner, http.MethodGet, "/api/executor-models?builtin=1", &bytes.Buffer{})
	(&catalogHandlers{deps: h.deps}).executorModels(c)
	var role map[string]executorModelCatalog
	if err := json.Unmarshal(response.Body.Bytes(), &role); response.Code != http.StatusOK || err != nil {
		t.Fatalf("role catalog: %d %s", response.Code, response.Body)
	}
	xhigh := []string{"low", "medium", "high", "xhigh"}
	max := []string{"low", "medium", "high", "xhigh", "max"}
	ultra := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	levels := append([]string{"none", "thinking"}, ultra...)
	for _, model := range []struct {
		engine string
		id     string
		levels []string
	}{
		{"codex", "gpt-5.5", xhigh},
		{"codex", "gpt-5.6-luna", max},
		{"codex", "gpt-6-luna", max},
		{"codex", "gpt-6.1-terra", ultra},
		{"codex", "gpt-6.2-sol", ultra},
		{"codex", "gpt-6.2-astra", ultra},
		{"claude-code", "claude-fable-5-2", max},
		{"claude-code", "claude-opus-5-5", max},
		{"claude-code", "claude-sonnet-4-6", []string{"low", "medium", "high", "max"}},
		{"kimi", "k3.1-pro", []string{"low", "high", "max"}},
		{"kimi", "k3", []string{"none", "low", "high", "max"}},
		{"kimi", "kimi-for-coding-highspeed", []string{"none", "thinking"}},
		{"dsh", "deepseek-flash", []string{"none", "low", "high", "max"}},
		{"dsh", "deepseek-v4-pro", []string{"none", "low", "high", "max"}},
		{"zcode", "glm-5.3", []string{"low", "high", "max"}},
		{"zcode", "glm-5.3-flash", []string{"low", "high", "max"}},
	} {
		t.Run(model.engine+"/"+model.id, func(t *testing.T) {
			chat := chatCatalogs[model.engine]
			chatIndex := slices.IndexFunc(chat.Models, func(m assistantModel) bool { return m.ID == model.id })
			if chatIndex < 0 || !slices.Equal(chat.Models[chatIndex].Efforts, model.levels) {
				t.Errorf("chat picker missing %s with efforts %v", model.id, model.levels)
			}
			roleKey := model.engine
			if roleKey == "claude-code" {
				roleKey = "claude"
			}
			roleModels := role[roleKey].Models
			roleIndex := slices.IndexFunc(roleModels, func(m executorModelInfo) bool { return m.ID == model.id })
			if roleIndex < 0 || !slices.Equal(roleModels[roleIndex].Efforts, effortOptions(model.levels)) {
				t.Errorf("role picker missing %s with efforts %v", model.id, model.levels)
			}
			for _, effort := range levels {
				err := validateAssistantEffort(context.Background(), owner, model.engine+":"+model.id, effort)
				if want := slices.Contains(model.levels, effort); (err == nil) != want {
					t.Errorf("%s effort %s: validation returned %v, want accepted=%t", model.id, effort, err, want)
				}
			}
			last := model.levels[len(model.levels)-1]
			body, err := json.Marshal(map[string]string{"model": model.id, "executor": model.engine, "effort": last})
			if err != nil {
				t.Fatal(err)
			}
			sessionID := model.engine + "-" + model.id
			c, response := workbenchContext(owner, http.MethodPut, "/api/assistant/sessions/"+sessionID, bytes.NewBuffer(body))
			c.Params = gin.Params{{Key: "id", Value: sessionID}}
			h.session(c)
			if response.Code != http.StatusOK {
				t.Fatalf("session rejected the bare model ID with executor %s: %d %s", model.engine, response.Code, response.Body)
			}
		})
	}
}
