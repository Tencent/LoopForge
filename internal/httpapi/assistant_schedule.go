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
	refs, err := h.normalizeScheduledReferences(job.ProjectID, job.References)
	if err != nil {
		return "", err
	}
	task := assistantTask{
		Name:          job.Name,
		Prompt:        prompt,
		References:    refs,
		Model:         strings.TrimSpace(job.AssistantModel),
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

// normalizeScheduledReferences shares the homepage resolver and stores only
// stable asset identity. Names and project metadata are presentation details;
// every execution resolves the current record again through prepareWorkbenchContext.
func (h *assistantHandlers) normalizeScheduledReferences(projectID string, refs []domain.AssistantReference) ([]domain.AssistantReference, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > 12 {
		return nil, fmt.Errorf("AI 定时任务最多引用 12 个项目资产")
	}
	allowed := map[string]bool{"agent": true, "skill": true, "sop": true, "issue": true, "run": true, "repository": true}
	seen := map[string]bool{}
	out := make([]domain.AssistantReference, 0, len(refs))
	for _, raw := range refs {
		typ, id := strings.TrimSpace(raw.Type), strings.TrimSpace(raw.ID)
		if !allowed[typ] || id == "" {
			return nil, fmt.Errorf("定时任务引用的项目资产无效")
		}
		key := typ + "\x00" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		canonical, _, err := h.workbenchReference(domain.AssistantReference{Type: typ, ID: id}, projectID)
		if err != nil {
			return nil, fmt.Errorf("定时任务引用资产 %s/%s 不可用：%w", typ, id, err)
		}
		out = append(out, domain.AssistantReference{Type: canonical.Type, ID: canonical.ID})
	}
	return out, nil
}

func (h *assistantHandlers) scheduledMessageReferences(projectID string, refs []domain.AssistantReference) ([]domain.AssistantReference, error) {
	out := make([]domain.AssistantReference, 0, len(refs))
	for _, ref := range refs {
		canonical, _, err := h.workbenchReference(ref, projectID)
		if err != nil {
			return nil, err
		}
		ownerProject := projectID
		switch ref.Type {
		case "issue":
			if v, ok := h.repo.Issue(ref.ID); ok {
				ownerProject = v.ProjectID
			}
		case "run":
			if v, ok := h.repo.RunState(ref.ID); ok {
				ownerProject = v.ExecutionContext.ProjectID
			}
		case "repository":
			if v, ok := h.repo.Repository(ref.ID); ok {
				ownerProject = v.ProjectID
			}
		case "agent":
			if v, ok := h.repo.Agent(ref.ID); ok && v.Shared {
				ownerProject = v.ProjectID
			}
		case "skill":
			if v, ok := h.repo.Skill(ref.ID); ok && v.Shared {
				ownerProject = v.ProjectID
			}
		case "sop":
			if v, ok := h.repo.Workflow(ref.ID); ok && (v.Shared || v.Generic) {
				ownerProject = v.ProjectID
			}
		}
		canonical.ProjectID = ownerProject
		if p, ok := h.repo.Project(ownerProject); ok {
			canonical.ProjectName = p.Name
		}
		out = append(out, canonical)
	}
	return out, nil
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
