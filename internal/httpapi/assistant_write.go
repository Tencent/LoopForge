package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

type assistantExecModeKey struct{}

// assistantExecMode changes how platform write tools behave.
// Chat confirm mode leaves AutoWrite false (writes are blocked and the model
// must submit drafts). Scheduled runs and the assistant auto/assisted approval
// modes set AutoWrite so native platform MCP writes execute immediately.
// ConfirmDangerous additionally blocks delete/publish/dispatch/start tools in
// assisted mode so those still go through user-confirmed drafts.
// DeferSOP blocks dispatch/start/run tools so AI scheduled jobs cannot start
// SOP; trigger jobs do that instead.
type assistantExecMode struct {
	Consultation     bool
	AutoWrite        bool
	DeferSOP         bool
	ConfirmDangerous bool
}

// assistantExecModeForApproval maps the assistant approval mode to the
// session gateway write policy. Writes are detected by the gateway itself
// (ReadOnly flag on each operation) — the model never hand-builds drafts.
func assistantExecModeForApproval(mode string) assistantExecMode {
	switch mode {
	case AssistantApprovalAuto:
		return assistantExecMode{AutoWrite: true}
	case AssistantApprovalAssisted:
		return assistantExecMode{AutoWrite: true, ConfirmDangerous: true}
	default:
		return assistantExecMode{}
	}
}

func withAssistantExecMode(ctx context.Context, mode assistantExecMode) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, assistantExecModeKey{}, mode)
}

func assistantExecModeFrom(ctx context.Context) assistantExecMode {
	if ctx == nil {
		return assistantExecMode{}
	}
	mode, _ := ctx.Value(assistantExecModeKey{}).(assistantExecMode)
	return mode
}

func assistantDeferredSOPTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "issues_dispatch", "runs_start", "runs_retry", "schedule_jobs_run", "schedule_jobs_save", "schedule_jobs_delete", "workflows_publish":
		return true
	default:
		return false
	}
}

func assistantScheduledSystemPromptText(mode assistantExecMode) string {
	if !mode.AutoWrite {
		return assistantSystemPromptText()
	}
	extra := "这是无人值守定时任务：直接调用当前项目和个人已安装的 MCP，以及 agentflow__* 平台工具。读写都立即生效。issues_save 把需求 id 放在 body.id（也可同时放 path.id）。完成后用自然语言汇报。"
	if mode.DeferSOP {
		extra += " 不要调用 issues_dispatch、runs_start、schedule_jobs_run、schedule_jobs_save；需要派发 SOP 时请单独创建触发任务。"
	}
	return extra + "\n可调用的平台写入工具：" + assistantWriteToolNames()
}

func (h *assistantHandlers) mergeIssueSaveBody(body any, path map[string]string) any {
	if h == nil || h.repo == nil {
		return body
	}
	patch := map[string]any{}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return body
		}
		if json.Unmarshal(raw, &patch) != nil {
			return body
		}
	}
	id := strings.TrimSpace(fmt.Sprint(patch["id"]))
	if id == "" || id == "<nil>" {
		id = strings.TrimSpace(path["id"])
		if id != "" {
			patch["id"] = id
		}
	}
	if id == "" || id == "<nil>" {
		return body
	}
	existing, ok := h.repo.Issue(id)
	if !ok {
		return body
	}
	baseRaw, err := json.Marshal(existing)
	if err != nil {
		return body
	}
	var base map[string]any
	if json.Unmarshal(baseRaw, &base) != nil {
		return body
	}
	for key, value := range patch {
		if value == nil {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		base[key] = value
	}
	return base
}

func (h *assistantHandlers) applyAssistantDrafts(ctx context.Context, user domain.User, projectID string, drafts []assistantDraft) []assistantDraft {
	out := make([]assistantDraft, 0, len(drafts))
	for _, draft := range drafts {
		applied := draft
		args := map[string]any{}
		if draft.Path != nil {
			args["path"] = draft.Path
		}
		if draft.Query != nil {
			args["query"] = draft.Query
		}
		if len(draft.Body) > 0 {
			var body any
			if json.Unmarshal(draft.Body, &body) == nil {
				args["body"] = body
			}
		}
		if _, err := h.dispatchAssistantMCPCall(ctx, user, projectID, assistantMCPCall{Server: "agentflow", Tool: draft.Tool, Arguments: args}); err == nil {
			applied.Saved = true
		} else if assistantExecModeFrom(ctx).DeferSOP && assistantDeferredSOPTool(draft.Tool) {
			applied.Saved = true
		} else {
			// 失败原因随草稿带回前端，供展示并打回给 AI 自动修正。
			applied.SaveError = truncateRunes(err.Error(), 300)
		}
		out = append(out, applied)
	}
	return out
}
