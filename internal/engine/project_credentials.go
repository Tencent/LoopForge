package engine

import (
	"context"
	"fmt"

	"agentflow/internal/domain"
)

func (e *Engine) ResolveRunProjectCredentialEnv(ctx context.Context, run domain.Run) (map[string]string, error) {
	wf := run.WorkflowSnapshot
	if len(wf.ProjectCredentialIDs) == 0 {
		return nil, nil
	}
	projectID := run.ExecutionContext.ProjectID
	if wf.Generic || projectID == "" || projectID != wf.ProjectID {
		return nil, fmt.Errorf("请将 SOP 复制到当前项目后重新绑定凭据")
	}
	project, found := e.repo.Project(projectID)
	user, signedIn := e.repo.User(run.ExecutionContext.ResponsibleUserID)
	if !found || project.ID != projectID || !signedIn {
		return nil, fmt.Errorf("运行责任人无权使用项目凭据")
	}
	use, _ := domain.ProjectCredentialAccess(project, user.ID, e.repo.IsPlatformAdmin(user.LoginName))
	if !use {
		return nil, fmt.Errorf("运行责任人无权使用项目凭据")
	}
	return e.repo.ResolveProjectCredentialEnv(ctx, projectID, wf.ProjectCredentialIDs)
}

func (e *Engine) requireProjectCredentialRunner(workflow domain.WorkflowDefinition, runnerID string) error {
	if len(workflow.ProjectCredentialIDs) == 0 || runnerID == "" {
		return nil
	}
	runner, ok := e.repo.Runner(runnerID)
	if !ok || !runner.SupportsProjectCredentials() {
		return fmt.Errorf("执行机器需升级至 %s 才能使用项目凭据", domain.RunnerVersionProjectCredentials)
	}
	return nil
}
