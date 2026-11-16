package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// scheduledSessionPolicy carries how a schedule job's AI run uses Studio
// conversations; legacy assistant tasks run without one.
type scheduledSessionPolicy struct {
	// Continue is the pinned conversation to append this run to; when it is
	// gone or belongs elsewhere, the run opens a new one.
	Continue string
	// KeepHistory keeps the conversations of earlier runs so each execution
	// record can open its own; the session janitor trims them later.
	KeepHistory bool
}

type scheduledSessionPolicyKey struct{}

func withScheduledSessionPolicy(ctx context.Context, p scheduledSessionPolicy) context.Context {
	return context.WithValue(ctx, scheduledSessionPolicyKey{}, p)
}

func scheduledSessionPolicyFrom(ctx context.Context) scheduledSessionPolicy {
	p, _ := ctx.Value(scheduledSessionPolicyKey{}).(scheduledSessionPolicy)
	return p
}

func (h *assistantHandlers) prepareScheduleJob(job domain.ScheduleJob) (string, error) {
	msg, _, err := h.runScheduleJobPrompt(job)
	return msg, err
}

// runScheduleJobPrompt runs an AI job's prompt as a Studio conversation turn:
// a new conversation per execution by default, or the job's pinned one.
func (h *assistantHandlers) runScheduleJobPrompt(job domain.ScheduleJob) (message, sessionID string, err error) {
	if h == nil || h.repo == nil {
		return "", "", fmt.Errorf("未连接数据源")
	}
	prompt := strings.TrimSpace(job.AssistantPrompt)
	if job.SharedTaskID != "" {
		version, verr := h.repo.SharedScheduleVersion(job.SharedTaskID, job.SharedTaskVersion)
		if verr != nil {
			return "", "", fmt.Errorf("读取订阅版本失败：%w", verr)
		}
		prompt, _, err = version.Render(job.ParameterValues)
		if err != nil {
			return "", "", err
		}
	}
	if prompt == "" {
		return "", "", nil
	}
	user, err := h.scheduleJobAssistantUser(job, job.AssistantModel)
	if err != nil {
		return "", "", err
	}
	task := assistantTask{
		Name:          job.Name,
		Prompt:        prompt,
		Model:         strings.TrimSpace(job.AssistantModel),
		ProjectID:     job.ProjectID,
		Notifications: domain.CloneNotificationBindings(job.Notifications),
	}
	ctx := withAssistantExecMode(context.Background(), assistantExecModeForApproval(AssistantApprovalAuto))
	ctx = withAssistantSessionOrigin(ctx, domain.AssistantSourceScheduled, job.ID)
	policy := scheduledSessionPolicy{KeepHistory: true}
	if job.UsesFixedSession() {
		policy.Continue = strings.TrimSpace(job.FixedSessionID)
	}
	ctx = withScheduledSessionPolicy(ctx, policy)
	if err := h.executeAssistantTask(ctx, user, &task); err != nil {
		if msg := strings.TrimSpace(task.LastError); msg != "" {
			return task.LastMessage, task.SessionID, fmt.Errorf("%s", msg)
		}
		if errors.Is(err, context.Canceled) {
			return task.LastMessage, task.SessionID, fmt.Errorf("生成已停止")
		}
		return task.LastMessage, task.SessionID, err
	}
	return firstNonEmpty(task.NotificationResult, task.LastMessage), task.SessionID, nil
}

// scheduleJobAssistantUser picks whose keys run the job: the member the job
// is bound to, else the project owner, else (for a non-shared job) its owner.
// A bound job never falls back to someone else's keys. The model's CLI decides
// which key they need. A scheduled run does not borrow the platform shared
// CodeBuddy key.
func (h *assistantHandlers) scheduleJobAssistantUser(job domain.ScheduleJob, model string) (domain.User, error) {
	engine, _ := assistantModelEngine(model)
	if job.RunAsUserID > 0 {
		return h.scheduleJobRunAsUser(job, engine)
	}
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
	return domain.User{}, fmt.Errorf("执行提示词需要项目 Owner 的 %s API Key：请在该账号的个人配置中配置，或编辑任务把执行身份改为你自己的 Key", assistantEngineLabel(engine))
}

// scheduleJobRunAsUser runs a bound job as that member while they can still
// write to the project.
func (h *assistantHandlers) scheduleJobRunAsUser(job domain.ScheduleJob, engine string) (domain.User, error) {
	name := firstNonEmpty(job.RunAsName, fmt.Sprintf("用户 %d", job.RunAsUserID))
	user, ok := h.repo.User(job.RunAsUserID)
	if !ok {
		return domain.User{}, fmt.Errorf("任务绑定的执行账号 %s 已不存在：请编辑任务重新选择执行身份", name)
	}
	if !scheduleProjectWriter(h.repo, job.ProjectID, user) {
		return domain.User{}, fmt.Errorf("%s 已不是该项目的可写成员，不能再用其 Key 执行：请编辑任务重新选择执行身份", name)
	}
	if key, _ := assistantEngineCredential(user, engine); key == "" {
		return domain.User{}, fmt.Errorf("执行提示词需要 %s 的 %s API Key：请在该账号的个人配置中配置", name, assistantEngineLabel(engine))
	}
	return user, nil
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
