package httpapi

import (
	"net/http"
	"strconv"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// TODO(knowledge-legacy-compat): 历史平台级知识库与沉淀审核队列 HTTP 兼容层。
// 这些端点仅用于向后兼容旧版调用方与既有 E2E/单测（如 tokenOwner、TestPersonalWorkspaceAccess）。
// 新接入请使用用户级连接器 API（/api/knowledge/connectors*，见 knowledge.go）。
// 下线计划：确认外部调用方与上游 E2E 全部迁移至个人知识连接器且无存量队列依赖后，
// 移除本文件、server.go 中的 legacy 路由注册、project_write_access.go 中的 legacy 守卫，
// 以及 internal/{domain,repo,knowledge}/*legacy*.go 与 internal/repo/run_knowledge.go。

func (h *knowledgeHandlers) legacyService() *knowledge.LegacyService {
	if h != nil && h.legacy != nil {
		return h.legacy
	}
	if h != nil && h.deps != nil && h.deps.repo != nil {
		return knowledge.NewLegacyService(h.deps.repo, randomID)
	}
	return nil
}

func (h *knowledgeHandlers) canManageLegacy(r *http.Request) bool {
	return !authRequired() || h.isPlatformAdmin(r)
}

func (h *knowledgeHandlers) legacyActor(r *http.Request) string {
	if user, ok := h.requestUser(r, true); ok && user != nil && user.LoginName != "" {
		return user.LoginName
	}
	return "local"
}

type legacyKnowledgeSettingsView struct {
	domain.KnowledgeSettings
	Ready     bool   `json:"ready"`
	CanManage bool   `json:"canManage"`
	ProxyPath string `json:"proxyPath"`
	UsageRule string `json:"usageRule"`
}

// 路由: GET/PUT /api/knowledge/settings (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacySettings(c *gin.Context) {
	svc := h.legacyService()
	current := svc.Settings()
	if c.Request.Method == http.MethodPut {
		if !h.canManageLegacy(c.Request) {
			c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以修改知识库接入配置"})
			return
		}
		var input knowledge.LegacySettingsInput
		if !decode(c, &input) {
			return
		}
		saved, err := svc.UpdateSettings(input, h.legacyActor(c.Request))
		if err != nil {
			respond(c, err)
			return
		}
		current = saved
	}
	c.JSON(http.StatusOK, legacyKnowledgeSettingsView{
		KnowledgeSettings: current.Public(),
		Ready:             current.Ready(),
		CanManage:         h.canManageLegacy(c.Request),
		ProxyPath:         knowledge.ProxyPath,
		UsageRule:         knowledge.UsageRule().Content,
	})
}

// 路由: POST /api/knowledge/check (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacyCheck(c *gin.Context) {
	if !h.canManageLegacy(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以检查知识库连接"})
		return
	}
	var input struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	c.JSON(http.StatusOK, h.legacyService().Check(c.Request.Context(), input.URL, input.Token))
}

// 路由: GET /api/knowledge/sources (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacySources(c *gin.Context) {
	list, err := h.legacyService().Sources(c.Request.Context(), c.Query("projectId"))
	render(c, list, err)
}

// 路由: POST /api/knowledge/search (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacySearch(c *gin.Context) {
	var input struct {
		Query     string   `json:"query"`
		ProjectID string   `json:"projectId"`
		Sources   []string `json:"sources"`
	}
	if !decode(c, &input) {
		return
	}
	out, err := h.legacyService().Search(c.Request.Context(), input.ProjectID, input.Query, input.Sources)
	render(c, out, err)
}

// 路由: GET /api/knowledge/document (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacyDocument(c *gin.Context) {
	start, _ := strconv.Atoi(c.Query("start"))
	end, _ := strconv.Atoi(c.Query("end"))
	doc, err := h.legacyService().Document(c.Request.Context(), c.Query("projectId"), c.Query("source"), c.Query("path"), start, end)
	render(c, doc, err)
}

// 路由: GET /api/knowledge/candidates (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacyCandidates(c *gin.Context) {
	c.JSON(http.StatusOK, h.legacyService().Candidates(c.Query("projectId")))
}

// 路由: GET/POST /api/knowledge/submissions (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacySubmissions(c *gin.Context) {
	svc := h.legacyService()
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, svc.Submissions(c.Query("projectId"), c.Query("status")))
		return
	}
	var input knowledge.LegacySubmissionInput
	if !decode(c, &input) {
		return
	}
	item, err := svc.Submit(input, h.legacyActor(c.Request))
	render(c, item, err)
}

// 路由: GET/PUT/DELETE /api/knowledge/submissions/:id (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacySubmission(c *gin.Context) {
	svc := h.legacyService()
	id := c.Param("id")
	switch c.Request.Method {
	case http.MethodGet:
		item, err := svc.Submission(id)
		render(c, item, err)
	case http.MethodDelete:
		respond(c, svc.Delete(id))
	case http.MethodPut:
		if _, err := svc.Submission(id); err != nil {
			respond(c, err)
			return
		}
		var input knowledge.LegacySubmissionEdit
		if !decode(c, &input) {
			return
		}
		item, err := svc.Edit(id, input)
		render(c, item, err)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

// 路由: POST /api/knowledge/submissions/:id/publish (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacyPublish(c *gin.Context) {
	item, err := h.legacyService().Publish(c.Request.Context(), c.Param("id"), h.legacyActor(c.Request))
	render(c, item, err)
}

// 路由: POST /api/knowledge/submissions/:id/reject (TODO(knowledge-legacy-compat): deprecated)
func (h *knowledgeHandlers) legacyReject(c *gin.Context) {
	svc := h.legacyService()
	if _, err := svc.Submission(c.Param("id")); err != nil {
		respond(c, err)
		return
	}
	var input struct {
		ReviewNote string `json:"reviewNote"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	item, err := svc.Reject(c.Param("id"), input.ReviewNote, h.legacyActor(c.Request))
	render(c, item, err)
}
