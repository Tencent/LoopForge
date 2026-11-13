package httpapi

import (
	"context"
	"errors"
	"fmt"
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
	// Source is "remote" for the Base URL's model list and "builtin" for the
	// executor catalog used when no Base URL is set.
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// probeCLIModels backs 「获取可用模型」 for the model catalog of a CLI other
// than CodeBuddy. The form's unsaved API Key, Base URL and protocol win over the stored ones
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
		Protocol *string `json:"protocol"`
	}
	if !decode(c, &req) {
		return
	}
	engine := strings.TrimSpace(req.Engine)
	if engine == assistantEngineCodeBuddy || !slices.Contains(assistantCLIEngines(), engine) {
		c.JSON(http.StatusBadRequest, cliModelProbeResult{Error: "不支持的 CLI：" + engine})
		return
	}
	if req.BaseURL != nil && !validCLIBaseURL(*req.BaseURL) {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: errInvalidCLIBaseURL})
		return
	}
	apiKey, baseURL, protocol := strings.TrimSpace(req.APIKey), "", ""
	if h.repo != nil {
		if stored, found := h.repo.User(id); found {
			storedKey, storedURL := assistantEngineCredential(stored, engine)
			if apiKey == "" {
				apiKey = storedKey
			}
			baseURL, protocol = storedURL, assistantEngineProtocol(stored, engine)
		}
	}
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Protocol != nil && runtime.IsAgentCLI(engine) {
		protocol = runtime.AgentCLIProtocol(engine, *req.Protocol)
	}
	if engine == runtime.AgentCLICursor {
		h.probeCursorModels(c, apiKey)
		return
	}
	if protocol == runtime.AgentCLIProtocolGemini {
		baseURL = ""
	}
	if baseURL == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(engine)), Source: "builtin"})
		return
	}
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写 " + assistantEngineLabel(engine) + " API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	models, err := assistantEngineDiscover(ctx, assistantEngineAnthropicModels(engine, protocol), apiKey, baseURL)
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "服务没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(models), Source: "remote"})
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
		c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(assistantEngineBuiltinModels(runtime.AgentCLICursor)), Source: "builtin"})
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
	c.JSON(http.StatusOK, cliModelProbeResult{Models: models, Source: "remote"})
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
		out = append(out, domain.CLIModel{ID: model.ID, Name: model.Name})
	}
	return out
}
