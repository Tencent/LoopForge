package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDeepSeekPersonalModelCatalog(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	path := fmt.Sprintf("/api/users/%d", f.user.ID)
	probe := func() cliModelProbeResult {
		t.Helper()
		rec := cvBRequest(t, f, http.MethodPost, path+"/cli-models/probe", map[string]string{"engine": "deepseek"}, true)
		var result cliModelProbeResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.Error != "" {
			t.Fatalf("model probe: status %d, response %s", rec.Code, rec.Body.String())
		}
		return result
	}
	official := probe()
	if official.Source != "builtin" || !slices.ContainsFunc(official.Models, func(model domain.CLIModel) bool {
		return model.ID == domain.DeepSeekDefaultModel
	}) {
		t.Fatalf("official model catalog = %+v", official)
	}

	const key = "deepseek-catalog-test-key"
	const gateway = "https://deepseek.example/v1"
	personal := domain.CLIModel{ID: "deepseek-custom", Name: "Personal DeepSeek"}
	rec := cvBRequest(t, f, http.MethodPut, path+"/credentials", map[string]any{
		"agentCliCredentials": map[string]any{
			"deepseek": map[string]any{"apiKey": key, "baseUrl": gateway, "models": []domain.CLIModel{personal}},
		},
	}, true)
	var saved domain.User
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &saved) != nil {
		t.Fatalf("save model catalog: status %d, response %s", rec.Code, rec.Body.String())
	}
	config := saved.AgentCLICredentials["deepseek"]
	if !config.APIKeyConfigured || strings.Contains(rec.Body.String(), key) || !slices.Equal(config.Models, []domain.CLIModel{personal}) {
		t.Fatal("personal model catalog must survive the save and the key must be redacted")
	}
	for _, engine := range assistantEngineOptions(saved, false) {
		if engine.ID == "deepseek" {
			t.Fatal("DeepSeek is only available as an SOP role executor")
		}
	}

	discover := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = discover })
	assistantEngineDiscover = func(_ context.Context, anthropic bool, apiKey, baseURL string) ([]assistantDiscoveredModel, error) {
		if anthropic || apiKey != key || baseURL != gateway {
			t.Fatal("model discovery did not use the stored DeepSeek gateway credential")
		}
		return []assistantDiscoveredModel{{ID: personal.ID, Name: personal.Name}}, nil
	}
	remote := probe()
	if remote.Source != "remote" || !slices.Equal(remote.Models, []domain.CLIModel{personal}) {
		t.Fatalf("gateway model catalog = %+v", remote)
	}
}
