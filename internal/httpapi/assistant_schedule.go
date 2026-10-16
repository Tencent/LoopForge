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
	if prompt == "" {
		return "", nil
	}
	user, err := h.scheduleJobAssistantUser(job)
	if err != nil {
		return "", err
	}
	task := assistantTask{
		Name:          job.Name,
		Prompt:        prompt,
		Model:         strings.TrimSpace(job.AssistantModel),
		ProjectID:     job.ProjectID,
		Notifications: domain.CloneNotificationBindings(job.Notifications),
	}
	ctx, cancel := context.WithTimeout(context.Background(), assistantScheduleTimeout)
	defer cancel()
	ctx = withAssistantExecMode(ctx, assistantExecMode{AutoWrite: true, DeferSOP: true})
	if err := h.executeAssistantTask(ctx, user, &task); err != nil {
		if msg := strings.TrimSpace(task.LastError); msg != "" {
			return task.LastMessage, fmt.Errorf("%s", msg)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return task.LastMessage, fmt.Errorf("生成已取消或超时")
		}
		return task.LastMessage, err
	}
	return firstNonEmpty(task.NotificationResult, task.LastMessage), nil
}

func (h *assistantHandlers) scheduleJobAssistantUser(job domain.ScheduleJob) (domain.User, error) {
	try := func(id int64) (domain.User, bool) {
		if id == 0 || h.repo == nil {
			return domain.User{}, false
		}
		user, ok := h.repo.User(id)
		return user, ok && strings.TrimSpace(user.CodeBuddyAPIKey) != ""
	}
	if project, ok := h.repo.Project(job.ProjectID); ok {
		if user, ok := try(project.OwnerID); ok {
			return user, nil
		}
	}
	if user, ok := try(job.OwnerUserID); ok {
		return user, nil
	}
	return domain.User{}, fmt.Errorf("执行提示词需要项目 Owner 的 CodeBuddy API Key：请在该账号的个人设置中配置")
}
