package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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

// auditWrite records one management mutation on both the structured log and
// the usage activity feed, so operators can see who changed what upstream.
func (h *knowledgeHandlers) auditWrite(c *gin.Context, action, connID, targetID, projectID string, err error) {
	status := "ok"
	if err != nil {
		status = "failed"
	}
	actor := h.actor(c.Request)
	attrs := []any{
		"event", "knowledge.management.audit",
		"actor", actor,
		"actorId", actor,
		"actorUsername", actor,
		"projectId", projectID,
		"connectionId", connID,
		"action", action,
		"targetId", targetID,
		"status", status,
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
		slog.WarnContext(c.Request.Context(), "knowledge management audit", attrs...)
	} else {
		slog.InfoContext(c.Request.Context(), "knowledge management audit", attrs...)
	}
	h.activity.add(knowledgeActivity{
		ServerID:  connID,
		Origin:    "admin_console",
		At:        time.Now().UTC(),
		Tool:      "admin:" + action,
		ProjectID: projectID,
		Query:     targetID,
		OK:        err == nil,
		Error:     errorText(err),
	})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// auditTarget names the created resource, or the addressed parent on failure.
func auditTarget(created, fallback string) string {
	if created = strings.TrimSpace(created); created != "" {
		return created
	}
	return fallback
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
	Ready              bool   `json:"ready"`
	CanManage          bool   `json:"canManage"`
	CanWrite           bool   `json:"canWrite"`
	ProxyPath          string `json:"proxyPath"`
	UsageRule          string `json:"usageRule"`
	DetectedWeKnoraURL string `json:"detectedWeKnoraUrl,omitempty"`
	WeKnoraUIURL       string `json:"weKnoraUiUrl,omitempty"`
}

// weKnoraConsoleCredentialsView carries the console sign-in only for the
// platform-managed WeKnora instance; it is served to platform administrators on
// demand instead of riding along with /api/knowledge/settings.
type weKnoraConsoleCredentialsView struct {
	Configured bool   `json:"configured"`
	Email      string `json:"email,omitempty"`
	Password   string `json:"password,omitempty"`
	Source     string `json:"source,omitempty"`
	UIURL      string `json:"uiUrl,omitempty"`
	Reason     string `json:"reason,omitempty"`
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
		KnowledgeSettings:  current.Public(),
		Ready:              current.Ready(),
		CanManage:          h.canManage(c.Request),
		CanWrite:           h.canWriteKnowledge(c, c.Query("projectId")),
		ProxyPath:          knowledge.ProxyPath,
		UsageRule:          knowledge.UsageRule().Content,
		DetectedWeKnoraURL: strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_URL")),
		WeKnoraUIURL:       strings.TrimSpace(os.Getenv("LOOPFORGE_WEKNORA_UI_URL")),
	})
}

// 路由: g.POST("/api/knowledge/check", h.check)
func (h *knowledgeHandlers) check(c *gin.Context) {
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以检查知识库连接"})
		return
	}
	var input struct {
		Provider     string `json:"provider"`
		URL          string `json:"url"`
		Token        string `json:"token"`
		ConnectionID string `json:"connectionId"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	c.JSON(http.StatusOK, h.svc.CheckConnection(c.Request.Context(), input.ConnectionID, input.Provider, input.URL, input.Token))
}

// 路由: g.GET("/api/knowledge/connections/:id/capabilities", h.connectionCapabilities)
func (h *knowledgeHandlers) connectionCapabilities(c *gin.Context) {
	refresh := strings.TrimSpace(c.Query("refresh"))
	force := refresh == "1" || strings.EqualFold(refresh, "true")
	// 强制刷新会让缓存与在途探测失效并立刻重发 6 个上游探针，只有能做接入
	// 配置的平台管理员才允许放大这一次调用；普通登录用户仍可读缓存结果。
	if force && !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以强制刷新知识库连接能力"})
		return
	}
	caps, err := h.svc.WeKnoraCapabilities(c.Request.Context(), c.Param("id"), force)
	render(c, caps, err)
}

// 路由: g.POST("/api/knowledge/weknora/provision", h.provisionWeKnora)
func (h *knowledgeHandlers) provisionWeKnora(c *gin.Context) {
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以启用或配置 WeKnora 实例"})
		return
	}
	var input knowledge.WeKnoraProvisionInput
	if !decode(c, &input) {
		return
	}
	res, err := h.svc.ProvisionWeKnora(c.Request.Context(), input, h.actor(c.Request))
	render(c, res, err)
}

// 路由: g.GET("/api/knowledge/weknora/console-credentials", h.weKnoraConsoleCredentials)
func (h *knowledgeHandlers) weKnoraConsoleCredentials(c *gin.Context) {
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以查看 WeKnora 控制台登录凭据"})
		return
	}
	login := h.svc.ManagedWeKnoraConsole(strings.TrimSpace(c.Query("connectionId")))
	c.Header("Cache-Control", "no-store")
	view := weKnoraConsoleCredentialsView{
		Configured: login.Configured,
		Email:      login.Email,
		UIURL:      login.UIURL,
	}
	if !login.Configured {
		view.Reason = "not_managed"
		c.JSON(http.StatusOK, view)
		return
	}
	view.Password = login.Password
	view.Source = login.Source
	slog.InfoContext(c.Request.Context(), "knowledge console credentials",
		"event", "knowledge.console.credentials.reveal",
		"actor", h.actor(c.Request),
		"source", login.Source,
	)
	c.JSON(http.StatusOK, view)
}

// 路由: g.DELETE("/api/knowledge/connections/:id", h.deleteConnection)
func (h *knowledgeHandlers) deleteConnection(c *gin.Context) {
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以删除知识库连接"})
		return
	}
	saved, err := h.svc.DeleteConnection(c.Param("id"), h.actor(c.Request))
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, saved.Public())
}

// 路由: g.GET("/api/knowledge/sources", h.sources)
func (h *knowledgeHandlers) sources(c *gin.Context) {
	ctx := c.Request.Context()
	projectID := c.Query("projectId")
	var (
		list []knowledge.SourceView
		err  error
	)
	if connID := strings.TrimSpace(c.Query("connectionId")); connID != "" {
		// An explicit connection may be disabled: the console still manages it,
		// while the default view and the Agent/MCP runtime only use active
		// connections.
		list, err = h.svc.SourcesForConnection(ctx, projectID, connID)
	} else {
		list, err = h.svc.Sources(ctx, projectID)
	}
	render(c, list, err)
}

// scopedConnectionID prefers the explicit connectionId query parameter, then
// the "connID::" prefix of a composite path ID, so audits name the connection.
func scopedConnectionID(connectionID, pathID string) string {
	if connectionID = strings.TrimSpace(connectionID); connectionID != "" {
		return connectionID
	}
	if idx := strings.Index(pathID, "::"); idx > 0 {
		return pathID[:idx]
	}
	return ""
}

// 路由: g.GET("/api/knowledge/sources/:id", h.source)
func (h *knowledgeHandlers) source(c *gin.Context) {
	kb, err := h.svc.GetWeKnoraKB(c.Request.Context(), c.Query("projectId"), c.Query("connectionId"), c.Param("id"))
	render(c, kb, err)
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

// 路由: g.GET("/api/knowledge/sources/:id/documents", h.documents)
func (h *knowledgeHandlers) documents(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize"))
	out, err := h.svc.Documents(c.Request.Context(), c.Query("projectId"), c.Query("connectionId"), c.Param("id"), c.Query("keyword"), page, pageSize)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/documents/:docId", h.documentDetail)
func (h *knowledgeHandlers) documentDetail(c *gin.Context) {
	doc, err := h.svc.GetWeKnoraDocument(c.Request.Context(), c.Query("projectId"), c.Query("connectionId"), c.Param("docId"))
	render(c, doc, err)
}

// 路由: g.GET("/api/knowledge/documents/:docId/chunks", h.documentChunks)
func (h *knowledgeHandlers) documentChunks(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize"))
	out, err := h.svc.ListWeKnoraChunks(c.Request.Context(), c.Query("projectId"), c.Query("connectionId"), c.Param("docId"), page, pageSize)
	render(c, out, err)
}

// 路由: g.GET("/api/knowledge/documents/:docId/preview", h.documentPreview)
func (h *knowledgeHandlers) documentPreview(c *gin.Context) {
	h.streamDocument(c, h.svc.OpenWeKnoraPreview)
}

// 路由: g.GET("/api/knowledge/documents/:docId/download", h.documentDownload)
func (h *knowledgeHandlers) documentDownload(c *gin.Context) {
	h.streamDocument(c, h.svc.OpenWeKnoraDownload)
}

type documentOpener func(context.Context, string, string, string) (*knowledge.StreamResponse, error)

// streamDocument forwards an upstream document body verbatim. The client has
// already downgraded dangerous MIME types; these headers additionally stop a
// stored HTML/SVG file from being sniffed or cached by the browser, and the
// exact Content-Length survives because gzip is skipped for these paths.
func (h *knowledgeHandlers) streamDocument(c *gin.Context, open documentOpener) {
	res, err := open(c.Request.Context(), c.Query("projectId"), c.Query("connectionId"), c.Param("docId"))
	if err != nil {
		respond(c, err)
		return
	}
	defer res.Body.Close()
	contentType := strings.TrimSpace(res.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	if res.ContentLength > 0 {
		c.Header("Content-Length", strconv.FormatInt(res.ContentLength, 10))
	}
	if res.ContentDisposition != "" {
		c.Header("Content-Disposition", res.ContentDisposition)
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, res.Body); err != nil {
		slog.WarnContext(c.Request.Context(), "knowledge document stream interrupted", "docId", c.Param("docId"), "error", err)
	}
}

func (h *knowledgeHandlers) requireKnowledgeWriter(c *gin.Context) (string, bool) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if projectID != "" {
		if !h.requireProjectWriter(c, projectID) {
			return "", false
		}
		return projectID, true
	}
	if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员或项目成员可以执行该操作"})
		return "", false
	}
	return "", true
}

// canWriteKnowledge reports whether the caller may mutate knowledge resources
// for projectID without writing a response. It mirrors requireKnowledgeWriter
// so the console's write gate cannot drift from the API gate.
func (h *knowledgeHandlers) canWriteKnowledge(c *gin.Context, projectID string) bool {
	if !authRequired() {
		return true
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return h.canManage(c.Request)
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok || h.isPlatformAdmin(c.Request) {
		return true
	}
	if user == nil {
		return false
	}
	return h.projectWriterAllowed(c, projectID, user.ID)
}

// 路由: g.GET("/api/knowledge/models", h.models)
func (h *knowledgeHandlers) models(c *gin.Context) {
	list, err := h.svc.WeKnoraModels(c.Request.Context(), c.Query("connectionId"))
	render(c, list, err)
}

// 路由: g.POST("/api/knowledge/models", h.createModel)
func (h *knowledgeHandlers) createModel(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input knowledge.WeKnoraCreateModelInput
	if !decode(c, &input) {
		return
	}
	if input.ConnectionID == "" {
		input.ConnectionID = strings.TrimSpace(c.Query("connectionId"))
	}
	model, err := h.svc.CreateWeKnoraModel(c.Request.Context(), input)
	h.auditWrite(c, "model.create", input.ConnectionID, auditTarget(model.ID, strings.TrimSpace(input.Name)), projectID, err)
	render(c, model, err)
}

// 路由: g.POST("/api/knowledge/sources", h.createSource)
func (h *knowledgeHandlers) createSource(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input knowledge.WeKnoraCreateKBInput
	if !decode(c, &input) {
		return
	}
	kb, err := h.svc.CreateWeKnoraKB(c.Request.Context(), projectID, input)
	h.auditWrite(c, "kb.create", input.ConnectionID, auditTarget(kb.ID, strings.TrimSpace(input.Name)), projectID, err)
	render(c, kb, err)
}

// 路由: g.PUT("/api/knowledge/sources/:id", h.updateSource)
func (h *knowledgeHandlers) updateSource(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decode(c, &input) {
		return
	}
	kb, err := h.svc.UpdateWeKnoraKB(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("id"), input.Name, input.Description)
	h.auditWrite(c, "kb.update", scopedConnectionID(c.Query("connectionId"), c.Param("id")), c.Param("id"), projectID, err)
	render(c, kb, err)
}

// 路由: g.DELETE("/api/knowledge/sources/:id", h.deleteSource)
func (h *knowledgeHandlers) deleteSource(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	err := h.svc.DeleteWeKnoraKB(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("id"))
	h.auditWrite(c, "kb.delete", scopedConnectionID(c.Query("connectionId"), c.Param("id")), c.Param("id"), projectID, err)
	if err != nil {
		respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// 路由: g.PATCH("/api/knowledge/sources/:id", h.patchSource)
func (h *knowledgeHandlers) patchSource(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	if input.Name == nil && input.Description == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请至少提供 name 或 description 之一"})
		return
	}
	kb, err := h.svc.PatchWeKnoraKB(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("id"), input.Name, input.Description)
	h.auditWrite(c, "kb.update", scopedConnectionID(c.Query("connectionId"), c.Param("id")), c.Param("id"), projectID, err)
	render(c, kb, err)
}

// 路由: g.POST("/api/knowledge/sources/:id/url", h.createFromURL)
func (h *knowledgeHandlers) createFromURL(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if !decode(c, &input) {
		return
	}
	doc, err := h.svc.CreateWeKnoraURLDoc(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("id"), input.URL)
	h.auditWrite(c, "doc.url", scopedConnectionID(c.Query("connectionId"), c.Param("id")), auditTarget(doc.ID, c.Param("id")), projectID, err)
	render(c, doc, err)
}

// 路由: g.POST("/api/knowledge/sources/:id/manual", h.createManual)
func (h *knowledgeHandlers) createManual(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Publish bool   `json:"publish"`
	}
	if !decode(c, &input) {
		return
	}
	doc, err := h.svc.CreateWeKnoraManualDoc(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("id"), input.Title, input.Content, input.Publish)
	h.auditWrite(c, "doc.create", scopedConnectionID(c.Query("connectionId"), c.Param("id")), auditTarget(doc.ID, c.Param("id")), projectID, err)
	render(c, doc, err)
}

// 路由: g.PUT("/api/knowledge/documents/:docId", h.updateDocument)
func (h *knowledgeHandlers) updateDocument(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	var input struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Publish bool   `json:"publish"`
	}
	if !decode(c, &input) {
		return
	}
	doc, err := h.svc.UpdateWeKnoraManualDoc(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("docId"), input.Title, input.Content, input.Publish)
	h.auditWrite(c, "doc.update", scopedConnectionID(c.Query("connectionId"), c.Param("docId")), c.Param("docId"), projectID, err)
	render(c, doc, err)
}

// 路由: g.POST("/api/knowledge/documents/:docId/reparse", h.reparseDocument)
func (h *knowledgeHandlers) reparseDocument(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	err := h.svc.ReparseWeKnoraDoc(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("docId"))
	h.auditWrite(c, "doc.reparse", scopedConnectionID(c.Query("connectionId"), c.Param("docId")), c.Param("docId"), projectID, err)
	if err != nil {
		respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// 路由: g.DELETE("/api/knowledge/documents/:docId", h.deleteDocument)
func (h *knowledgeHandlers) deleteDocument(c *gin.Context) {
	projectID, ok := h.requireKnowledgeWriter(c)
	if !ok {
		return
	}
	err := h.svc.DeleteWeKnoraDoc(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("docId"))
	h.auditWrite(c, "doc.delete", scopedConnectionID(c.Query("connectionId"), c.Param("docId")), c.Param("docId"), projectID, err)
	if err != nil {
		respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

const (
	maxKnowledgeUpload = 50 << 20
	// knowledgeUploadEnvelope leaves room for multipart boundaries and field
	// headers, so a file at the exact limit is not rejected by framing bytes.
	// The contract is one file plus a short filename; a longer envelope is
	// rejected rather than risking a false 413.
	knowledgeUploadEnvelope = 1 << 20
)

// 路由: g.POST("/api/knowledge/sources/:id/upload", h.upload)
func (h *knowledgeHandlers) upload(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if projectID != "" {
		if !h.requireProjectWriter(c, projectID) {
			return
		}
	} else if !h.canManage(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员或项目成员可以上传文档"})
		return
	}
	connID := scopedConnectionID(c.Query("connectionId"), c.Param("id"))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxKnowledgeUpload+knowledgeUploadEnvelope)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "文件超过 50MB 上限"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要上传的文件（字段名 file）"})
		return
	}
	if fileHeader.Size > maxKnowledgeUpload {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "文件超过 50MB 上限"})
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取上传文件"})
		return
	}
	defer f.Close()
	// The multipart file streams straight into the upstream request: no full
	// buffering, so a 50MB document costs no 50MB of heap.
	doc, err := h.svc.UploadDocumentReader(c.Request.Context(), projectID, c.Query("connectionId"), c.Param("id"), fileHeader.Filename, f)
	h.auditWrite(c, "doc.upload", connID, auditTarget(doc.ID, c.Param("id")), projectID, err)
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
	if ks := h.repo.KnowledgeSettings(); ks.Ready() {
		label := "团队知识库"
		if ks.EffectiveProvider() == domain.KnowledgeProviderWeKnora {
			label = "团队知识库（WeKnora）"
		}
		options = append(options, gin.H{"id": domain.KnowledgeMCPServerID, "name": label, "scoped": true})
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
		if err != nil {
			c.JSON(400, gin.H{"error": ref.ServerID + "：" + err.Error()})
			return
		}
		if len(tools) == 0 {
			c.JSON(400, gin.H{"error": ref.ServerID + "：未发现声明为只读的工具，请检查端点的工具配置"})
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
