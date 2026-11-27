package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// One shared credential per CLI: CodeBuddy keeps its network environment,
// Codex and Claude Code take a Base URL, and the admin reads them together.
func TestPlatformAdminsConfigureSharedKeysPerCLI(t *testing.T) {
	st, client, owner, member := platformSettingsFixture(t)
	stubCLIModelDiscovery(t)
	const origin = "http://localhost"

	if w := client.call(owner, "GET", "/api/platform/model-keys/kimi", "", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "CodeBuddy") {
		t.Fatalf("unsupported CLI: %d %s", w.Code, w.Body.String())
	}
	if w := client.call(member, "GET", "/api/platform/model-keys/codex", "", ""); w.Code != 403 {
		t.Fatalf("member read: %d", w.Code)
	}
	if w := client.call(member, "PUT", "/api/platform/model-keys/codex", origin, `{"apiKey":"sk","models":["m"]}`); w.Code != 403 {
		t.Fatalf("member save: %d", w.Code)
	}
	for body, want := range map[string]string{
		`{"apiKey":"sk","baseUrl":"ftp://gateway.example.com","models":["gpt-6.1-terra"]}`: "Base URL",
		`{"apiKey":"sk","baseUrl":"https://gateway.example.com/v1","models":[]}`:           "至少选择",
		`{"apiKey":"bad key","models":["gpt-6.1-terra"]}`:                                  "API Key",
		`{"internetEnvironment":"internal","models":["gpt-6.1-terra"]}`:                    "API Key",
	} {
		if w := client.call(owner, "PUT", "/api/platform/model-keys/codex", origin, body); w.Code != 400 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("invalid %s: %d %s", body, w.Code, w.Body.String())
		}
	}

	save := `{"apiKey":"sk_gateway","baseUrl":"https://gateway.example.com/v1","models":["gpt-6.1-terra"," gpt-6.1-sol ","gpt-6.1-terra"]}`
	if w := client.call(owner, "PUT", "/api/platform/model-keys/codex", origin, save); w.Code != 200 || strings.Contains(w.Body.String(), "sk_gateway") {
		t.Fatalf("save codex: %d %s", w.Code, w.Body.String())
	}
	key, ok := st.SharedModelKey(domain.SharedModelProviderCodex)
	if !ok || key.APIKey != "sk_gateway" || key.BaseURL != "https://gateway.example.com/v1" || strings.Join(key.Models, ",") != "gpt-6.1-terra,gpt-6.1-sol" || key.InternetEnvironment != "" {
		t.Fatalf("stored codex key = %+v", key)
	}

	// Editing only the models keeps the saved key, and a network environment
	// is ignored for CLIs that address their gateway by Base URL.
	if w := client.call(owner, "PUT", "/api/platform/model-keys/codex", origin, `{"baseUrl":"https://gateway.example.com/v1","internetEnvironment":"internal","models":["gpt-6.1-sol"]}`); w.Code != 200 {
		t.Fatalf("models-only update: %d %s", w.Code, w.Body.String())
	}
	if key, _ := st.SharedModelKey(domain.SharedModelProviderCodex); key.APIKey != "sk_gateway" || key.InternetEnvironment != "" || strings.Join(key.Models, ",") != "gpt-6.1-sol" {
		t.Fatalf("models-only update = %+v", key)
	}

	// CodeBuddy keeps its environment and its older endpoint.
	if w := client.call(owner, "PUT", "/api/platform/codebuddy-key", origin, `{"apiKey":"ck_shared","internetEnvironment":"internal","models":["glm-5.1"]}`); w.Code != 200 {
		t.Fatalf("legacy codebuddy endpoint: %d %s", w.Code, w.Body.String())
	}
	if key, _ := st.SharedModelKey(domain.SharedModelProviderCodeBuddy); key.APIKey != "ck_shared" || key.InternetEnvironment != "internal" {
		t.Fatalf("codebuddy key = %+v", key)
	}

	w := client.call(owner, "GET", "/api/platform/model-keys", "", "")
	var list struct {
		Keys []repo.PlatformModelKeyStatus `json:"keys"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Keys) != len(domain.SharedModelProviders) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	byProvider := map[string]repo.PlatformModelKeyStatus{}
	for _, status := range list.Keys {
		byProvider[status.Provider] = status
	}
	if !byProvider["codebuddy"].Configured || byProvider["codebuddy"].InternetEnvironment != "internal" {
		t.Fatalf("codebuddy status = %+v", byProvider["codebuddy"])
	}
	if !byProvider["codex"].Configured || byProvider["codex"].BaseURL != "https://gateway.example.com/v1" {
		t.Fatalf("codex status = %+v", byProvider["codex"])
	}
	if byProvider["claude-code"].Configured {
		t.Fatalf("claude-code must stay unconfigured: %+v", byProvider["claude-code"])
	}
	if strings.Contains(w.Body.String(), "sk_gateway") || strings.Contains(w.Body.String(), "ck_shared") {
		t.Fatal("status list must not reveal keys")
	}

	if w := client.call(member, "POST", "/api/platform/model-keys/codex/probe", origin, `{}`); w.Code != 403 {
		t.Fatalf("member probe: %d", w.Code)
	}
	if w := client.call(owner, "GET", "/api/platform/model-keys/claude-code", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "claude-opus-5") {
		t.Fatalf("claude-code catalog: %d %s", w.Code, w.Body.String())
	}

	if w := client.call(owner, "DELETE", "/api/platform/model-keys/codex", origin, ""); w.Code != 200 || strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("clear codex: %d %s", w.Code, w.Body.String())
	}
	if _, ok := st.SharedModelKey(domain.SharedModelProviderCodex); ok {
		t.Fatal("cleared codex key must not be lent")
	}
	if _, ok := st.SharedModelKey(domain.SharedModelProviderCodeBuddy); !ok {
		t.Fatal("clearing one CLI must keep the others")
	}
}

// A member without their own Codex key chats on the shared one, limited to the
// models the administrator allowed, and the engine switch reports it.
func TestSharedCodexKeyServesChatWithoutPersonalKey(t *testing.T) {
	st, newcomer := sponsorFixture(t)
	stubCLIModelDiscovery(t)
	if err := st.SetPlatformModelKey(repo.PlatformModelKeyUpdate{
		Provider: domain.SharedModelProviderCodex, APIKey: "codex-shared",
		BaseURL: "https://gateway.example.com/v1", Models: []string{"gpt-6.1-terra"},
	}, 0); err != nil {
		t.Fatal(err)
	}
	var used domain.User
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(_ context.Context, _ string, u domain.User) (string, error) {
		used = u
		return `{"message":"ok","drafts":[]}`, nil
	}}

	rec := sponsoredChat(h, newcomer, "codex:gpt-6.1-terra")
	if rec.Code != 200 {
		t.Fatalf("shared codex turn: %d %s", rec.Code, rec.Body.String())
	}
	if used.ID != newcomer.ID || used.CodexAPIKey != "codex-shared" || used.CodexBaseURL != "https://gateway.example.com/v1" || used.GitToken != "newcomer-git" {
		t.Fatalf("turn must run as the newcomer with the shared Codex key only: %+v", used)
	}
	if stored, _ := st.User(newcomer.ID); stored.CodexAPIKey != "" {
		t.Fatal("the shared key must never be persisted on the member")
	}

	rec = sponsoredChat(h, newcomer, "codex:gpt-6.1-sol")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "允许的模型") || !strings.Contains(rec.Body.String(), "Codex") {
		t.Fatalf("other Codex models need an own key: %d %s", rec.Code, rec.Body.String())
	}

	mrec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(mrec)
	c.Request = httptest.NewRequest("GET", "/api/assistant/models", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(newcomer)))
	h.models(c)
	var catalog assistantModelCatalog
	if mrec.Code != 200 || json.Unmarshal(mrec.Body.Bytes(), &catalog) != nil {
		t.Fatalf("catalog: %d %s", mrec.Code, mrec.Body.String())
	}
	var codex *assistantEngineOption
	for i := range catalog.Engines {
		if catalog.Engines[i].ID == "codex" {
			codex = &catalog.Engines[i]
		}
	}
	if codex == nil || !codex.Configured || codex.Personal || !codex.Shared {
		t.Fatalf("the switch must offer Codex through the shared key: %+v", catalog.Engines)
	}
	listed := map[string]bool{}
	for _, model := range catalog.Models {
		listed[model.ID] = true
	}
	if !listed["codex:gpt-6.1-terra"] || listed["codex:gpt-6.1-sol"] {
		t.Fatalf("only the allowed Codex models may be offered: %v", listed)
	}
	if strings.Contains(mrec.Body.String(), "codex-shared") {
		t.Fatal("the catalog must not reveal the shared key")
	}
}

// The shared-key form can list the models a gateway publishes before saving,
// using the typed key and Base URL without storing them.
func TestPlatformSharedKeyProbeListsGatewayModels(t *testing.T) {
	st, client, owner, _ := platformSettingsFixture(t)
	stubCLIModelDiscovery(t)
	previous := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = previous })
	var asked []string
	assistantEngineDiscover = func(_ context.Context, anthropic bool, apiKey, baseURL string) ([]assistantDiscoveredModel, error) {
		asked = append(asked, apiKey+"@"+baseURL)
		if anthropic {
			return nil, nil
		}
		return []assistantDiscoveredModel{{ID: "gpt-6.1-terra", Name: "GPT-6.1-Terra"}}, nil
	}
	const origin = "http://localhost"
	w := client.call(owner, "POST", "/api/platform/model-keys/codex/probe", origin, `{"apiKey":"sk_probe","baseUrl":"https://gateway.example.com/v1"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "gpt-6.1-terra") {
		t.Fatalf("probe: %d %s", w.Code, w.Body.String())
	}
	if len(asked) != 1 || asked[0] != "sk_probe@https://gateway.example.com/v1" {
		t.Fatalf("probe used %v", asked)
	}

	// The stored key is write-only: probing or saving it against another
	// Base URL needs it typed again, or the new URL's owner would receive it.
	save := `{"apiKey":"sk_stored","baseUrl":"https://gateway.example.com/v1","models":["gpt-6.1-terra"]}`
	if w := client.call(owner, "PUT", "/api/platform/model-keys/codex", origin, save); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	asked = nil
	for _, body := range []string{`{"baseUrl":"https://gateway.example.com/v1"}`, `{"baseUrl":"https://gateway.example.com/v1/"}`, `{}`} {
		if w := client.call(owner, "POST", "/api/platform/model-keys/codex/probe", origin, body); w.Code != 200 || !strings.Contains(w.Body.String(), "gpt-6.1-terra") {
			t.Fatalf("probe stored key %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(asked) != 3 || asked[0] != "sk_stored@https://gateway.example.com/v1" || asked[2] != "sk_stored@https://gateway.example.com/v1" {
		t.Fatalf("stored-key probes used %v", asked)
	}
	asked = nil
	if w := client.call(owner, "POST", "/api/platform/model-keys/codex/probe", origin, `{"baseUrl":"https://collector.example.net/v1"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "重新填写") {
		t.Fatalf("stored key to another Base URL: %d %s", w.Code, w.Body.String())
	}
	if len(asked) != 0 {
		t.Fatalf("the stored key must not reach another Base URL: %v", asked)
	}
	if w := client.call(owner, "POST", "/api/platform/model-keys/codex/probe", origin, `{"apiKey":"sk_new","baseUrl":"https://collector.example.net/v1"}`); w.Code != 200 || len(asked) != 1 || asked[0] != "sk_new@https://collector.example.net/v1" {
		t.Fatalf("a typed key may probe any Base URL: %d %s %v", w.Code, w.Body.String(), asked)
	}
	for _, body := range []string{
		`{"baseUrl":"https://collector.example.net/v1","models":["gpt-6.1-terra"]}`,
		`{"models":["gpt-6.1-terra"]}`,
	} {
		if w := client.call(owner, "PUT", "/api/platform/model-keys/codex", origin, body); w.Code != 400 || !strings.Contains(w.Body.String(), "重新填写") {
			t.Fatalf("move stored key %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if key, _ := st.SharedModelKey(domain.SharedModelProviderCodex); key.APIKey != "sk_stored" || key.BaseURL != "https://gateway.example.com/v1" {
		t.Fatalf("rejected moves must keep the stored key: %+v", key)
	}
	if w := client.call(owner, "PUT", "/api/platform/model-keys/codex", origin, `{"apiKey":"sk_moved","baseUrl":"https://collector.example.net/v1","models":["gpt-6.1-terra"]}`); w.Code != 200 {
		t.Fatalf("move with a typed key: %d %s", w.Code, w.Body.String())
	}

	if w := client.call(owner, "POST", "/api/platform/model-keys/codex/probe", origin, `{"baseUrl":"not-a-url"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "Base URL") {
		t.Fatalf("invalid base URL: %d %s", w.Code, w.Body.String())
	}
	if w := client.call(owner, "POST", "/api/platform/model-keys/codebuddy/probe", origin, `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "网络环境") {
		t.Fatalf("codebuddy probe: %d %s", w.Code, w.Body.String())
	}
}
