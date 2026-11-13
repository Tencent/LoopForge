package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"agentflow/internal/domain"
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

// Summaries use fixed wording; sanitized provider details are available in the
// diagnostic report instead of interpolating arbitrary stderr into the UI.
func assistantGenerationFailure(ctx context.Context, err error) (int, string) {
	failure := classifyAssistantFailure(ctx, err)
	status := http.StatusBadGateway
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	return status, failure.Message
}

func classifyAssistantFailure(ctx context.Context, err error) domain.AssistantFailure {
	f := domain.AssistantFailure{Code: "GENERATION_FAILED", Phase: "execution", Message: "AI 助手生成失败，具体原因见诊断信息", Suggestion: "下载诊断信息交给管理员排查；重试前请检查已执行的操作。"}
	if err == nil {
		return f
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		f.Code, f.Message = "EXECUTION_TIMEOUT", "生成已取消或超时，请重试"
		return f
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		f.Code, f.Message = "EXECUTION_CANCELLED", "生成已取消或超时，请重试"
		return f
	}
	if errors.Is(err, runtime.ErrMaxTurns) || assistantCLIMaxTurns(err.Error()) {
		f.Code, f.Message = "EXECUTION_LIMIT", "AI 助手已达到执行轮数上限，未能完成回答，请重试"
		return f
	}
	var missing assistantCLIMissingError
	if errors.As(err, &missing) {
		f.Code, f.Phase, f.Message, f.Suggestion = "CLI_NOT_INSTALLED", "setup", missing.Error(), "请管理员检查执行器的安装与配置。"
		return f
	}
	// The process error usually carries the CLI's stderr tail. Classify the
	// error itself first so unrelated stderr words (an earlier retry warning,
	// sandbox notices) cannot outrank the real cause; stderr is the fallback.
	f, _ = classifyAssistantFailureParts(f, err)
	return f
}

// assistantFailureGeneric reports codes that only say "the CLI failed" and
// leave room for a more specific cause from stderr or structured events.
func assistantFailureGeneric(code string) bool {
	return code == "GENERATION_FAILED" || code == "CLI_EXIT_NONZERO" || code == "MISSING_RESULT"
}

// classifyAssistantFailureParts classifies the error text before its stderr
// tail first; specific reports whether that part alone named the cause.
func classifyAssistantFailureParts(f domain.AssistantFailure, err error) (domain.AssistantFailure, bool) {
	head, tail, _ := strings.Cut(err.Error(), "; stderr=")
	c := classifyAssistantFailureText(f, err, head)
	if !assistantFailureGeneric(c.Code) || tail == "" {
		return c, !assistantFailureGeneric(c.Code)
	}
	if t := classifyAssistantFailureText(f, err, tail); !assistantFailureGeneric(t.Code) {
		return t, false
	}
	return c, false
}

var assistantModelMissingRe = regexp.MustCompile("(?i)\\bmodel\\s+[`'\"]?[^\\s`'\"]*[`'\"]?\\s*(?:does not exist|not found|is not available)")

func classifyAssistantFailureText(f domain.AssistantFailure, err error, text string) domain.AssistantFailure {
	message := strings.ToLower(text)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(message, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has("insufficient_quota", "quota exceeded", "credit balance", "billing", "额度不足", "余额不足"):
		f.Code, f.Phase, f.Message, f.Suggestion = "QUOTA_EXCEEDED", "provider", "模型服务额度不足，无法继续生成", "请检查模型服务的余额或额度。"
	case has("rate limit", "rate_limit", "too many requests", "http 429", "status 429", "status code 429", "status=429"):
		f.Code, f.Phase, f.Message, f.Suggestion = "RATE_LIMITED", "provider", "模型服务请求过于频繁（429）", "请稍后再试，或检查模型服务的限流配置。"
	case errors.Is(err, errAssistantCredentialRejected), has("unauthorized", "invalid api key", "invalid_api_key", "invalid token", "api key expired", "invalid credential", "http 401", "status 401", "status code 401", "status=401", "authentication_error"):
		f.Code, f.Phase, f.Message, f.Suggestion = "AUTHENTICATION_FAILED", "provider", "CodeBuddy 凭据无效或已过期，请检查个人配置后重试", "请检查所用 CLI 的 API Key 和 Base URL。"
		var credential assistantCredentialError
		if errors.As(err, &credential) {
			f.Message = credential.Error()
		}
		if err == errAssistantCredentialRejected {
			f.Message = err.Error()
		}
	case has("forbidden", "http 403", "status 403", "status code 403", "status=403"):
		f.Code, f.Phase, f.Message, f.Suggestion = "PERMISSION_DENIED", "provider", "模型服务拒绝访问（403）", "请检查 API Key 的权限及模型访问范围。"
	case has("context_length", "context window", "maximum context", "prompt is too long", "too many tokens"):
		f.Code, f.Phase, f.Message, f.Suggestion = "CONTEXT_LIMIT_EXCEEDED", "provider", "对话内容超过模型的上下文上限", "请缩短输入、开启新对话，或使用上下文更大的模型。"
	case has("model_not_found", "model not found", "unknown model", "model does not exist") || assistantModelMissingRe.MatchString(text):
		f.Code, f.Phase, f.Message, f.Suggestion = "MODEL_UNAVAILABLE", "provider", "所选模型不存在或当前账号无权使用", "请检查模型名称和访问权限，或切换模型。"
	case has("upstream unavailable", "service unavailable", "bad gateway", "gateway timeout", "http 502", "http 503", "http 504", "status 502", "status 503", "status 504", "status code 502", "status code 503", "status code 504", "overloaded", "servers are currently busy"):
		f.Code, f.Phase, f.Message, f.Suggestion = "UPSTREAM_UNAVAILABLE", "provider", "模型服务暂时不可用", "请稍后再试，或切换模型服务。"
	case has("connection refused", "connection reset", "connection closed", "network interrupted", "network error", "network_error", "no such host", "dial tcp", "tls handshake", "tls: ", "i/o timeout", "broken pipe", "stream disconnected", "econnreset", "econnrefused", "unexpected eof"):
		f.Code, f.Phase, f.Message, f.Suggestion = "NETWORK_ERROR", "connection", "CLI 与模型服务连接失败或连接中断", "请检查模型服务地址、网络与代理配置。"
	default:
		var exit *exec.ExitError
		var path *os.PathError
		switch {
		case errors.As(err, &path), errors.Is(err, exec.ErrNotFound):
			f.Code, f.Phase, f.Message, f.Suggestion = "CLI_START_FAILED", "setup", "CLI 启动或初始化失败", "请管理员检查 CLI 的安装、目录与文件权限。"
		case errors.Is(err, errAssistantMissingResult):
			f.Code, f.Phase, f.Message = "MISSING_RESULT", "protocol", "AI 助手生成中断，未返回完整回答，请重试"
		case errors.As(err, &exit), has("exit status"):
			f.Code, f.Message = "CLI_EXIT_NONZERO", "CLI 异常退出，未完成本轮回复"
		}
	}
	return f
}

// assistantGenerationFailedMessage is the generic generation failure. A CLI
// that was missing is reported separately, and a finished install replaces
// both that report and this generic text.
const assistantGenerationFailedMessage = "AI 助手生成失败，请稍后重试"
