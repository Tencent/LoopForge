package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

func (h *assistantHandlers) prepareScheduleJob(job domain.ScheduleJob) (string, error) {
	if h == nil || h.repo == nil {
		return "", fmt.Errorf("未连接数据源")
	}
	prompt := strings.TrimSpace(job.AssistantPrompt)
	if job.SharedTaskID != "" {
		version, err := h.repo.SharedScheduleVersion(job.SharedTaskID, job.SharedTaskVersion)
		if err != nil {
			return "", fmt.Errorf("读取订阅版本失败：%w", err)
		}
		prompt, _, err = version.Render(job.ParameterValues)
		if err != nil {
			return "", err
		}
	}
	if prompt == "" {
		return "", nil
	}
	user, err := h.scheduleJobAssistantUser(job, job.AssistantModel)
	if err != nil {
		return "", err
	}
	task := assistantTask{
		Name:          job.Name,
		Prompt:        prompt,
		Model:         strings.TrimSpace(job.AssistantModel),
		Effort:        strings.TrimSpace(job.AssistantEffort),
		ProjectID:     job.ProjectID,
		Notifications: domain.CloneNotificationBindings(job.Notifications),
	}
	ctx := withAssistantExecMode(context.Background(), assistantExecModeForApproval(AssistantApprovalAuto))
	ctx = withAssistantSessionOrigin(ctx, domain.AssistantSourceScheduled, job.ID)
	if err := h.executeAssistantTask(ctx, user, &task); err != nil {
		if msg := strings.TrimSpace(task.LastError); msg != "" {
			return task.LastMessage, fmt.Errorf("%s", msg)
		}
		if errors.Is(err, context.Canceled) {
			return task.LastMessage, fmt.Errorf("生成已停止")
		}
		return task.LastMessage, err
	}
	return firstNonEmpty(task.NotificationResult, task.LastMessage), nil
}

// scheduleJobAssistantUser picks whose keys run the job: the project owner,
// else (for a non-shared job) its owner. The model's CLI decides which key
// they need. A scheduled run does not borrow the platform shared CodeBuddy key.
func (h *assistantHandlers) scheduleJobAssistantUser(job domain.ScheduleJob, model string) (domain.User, error) {
	engine, _ := assistantModelEngine(model)
	try := func(id int64) (domain.User, bool) {
		if id == 0 || h.repo == nil {
			return domain.User{}, false
		}
		user, ok := h.repo.User(id)
		key, _ := assistantEngineCredential(user, engine)
		return user, ok && key != ""
	}
	if project, ok := h.repo.Project(job.ProjectID); ok {
		if user, ok := try(project.OwnerID); ok {
			return user, nil
		}
	}
	if job.SharedTaskID == "" {
		if user, ok := try(job.OwnerUserID); ok {
			return user, nil
		}
	}
	return domain.User{}, fmt.Errorf("执行提示词需要项目 Owner 的 %s API Key：请在该账号的个人配置中配置", assistantEngineLabel(engine))
}
