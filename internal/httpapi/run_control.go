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

// controlActor resolves the authenticated identity behind a run control
// request for the audit trail. Service-token and no-auth deployments resolve
// to the zero Actor, which records the decision as system-initiated.
func (h *runsHandlers) controlActor(c *gin.Context) domain.Actor {
	user, ok := h.deps.requestUser(c.Request, true)
	if !ok || user == nil || user.ID <= 0 {
		return domain.Actor{}
	}
	return domain.Actor{ID: user.ID, Name: user.LoginName, Admin: h.deps.isPlatformAdmin(c.Request)}
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
	waiting := []gin.H{}
	switch {
	case run.IsWorkspace():
		mode = "workspace"
		reason = "对话工作区按需使用，不执行 LOOP 节点"
	case run.HistoricalGroup():
		reason = "历史角色协作记录，没有可继续的 LOOP 节点"
	default:
		for _, n := range run.Nodes {
			if n == nil || n.Status != "waiting_approval" {
				continue
			}
			if len(waiting) == 0 {
				actions = append(actions, "runs_approve", "runs_reject")
				reason = "有节点等待人工审批"
			}
			waiting = append(waiting, h.waitingApprovalEntry(run, n.NodeID))
		}
		if run.PauseRequested {
			actions = append(actions, "runs_resume")
			reason = "LOOP 已暂停"
		}
	}
	return gin.H{"runId": run.ID, "projectId": run.ExecutionContext.ProjectID, "mode": mode, "status": run.Status, "pauseRequested": run.PauseRequested, "waitingReason": reason, "waitingApprovals": waiting, "allowedActions": actions, "acceptance": run.WorkflowSnapshot.GroupCompletion, "notice": "节点执行完成不等于测试通过或用户验收；以实际记录为准"}
}

// waitingApprovalEntry describes one node waiting for a human decision, so
// the UI can name the pinned approvers and disable actions for others.
func (h *runsHandlers) waitingApprovalEntry(run domain.Run, nodeID string) gin.H {
	entry := gin.H{"nodeId": nodeID, "nodeName": nodeID}
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID != nodeID {
			continue
		}
		if node.Name != "" {
			entry["nodeName"] = node.Name
		}
		if len(node.Approvers) == 0 {
			entry["openToAll"] = true
			break
		}
		approvers := make([]gin.H, 0, len(node.Approvers))
		for _, id := range node.Approvers {
			name := ""
			if user, ok := h.repo.User(id); ok {
				name = firstNonEmpty(user.ChineseName, user.LoginName)
			}
			approvers = append(approvers, gin.H{"id": id, "name": name})
		}
		entry["openToAll"] = false
		entry["approvers"] = approvers
		break
	}
	return entry
}
func (h *runsHandlers) rejectNonSOPControl(c *gin.Context, id string) bool {
	run, ok := h.repo.RunState(id)
	if !ok || (!run.IsWorkspace() && !run.HistoricalGroup()) {
		return false
	}
	if _, ok := h.projectRunUser(c, run.ExecutionContext.ProjectID); !ok {
		return true
	}
	c.JSON(409, gin.H{"code": "NON_SOP_RUN", "error": "该记录不执行 LOOP 节点，请使用普通对话工作区或新建 LOOP 运行", "control": h.runControlPayload(run)})
	return true
}
