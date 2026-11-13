package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"syscall"

	"agentflow/internal/runtime"
)

var errAssistantMissingResult = errors.New("CodeBuddy did not emit type=result")

func assistantCLIMaxTurns(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(text, "max turns (") && strings.HasSuffix(text, ") exceeded")
}

// Some CLI failures are emitted as assistant text instead of type=result.
// Keep their terminal cause even when the process itself exits successfully.
func assistantCLIStreamFailure(line string) error {
	var event struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return nil
	}
	if event.Type == "assistant" {
		for _, block := range event.Message.Content {
			if assistantCLIMaxTurns(block.Text) {
				return runtime.ErrMaxTurns
			}
		}
	}
	if event.Type == "error" {
		var message string
		if json.Unmarshal(event.Error, &message) != nil {
			var detail struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(event.Error, &detail)
			message = detail.Message
		}
		if assistantCLIMaxTurns(message) {
			return runtime.ErrMaxTurns
		}
		if strings.TrimSpace(message) != "" {
			return errors.New(message)
		}
	}
	return nil
}

// Only return fixed user-facing messages: CLI errors and stderr may contain
// credentials or internal paths and must never be copied into the response.
func assistantGenerationFailure(ctx context.Context, err error) (int, string) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "生成已取消或超时，请重试"
	}
	if errors.Is(err, runtime.ErrMaxTurns) || assistantCLIMaxTurns(err.Error()) {
		return http.StatusBadGateway, "AI 助手已达到执行轮数上限，未能完成回答，请重试"
	}
	if errors.Is(err, errAssistantDSHVersionUnsupported) {
		return http.StatusBadGateway, err.Error()
	}
	if errors.Is(err, syscall.E2BIG) {
		return http.StatusBadGateway, "[ASSISTANT_ARGV_TOO_LARGE] 提示词超过执行器启动参数限制，请联系管理员升级执行器输入传输方式"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unauthorized") || strings.Contains(message, "invalid api key") || strings.Contains(message, "api key expired") || strings.Contains(message, "invalid credential") || strings.Contains(message, "status code 401") || strings.Contains(message, "http 401") {
		return http.StatusBadGateway, "CodeBuddy 凭据无效或已过期，请检查个人配置后重试"
	}
	if errors.Is(err, errAssistantMissingResult) {
		return http.StatusBadGateway, "AI 助手生成中断，未返回完整回答，请重试"
	}
	return http.StatusBadGateway, assistantGenerationFailedMessage
}

// assistantGenerationFailedMessage is the generic generation failure. A CLI
// that was missing is reported separately, and a finished install replaces
// both that report and this generic text.
const assistantGenerationFailedMessage = "AI 助手生成失败，请稍后重试"
