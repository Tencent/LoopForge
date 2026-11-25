package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *assistantHandlers) runNodeConversation(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return
	}
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok || run.IsWorkspace() || run.HistoricalGroup() {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 运行不存在"})
		return
	}
	projectID := run.ExecutionContext.ProjectID
	if projectID == "" || !h.workbenchProjectAllowed(c, projectID, user.ID) ||
		!h.projectWriterAllowed(c, projectID, user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有可编辑此项目的成员可以继续节点对话"})
		return
	}
	var expected struct {
		Status      string `json:"status"`
		Attempt     *int   `json:"attempt"`
		ReworkCount *int   `json:"reworkCount"`
	}
	if c.Request.Method == http.MethodGet {
		attempt, attemptErr := strconv.Atoi(c.Query("attempt"))
		reworkCount, reworkErr := strconv.Atoi(c.Query("reworkCount"))
		if attemptErr == nil && reworkErr == nil {
			expected.Attempt, expected.ReworkCount = &attempt, &reworkCount
		}
		expected.Status = c.Query("status")
	} else if !decodeLimit(c, &expected, 4<<10) {
		return
	}
	if expected.Status == "" || expected.Attempt == nil || expected.ReworkCount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少节点状态或执行次数"})
		return
	}
	for index, definition := range run.WorkflowSnapshot.Nodes {
		if definition.ID != c.Param("nodeId") {
			continue
		}
		node := run.Nodes[definition.ID]
		if node == nil || !reviewableNodeStatus(node.Status) ||
			node.Status != expected.Status || node.Attempt != *expected.Attempt || node.ReworkCount != *expected.ReworkCount {
			c.JSON(http.StatusConflict, gin.H{"error": "节点状态或执行次数已变化，请刷新后重试"})
			return
		}
		ref := domain.AssistantRunNodeReview{NodeID: definition.ID, Attempt: node.Attempt, ReworkCount: node.ReworkCount}
		c.Header("Cache-Control", "no-store")
		if c.Request.Method == http.MethodGet {
			sessionID := repo.RunNodeAssistantSessionID(run.ID, ref)
			if session, found := h.repo.SharedAssistantSession(sessionID); found {
				c.JSON(http.StatusOK, gin.H{"id": session.ID})
			} else {
				c.Status(http.StatusNoContent)
			}
			return
		}
		root, err := h.ensureRunConversation(run, user.ID)
		if err == nil {
			title := "节点讨论 · " + nonEmptyNodeName(definition)
			var session domain.AssistantSession
			session, err = h.repo.EnsureRunNodeAssistantSession(root, ref, title, user.ID, runNodeConversationHistory(run, index, node))
			if err == nil {
				c.JSON(http.StatusOK, gin.H{"id": session.ID, "projectId": session.ProjectID})
				return
			}
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "打开节点对话失败"})
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "运行中没有该节点"})
}

func reviewableNodeStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "waiting_approval", "skipped", "canceled", "cancelled":
		return true
	}
	return false
}

func nonEmptyNodeName(definition domain.WorkflowNode) string {
	if definition.Name != "" {
		return definition.Name
	}
	return definition.ID
}

func runNodeConversationHistory(run domain.Run, index int, node *domain.NodeRun) []domain.AssistantMessage {
	definition := run.WorkflowSnapshot.Nodes[index]
	var input strings.Builder
	fmt.Fprintf(&input, "SOP 节点执行记录\n\n%s · %s · 第 %d 次执行\n\n运行：%s", run.WorkflowSnapshot.Name, nonEmptyNodeName(definition), node.Attempt, run.ID)
	fmt.Fprintf(&input, "\n\n节点状态：%s", node.Status)
	if run.Input != "" {
		fmt.Fprintf(&input, "\n\n原始任务\n\n%s", truncateRunes(run.Input, 4000))
	}
	if definition.TaskPrompt != "" {
		fmt.Fprintf(&input, "\n\n节点任务\n\n%s", truncateRunes(definition.TaskPrompt, 2400))
	}
	if node.Feedback != "" {
		fmt.Fprintf(&input, "\n\n本次补充说明\n\n%s", truncateRunes(node.Feedback, 4000))
	}
	upstream := make(map[string]bool)
	for _, edge := range run.WorkflowSnapshot.Edges {
		if edge.Target == definition.ID {
			upstream[edge.Source] = true
		}
	}
	if len(run.WorkflowSnapshot.Edges) == 0 && index > 0 {
		upstream[run.WorkflowSnapshot.Nodes[index-1].ID] = true
	}
	for _, source := range run.WorkflowSnapshot.Nodes {
		if prior := run.Nodes[source.ID]; upstream[source.ID] && prior != nil && prior.Output != "" {
			fmt.Fprintf(&input, "\n\n上游交接 · %s\n\n%s", nonEmptyNodeName(source), truncateRunes(prior.Output, 2400))
		}
	}
	startedAt := node.StartedAt
	if startedAt.IsZero() {
		startedAt = run.CreatedAt
	}
	history := []domain.AssistantMessage{{Role: "user", Content: truncateRunes(gitwork.Redact(input.String()), 16000), AuthorName: "SOP 运行", CreatedAt: startedAt}}
	var output strings.Builder
	output.WriteString(node.Output)
	if node.Error != "" {
		fmt.Fprintf(&output, "\n\n执行提示\n\n%s", node.Error)
	}
	if node.Status == "waiting_approval" && node.GateResult != nil {
		fmt.Fprintf(&output, "\n\n审核说明\n\n%s\n\n%s", node.GateResult.Reason, node.GateResult.ActionRequired)
	}
	var files []string
	for _, artifact := range run.Artifacts {
		if artifact.NodeID == definition.ID && artifact.Attempt == node.Attempt {
			files = append(files, "- "+strings.ReplaceAll(artifact.Path, "\n", " "))
		}
	}
	if len(files) > 0 {
		fmt.Fprintf(&output, "\n\n本次产物\n\n%s", strings.Join(files, "\n"))
	}
	if reply := strings.TrimSpace(output.String()); reply != "" {
		content, _ := json.Marshal(map[string]string{"message": truncateRunes(gitwork.Redact(reply), 24000)})
		finishedAt := node.FinishedAt
		if finishedAt.IsZero() {
			finishedAt = run.UpdatedAt
		}
		history = append(history, domain.AssistantMessage{Role: "assistant", Content: string(content), SpeakerName: nonEmptyNodeName(definition), CreatedAt: finishedAt})
	}
	return history
}
