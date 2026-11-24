package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func (h *projectsHandlers) joinRequestUser(c *gin.Context) (int64, bool) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return 0, false
	}
	return user.ID, true
}

// joinRoleGrantable limits which role a reviewer may grant: applicants and
// maintainers can only ask for or grant member and viewer; owners and
// platform admins may also grant maintainer.
func joinRoleGrantable(p domain.Project, userID int64, admin bool, role string) bool {
	if role == domain.ProjectRoleMember || role == domain.ProjectRoleViewer {
		return true
	}
	return role == domain.ProjectRoleMaintainer && (admin || p.OwnerID == userID)
}

func (h *projectsHandlers) createJoinRequest(c *gin.Context) {
	uid, ok := h.joinRequestUser(c)
	if !ok {
		return
	}
	p, found := h.repo.Project(c.Param("id"))
	if !found || p.ArchivedAt != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在或已删除"})
		return
	}
	if p.OwnerID == 0 && len(p.Members) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该项目对所有人开放，无需申请"})
		return
	}
	var body struct {
		Role   string `json:"role"`
		Reason string `json:"reason"`
	}
	if !decode(c, &body) {
		return
	}
	role, valid := domain.NormalizeProjectMemberRole(body.Role)
	if !valid || !joinRoleGrantable(p, uid, false, role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只能申请成员或只读角色"})
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if len([]rune(reason)) > domain.ProjectJoinReasonMaxRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "申请理由不能超过 500 字"})
		return
	}
	v, err := h.repo.CreateProjectJoinRequest(p.ID, uid, role, reason)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// joinRequests lists the caller's own requests plus every request of the
// projects the caller reviews. Query: projectId, status.
func (h *projectsHandlers) joinRequests(c *gin.Context) {
	uid, ok := h.joinRequestUser(c)
	if !ok {
		return
	}
	var projectIDs []string
	if id := strings.TrimSpace(c.Query("projectId")); id != "" {
		projectIDs = []string{id}
	}
	status := strings.TrimSpace(c.Query("status"))
	all, err := h.repo.ProjectJoinRequests(projectIDs, 0, status)
	if err != nil {
		respond(c, err)
		return
	}
	admin := h.isPlatformAdmin(c.Request)
	reviews := map[string]bool{}
	out := make([]domain.ProjectJoinRequest, 0, len(all))
	for _, v := range all {
		allowed, seen := reviews[v.ProjectID]
		if !seen {
			p, found := h.repo.Project(v.ProjectID)
			allowed = admin || found && domain.CanReviewProjectJoin(p, uid)
			reviews[v.ProjectID] = allowed
		}
		if allowed || v.UserID == uid {
			out = append(out, v)
		}
	}
	c.JSON(http.StatusOK, out)
}

func (h *projectsHandlers) reviewJoinRequest(c *gin.Context, approve bool) {
	uid, ok := h.joinRequestUser(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	v, found := h.repo.ProjectJoinRequest(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "申请不存在"})
		return
	}
	p, found := h.repo.Project(v.ProjectID)
	admin := h.isPlatformAdmin(c.Request)
	if !found || !admin && !domain.CanReviewProjectJoin(p, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有项目 Owner、维护者或平台管理员可以审批加入申请"})
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if c.Request.ContentLength != 0 && !decode(c, &body) {
		return
	}
	role := v.Role
	if strings.TrimSpace(body.Role) != "" {
		var valid bool
		if role, valid = domain.NormalizeProjectMemberRole(body.Role); !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "角色只能是 maintainer、member 或 viewer"})
			return
		}
	}
	if approve && !joinRoleGrantable(p, uid, admin, role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有项目 Owner 或平台管理员可以授予维护者角色"})
		return
	}
	v, err := h.repo.ReviewProjectJoinRequest(id, uid, admin, approve, role)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *projectsHandlers) approveJoinRequest(c *gin.Context) { h.reviewJoinRequest(c, true) }

func (h *projectsHandlers) rejectJoinRequest(c *gin.Context) { h.reviewJoinRequest(c, false) }

func (h *projectsHandlers) withdrawJoinRequest(c *gin.Context) {
	uid, ok := h.joinRequestUser(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	v, err := h.repo.WithdrawProjectJoinRequest(id, uid)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
