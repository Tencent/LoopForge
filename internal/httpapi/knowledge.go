package httpapi

import (
	"net/http"
	"strconv"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// knowledgeHandlers exposes the 知识库 module over HTTP: upstream connection
// settings, trial search, node usage, the agent-facing read-only proxy
// (knowledge_proxy.go), and the review queue. Business rules live in
// knowledge.Service; handlers only do auth, decoding and rendering.
type knowledgeHandlers struct {
	*deps
	svc      *knowledge.Service
	activity *knowledgeActivityLog
}

func (h *knowledgeHandlers) canManage(r *http.Request) bool {
	return !authRequired() || h.isPlatformAdmin(r)
}

func (h *knowledgeHandlers) actor(r *http.Request) string {
	if user, ok := h.requestUser(r, true); ok && user != nil && user.LoginName != "" {
		return user.LoginName
	}
	return "local"
}

// render writes value on success or the mapped error otherwise.
func render[T any](c *gin.Context, value T, err error) {
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

type knowledgeSettingsView struct {
	domain.KnowledgeSettings
	Ready     bool   `json:"ready"`
	CanManage bool   `json:"canManage"`
	ProxyPath string `json:"proxyPath"`
	UsageRule string `json:"usageRule"`
}

// 路由: g.GET("/api/knowledge/settings", h.settings)
//
//	g.PUT("/api/knowledge/settings", h.settings)
func (h *knowledgeHandlers) settings(c *gin.Context) {
	current := h.svc.Settings()
	if c.Request.Method == http.MethodPut {
		if !h.canManage(c.Request) {
			c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以修改知识库接入配置"})
			return
		}
		var input knowledge.SettingsInput
		if !decode(c, &input) {
			return
		}
		saved, err := h.svc.UpdateSettings(input, h.actor(c.Request))
		if err != nil {
			respond(c, err)
			return
		}
		current = saved
	}
	c.JSON(http.StatusOK, knowledgeSettingsView{
		KnowledgeSettings: current.Public(), Ready: current.Ready(),
		CanManage: h.canManage(c.Request), ProxyPath: knowledge.ProxyPath,
		UsageRule: knowledge.UsageRule().Content,
	})
}

// 路由: g.POST("/api/knowledge/check", h.check)
func (h *knowledgeHandlers) check(c *gin.Context) {
	if !h.canManage(c.Request) {
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
	c.JSON(http.StatusOK, h.svc.Check(c.Request.Context(), input.URL, input.Token))
}

// 路由: g.GET("/api/knowledge/sources", h.sources)
func (h *knowledgeHandlers) sources(c *gin.Context) {
	list, err := h.svc.Sources(c.Request.Context(), c.Query("projectId"))
	render(c, list, err)
}

// 路由: g.POST("/api/knowledge/search", h.search)
func (h *knowledgeHandlers) search(c *gin.Context) {
	var input struct {
		Query     string   `json:"query"`
		ProjectID string   `json:"projectId"`
		Sources   []string `json:"sources"`
	}
	if !decode(c, &input) {
		return
	}
	out, err := h.svc.Search(c.Request.Context(), input.ProjectID, input.Query, input.Sources)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/document", h.document)
func (h *knowledgeHandlers) document(c *gin.Context) {
	start, _ := strconv.Atoi(c.Query("start"))
	end, _ := strconv.Atoi(c.Query("end"))
	doc, err := h.svc.Document(c.Request.Context(), c.Query("projectId"), c.Query("source"), c.Query("path"), start, end)
	render(c, doc, err)
}

// 路由: g.GET("/api/knowledge/usage", h.usage)
func (h *knowledgeHandlers) usage(c *gin.Context) {
	projectID := c.Query("projectId")
	agents, total := h.svc.Usage(projectID)
	activity := make([]knowledgeActivity, 0)
	for _, item := range h.activity.recent() {
		if projectID == "" || item.ProjectID == projectID {
			activity = append(activity, item)
		}
	}
	c.JSON(http.StatusOK, gin.H{"agents": agents, "totalAgents": total, "activity": activity})
}

// 路由: g.GET("/api/knowledge/candidates", h.candidates)
func (h *knowledgeHandlers) candidates(c *gin.Context) {
	items := h.svc.Candidates(c.Query("projectId"))
	visible := items[:0]
	for _, item := range items {
		if !h.personalWorkspaceRun(item.Artifact.RunID) {
			visible = append(visible, item)
		}
	}
	c.JSON(http.StatusOK, visible)
}

// Knowledge submissions are platform-wide, so personal workspace artifacts
// cannot safely enter that shared review queue.
func (h *knowledgeHandlers) personalWorkspaceRun(id string) bool {
	run, ok := h.repo.RunState(id)
	return ok && run.IsWorkspace() && run.ExecutionContext.ProjectID == ""
}

// An artifact-derived submission remains shared after its run is deleted.
// When its source can no longer be verified, do not expose it as public data.
func (h *knowledgeHandlers) restrictedKnowledgeSubmission(item domain.KnowledgeSubmission) bool {
	if item.RunID == "" {
		return item.ArtifactID != ""
	}
	run, ok := h.repo.RunState(item.RunID)
	return !ok || run.IsWorkspace() && run.ExecutionContext.ProjectID == ""
}

// 路由: g.GET("/api/knowledge/submissions", h.submissions)
//
//	g.POST("/api/knowledge/submissions", h.submissions)
func (h *knowledgeHandlers) submissions(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		items := h.svc.Submissions(c.Query("projectId"), c.Query("status"))
		visible := items[:0]
		for _, item := range items {
			if !h.restrictedKnowledgeSubmission(item) {
				visible = append(visible, item)
			}
		}
		c.JSON(http.StatusOK, visible)
		return
	}
	var input knowledge.SubmissionInput
	if !decode(c, &input) {
		return
	}
	if input.ArtifactID != "" {
		run, exists := h.repo.RunState(input.RunID)
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "运行产物不存在"})
			return
		}
		if run.IsWorkspace() && run.ExecutionContext.ProjectID == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "个人工作区产物不能提交到共享知识库"})
			return
		}
		if _, ok := h.repo.Artifact(input.RunID, input.ArtifactID); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "运行产物不存在"})
			return
		}
	}
	item, err := h.svc.Submit(input, h.actor(c.Request))
	render(c, item, err)
}

// 路由: g.GET("/api/knowledge/submissions/:id", h.submission)
//
//	g.PUT("/api/knowledge/submissions/:id", h.submission)
//	g.DELETE("/api/knowledge/submissions/:id", h.submission)
func (h *knowledgeHandlers) submission(c *gin.Context) {
	id := c.Param("id")
	if item, err := h.svc.Submission(id); err == nil && h.restrictedKnowledgeSubmission(item) {
		c.JSON(http.StatusForbidden, gin.H{"error": "个人工作区产物不能提交到共享知识库"})
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		item, err := h.svc.Submission(id)
		render(c, item, err)
	case http.MethodDelete:
		respond(c, h.svc.Delete(id))
	case http.MethodPut:
		if _, err := h.svc.Submission(id); err != nil {
			respond(c, err)
			return
		}
		var input knowledge.SubmissionEdit
		if !decode(c, &input) {
			return
		}
		item, err := h.svc.Edit(id, input)
		render(c, item, err)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

// 路由: g.POST("/api/knowledge/submissions/:id/publish", h.publish)
func (h *knowledgeHandlers) publish(c *gin.Context) {
	if item, err := h.svc.Submission(c.Param("id")); err == nil && h.restrictedKnowledgeSubmission(item) {
		c.JSON(http.StatusForbidden, gin.H{"error": "个人工作区产物不能提交到共享知识库"})
		return
	}
	item, err := h.svc.Publish(c.Request.Context(), c.Param("id"), h.actor(c.Request))
	render(c, item, err)
}

// 路由: g.POST("/api/knowledge/submissions/:id/reject", h.reject)
func (h *knowledgeHandlers) reject(c *gin.Context) {
	item, err := h.svc.Submission(c.Param("id"))
	if err != nil {
		respond(c, err)
		return
	}
	if h.restrictedKnowledgeSubmission(item) {
		c.JSON(http.StatusForbidden, gin.H{"error": "个人工作区产物不能提交到共享知识库"})
		return
	}
	var input struct {
		ReviewNote string `json:"reviewNote"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	item, err = h.svc.Reject(c.Param("id"), input.ReviewNote, h.actor(c.Request))
	render(c, item, err)
}
