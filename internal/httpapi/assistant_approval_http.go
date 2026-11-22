package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

var approvalReviewHTTPClient = &http.Client{Timeout: 25 * time.Second}

// errApprovalHTTPFatal marks a definitive auth or missing-model HTTP error
// where falling back to spawning the same CLI with the same key/model would
// only burn extra seconds before failing again.
type errApprovalHTTPFatal struct {
	status int
	msg    string
}

func (e errApprovalHTTPFatal) Error() string { return e.msg }

type approvalHTTPTarget struct {
	Protocol string
	Endpoint string
	APIKey   string
	Model    string
}

func resolveApprovalHTTPTarget(user domain.User, model string) (approvalHTTPTarget, bool) {
	engine, modelID := assistantModelEngine(strings.TrimSpace(model))
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return approvalHTTPTarget{}, false
	}
	var protocol, baseURL, apiKey string
	switch engine {
	case assistantEngineClaude:
		apiKey = strings.TrimSpace(user.ClaudeCodeAPIKey)
		baseURL = strings.TrimSpace(user.ClaudeCodeBaseURL)
		protocol = runtime.AgentCLIProtocolMessages
	case assistantEngineCodex:
		apiKey = strings.TrimSpace(user.CodexAPIKey)
		baseURL = strings.TrimSpace(user.CodexBaseURL)
		protocol = runtime.AgentCLIProtocolResponses
	case assistantEngineCodeBuddy, runtime.AgentCLICursor:
		return approvalHTTPTarget{}, false
	default:
		if !runtime.IsAgentCLI(engine) {
			return approvalHTTPTarget{}, false
		}
		cred := user.AgentCLICredentials[engine]
		apiKey = strings.TrimSpace(cred.APIKey)
		baseURL = strings.TrimSpace(cred.BaseURL)
		protocol = runtime.AgentCLIProtocol(engine, cred.Protocol)
		if baseURL == "" {
			switch {
			case engine == runtime.AgentCLIDeepSeek:
				baseURL = "https://api.deepseek.com/v1"
				protocol = runtime.AgentCLIProtocolChat
			case protocol == runtime.AgentCLIProtocolGemini:
				baseURL = "https://generativelanguage.googleapis.com"
			default:
				return approvalHTTPTarget{}, false
			}
		}
	}
	return buildApprovalHTTPTarget(protocol, baseURL, apiKey, modelID)
}

func resolveApprovalCustomAPITarget(cfg domain.ApprovalReviewAPIConfig) (approvalHTTPTarget, bool) {
	return buildApprovalHTTPTarget(cfg.Protocol, cfg.BaseURL, cfg.APIKey, cfg.Model)
}

func buildApprovalHTTPTarget(protocol, baseURL, apiKey, modelID string) (approvalHTTPTarget, bool) {
	protocol = strings.TrimSpace(protocol)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	modelID = strings.TrimSpace(modelID)
	if apiKey == "" || modelID == "" {
		return approvalHTTPTarget{}, false
	}
	if baseURL == "" {
		switch protocol {
		case runtime.AgentCLIProtocolMessages:
			baseURL = "https://api.anthropic.com"
		case runtime.AgentCLIProtocolResponses, runtime.AgentCLIProtocolChat:
			baseURL = "https://api.openai.com/v1"
		case runtime.AgentCLIProtocolGemini:
			baseURL = "https://generativelanguage.googleapis.com"
		default:
			return approvalHTTPTarget{}, false
		}
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return approvalHTTPTarget{}, false
	}
	var endpoint string
	switch protocol {
	case runtime.AgentCLIProtocolMessages:
		switch {
		case strings.HasSuffix(baseURL, "/messages"):
			endpoint = baseURL
		case strings.HasSuffix(baseURL, "/v1"):
			endpoint = baseURL + "/messages"
		default:
			endpoint = baseURL + "/v1/messages"
		}
	case runtime.AgentCLIProtocolResponses:
		if strings.HasSuffix(baseURL, "/responses") {
			endpoint = baseURL
		} else {
			endpoint = baseURL + "/responses"
		}
	case runtime.AgentCLIProtocolChat:
		if strings.HasSuffix(baseURL, "/chat/completions") {
			endpoint = baseURL
		} else {
			endpoint = baseURL + "/chat/completions"
		}
	case runtime.AgentCLIProtocolGemini:
		modelPath := strings.TrimPrefix(modelID, "models/")
		switch {
		case strings.HasSuffix(baseURL, ":generateContent"):
			endpoint = baseURL
		case strings.HasSuffix(baseURL, "/v1beta"):
			endpoint = baseURL + "/models/" + url.PathEscape(modelPath) + ":generateContent"
		default:
			endpoint = baseURL + "/v1beta/models/" + url.PathEscape(modelPath) + ":generateContent"
		}
	default:
		return approvalHTTPTarget{}, false
	}
	return approvalHTTPTarget{
		Protocol: protocol,
		Endpoint: endpoint,
		APIKey:   apiKey,
		Model:    modelID,
	}, true
}

// runAssistantApprovalHTTP executes a direct, tool-free single-turn review call
// against OpenAI, Anthropic, or Gemini compatible endpoints, disabling
// reasoning/thinking first so reviews finish in ~1s with minimal tokens instead
// of cold-starting a full CLI subprocess.
func runAssistantApprovalHTTP(ctx context.Context, target approvalHTTPTarget, systemPrompt, prompt string) (assistantApprovalReview, error) {
	payloads := approvalHTTPPayloads(target.Protocol, target.Model, systemPrompt, prompt)
	var lastErr error
	for _, payload := range payloads {
		raw, status, err := postApprovalHTTP(ctx, target, payload)
		if err != nil {
			lastErr = err
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				return assistantApprovalReview{}, errApprovalHTTPFatal{status: status, msg: err.Error()}
			}
			// On Gemini, CLI aliases (such as agy's effort-suffixed IDs) may 404
			// on raw generateContent and should fall back to the CLI instead of
			// aborting immediately.
			if status == http.StatusNotFound && target.Protocol != runtime.AgentCLIProtocolGemini {
				return assistantApprovalReview{}, errApprovalHTTPFatal{status: status, msg: err.Error()}
			}
			// 400/422 often means the endpoint does not recognize thinking/reasoning_effort;
			// try the simpler fallback payload.
			if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
				continue
			}
			return assistantApprovalReview{}, err
		}
		text, err := extractApprovalHTTPText(target.Protocol, raw)
		if err != nil || strings.TrimSpace(text) == "" {
			lastErr = fmt.Errorf("empty review content")
			continue
		}
		review, err := decodeAssistantApprovalReview(text)
		if err != nil {
			lastErr = err
			continue
		}
		return review, nil
	}
	if lastErr == nil {
		lastErr = errors.New("approval HTTP review produced no valid assessment")
	}
	return assistantApprovalReview{}, lastErr
}

func approvalHTTPPayloads(protocol, model, systemPrompt, prompt string) []map[string]any {
	switch protocol {
	case runtime.AgentCLIProtocolMessages:
		return []map[string]any{
			{
				"model":      model,
				"max_tokens": 256,
				"thinking":   map[string]any{"type": "disabled"},
				"system":     systemPrompt,
				"messages":   []map[string]any{{"role": "user", "content": prompt}},
			},
			{
				"model":      model,
				"max_tokens": 1024,
				"system":     systemPrompt,
				"messages":   []map[string]any{{"role": "user", "content": prompt}},
			},
		}
	case runtime.AgentCLIProtocolResponses:
		return []map[string]any{
			{
				"model":             model,
				"max_output_tokens": 256,
				"reasoning":         map[string]any{"effort": "none"},
				"instructions":      systemPrompt,
				"input":             prompt,
			},
			{
				"model":             model,
				"max_output_tokens": 1024,
				"reasoning":         map[string]any{"effort": "low"},
				"instructions":      systemPrompt,
				"input":             prompt,
			},
			{
				"model":             model,
				"max_output_tokens": 1024,
				"instructions":      systemPrompt,
				"input":             prompt,
			},
		}
	case runtime.AgentCLIProtocolGemini:
		sys := map[string]any{"parts": []map[string]any{{"text": systemPrompt}}}
		contents := []map[string]any{{"role": "user", "parts": []map[string]any{{"text": prompt}}}}
		return []map[string]any{
			{
				"systemInstruction": sys,
				"contents":          contents,
				"generationConfig": map[string]any{
					"maxOutputTokens":  256,
					"temperature":      0,
					"responseMimeType": "application/json",
					"thinkingConfig":   map[string]any{"thinkingBudget": 0},
				},
			},
			{
				"systemInstruction": sys,
				"contents":          contents,
				"generationConfig": map[string]any{
					"maxOutputTokens": 1024,
					"temperature":     0,
				},
			},
		}
	default: // chat completions
		msgs := []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		}
		return []map[string]any{
			{
				"model":            model,
				"max_tokens":       256,
				"temperature":      0,
				"reasoning_effort": "none",
				"messages":         msgs,
			},
			{
				"model":      model,
				"max_tokens": 1024,
				"messages":   msgs,
			},
		}
	}
}

func postApprovalHTTP(ctx context.Context, target approvalHTTPTarget, payload map[string]any) ([]byte, int, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	schemes := []string{"bearer"}
	switch target.Protocol {
	case runtime.AgentCLIProtocolMessages:
		schemes = []string{"x-api-key", "bearer"}
	case runtime.AgentCLIProtocolGemini:
		schemes = []string{"x-goog-api-key", "bearer"}
	}
	var lastStatus int
	var lastErr error
	for _, scheme := range schemes {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		if target.Protocol == runtime.AgentCLIProtocolMessages {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		switch scheme {
		case "x-api-key":
			req.Header.Set("x-api-key", target.APIKey)
		case "x-goog-api-key":
			req.Header.Set("x-goog-api-key", target.APIKey)
		default:
			req.Header.Set("Authorization", "Bearer "+target.APIKey)
		}
		resp, err := approvalReviewHTTPClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, readErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return raw, resp.StatusCode, nil
		}
		lastStatus = resp.StatusCode
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateRunes(strings.TrimSpace(string(raw)), 200))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			continue
		}
		return raw, resp.StatusCode, lastErr
	}
	return nil, lastStatus, lastErr
}

func extractApprovalHTTPText(protocol string, raw []byte) (string, error) {
	switch protocol {
	case runtime.AgentCLIProtocolMessages:
		var resp struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", err
		}
		var b strings.Builder
		for _, block := range resp.Content {
			if block.Type == "text" {
				b.WriteString(block.Text)
			}
		}
		return b.String(), nil
	case runtime.AgentCLIProtocolResponses:
		var resp struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", err
		}
		if strings.TrimSpace(resp.OutputText) != "" {
			return resp.OutputText, nil
		}
		var b strings.Builder
		for _, item := range resp.Output {
			if item.Type != "message" {
				continue
			}
			for _, block := range item.Content {
				if block.Type == "output_text" || block.Type == "text" || block.Type == "" {
					b.WriteString(block.Text)
				}
			}
		}
		return b.String(), nil
	case runtime.AgentCLIProtocolGemini:
		var resp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text    string `json:"text"`
						Thought bool   `json:"thought"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", err
		}
		if len(resp.Candidates) == 0 {
			return "", nil
		}
		var b strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			if !part.Thought {
				b.WriteString(part.Text)
			}
		}
		return b.String(), nil
	default: // chat completions
		var resp struct {
			Choices []struct {
				Message struct {
					Content any `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", nil
		}
		switch content := resp.Choices[0].Message.Content.(type) {
		case string:
			return content, nil
		case []any:
			var b strings.Builder
			for _, part := range content {
				if obj, ok := part.(map[string]any); ok {
					if text, ok := obj["text"].(string); ok {
						b.WriteString(text)
					}
				}
			}
			return b.String(), nil
		}
		return "", nil
	}
}
