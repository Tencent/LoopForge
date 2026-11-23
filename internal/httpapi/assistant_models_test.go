package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"agentflow/internal/domain"
)

func TestAssistantModelPickerOmitsCodeBuddyCLIDefault(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "default-model,model-a,model-b")
	h, user := workbenchFixture(t)
	stubCLIModelDiscovery(t)

	c, rec := workbenchContext(user, http.MethodGet, "/api/assistant/models", bytes.NewBuffer(nil))
	h.models(c)
	var catalog assistantModelCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &catalog) != nil {
		t.Fatalf("model picker: %d %s", rec.Code, rec.Body)
	}
	if catalog.Default != assistantDefaultModel || len(catalog.DefaultEfforts) == 0 ||
		assistantCatalogLists(catalog, assistantDefaultModel) || assistantCatalogLists(catalog, "default-model") || !assistantCatalogLists(catalog, "model-b") {
		t.Fatalf("model picker shows the wrong choices: %+v", catalog)
	}
	if got := assistantAllowedModel("", h.chatCatalog(context.Background(), user)); got != assistantDefaultModel {
		t.Fatalf("an omitted request model should still use the CLI fallback, got %q", got)
	}
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "")
	c, rec = workbenchContext(user, http.MethodGet, "/api/assistant/models", bytes.NewBuffer(nil))
	h.models(c)
	var unconfigured assistantModelCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &unconfigured) != nil ||
		unconfigured.Default != assistantDefaultModel || unconfigured.Models == nil || len(unconfigured.Models) != 0 {
		t.Fatalf("unconfigured picker: %d %s", rec.Code, rec.Body)
	}
}

func TestCodeBuddyModelPickersUseOnlyTheCurrentAccount(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "")
	t.Setenv(assistantSponsorLoginEnv, "")
	h, first := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "second-account", CodeBuddyAPIKey: "second-key"}); err != nil {
		t.Fatal(err)
	}
	second, _ := h.repo.UserByLoginName("second-account")
	stubCLIModelDiscovery(t)
	discoverCLIModels = func(_ context.Context, environment, key string) []string {
		switch key {
		case first.CodeBuddyAPIKey:
			return []string{"first-account-model"}
		case second.CodeBuddyAPIKey:
			return []string{"second-account-model"}
		case "":
			return []string{"public-model"}
		default:
			t.Fatalf("unexpected discovery account in environment %q", environment)
			return nil
		}
	}
	for _, user := range []domain.User{first, second, {}} {
		refreshCLIModels(context.Background(), user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
	}

	executors := &catalogHandlers{deps: h.deps}
	for _, tc := range []struct {
		name string
		user domain.User
		want string
	}{
		{"first", first, "first-account-model"},
		{"second", second, "second-account-model"},
		{"guest", domain.User{}, "public-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := workbenchContext(tc.user, http.MethodGet, "/api/assistant/models", &bytes.Buffer{})
			var chat assistantModelCatalog
			h.models(c)
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &chat) != nil {
				t.Fatalf("chat models: %d %s", rec.Code, rec.Body)
			}
			var chatIDs []string
			for _, model := range chat.Models {
				if model.Engine == "" || model.Engine == assistantEngineCodeBuddy {
					chatIDs = append(chatIDs, model.ID)
				}
			}
			if !slices.Equal(chatIDs, []string{tc.want}) {
				t.Errorf("chat models = %v, want only %q", chatIDs, tc.want)
			}

			c, rec = workbenchContext(tc.user, http.MethodGet, "/api/executor-models", &bytes.Buffer{})
			var catalog map[string]executorModelCatalog
			executors.executorModels(c)
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &catalog) != nil {
				t.Fatalf("executor models: %d %s", rec.Code, rec.Body)
			}
			var executorIDs []string
			for _, model := range catalog["codebuddy"].Models {
				executorIDs = append(executorIDs, model.ID)
			}
			if !slices.Equal(executorIDs, []string{tc.want}) {
				t.Errorf("executor models = %v, want only %q", executorIDs, tc.want)
			}
		})
	}
}

func TestCodeBuddyModelDiscoveryPerEnvironment(t *testing.T) {
	help := "  --model <model>  Model for the current session. Currently supported: (gpt-5.5, glm-5.3, echo, custom-local:glm-5.2, kimi-k3)\n"
	if got := parseCodeBuddyModels(help); !slices.Equal(got, []string{"gpt-5.5", "glm-5.3", "kimi-k3"}) {
		t.Fatalf("parsed %v", got)
	}
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "")
	calls := map[string]int{}
	stubCLIModelDiscovery(t)
	discoverCLIModels = func(_ context.Context, environment, apiKey string) []string {
		calls[environment+"|"+apiKey]++
		if environment == "ioa" && apiKey != "" {
			return []string{"claude-sonnet-5", "claude-opus-5.5"}
		}
		if environment == "ioa" {
			return []string{"claude-sonnet-5"}
		}
		return []string{"gpt-5.5"}
	}
	for range 2 {
		refreshCLIModels(context.Background(), "ioa", "")
	}
	refreshCLIModels(context.Background(), "", "")
	if calls["ioa|"] != 1 || calls["|"] != 1 {
		t.Fatalf("discovery must be cached per environment: %v", calls)
	}
	ioa := loadAssistantModels("ioa", "")
	if ioa.Default != "default" || assistantAllowedModel("claude-sonnet-5", ioa) != "claude-sonnet-5" || assistantAllowedModel("gpt-5.5", ioa) != "default" || assistantAllowedModel("claude-opus-5.5", ioa) != "default" {
		t.Fatalf("ioa catalog: %+v", ioa)
	}
	for range 2 {
		refreshCLIModels(context.Background(), "ioa", "user-key")
	}
	if calls["ioa|user-key"] != 1 {
		t.Fatalf("keyed discovery must be cached per account: %v", calls)
	}
	if account := loadAssistantModels("ioa", "user-key"); !assistantCatalogLists(account, "claude-opus-5.5") {
		t.Fatalf("account models must be listed: %+v", account)
	}
	if public := loadAssistantModels("ioa", ""); assistantCatalogLists(public, "claude-opus-5.5") {
		t.Fatalf("account models appeared in the public catalog: %+v", public)
	}
	if intl := loadAssistantModels("", ""); assistantAllowedModel("gpt-5.5", intl) != "gpt-5.5" {
		t.Fatalf("international catalog: %+v", intl)
	}
}
