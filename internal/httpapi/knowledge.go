package httpapi

import (
	"context"
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
	c.JSON(http.StatusOK, h.svc.Candidates(c.Query("projectId")))
}

// 路由: g.GET("/api/knowledge/submissions", h.submissions)
//
//	g.POST("/api/knowledge/submissions", h.submissions)
func (h *knowledgeHandlers) submissions(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, h.svc.Submissions(c.Query("projectId"), c.Query("status")))
		return
	}
	var input knowledge.SubmissionInput
	if !decode(c, &input) {
		return
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
	item, err := h.svc.Publish(c.Request.Context(), c.Param("id"), h.actor(c.Request))
	render(c, item, err)
}

// 路由: g.POST("/api/knowledge/submissions/:id/reject", h.reject)
func (h *knowledgeHandlers) reject(c *gin.Context) {
	if _, err := h.svc.Submission(c.Param("id")); err != nil {
		respond(c, err)
		return
	}
	var input struct {
		ReviewNote string `json:"reviewNote"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	item, err := h.svc.Reject(c.Param("id"), input.ReviewNote, h.actor(c.Request))
	render(c, item, err)
}

// referenceOptions returns only identifiers and display names to the editor.
func (h *knowledgeHandlers) referenceOptions(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	project := c.Query("projectId")
	if project == "" {
		c.JSON(400, gin.H{"error": "请选择项目"})
		return
	}
	if !h.requireProjectWriter(c, project) {
		return
	}
	if c.Query("sources") == "true" {
		items, err := h.svc.Sources(c.Request.Context(), project)
		if err != nil {
			render(c, items, err)
			return
		}
		allowed := []gin.H{}
		for _, s := range items {
			if s.Allowed {
				allowed = append(allowed, gin.H{"id": s.ID, "name": s.Name})
			}
		}
		c.JSON(200, allowed)
		return
	}
	options := []gin.H{}
	if h.repo.KnowledgeSettings().Ready() {
		options = append(options, gin.H{"id": domain.KnowledgeMCPServerID, "name": "团队知识库", "scoped": true})
	}
	for _, s := range assistantOutboundMCPServers(h.repo.MCPServers(), project, user.ID) {
		if s.Transport == "http" && s.ID != domain.KnowledgeMCPServerID {
			options = append(options, gin.H{"id": s.ID, "name": s.Name})
		}
	}
	c.JSON(200, options)
}

func (h *knowledgeHandlers) checkReferences(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	project := c.Query("projectId")
	var body struct {
		Refs []domain.KnowledgeRef `json:"knowledgeRefs"`
	}
	if !decode(c, &body) {
		return
	}
	if err := domain.ValidateKnowledgeRefs(body.Refs); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), knowledge.CallTimeout)
	defer cancel()
	for _, ref := range body.Refs {
		if ctx.Err() != nil {
			c.JSON(504, gin.H{"error": "知识服务探测超时"})
			return
		}
		tools, _, err := referenceTools(ctx, h.deps, project, user.ID, ref)
		if err != nil || len(tools) == 0 {
			c.JSON(400, gin.H{"error": ref.ServerID + "：当前身份无法发现可用只读工具"})
			return
		}
		if ref.ServerID == domain.KnowledgeMCPServerID && len(ref.Sources) > 0 {
			sources, err := h.svc.Sources(ctx, project)
			if err != nil {
				c.JSON(400, gin.H{"error": "无法校验团队知识库来源"})
				return
			}
			allowed := map[string]bool{}
			for _, s := range sources {
				allowed[s.ID] = s.Allowed
			}
			for _, id := range ref.Sources {
				if !allowed[id] {
					c.JSON(400, gin.H{"error": "知识库来源失效或未授权"})
					return
				}
			}
		}
	}
	c.Status(204)
}
