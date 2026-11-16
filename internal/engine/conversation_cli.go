package engine

import (
	"agentflow/internal/runtime"
	"context"
	"fmt"
)

// AcquireConversationCLI uses the workspace's own environment, including its
// preparation scripts and browser. The caller also owns the durable workspace
// lease; this local fence excludes scheduler execution and terminal takeover.
func (e *Engine) AcquireConversationCLI(ctx context.Context, id, root string) (runtime.Sandbox, func(), error) {
	run, ok := e.repo.RunState(id)
	if !ok {
		return nil, nil, fmt.Errorf("工作区不存在")
	}
	env, err := e.acquireConversationEnvironment(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	env.nodeMu.Lock()
	if env.activeExecutions.Load() > 0 || run.HasActiveNode() || e.workspaceTerminals.HasRunScope(id) {
		env.nodeMu.Unlock()
		return nil, nil, fmt.Errorf("工作区仍有执行或终端占用，请结束后重试")
	}
	if err = runtime.ConfigureConversationSandbox(ctx, env.sandbox, root); err == nil {
		_, err = e.prepareLocalEnvironment(ctx, run, env)
	}
	if err != nil {
		env.nodeMu.Unlock()
		return nil, nil, err
	}
	env.activeExecutions.Add(1)
	env.nodeMu.Unlock()
	return env.sandbox, func() { env.activeExecutions.Add(-1) }, nil
}
