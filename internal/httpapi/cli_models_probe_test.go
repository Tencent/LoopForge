package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestProbeCLIModelsUsesOfficialEndpointWithoutBaseURL(t *testing.T) {
	h, owner := workbenchFixture(t)
	original := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = original })
	handlers := &usersHandlers{deps: h.deps}
	for _, check := range []struct {
		engine, url string
		anthropic   bool
	}{
		{"codex", "https://api.openai.com/v1", false},
		{"claude-code", "https://api.anthropic.com", true},
		{"kimi", "https://api.kimi.com/coding/v1", false},
		{"dsh", "https://api.deepseek.com", false},
		{"zcode", "https://open.bigmodel.cn/api/coding/paas/v4", false},
	} {
		t.Run(check.engine, func(t *testing.T) {
			called := false
			assistantEngineDiscover = func(_ context.Context, anthropic bool, key, endpoint string) ([]assistantDiscoveredModel, error) {
				called = true
				if anthropic != check.anthropic || endpoint != check.url || key != "fixture-key" {
					t.Fatalf("unexpected probe: Anthropic=%v endpoint=%s", anthropic, endpoint)
				}
				return []assistantDiscoveredModel{{ID: "official-model"}}, nil
			}
			input := bytes.NewBufferString(`{"engine":"` + check.engine + `","apiKey":"fixture-key","baseUrl":""}`)
			c, w := workbenchContext(owner, http.MethodPost, "/", input)
			c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(owner.ID, 10)}}
			handlers.probeCLIModels(c)
			if !called || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"source":"official"`) ||
				!strings.Contains(w.Body.String(), `"id":"official-model"`) || !strings.Contains(w.Body.String(), `"connected":true`) {
				t.Fatalf("result %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProbeCodeBuddyIOAModelsReportsMissingCLI(t *testing.T) {
	h, owner := workbenchFixture(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", "")
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())
	original := assistantCLIGlobalDirs
	assistantCLIGlobalDirs = []string{bin}
	t.Cleanup(func() { assistantCLIGlobalDirs = original })

	payload := bytes.NewBufferString(`{"engine":"codebuddy","environment":"ioa"}`)
	c, w := workbenchContext(owner, http.MethodPost, "/", payload)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(owner.ID, 10)}}
	(&usersHandlers{deps: h.deps}).probeCLIModels(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "没有安装") {
		t.Fatalf("missing CodeBuddy CLI: %d %s", w.Code, w.Body.String())
	}
}

func TestDiscoverGeminiModelsKeepsGenerativeIDs(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != "fixture-key" {
			http.Error(w, "unexpected model-list request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"models/embedding","supportedGenerationMethods":["embedContent"]},
			{"name":"models/gemini-fixture","displayName":"Gemini Fixture","supportedGenerationMethods":["generateContent"]}
		]}`)
	}))
	t.Cleanup(service.Close)
	models, err := discoverGeminiModels(context.Background(), "fixture-key", service.URL+"/v1beta")
	if err != nil || !reflect.DeepEqual(models, []assistantDiscoveredModel{{ID: "gemini-fixture", Name: "Gemini Fixture"}}) {
		t.Fatalf("models=%+v error=%v", models, err)
	}
}

func TestAntigravityCatalogFollowsCredentialSource(t *testing.T) {
	ctx := context.Background()
	user := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		"agy": {APIKey: "fixture-key", Protocol: domain.AgentCLIProtocolGemini},
	}}
	builtin, _ := assistantEngineModels(ctx, user, nil)
	for id, efforts := range map[string][]string{
		"gemini-4-neon-high":     {"low", "medium", "high", "xhigh"},
		"gemini-4-carbon-medium": {"low", "medium", "high"},
		"gemini-4-argon-medium":  {"low", "medium", "high"},
		"gemini-4-hydrogen-high": {"low", "medium", "high", "xhigh"},
	} {
		var found []string
		for _, model := range builtin {
			if model.ID == "agy:"+id {
				found = model.Efforts
				break
			}
		}
		if !reflect.DeepEqual(found, efforts) {
			t.Errorf("built-in %s efforts = %v, want %v", id, found, efforts)
		}
	}

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1beta/models" && r.Header.Get("x-goog-api-key") == "fixture-key":
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-gateway","displayName":"Gateway Gemini","supportedGenerationMethods":["generateContent"]}]}`)
		case (r.URL.Path == "/models" || r.URL.Path == "/v1/models") && r.Header.Get("Authorization") == "Bearer fixture-key":
			_, _ = io.WriteString(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected Gemini catalog request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(service.Close)
	cred := user.AgentCLICredentials["agy"]
	cred.BaseURL = service.URL
	user.AgentCLICredentials["agy"] = cred
	custom, _ := assistantEngineModels(ctx, user, nil)
	if len(custom) != 1 || custom[0].ID != "agy:gemini-gateway" {
		t.Fatalf("custom endpoint models = %+v", custom)
	}
	cred.Protocol = domain.AgentCLIProtocolChat
	user.AgentCLICredentials["agy"] = cred
	if fallback, _ := assistantEngineModels(ctx, user, nil); len(fallback) != 0 {
		t.Fatalf("an unavailable gateway offered native models: %+v", fallback)
	}
}

func TestCursorModelFamiliesFoldGemini4(t *testing.T) {
	output := `
gemini-4-neon-low - Gemini 4 Neon Low
gemini-4-neon-medium - Gemini 4 Neon Medium
gemini-4-neon-high - Gemini 4 Neon High
gemini-4-neon-xhigh - Gemini 4 Neon Extra High
gemini-4-carbon-low - Gemini 4 Carbon Low
gemini-4-carbon-medium - Gemini 4 Carbon Medium
gemini-4-carbon-high - Gemini 4 Carbon High
gemini-4-hydrogen-low - Gemini 4 Hydrogen Low
gemini-4-hydrogen-medium - Gemini 4 Hydrogen Medium
gemini-4-hydrogen-high - Gemini 4 Hydrogen High
gemini-4-hydrogen-xhigh - Gemini 4 Hydrogen Extra High
gemini-4-argon-low - Gemini 4 Argon Low
gemini-4-argon-medium - Gemini 4 Argon Medium (default)
gemini-4-argon-high - Gemini 4 Argon High
`
	got := cursorModelFamilies(parseCursorModels(output))
	want := []domain.CLIModel{
		{ID: "gemini-4-neon", Name: "Gemini 4 Neon"},
		{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon"},
		{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		{ID: "gemini-4-argon", Name: "Gemini 4 Argon"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families:\n got %v\nwant %v", got, want)
	}
}
