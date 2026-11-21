package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

type cliModelProbeResult struct {
	Models []domain.CLIModel `json:"models"`
	// Source identifies the configured gateway, official provider, Runner or built-in catalog.
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

const assistantAnthropicAPIBase = "https://api.anthropic.com"

// probeCLIModels backs 「获取可用模型」 for a personal model catalog.
// The form's unsaved API Key, Base URL and protocol win over the stored ones
// so a user can probe before saving; nothing is stored. Like
// verifyGitToken it answers 200 with a structured result.
func (h *usersHandlers) probeCLIModels(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, cliModelProbeResult{Error: "只能使用当前登录用户的凭据获取模型"})
		return
	}
	var req struct {
		Engine string `json:"engine"`
		APIKey string `json:"apiKey"`
		// BaseURL is the form's value; null falls back to the stored one,
		// while "" means the official endpoint.
		BaseURL *string `json:"baseUrl"`
		// Protocol is the agent CLI's form value; null keeps the stored one.
		Protocol    *string `json:"protocol"`
		Environment *string `json:"environment"`
	}
	if !decode(c, &req) {
		return
	}
	engine := strings.TrimSpace(req.Engine)
	if engine != assistantEngineCodeBuddy && !slices.Contains(assistantCLIEngines(), engine) {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "不支持的 CLI：" + engine})
		return
	}
	if req.BaseURL != nil && !validCLIBaseURL(*req.BaseURL) {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: errInvalidCLIBaseURL})
		return
	}
	apiKey, baseURL, protocol, environment := strings.TrimSpace(req.APIKey), "", "", ""
	if h.repo != nil {
		if stored, found := h.repo.User(id); found {
			storedKey, storedURL := assistantEngineCredential(stored, engine)
			if apiKey == "" {
				apiKey = storedKey
			}
			baseURL, protocol = storedURL, assistantEngineProtocol(stored, engine)
			environment = stored.CodeBuddyInternetEnvironment
		}
	}
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
	}
	configuredBaseURL := baseURL
	if req.Protocol != nil && runtime.IsAgentCLI(engine) {
		protocol = runtime.AgentCLIProtocol(engine, *req.Protocol)
	}
	c.Header("Cache-Control", "private, no-store")
	if engine == assistantEngineCodeBuddy && req.Environment != nil {
		var ok bool
		environment, ok = domain.NormalizeCodeBuddyInternetEnvironment(*req.Environment)
		if !ok {
			c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "请选择有效的 CodeBuddy 网络环境"})
			return
		}
	}
	if engine == assistantEngineCodeBuddy && baseURL == "" {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		var models []domain.CLIModel
		for _, id := range discoverCLIModels(ctx, environment, apiKey) {
			if !codeBuddyIsDefaultModel(id) && assistantValidEngineModel(id) {
				models = append(models, domain.CLIModel{ID: id})
			}
		}
		if len(models) == 0 {
			if _, err := resolveAssistantEngineBin(engine); errors.Is(err, errAssistantCLIMissing) {
				c.JSON(http.StatusOK, cliModelProbeResult{Error: err.Error()})
				return
			}
			c.JSON(http.StatusOK, cliModelProbeResult{Error: "CodeBuddy CLI 没有返回模型，请检查 CLI 和个人凭据"})
			return
		}
		source := "official"
		if apiKey == "" {
			source = "builtin"
		}
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(engine, models, ""), Source: source})
		return
	}
	if engine == runtime.AgentCLICursor {
		h.probeCursorModels(c, apiKey)
		return
	}
	if baseURL == "" && apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(engine, cliModelsOf(assistantEngineBuiltinModels(engine)), ""), Source: "builtin"})
		return
	}
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(engine) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	source := "remote"
	anthropic := assistantEngineAnthropicModels(engine, protocol)
	if baseURL == "" {
		source = "official"
		switch engine {
		case assistantEngineCodex:
			baseURL = "https://api.openai.com/v1"
		case assistantEngineClaude:
			baseURL = assistantAnthropicAPIBase
		case runtime.AgentCLIKimi:
			baseURL, anthropic = "https://api.kimi.com/coding/v1", false
		case runtime.AgentCLIDeepSeek:
			baseURL, anthropic = "https://api.deepseek.com", false
		case runtime.AgentCLIZCode:
			baseURL, anthropic = "https://open.bigmodel.cn/api/coding/paas/v4", false
		}
	}
	var models []assistantDiscoveredModel
	if engine == runtime.AgentCLIAntigravity && protocol == runtime.AgentCLIProtocolGemini {
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com"
		}
		models, err = discoverGeminiModels(ctx, apiKey, baseURL)
	} else if baseURL != "" {
		models, err = assistantEngineDiscover(ctx, anthropic, apiKey, baseURL)
	} else {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先在个人配置中填写模型服务的 Base URL"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "服务没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(engine, cliModelsOf(models), configuredBaseURL), Source: source})
}

func discoverGeminiModels(ctx context.Context, key, baseURL string) ([]assistantDiscoveredModel, error) {
	baseURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1beta")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1beta/models?pageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", key)
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini 模型接口返回 HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	var models []assistantDiscoveredModel
	for _, item := range payload.Models {
		id := strings.TrimPrefix(item.Name, "models/")
		if assistantValidEngineModel(id) && slices.Contains(item.Methods, "generateContent") {
			models = append(models, assistantDiscoveredModel{ID: id, Name: item.DisplayName})
			if len(models) == assistantDiscoveryMaxModels {
				break
			}
		}
	}
	return models, nil
}

// probeCursorModels asks cursor-agent itself: Cursor has no Base URL, and
// the models a key may use depend on its account plan.
func (h *usersHandlers) probeCursorModels(c *gin.Context, apiKey string) {
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(runtime.AgentCLICursor) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	models, err := cursorAgentModels(ctx, apiKey)
	if errors.Is(err, errAssistantCLIMissing) {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLICursor, cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLICursor)), ""), Source: "builtin"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "Cursor CLI 没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsWithEfforts(runtime.AgentCLICursor, models, ""), Source: "official"})
}

var cursorModelLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*) - (.+)$`)

// cursorAgentModels runs `cursor-agent models` in a throwaway HOME with only
// the user's key, so no server token or stored CLI login is visible to it.
func cursorAgentModels(ctx context.Context, apiKey string) ([]domain.CLIModel, error) {
	bin, err := resolveAssistantEngineBin(runtime.AgentCLICursor)
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "agentflow-cursor-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	env := []string{"HOME=" + home, "CURSOR_API_KEY=" + apiKey, "NO_COLOR=1", "CI=1"}
	for _, key := range []string{"PATH", "LANG", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.Dir, cmd.Env = home, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("Cursor CLI 响应超时")
		}
		if strings.Contains(strings.ToLower(string(out)), "unauthorized") || strings.Contains(string(out), "401") {
			return nil, assistantCredentialError{engine: runtime.AgentCLICursor}
		}
		return nil, fmt.Errorf("cursor-agent models 退出：%v", err)
	}
	return cursorModelFamilies(parseCursorModels(string(out))), nil
}

// cursorModelFamilies folds the effort variants cursor-agent lists into one
// row per family. The built-in table wins; other IDs ending in a recognized
// effort suffix fold the same way.
func cursorModelFamilies(models []domain.CLIModel) []domain.CLIModel {
	return runtime.FoldCursorCLIModels(models)
}

func parseCursorModels(output string) []domain.CLIModel {
	models := []domain.CLIModel{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.Join(strings.Fields(strings.ReplaceAll(line, "\u200b", "")), " ")
		if match := cursorModelLine.FindStringSubmatch(line); match != nil {
			models = append(models, domain.CLIModel{ID: match[1], Name: strings.TrimSuffix(match[2], " (default)")})
		}
	}
	return models
}

func cliModelsOf(models []assistantDiscoveredModel) []domain.CLIModel {
	out := make([]domain.CLIModel, 0, len(models))
	for _, model := range models {
		out = append(out, domain.CLIModel{
			ID: model.ID, Name: model.Name, Efforts: model.Efforts, DefaultEffort: model.DefaultEffort,
			ReasoningEfforts: model.ReasoningEfforts, DiscoveredReasoningEfforts: model.DiscoveredReasoningEfforts,
		})
	}
	return out
}

// Standard model listings often contain only IDs. Supply the CLI's catalog
// levels when the endpoint omits them, and constrain advertised metadata to
// levels this CLI can actually pass to its provider.
func cliModelsWithEfforts(engine string, models []domain.CLIModel, baseURL string) []domain.CLIModel {
	for i := range models {
		id := models[i].ID
		if engine != assistantEngineCodeBuddy {
			id = engine + ":" + id
		}
		levels := append([]string{}, assistantModelEffortsForBaseURL(id, models, baseURL)...)
		models[i].DiscoveredReasoningEfforts = &levels
	}
	return models
}
