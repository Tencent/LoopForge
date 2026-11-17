package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

func openAIBaseURL(user domain.User) string {
	if base := strings.TrimRight(strings.TrimSpace(user.OpenAIBaseURL), "/"); base != "" {
		return base
	}
	return "https://api.openai.com/v1"
}

func assistantExecutionReminder(req assistantRequest) string {
	if provider, _ := assistantModelEngine(req.Model); provider == assistantEngineOpenAI {
		return "当前由 LoopForge 直接调用 OpenAI API。只使用本轮提供的会话工具读取数据或执行操作，遵循当前审批模式。对话上下文由平台提供。"
	}
	return assistantPlatformExecutionReminder
}

// API turns are stateless at the provider. LoopForge supplies the conversation
// transcript and dispatches tools through its existing authenticated gateway.
// No CLI process, native login directory or provider conversation is created.
func runAssistantModel(ctx context.Context, req assistantCLIRequest, user domain.User, onEvent func(assistantStreamEvent)) (string, string, error) {
	req.Model = assistantRequestModel(req.Executor, req.Model)
	provider, _ := assistantModelEngine(req.Model)
	if provider == assistantEngineOpenAI {
		return runAssistantOpenAI(ctx, req, user, onEvent)
	}
	return runAssistantCLI(ctx, req, user, onEvent)
}

func openAIGateway(configPath string) (*mcpclient.Client, error) {
	if configPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var config struct {
		Servers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	server := config.Servers[agentToolSessionMCPServerID]
	if server.URL == "" {
		return nil, fmt.Errorf("OpenAI API 缺少会话工具网关")
	}
	return mcpclient.New(server.URL, nil), nil
}

func runAssistantOpenAI(ctx context.Context, req assistantCLIRequest, user domain.User, emit func(assistantStreamEvent)) (string, string, error) {
	_, model := assistantModelEngine(req.Model)
	if strings.TrimSpace(user.OpenAIAPIKey) == "" {
		return "", "", fmt.Errorf("请先在个人配置中配置 OpenAI API Key")
	}
	if !assistantValidEngineModel(model) || model == "default" {
		return "", "", fmt.Errorf("请为 OpenAI API 选择具体模型")
	}
	gateway, err := openAIGateway(req.MCPConfig)
	if err != nil {
		return "", "", err
	}
	input := []any{map[string]any{"role": "user", "content": req.Prompt}}
	for round := 0; round < 32; round++ {
		if err := ctx.Err(); err != nil {
			return "", req.SessionID, err
		}
		tools := []any{}
		allowed := map[string]bool{}
		if gateway != nil {
			catalog, err := gateway.ListToolsContext(ctx)
			if err != nil {
				return "", req.SessionID, fmt.Errorf("读取会话工具失败: %w", err)
			}
			for _, tool := range catalog {
				tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema, "strict": false})
				allowed[tool.Name] = true
			}
		}
		body := map[string]any{"model": model, "input": input, "stream": true, "store": false, "max_output_tokens": 4096}
		if req.SystemPrompt != "" {
			body["instructions"] = req.SystemPrompt
		}
		if len(tools) > 0 {
			body["tools"], body["parallel_tool_calls"] = tools, false
		}
		if req.Effort != "" && len(assistantModelEfforts(user, req.Model)) > 0 {
			body["reasoning"] = map[string]string{"effort": req.Effort}
		}
		response, err := openAIResponse(ctx, user, body, emit)
		if err != nil {
			return "", req.SessionID, err
		}
		if req.OnUsage != nil {
			u := response.Usage
			req.OnUsage(domain.TokenUsage{InputTokens: u.Input, OutputTokens: u.Output, TotalTokens: u.Total, CacheReadTokens: u.Details.Cached, ReasoningTokens: u.OutputDetails.Reasoning, CostStatus: "unknown"})
		}
		var text strings.Builder
		calls := 0
		for _, item := range response.Output {
			input = append(input, item)
			var output struct {
				Type, Name, Arguments string
				CallID                string `json:"call_id"`
				Content               []struct{ Type, Text string }
			}
			if err := json.Unmarshal(item, &output); err != nil {
				return "", req.SessionID, err
			}
			if output.Type != "function_call" {
				for _, content := range output.Content {
					if content.Type == "output_text" {
						text.WriteString(content.Text)
					}
				}
				continue
			}
			calls++
			if !allowed[output.Name] {
				return "", req.SessionID, errAgentToolForbidden
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(output.Arguments), &args); err != nil {
				return "", req.SessionID, fmt.Errorf("OpenAI API 工具参数无效")
			}
			if emit != nil {
				emit(assistantStreamEvent{Kind: "tool", Tool: output.Name, ID: output.CallID})
			}
			result, callErr := gateway.CallToolContext(ctx, output.Name, args)
			if ctx.Err() != nil {
				return "", req.SessionID, ctx.Err()
			}
			if callErr != nil {
				result = "工具执行失败: " + redactAgentToolResult(callErr.Error(), user)
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": output.CallID, "output": result})
		}
		if calls == 0 {
			if strings.TrimSpace(text.String()) == "" {
				return "", req.SessionID, fmt.Errorf("OpenAI API 未返回文本或工具调用")
			}
			return text.String(), req.SessionID, nil
		}
	}
	return "", req.SessionID, fmt.Errorf("本轮 OpenAI API 工具调用次数过多，请继续下一轮")
}

type openAIModelResponse struct {
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Usage  struct {
		Input   int64 `json:"input_tokens"`
		Output  int64 `json:"output_tokens"`
		Total   int64 `json:"total_tokens"`
		Details struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			Reasoning int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func openAIResponse(ctx context.Context, user domain.User, payload map[string]any, emit func(assistantStreamEvent)) (openAIModelResponse, error) {
	var result openAIModelResponse
	raw, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIBaseURL(user)+"/responses", bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(user.OpenAIAPIKey))
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return result, fmt.Errorf("OpenAI API 请求失败: %s", redactAgentToolResult(err.Error(), user))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		var body struct {
			Error struct{ Message, Code string }
		}
		_ = json.Unmarshal(data, &body)
		return result, fmt.Errorf("OpenAI API HTTP %d: %s %s", response.StatusCode, redactAgentToolResult(body.Error.Code, user), redactAgentToolResult(body.Error.Message, user))
	}
	// Responses streaming events each carry one JSON data line. Bound the event
	// size, honor cancellation, and require a terminal completion event.
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		var event struct {
			Type, Delta, Message string
			Response             openAIModelResponse
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return result, fmt.Errorf("OpenAI API 流式响应无效")
		}
		switch event.Type {
		case "response.output_text.delta":
			if emit != nil {
				emit(assistantStreamEvent{Kind: "answer", Text: event.Delta})
			}
		case "response.completed":
			return event.Response, nil
		case "error", "response.failed", "response.incomplete":
			return result, fmt.Errorf("OpenAI API 响应失败或不完整: %s", redactAgentToolResult(event.Message, user))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	return result, fmt.Errorf("OpenAI API 响应在完成前中断")
}
