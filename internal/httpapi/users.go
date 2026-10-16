package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type usersHandlers struct {
	*deps
}

func (h *usersHandlers) users(c *gin.Context) {
	if h.repo == nil {
		c.JSON(http.StatusOK, []domain.User{})
		return
	}
	c.JSON(http.StatusOK, domain.PublicUsers(h.repo.Users()))
}

func (h *usersHandlers) user(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, map[string]string{"error": "只能配置当前登录用户的凭据"})
		return
	}
	var body repo.UserCredentialUpdate
	if !decode(c, &body) {
		return
	}
	for _, raw := range []*string{body.CodexBaseURL, body.ClaudeCodeBaseURL} {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(*raw))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(*raw) > 2048 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "Base URL 必须为 HTTP(S) 服务地址，不能包含账号密码、查询参数或片段"})
			return
		}
	}
	saved, err := h.repo.UpdateUserCredentials(id, body)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, domain.PublicUser(saved))
}

func (h *usersHandlers) platformAdmins(c *gin.Context) {
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, map[string]string{"error": "只有平台管理员可以管理该名单"})
		return
	}
	if h.repo == nil {
		c.JSON(http.StatusOK, []repo.PlatformAdminRecord{})
		return
	}
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, h.repo.ListPlatformAdmins())
		return
	}
	var req struct {
		LoginName string `json:"loginName"`
		Role      string `json:"role"`
	}
	if !decode(c, &req) {
		return
	}
	login := strings.TrimSpace(req.LoginName)
	if login == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "loginName is required"})
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role == "" || role == "none" || role == "revoke" {
		if err := h.repo.RevokePlatformAdmin(login); err != nil {
			respond(c, err)
			return
		}
		c.JSON(http.StatusOK, h.repo.ListPlatformAdmins())
		return
	}
	if role != domain.PlatformRoleAdmin {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "role 只支持 admin 或空（撤销）"})
		return
	}
	if _, err := h.repo.GrantPlatformAdmin(login); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, h.repo.ListPlatformAdmins())
}
