package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *knowledgeHandlers) requireSharedAdmin(c *gin.Context) bool {
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	c.JSON(403, gin.H{"error": "只有平台管理员可以管理公共知识库"})
	return false
}

func (h *knowledgeHandlers) sharedBases(c *gin.Context) {
	if !h.requireSharedAdmin(c) {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decode(c, &input) {
		return
	}
	var owner int64
	if user, ok := h.requestUser(c.Request, true); ok {
		owner = user.ID
	}
	out, err := h.svc.CreateSharedBase(input.Name, owner)
	render(c, out, err)
}

func (h *knowledgeHandlers) canManageBindings(c *gin.Context, projectID string) bool {
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	if ok {
		if p, found := h.repo.Project(strings.TrimSpace(projectID)); found {
			if p.OwnerID == user.ID {
				return true
			}
			for _, m := range p.Members {
				if m.UserID == user.ID && strings.EqualFold(strings.TrimSpace(m.Role), "maintainer") {
					return true
				}
			}
		}
	}
	return false
}

func (h *knowledgeHandlers) sharedBindings(c *gin.Context) {
	if c.Request.Method == "GET" {
		out, err := h.svc.SharedBindings(c.Query("projectId"))
		render(c, out, err)
		return
	}
	var input struct {
		ProjectID string `json:"projectId"`
		BaseID    string `json:"baseId"`
		Enabled   bool   `json:"enabled"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.canManageBindings(c, input.ProjectID) {
		c.JSON(403, gin.H{"error": "只有项目 Owner、维护者或平台管理员可以修改公共知识库加载"})
		return
	}
	if _, ok := h.repo.Project(strings.TrimSpace(input.ProjectID)); !ok {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}
	if err := h.svc.SetSharedBinding(input.ProjectID, input.BaseID, input.Enabled); err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"updated": true})
}

func (h *knowledgeHandlers) requireManagementScope(c *gin.Context, project string) bool {
	if strings.TrimSpace(project) == "" {
		return h.requireSharedAdmin(c)
	}
	return true
}
