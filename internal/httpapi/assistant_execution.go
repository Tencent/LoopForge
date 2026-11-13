package httpapi

import (
	"agentflow/internal/domain"
	"fmt"
	"github.com/gin-gonic/gin"
)

// A cloud runner in a saved draft was allocated by the server. Resolve the
// pool again when the first turn starts instead of treating it as client input.
func draftExecutionTarget(old domain.AssistantSession, requested *domain.AssistantExecutionTarget) *domain.AssistantExecutionTarget {
	if requested == nil {
		requested = old.ExecutionTarget
	}
	if requested != nil && old.ID != "" && *requested == old.Execution() && requested.Pool == domain.ExecutionModeCloud {
		return &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
	}
	return requested
}

func (h *assistantHandlers) newExecutionTarget(userID int64, projectID string, requested *domain.AssistantExecutionTarget) (*domain.AssistantExecutionTarget, error) {
	target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
	if requested != nil {
		target = *requested
	}
	switch target.Pool {
	case domain.ExecutionModeCloud:
		if target.RunnerID != "" {
			return nil, fmt.Errorf("云端机器由平台分配")
		}
		if h.engine != nil {
			var err error
			target.RunnerID, err = h.engine.ConversationCloudRunner(projectID)
			if err != nil {
				return nil, err
			}
		}
	case domain.ExecutionModeRunner:
		runner, ok := h.repo.Runner(target.RunnerID)
		if !ok || runner.OwnerUserID != userID {
			return nil, fmt.Errorf("无权使用此执行机器")
		}
		if !runner.SupportsConversationCLI() {
			return nil, fmt.Errorf("执行机器需升级至 %s 才能执行对话", domain.RunnerVersionConversationCLI)
		}
	default:
		return nil, fmt.Errorf("无效的执行机器")
	}
	return &target, nil
}

func (h *assistantHandlers) sessionMode(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	owner, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	saved, ok := h.repo.AssistantSession(owner, c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "会话不存在"})
		return
	}
	if g, ok := h.repo.AssistantGeneration(owner, saved.ID); ok && g.Status == "running" {
		c.JSON(409, gin.H{"error": "请先停止当前生成再切换模式"})
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if !decode(c, &body) {
		return
	}
	if body.Mode == domain.AssistantModeWorkspace {
		runID := saved.WorkspaceRunID
		if saved.RunID != "" {
			runID = saved.RunID
		}
		run, exists := h.repo.RunState(runID)
		if !exists || run.ExecutionContext.ExecutionRunnerID != saved.Execution().RunnerID {
			c.JSON(409, gin.H{"error": "工作区与会话不在同一执行机器"})
			return
		}
		if !run.IsWorkspace() && !sopStopped(run) {
			c.JSON(409, gin.H{"error": "请先暂停或接管 SOP，再进入工作区模式"})
			return
		}
		if h.engine == nil {
			c.JSON(409, gin.H{"error": "工作区引擎不可用"})
			return
		}
		request, err := h.engine.TrustedWorkspaceRequest(runID, domain.RunnerWorkspaceRequest{})
		if err != nil || request.Sandbox == nil || request.Sandbox.Profile.Driver != "docker" {
			c.JSON(409, gin.H{"error": "对话工作区仅支持 Docker"})
			return
		}
	}
	if err := h.repo.SetAssistantRuntimeMode(owner, saved.ID, saved.ProjectID, body.Mode); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	saved, _ = h.repo.AssistantSession(owner, saved.ID)
	c.JSON(200, saved)
}
