package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
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
	user, shared, err := h.scheduleJobAssistantUser(job, job.AssistantModel)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(job.AssistantModel)
	if shared != nil {
		// On the shared key a run only uses the models the administrator
		// allows, and never silently switches to another one.
		allowed, ok := assistantSponsoredModelFor(shared, scheduleSharedRequestedModel(model))
		if !ok {
			return "", fmt.Errorf("定时任务使用平台共享 Key 执行，只能用平台管理员允许的模型（%s），任务指定的 %s 不在其中，本次没有运行；请在定时任务里改选允许的模型，或在个人配置中填写自己的 Key", strings.Join(shared, "、"), model)
		}
		model = allowed
	}
	task := assistantTask{
		Name:          job.Name,
		Prompt:        prompt,
		Model:         model,
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

// scheduleJobAssistantUser picks who runs the job: the member the job is bound
// to (whoever last saved it), else the project owner, else (for a non-shared
// job) its owner. A bound job never falls back to someone else's account. The
// model's CLI decides which key they need: their own, else for CodeBuddy the
// platform's shared key, whose allowed models are returned as shared (nil on
// the user's own key).
func (h *assistantHandlers) scheduleJobAssistantUser(job domain.ScheduleJob, model string) (domain.User, []string, error) {
	engine, _ := assistantModelEngine(model)
	if job.RunAsUserID > 0 {
		return h.scheduleJobRunAsUser(job, engine)
	}
	var candidates []domain.User
	add := func(id int64) {
		if id == 0 || h.repo == nil {
			return
		}
		if user, ok := h.repo.User(id); ok {
			candidates = append(candidates, user)
		}
	}
	if project, ok := h.repo.Project(job.ProjectID); ok {
		add(project.OwnerID)
	}
	if job.SharedTaskID == "" {
		add(job.OwnerUserID)
	}
	// Personal keys go first in the original order, so a job whose recorded
	// owner still has a key keeps running on it after the project changes
	// owner; the shared key is only the fallback.
	for _, user := range candidates {
		if key, _ := assistantEngineCredential(user, engine); key != "" {
			return user, nil, nil
		}
	}
	for _, user := range candidates {
		if user, shared, ok := scheduleKeyUser(h.repo, user, engine); ok {
			return user, shared, nil
		}
	}
	return domain.User{}, nil, fmt.Errorf("执行提示词需要项目 Owner 的 %s API Key：请在该账号的个人配置中配置，或在自己的个人配置里填好 Key 后编辑并保存一次任务，改用你的 Key", assistantEngineLabel(engine))
}

// scheduleJobRunAsUser runs a bound job as that member while they can still
// write to the project.
func (h *assistantHandlers) scheduleJobRunAsUser(job domain.ScheduleJob, engine string) (domain.User, []string, error) {
	name := firstNonEmpty(job.RunAsName, fmt.Sprintf("用户 %d", job.RunAsUserID))
	user, ok := h.repo.User(job.RunAsUserID)
	if !ok {
		return domain.User{}, nil, fmt.Errorf("任务保存人 %s 的账号已不存在：请编辑并保存一次任务，改用你的 Key", name)
	}
	if !scheduleProjectWriter(h.repo, job.ProjectID, user) {
		return domain.User{}, nil, fmt.Errorf("%s 已不是该项目的可写成员，不能再用其 Key 执行：请编辑并保存一次任务，改用你的 Key", name)
	}
	runUser, shared, ok := scheduleKeyUser(h.repo, user, engine)
	if !ok {
		if engine != assistantEngineCodeBuddy {
			return domain.User{}, nil, fmt.Errorf("执行提示词需要 %s 的 %s API Key：请在该账号的个人配置中配置", name, assistantEngineLabel(engine))
		}
		return domain.User{}, nil, fmt.Errorf("执行提示词需要 %s 的 CodeBuddy API Key，平台也没有配置共享 Key：请在该账号的个人配置中配置，或联系平台管理员配置共享 Key", name)
	}
	return runUser, shared, nil
}

// scheduleKeyUser is user with the key a scheduled run for engine uses: their
// own, else for CodeBuddy the platform's shared key (whose allowed models are
// returned as shared). Everything but the key stays the user's own. ok is
// false when neither exists.
func scheduleKeyUser(r interface {
	SharedCodeBuddyKey() (repo.SharedCodeBuddyKey, bool)
}, user domain.User, engine string) (domain.User, []string, bool) {
	if key, _ := assistantEngineCredential(user, engine); key != "" {
		return user, nil, true
	}
	if engine != assistantEngineCodeBuddy || r == nil {
		return user, nil, false
	}
	key, found := r.SharedCodeBuddyKey()
	if !found || strings.TrimSpace(key.APIKey) == "" || len(key.Models) == 0 || (key.SponsorID != 0 && key.SponsorID == user.ID) {
		return user, nil, false
	}
	user.CodeBuddyAPIKey = key.APIKey
	user.CodeBuddyInternetEnvironment = key.InternetEnvironment
	return user, key.Models, true
}

// scheduleProjectWriter mirrors scheduleProjectAllowed for a stored user.
func scheduleProjectWriter(r interface {
	Project(string) (domain.Project, bool)
	IsPlatformAdmin(string) bool
}, projectID string, user domain.User) bool {
	if r.IsPlatformAdmin(user.LoginName) {
		return true
	}
	project, ok := r.Project(projectID)
	if !ok {
		return false
	}
	if project.OwnerID == user.ID {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == user.ID && !strings.EqualFold(member.Role, "viewer") {
			return true
		}
	}
	return false
}
