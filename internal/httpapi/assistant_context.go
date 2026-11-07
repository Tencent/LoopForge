package httpapi

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

func (h *assistantHandlers) validateSessionIssue(c *gin.Context, projectID, issueID string, userID int64) error {
	if issueID == "" {
		return nil
	}
	if projectID == "" || !h.workbenchProjectAllowed(c, projectID, userID) {
		return fmt.Errorf("请先选择有权限的项目，再关联需求")
	}
	issue, ok := h.repo.Issue(issueID)
	if !ok || issue.ProjectID != projectID {
		return fmt.Errorf("需求不存在或不属于当前项目")
	}
	return nil
}
func (h *assistantHandlers) sessionIssue(c *gin.Context) {
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
	s, ok := h.repo.AssistantSession(owner, c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "会话不存在"})
		return
	}
	var body struct {
		IssueID   string    `json:"issueId"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	if !decode(c, &body) {
		return
	}
	body.IssueID = strings.TrimSpace(body.IssueID)
	if err := h.validateSessionIssue(c, s.ProjectID, body.IssueID, user.ID); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	saved, err := h.repo.SetAssistantSessionIssue(owner, s.ID, body.IssueID, body.UpdatedAt)
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, saved)
}
func (h *assistantHandlers) issueConversations(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	issue, ok := h.repo.Issue(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "需求不存在"})
		return
	}
	if !h.workbenchProjectAllowed(c, issue.ProjectID, user.ID) {
		c.JSON(403, gin.H{"error": "无权访问项目"})
		return
	}
	c.Set("assistantIssueID", issue.ID)
	h.listSessions(c)
}
