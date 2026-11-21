package repo

import (
	"errors"

	"agentflow/internal/domain"
)

var ErrAssistantDefaultExecutorUnavailable = errors.New("请选择已配置的执行器")

func firstAssistantExecutor(user domain.User) string {
	for _, executor := range append([]string{"codebuddy", "codex", "claude-code"}, domain.AgentCLIExecutorKinds...) {
		if user.HasExecutorCredential(executor) {
			return executor
		}
	}
	return ""
}
