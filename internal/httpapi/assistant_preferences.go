package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// preferences handles GET/PUT /api/assistant/preferences: the signed-in
// user's Main Bot settings.
func (h *assistantHandlers) preferences(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	if c.Request.Method == "GET" {
		c.JSON(200, h.repo.AssistantPreferences(user.ID).Sanitized())
		return
	}
	var body domain.AssistantPreferences
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "设置格式不正确"})
		return
	}
	if utf8.RuneCountInString(body.Instructions) > domain.AssistantPreferencesInstructionsMax {
		c.JSON(400, gin.H{"error": fmt.Sprintf("自定义规则最多 %d 字", domain.AssistantPreferencesInstructionsMax)})
		return
	}
	existing := h.repo.AssistantPreferences(user.ID)
	normalized, err := normalizeAndValidateAssistantPreferences(body, existing)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	saved, err := h.repo.PutAssistantPreferences(user.ID, normalized)
	if err != nil {
		c.JSON(500, gin.H{"error": "保存设置失败，请重试"})
		return
	}
	c.JSON(200, saved.Sanitized())
}

// probeApprovalReviewModels backs 「获取可用模型」 in the "帮我批准审查模型 →
// 自定义 API 和模型" settings panel. Unsaved form fields override stored ones
// so the user can fetch models before saving; nothing is persisted.
func (h *assistantHandlers) probeApprovalReviewModels(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, cliModelProbeResult{Error: "请先登录"})
		return
	}
	var req struct {
		Protocol    string  `json:"protocol"`
		BaseURL     *string `json:"baseUrl"`
		APIKey      string  `json:"apiKey"`
		APIKeyClear bool    `json:"apiKeyClear"`
	}
	if !decode(c, &req) {
		return
	}
	protocol := strings.TrimSpace(req.Protocol)
	switch protocol {
	case runtime.AgentCLIProtocolChat, runtime.AgentCLIProtocolResponses, runtime.AgentCLIProtocolMessages, runtime.AgentCLIProtocolGemini:
	default:
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请选择支持的接口协议（Chat Completions / Responses / Messages / Gemini）"})
		return
	}
	var existingAPI *domain.ApprovalReviewAPIConfig
	if h.repo != nil {
		existingAPI = h.repo.AssistantPreferences(user.ID).ApprovalReviewAPI
	}
	baseURL := ""
	if existingAPI != nil {
		baseURL = strings.TrimSpace(existingAPI.BaseURL)
	}
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
	}
	if baseURL != "" {
		if err := validateApprovalReviewBaseURL(baseURL); err != nil {
			c.JSON(http.StatusOK, cliModelProbeResult{Error: err.Error()})
			return
		}
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && !req.APIKeyClear && existingAPI != nil && strings.TrimSpace(existingAPI.APIKey) != "" {
		if approvalReviewSameOrigin(existingAPI.Protocol, existingAPI.BaseURL, protocol, baseURL) {
			apiKey = strings.TrimSpace(existingAPI.APIKey)
		} else {
			c.JSON(http.StatusOK, cliModelProbeResult{Error: "API 地址已跨源变更，请重新填写自定义审查 API Key"})
			return
		}
	}
	if apiKey == "" {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "请先填写自定义审查 API Key"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	models, err := discoverApprovalReviewAPIModels(ctx, protocol, apiKey, baseURL)
	if err != nil {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "获取模型列表失败：" + err.Error()})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "服务没有返回可用模型，可以手动填写模型 ID"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: cliModelsOf(models), Source: "remote"})
}

// approvalReviewEffectiveOrigin returns the canonical scheme://host:port that a
// custom review API config connects to (mapping empty BaseURL to the protocol's
// official endpoint). It is used to prevent reusing a stored key across origins.
func approvalReviewEffectiveOrigin(protocol, rawBaseURL string) string {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		switch strings.TrimSpace(protocol) {
		case runtime.AgentCLIProtocolMessages:
			baseURL = assistantAnthropicAPIBase
		case runtime.AgentCLIProtocolChat, runtime.AgentCLIProtocolResponses:
			baseURL = "https://api.openai.com/v1"
		case runtime.AgentCLIProtocolGemini:
			baseURL = "https://generativelanguage.googleapis.com"
		default:
			return ""
		}
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

func approvalReviewSameOrigin(protoA, baseA, protoB, baseB string) bool {
	originA := approvalReviewEffectiveOrigin(protoA, baseA)
	originB := approvalReviewEffectiveOrigin(protoB, baseB)
	return originA != "" && originA == originB
}

func normalizeAndValidateAssistantPreferences(body, existing domain.AssistantPreferences) (domain.AssistantPreferences, error) {
	out := body
	rawMode := strings.TrimSpace(body.ApprovalReviewMode)
	modeExplicit := rawMode != ""
	mode := rawMode
	if mode == "" {
		switch {
		case body.ApprovalReviewAPI != nil && (strings.TrimSpace(body.ApprovalReviewAPI.Model) != "" || strings.TrimSpace(body.ApprovalReviewAPI.APIKey) != "" || strings.TrimSpace(body.ApprovalReviewAPI.BaseURL) != ""):
			mode = domain.ApprovalReviewModeAPI
		case strings.TrimSpace(body.ApprovalReviewModel) != "":
			mode = domain.ApprovalReviewModeCLI
		default:
			// Legacy clients omit the approvalReview* fields when saving other
			// settings; preserve the existing review mode instead of resetting.
			mode = existing.EffectiveApprovalReviewMode()
		}
	}
	switch mode {
	case domain.ApprovalReviewModeFollow, domain.ApprovalReviewModeCLI, domain.ApprovalReviewModeAPI:
	default:
		return out, fmt.Errorf("审查模式不受支持")
	}
	out.ApprovalReviewMode = mode

	if strings.TrimSpace(body.ApprovalReviewModel) != "" {
		reviewModel, err := validateApprovalReviewModel(body.ApprovalReviewModel)
		if err != nil {
			return out, err
		}
		out.ApprovalReviewModel = reviewModel
	} else if modeExplicit && mode == domain.ApprovalReviewModeCLI {
		return out, fmt.Errorf("请选择或填写自定义审查模型 ID")
	} else {
		out.ApprovalReviewModel = existing.ApprovalReviewModel
	}

	originChangedWithoutKey := false
	if body.ApprovalReviewAPI != nil {
		cfg := domain.ApprovalReviewAPIConfig{
			Protocol: strings.TrimSpace(body.ApprovalReviewAPI.Protocol),
			BaseURL:  strings.TrimSpace(body.ApprovalReviewAPI.BaseURL),
			Model:    strings.TrimSpace(body.ApprovalReviewAPI.Model),
		}
		switch {
		case body.ApprovalReviewAPI.APIKeyClear:
			cfg.APIKey = ""
		case strings.TrimSpace(body.ApprovalReviewAPI.APIKey) != "":
			cfg.APIKey = strings.TrimSpace(body.ApprovalReviewAPI.APIKey)
		case existing.ApprovalReviewAPI != nil && strings.TrimSpace(existing.ApprovalReviewAPI.APIKey) != "":
			if approvalReviewSameOrigin(existing.ApprovalReviewAPI.Protocol, existing.ApprovalReviewAPI.BaseURL, cfg.Protocol, cfg.BaseURL) {
				cfg.APIKey = strings.TrimSpace(existing.ApprovalReviewAPI.APIKey)
			} else {
				originChangedWithoutKey = true
			}
		}
		if cfg.Protocol == "" && cfg.BaseURL == "" && cfg.Model == "" && cfg.APIKey == "" {
			out.ApprovalReviewAPI = nil
		} else {
			out.ApprovalReviewAPI = &cfg
		}
	} else {
		out.ApprovalReviewAPI = existing.ApprovalReviewAPI
	}

	if mode == domain.ApprovalReviewModeAPI {
		if err := validateApprovalReviewAPIShape(out.ApprovalReviewAPI); err != nil {
			return out, err
		}
		if originChangedWithoutKey {
			return out, fmt.Errorf("API 地址已跨源变更，请重新填写自定义审查 API Key")
		}
		if err := validateApprovalReviewAPIKey(out.ApprovalReviewAPI); err != nil {
			return out, err
		}
	} else if out.ApprovalReviewAPI != nil && out.ApprovalReviewAPI.BaseURL != "" {
		if err := validateApprovalReviewBaseURL(out.ApprovalReviewAPI.BaseURL); err != nil {
			return out, err
		}
	}
	return out, nil
}

func validateApprovalReviewAPIShape(cfg *domain.ApprovalReviewAPIConfig) error {
	if cfg == nil {
		return fmt.Errorf("请填写自定义审查 API 配置")
	}
	switch cfg.Protocol {
	case runtime.AgentCLIProtocolChat, runtime.AgentCLIProtocolResponses, runtime.AgentCLIProtocolMessages, runtime.AgentCLIProtocolGemini:
	default:
		return fmt.Errorf("请选择支持的接口协议（Chat Completions / Responses / Messages / Gemini）")
	}
	if cfg.BaseURL != "" {
		if err := validateApprovalReviewBaseURL(cfg.BaseURL); err != nil {
			return err
		}
	}
	if cfg.Model == "" {
		return fmt.Errorf("请填写自定义审查模型 ID")
	}
	if len(cfg.Model) > 160 || !assistantValidEngineModel(cfg.Model) {
		return fmt.Errorf("自定义审查模型 ID 格式不正确")
	}
	return nil
}

func validateApprovalReviewAPIKey(cfg *domain.ApprovalReviewAPIConfig) error {
	if cfg == nil || cfg.APIKey == "" {
		return fmt.Errorf("请填写自定义审查 API Key")
	}
	if len(cfg.APIKey) > 512 || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return fmt.Errorf("自定义审查 API Key 格式不正确")
	}
	return nil
}

func validateApprovalReviewBaseURL(raw string) error {
	if len(raw) > 512 || strings.ContainsAny(raw, " \t\r\n") {
		return fmt.Errorf("Base URL 格式不正确")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("Base URL 必须以 https:// 开头")
	}
	if u.Scheme == "http" && len(approvalAllowedEgressPrefixes()) == 0 {
		return fmt.Errorf("自定义审查 Base URL 仅支持 https://（如需通过 http:// 访问内网网关，请配置 %s）", approvalEgressAllowCIDRsEnv)
	}
	return nil
}

func validateApprovalReviewModel(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if rest, ok := strings.CutPrefix(value, assistantEngineCodeBuddy+":"); ok {
		value = strings.TrimSpace(rest)
		if value == "" {
			return "", fmt.Errorf("审查模型 ID 不能为空")
		}
	}
	if len(value) > 160 {
		return "", fmt.Errorf("审查模型 ID 过长")
	}
	if strings.Contains(value, ":") && !assistantModelHasEnginePrefix(value) {
		return "", fmt.Errorf("审查模型 CLI 前缀不受支持，请从已配置模型选择或使用 <CLI>:<模型ID> 格式")
	}
	_, modelID := assistantModelEngine(value)
	if !assistantValidEngineModel(modelID) {
		return "", fmt.Errorf("审查模型 ID 格式不正确")
	}
	return value, nil
}
