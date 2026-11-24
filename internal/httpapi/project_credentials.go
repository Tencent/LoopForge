package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

type projectCredentialsHandlers struct {
	*deps
}

func (d deps) projectCredentialAccess(req *http.Request, projectID string, userID int64) (use, manage bool) {
	project, ok := d.repo.Project(projectID)
	if !ok || project.ID != projectID {
		return false, false
	}
	return domain.ProjectCredentialAccess(project, userID, d.isPlatformAdmin(req))
}

func (h *projectCredentialsHandlers) authorize(c *gin.Context, manage bool) (string, bool, bool) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后访问项目凭据"})
		return "", false, false
	}
	id := strings.TrimSpace(c.Param("id"))
	use, canManage := h.projectCredentialAccess(c.Request, id, user.ID)
	if !use || (manage && !canManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问或管理该项目的凭据"})
		return "", false, false
	}
	c.Header("Cache-Control", "no-store")
	return id, canManage, true
}

func (h *projectCredentialsHandlers) list(c *gin.Context) {
	projectID, canManage, ok := h.authorize(c, false)
	if !ok {
		return
	}
	items, err := h.repo.ProjectCredentials(c.Request.Context(), projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取项目凭据失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"credentials": items, "canManage": canManage})
}

func (h *projectCredentialsHandlers) save(c *gin.Context) {
	projectID, _, ok := h.authorize(c, true)
	if !ok {
		return
	}
	var input domain.ProjectCredentialInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512*1024)
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目凭据格式无效或超过大小限制"})
		return
	}
	id := c.Param("credentialId")
	create := c.Request.Method == http.MethodPost
	if create {
		id = randomID()
	}
	credential, err := h.repo.SaveProjectCredential(c.Request.Context(), projectID, id, create, input)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, repo.ErrProjectCredentialKey):
			status = http.StatusServiceUnavailable
		case errors.Is(err, repo.ErrProjectCredentialUnavailable):
			status = http.StatusNotFound
		case errors.Is(err, repo.ErrProjectCredentialName):
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	c.JSON(status, credential)
}

func (h *projectCredentialsHandlers) delete(c *gin.Context) {
	projectID, _, ok := h.authorize(c, true)
	if !ok {
		return
	}
	if err := h.repo.DeleteProjectCredential(c.Request.Context(), projectID, c.Param("credentialId")); err != nil {
		if errors.Is(err, repo.ErrProjectCredentialUnavailable) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除项目凭据失败"})
		return
	}
	c.Status(http.StatusNoContent)
}
