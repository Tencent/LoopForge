package httpapi

import (
	"context"
	"fmt"
	"slices"

	"agentflow/internal/domain"
)

func (h *runnersHandlers) runnerTaskProjectCredentialEnv(ctx context.Context, task domain.RunnerTask, runner domain.Runner) (map[string]string, error) {
	if len(task.ProjectCredentialIDs) == 0 {
		return nil, nil
	}
	run, ok := h.repo.RunState(task.RunID)
	if !ok || !slices.Equal(task.ProjectCredentialIDs, run.WorkflowSnapshot.ProjectCredentialIDs) {
		return nil, fmt.Errorf("任务的项目凭据绑定已失效，请重试")
	}
	if task.LeaseOwner != runner.ID || task.RunnerID != runner.ID || run.ExecutionContext.ExecutionRunnerID != runner.ID {
		return nil, fmt.Errorf("任务执行机器与运行绑定不一致")
	}
	if !runner.SupportsProjectCredentials() {
		return nil, fmt.Errorf("执行机器需升级至 %s 才能使用项目凭据", domain.RunnerVersionProjectCredentials)
	}
	return h.engine.ResolveRunProjectCredentialEnv(ctx, run)
}
