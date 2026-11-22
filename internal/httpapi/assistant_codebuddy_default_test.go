package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestCodeBuddyBaseURLSettingsAndCatalog(t *testing.T) {
	const key = "ck_fixture_gateway"
	const model = "fixture-flash"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/gateway/v1/models" || r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "unexpected model discovery request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"`+model+`"}]}`)
	}))
	t.Cleanup(gateway.Close)
	f := cvBNewHTTPFixture(t)
	baseURL := gateway.URL + "/gateway/v1"
	userPath := "/api/users/" + strconv.FormatInt(f.user.ID, 10)
	set := func(fields map[string]any, want int) domain.User {
		t.Helper()
		r := cvBRequest(t, f, http.MethodPut, userPath+"/credentials", fields, true)
		if r.Code != want || strings.Contains(r.Body.String(), key) {
			t.Fatalf("credential update returned %d, want %d with redacted data", r.Code, want)
		}
		var public domain.User
		if want == http.StatusOK {
			if err := json.Unmarshal(r.Body.Bytes(), &public); err != nil {
				t.Fatal(err)
			}
		}
		return public
	}
	set(map[string]any{"codeBuddyApiKey": key, "codeBuddyInternetEnvironment": "ioa"}, http.StatusOK)
	set(map[string]any{"codeBuddyBaseUrl": "https://user:pass@gateway.example/v1"}, http.StatusBadRequest)
	if saved := set(map[string]any{"codeBuddyBaseUrl": " " + baseURL + " "}, http.StatusOK); saved.CodeBuddyBaseURL != baseURL || !saved.CodeBuddyAPIKeyConfigured || saved.CodeBuddyInternetEnvironment != "ioa" {
		t.Fatal("updating the URL did not preserve the key and edition")
	}
	response := cvBRequest(t, f, http.MethodGet, "/api/assistant/models", nil, true)
	var catalog assistantModelCatalog
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil || catalog.Default != model || !assistantCatalogLists(catalog, model) {
		t.Fatalf("gateway model not offered: %s", response.Body.String())
	}
	probe := cvBRequest(t, f, http.MethodPost, userPath+"/cli-models/probe", map[string]string{"engine": "codebuddy"}, true)
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"id":"`+model+`"`) {
		t.Fatalf("gateway probe returned %d: %s", probe.Code, probe.Body.String())
	}
	if saved := set(map[string]any{"codeBuddyBaseUrl": ""}, http.StatusOK); saved.CodeBuddyBaseURL != "" || !saved.CodeBuddyAPIKeyConfigured {
		t.Fatal("clearing the custom URL must preserve authentication")
	}
	set(map[string]any{"codeBuddyBaseUrl": baseURL}, http.StatusOK)
	if saved := set(map[string]any{"codeBuddyApiKeyClear": true}, http.StatusOK); saved.CodeBuddyBaseURL != "" || saved.CodeBuddyAPIKeyConfigured {
		t.Fatal("deleting the credential must remove the gateway")
	}
}

// A request without a model runs on the default the picker shows: the first
// saved personal CodeBuddy model. Environment models stay accepted.
func TestCodeBuddyPersonalModelsDefaultMatchesPicker(t *testing.T) {
	user := domain.User{CodeBuddyAPIKey: "test-only", CodeBuddyModels: []domain.CLIModel{{ID: "personal-a"}, {ID: "personal-b", Name: "B"}}}
	base := assistantModelCatalog{Default: "env-default", Models: []assistantModel{{ID: "env-default"}, {ID: "env-other"}}}

	ctx := context.Background()
	catalog := withCodeBuddyPersonalModels(ctx, base, user)
	if catalog.Default != "personal-a" {
		t.Fatalf("default = %q, want the first saved model", catalog.Default)
	}
	for _, id := range []string{"env-default", "env-other", "personal-a", "personal-b"} {
		if got := assistantAllowedUserModel(id, catalog, user); got != id {
			t.Fatalf("model %s not accepted: %q", id, got)
		}
	}
	if got := assistantAllowedUserModel("", catalog, user); got != "personal-a" {
		t.Fatalf("empty request runs on %q, want personal-a", got)
	}

	// Without saved models (or without a personal key) nothing changes.
	if got := withCodeBuddyPersonalModels(ctx, base, domain.User{CodeBuddyAPIKey: "test-only"}); got.Default != "env-default" || len(got.Models) != 2 {
		t.Fatalf("catalog without saved models changed: %+v", got)
	}
	if got := withCodeBuddyPersonalModels(ctx, base, domain.User{CodeBuddyModels: user.CodeBuddyModels}); got.Default != "env-default" {
		t.Fatalf("saved models without a key changed the default: %+v", got)
	}

	if model, err := scheduledRunModel(context.Background(), "", user); err != nil || model != "personal-a" {
		t.Fatalf("scheduled run without a model = %q, %v; want personal-a", model, err)
	}
	if model, err := scheduledRunModel(context.Background(), "personal-b", user); err != nil || model != "personal-b" {
		t.Fatalf("scheduled run with a saved model = %q, %v", model, err)
	}
}
