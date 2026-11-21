package repo

import (
	"errors"
	"strings"

	"agentflow/internal/domain"
)

var ErrAssistantDefaultExecutorUnavailable = errors.New("请选择已配置的执行器")

func hasAssistantExecutor(user domain.User, executor string) bool {
	var key string
	switch executor {
	case "codebuddy":
		key = user.CodeBuddyAPIKey
	case "codex":
		key = user.CodexAPIKey
	case "claude-code":
		key = user.ClaudeCodeAPIKey
	default:
		if domain.IsAgentCLIKind(executor) {
			key = user.AgentCLICredentials[executor].APIKey
		}
	}
	return strings.TrimSpace(key) != ""
}

func firstAssistantExecutor(user domain.User) string {
	for _, executor := range append([]string{"codebuddy", "codex", "claude-code"}, domain.AgentCLIExecutorKinds...) {
		if hasAssistantExecutor(user, executor) {
			return executor
		}
	}
	return ""
}
