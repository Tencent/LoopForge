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
				!strings.Contains(w.Body.String(), `"id":"official-model"`) {
				t.Fatalf("result %d %s", w.Code, w.Body.String())
			}
		})
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

func TestCursorModelFamiliesFoldGemini4(t *testing.T) {
	output := `
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
		{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon"},
		{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		{ID: "gemini-4-argon", Name: "Gemini 4 Argon"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families:\n got %v\nwant %v", got, want)
	}
}
