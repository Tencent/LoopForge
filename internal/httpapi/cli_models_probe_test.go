package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestProbeOfficialCodeBuddyModelsReportsSpecificResults(t *testing.T) {
	for _, tt := range []struct {
		name, body, wantError, wantModel string
		deadline                         time.Duration
	}{
		{
			name:      "wrapped help heading",
			body:      `printf 'Currently\n supported :\n (fixture-model, echo, default-model, --invalid)\n'`,
			wantModel: "fixture-model",
		},
		{
			name:      "unrecognized output",
			body:      `printf 'Ordinary CLI help\n'`,
			wantError: "无法解析 CodeBuddy CLI 的模型列表",
		},
		{
			name:      "no selectable models",
			body:      `printf 'Currently supported: (default, echo, default-model)\n'`,
			wantError: "没有列出可添加的模型",
		},
		{
			name:      "unsuccessful process",
			body:      `printf '%s\n' "$CODEBUDDY_API_KEY" >&2; exit 17`,
			wantError: "退出码 17",
		},
		{
			name:      "rejected api key",
			body:      `printf 'HTTP 401: invalid api key %s\n' "$CODEBUDDY_API_KEY" >&2; exit 1`,
			wantError: "拒绝了你的 API Key",
		},
		{
			name:      "timed out",
			body:      `exec /bin/sleep 5`,
			wantError: "获取 CodeBuddy 模型列表超时",
			deadline:  50 * time.Millisecond,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installCodeBuddyProbeFixture(t, "[ \"$1\" = --help ] || exit 91\n"+tt.body)
			ctx := context.Background()
			if tt.deadline > 0 {
				withDeadline, cancel := context.WithTimeout(ctx, tt.deadline)
				defer cancel()
				ctx = withDeadline
			}
			result := probeOfficialCodeBuddyModels(ctx, "internal", "test-only-catalog-key")
			if result.Connected || strings.Contains(result.Error, "test-only-catalog-key") {
				t.Fatalf("catalog probe returned an unsafe result: %+v", result)
			}
			if tt.wantError != "" {
				if !strings.Contains(result.Error, tt.wantError) || len(result.Models) != 0 {
					t.Fatalf("want %q with no models, got %+v", tt.wantError, result)
				}
			} else if result.Error != "" || result.Source != "official" || len(result.Models) != 1 || result.Models[0].ID != tt.wantModel {
				t.Fatalf("want model %q, got %+v", tt.wantModel, result)
			}
		})
	}
}

func TestProbeOfficialCodeBuddyConnectionUsesFormOrSavedCredentials(t *testing.T) {
	installCodeBuddyProbeFixture(t, `
model=
while [ "$#" -gt 0 ]; do
    if [ "$1" = --model ]; then
        model=$2
        shift
    fi
    shift
done
case "$CODEBUDDY_API_KEY:$CODEBUDDY_INTERNET_ENVIRONMENT:$model" in
    saved-fixture:internal:deepseek-v4.1-flash|draft-fixture:ioa:manual-model)
        printf '%s\n' '{"type":"result","subtype":"success","result":"OK"}' ;;
    rejected-fixture:ioa:manual-model)
        printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"401 Authentication required"}]}}' '{"type":"result","subtype":"success","result":""}' ;;
    *) exit 91 ;;
esac
`)
	f := cvBNewHTTPFixture(t)
	path := "/api/users/" + strconv.FormatInt(f.user.ID, 10)
	saved := cvBRequest(t, f, http.MethodPut, path+"/credentials", map[string]string{
		"codeBuddyApiKey": "saved-fixture", "codeBuddyInternetEnvironment": "internal",
	}, true)
	if saved.Code != http.StatusOK {
		t.Fatalf("saving test credentials: %d", saved.Code)
	}
	for _, tt := range []struct {
		name, key, model, wantError string
	}{
		{name: "saved settings and default test model"},
		{name: "draft settings and manually configured model", key: "draft-fixture", model: "manual-model"},
		{name: "authentication failed even with a clean exit", key: "rejected-fixture", model: "manual-model", wantError: "拒绝了你的 API Key"},
		{name: "invalid model", key: "draft-fixture", model: "--model", wantError: "模型 ID 无效"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]any{"engine": "codebuddy", "connectOnly": true, "model": tt.model}
			if tt.key != "" {
				body["apiKey"], body["environment"] = tt.key, "ioa"
			}
			response := cvBRequest(t, f, http.MethodPost, path+"/cli-models/probe", body, true)
			var result cliModelProbeResult
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
				t.Fatalf("connection response: %d %s", response.Code, response.Body)
			}
			if tt.wantError == "" {
				if !result.Connected || result.Error != "" || result.Source != "official" || len(result.Models) != 0 {
					t.Fatalf("expected a live connection result, got %+v", result)
				}
			} else if result.Connected || !strings.Contains(result.Error, tt.wantError) {
				t.Fatalf("want %q with no connection, got %+v", tt.wantError, result)
			}
		})
	}
}

func installCodeBuddyProbeFixture(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codebuddy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
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
