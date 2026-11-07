package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"github.com/gin-gonic/gin"
)

type assistantControlScopeKey struct{}
type assistantControlScope struct {
	RunID, SessionID, GenerationID string
	MessageIndex                   int
}

func assistantControlScopeFrom(ctx context.Context) assistantControlScope {
	if ctx != nil {
		scope, _ := ctx.Value(assistantControlScopeKey{}).(assistantControlScope)
		return scope
	}
	return assistantControlScope{}
}

func (h *runsHandlers) runControl(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	if _, ok := h.projectRunUser(c, run.ExecutionContext.ProjectID); !ok {
		return
	}
	c.JSON(200, h.runControlPayload(run))
}
func (h *runsHandlers) runControlPayload(run domain.Run) gin.H {
	mode, reason := "sop", ""
	actions := []string{}
	switch {
	case run.IsWorkspace():
		mode = "workspace"
		reason = "对话工作区按需使用，不执行 SOP 节点"
	case run.HistoricalGroup():
		reason = "历史角色协作记录，没有可继续的 SOP 节点"
	default:
		for _, n := range run.Nodes {
			if n != nil && n.Status == "waiting_approval" {
				actions = append(actions, "runs_approve", "runs_reject")
				reason = "有节点等待人工审批"
				break
			}
		}
		if run.PauseRequested {
			actions = append(actions, "runs_resume")
			reason = "SOP 已暂停"
		}
	}
	return gin.H{"runId": run.ID, "projectId": run.ExecutionContext.ProjectID, "mode": mode, "status": run.Status, "pauseRequested": run.PauseRequested, "waitingReason": reason, "allowedActions": actions, "acceptance": run.WorkflowSnapshot.GroupCompletion, "notice": "节点执行完成不等于测试通过或用户验收；以实际记录为准"}
}
func (h *runsHandlers) rejectNonSOPControl(c *gin.Context, id string) bool {
	run, ok := h.repo.RunState(id)
	if !ok || (!run.IsWorkspace() && !run.HistoricalGroup()) {
		return false
	}
	if _, ok := h.projectRunUser(c, run.ExecutionContext.ProjectID); !ok {
		return true
	}
	c.JSON(409, gin.H{"code": "NON_SOP_RUN", "error": "该记录不执行 SOP 节点，请使用普通对话工作区或新建 SOP 运行", "control": h.runControlPayload(run)})
	return true
}
