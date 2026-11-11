package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// knowledgeHandlers exposes the 知识库 module over HTTP: trial search, node
// usage, the agent-facing read-only proxy (knowledge_proxy.go), the knowledge
// base management endpoints (knowledge_bases.go), and the review queue.
// Business rules live in knowledge.Service; handlers only do auth, decoding
// and rendering.
type knowledgeHandlers struct {
	*deps
	svc *knowledge.Service
}

func (h *knowledgeHandlers) canManage(r *http.Request) bool {
	return !authRequired() || h.isPlatformAdmin(r)
}

func (h *knowledgeHandlers) withGapActor(c *gin.Context) context.Context {
	ctx := c.Request.Context()
	actor := knowledge.GapActor{}
	if user, ok := h.requestUser(c.Request, true); ok && user != nil {
		actor.UserID = user.ID
	}
	return knowledge.WithGapActor(ctx, actor)
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

// 路由: g.POST("/api/knowledge/check", h.check)
func (h *knowledgeHandlers) check(c *gin.Context) {
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以检查知识库连接"})
		return
	}
	c.JSON(http.StatusOK, h.svc.Check(c.Request.Context()))
}

// 路由: g.GET("/api/knowledge/sources", h.sources)
func (h *knowledgeHandlers) sources(c *gin.Context) {
	if !h.requireKnowledgeReader(c, c.Query("projectId")) {
		return
	}
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
	if !h.requireKnowledgeReader(c, input.ProjectID) {
		return
	}
	out, err := h.svc.Search(h.withGapActor(c), input.ProjectID, input.Query, input.Sources)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/document", h.document)
func (h *knowledgeHandlers) document(c *gin.Context) {
	if !h.requireKnowledgeReader(c, c.Query("projectId")) {
		return
	}
	start, _ := strconv.Atoi(c.Query("start"))
	end, _ := strconv.Atoi(c.Query("end"))
	doc, err := h.svc.Document(c.Request.Context(), c.Query("projectId"), c.Query("source"), c.Query("path"), start, end)
	render(c, doc, err)
}

// 路由: g.GET("/api/knowledge/usage", h.usage)
func (h *knowledgeHandlers) usage(c *gin.Context) {
	if !h.requireKnowledgeReader(c, c.Query("projectId")) {
		return
	}
	projectID := c.Query("projectId")
	agents, total := h.svc.Usage(projectID)
	c.JSON(http.StatusOK, gin.H{"agents": agents, "totalAgents": total, "activity": h.svc.Activities(projectID, knowledge.ActivityLimit)})
}

// 路由: g.GET("/api/knowledge/candidates", h.candidates)
func (h *knowledgeHandlers) candidates(c *gin.Context) {
	if !h.requireKnowledgeReader(c, c.Query("projectId")) {
		return
	}
	c.JSON(http.StatusOK, h.svc.Candidates(c.Query("projectId")))
}

// 路由: g.GET("/api/knowledge/submissions", h.submissions)
//
//	g.POST("/api/knowledge/submissions", h.submissions)
func (h *knowledgeHandlers) submissions(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		project := strings.TrimSpace(c.Query("projectId"))
		if project != "" && !h.requireKnowledgeReader(c, project) {
			return
		}
		items := h.svc.Submissions(project, c.Query("status"))
		visible := make([]domain.KnowledgeSubmission, 0, len(items))
		for _, item := range items {
			if h.canReadKnowledgeSubmission(c, item) {
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
		art, ok := h.repo.Artifact(input.RunID, input.ArtifactID)
		if ok {
			if run, ok := h.repo.Run(art.RunID); ok {
				input.ProjectID = run.ExecutionContext.ProjectID
			}
		}
	}
	if strings.TrimSpace(input.ProjectID) != "" && !h.requireKnowledgeManager(c, input.ProjectID) {
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
	if !h.requireSubmissionAccess(c, id, c.Request.Method != http.MethodGet) {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		item, err := h.svc.SubmissionForRead(id)
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
	if !h.requireSubmissionAccess(c, c.Param("id"), true) {
		return
	}
	item, err := h.svc.Publish(c.Request.Context(), c.Param("id"), h.actor(c.Request))
	render(c, item, err)
}

// 路由: g.POST("/api/knowledge/submissions/:id/reject", h.reject)
func (h *knowledgeHandlers) reject(c *gin.Context) {
	if !h.requireSubmissionAccess(c, c.Param("id"), true) {
		return
	}
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

func (h *knowledgeHandlers) canReadKnowledgeSubmission(c *gin.Context, item domain.KnowledgeSubmission) bool {
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	if strings.TrimSpace(item.ProjectID) != "" {
		return h.canReadKnowledgeProject(c, item.ProjectID)
	}
	return item.CreatedBy != "" && item.CreatedBy == h.actor(c.Request)
}

func (h *knowledgeHandlers) requireSubmissionAccess(c *gin.Context, id string, write bool) bool {
	item, err := h.svc.Submission(id)
	if err != nil {
		respond(c, err)
		return false
	}
	if write {
		return h.requireKnowledgeManager(c, item.ProjectID)
	}
	if h.canReadKnowledgeSubmission(c, item) {
		return true
	}
	c.JSON(403, gin.H{"error": "无权读取该审核条目"})
	return false
}
