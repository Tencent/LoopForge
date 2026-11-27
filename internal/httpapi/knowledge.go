package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// knowledgeHandlers exposes user-owned personal knowledge connectors over HTTP
// plus the agent-facing stateless read-only proxy (knowledge_proxy.go).
type knowledgeHandlers struct {
	*deps
	svc      *knowledge.Service
	exec     *knowledge.Executor
	legacy   *knowledge.LegacyService // TODO(knowledge-legacy-compat): remove after legacy KB sunset
	activity *knowledgeActivityLog
}

func (h *knowledgeHandlers) service() *knowledge.Service {
	if h != nil && h.svc != nil {
		return h.svc
	}
	if h != nil && h.deps != nil {
		return h.deps.knowledgeService()
	}
	return nil
}

func (h *knowledgeHandlers) executor() *knowledge.Executor {
	if h != nil && h.exec != nil {
		return h.exec
	}
	if h != nil && h.deps != nil {
		return h.deps.executor()
	}
	return nil
}

func (h *knowledgeHandlers) ownerUserID(c *gin.Context) (int64, bool) {
	if h != nil && h.deps != nil {
		if user, ok := h.requestUser(c.Request, true); ok && user != nil && user.ID > 0 {
			return user.ID, true
		}
	}
	if !authRequired() {
		return 0, true
	}
	c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
	return 0, false
}

type connectorRequestPayload struct {
	ID          string                           `json:"id,omitempty"`
	ConnectorID string                           `json:"connectorId,omitempty"`
	Name        string                           `json:"name"`
	Type        string                           `json:"type"`
	Endpoint    string                           `json:"endpoint"`
	Config      *domain.KnowledgeConnectorConfig `json:"config,omitempty"`
	Secret      domain.KnowledgeConnectorSecret  `json:"secret,omitempty"`
	Token       string                           `json:"token,omitempty"`
	Headers     map[string]string                `json:"headers,omitempty"`
	AuthType    string                           `json:"authType,omitempty"`
	ClearSecret bool                             `json:"clearSecret,omitempty"`
	Enabled     *bool                            `json:"enabled,omitempty"`
}

func (p connectorRequestPayload) toInput() knowledge.ConnectorInput {
	secret := p.Secret
	if strings.TrimSpace(secret.AuthType) == "" && strings.TrimSpace(p.AuthType) != "" {
		secret.AuthType = strings.TrimSpace(p.AuthType)
	}
	if strings.TrimSpace(secret.Token) == "" && strings.TrimSpace(p.Token) != "" {
		secret.Token = strings.TrimSpace(p.Token)
	}
	if len(secret.Headers) == 0 && len(p.Headers) > 0 {
		secret.Headers = p.Headers
	}
	return knowledge.ConnectorInput{
		ID:          strings.TrimSpace(p.ID),
		Name:        p.Name,
		Type:        p.Type,
		Endpoint:    p.Endpoint,
		Config:      p.Config,
		Secret:      secret,
		ClearSecret: p.ClearSecret,
		Enabled:     p.Enabled,
	}
}

func render[T any](c *gin.Context, value T, err error) {
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

// 路由: GET /api/knowledge/connector-types
func (h *knowledgeHandlers) connectorTypes(c *gin.Context) {
	c.JSON(http.StatusOK, h.service().ConnectorTypes())
}

// 路由: GET /api/knowledge/connectors
func (h *knowledgeHandlers) listConnectors(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	items := h.service().ListConnectors(uid)
	if items == nil {
		items = []domain.KnowledgeConnector{}
	}
	c.JSON(http.StatusOK, items)
}

// 路由: POST /api/knowledge/connectors
func (h *knowledgeHandlers) createConnector(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var payload connectorRequestPayload
	if !decode(c, &payload) {
		return
	}
	in := payload.toInput()
	in.ID = ""
	created, err := h.service().CreateConnector(c.Request.Context(), uid, in)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// 路由: GET /api/knowledge/connectors/:id
func (h *knowledgeHandlers) getConnector(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	item, found := h.service().GetConnector(uid, c.Param("id"))
	if !found {
		respond(c, apperr.New(apperr.NotFound, knowledge.ErrConnectorNotFound.Error()))
		return
	}
	c.JSON(http.StatusOK, item)
}

// 路由: PUT /api/knowledge/connectors/:id
func (h *knowledgeHandlers) updateConnector(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var payload connectorRequestPayload
	if !decode(c, &payload) {
		return
	}
	updated, err := h.service().UpdateConnector(c.Request.Context(), uid, c.Param("id"), payload.toInput())
	render(c, updated, err)
}

// 路由: DELETE /api/knowledge/connectors/:id
func (h *knowledgeHandlers) deleteConnector(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	if err := h.service().DeleteConnector(uid, c.Param("id")); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// 路由: POST /api/knowledge/connectors/check
func (h *knowledgeHandlers) checkConnector(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var payload connectorRequestPayload
	if !decodeOptional(c, &payload) {
		return
	}
	connID := strings.TrimSpace(payload.ConnectorID)
	if connID == "" {
		connID = strings.TrimSpace(payload.ID)
	}
	input := payload.toInput()
	c.JSON(http.StatusOK, h.service().CheckConnector(c.Request.Context(), uid, connID, &input))
}

// 路由: POST /api/knowledge/connectors/:id/check
func (h *knowledgeHandlers) checkConnectorByID(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var payload connectorRequestPayload
	if !decodeOptional(c, &payload) {
		return
	}
	input := payload.toInput()
	c.JSON(http.StatusOK, h.service().CheckConnector(c.Request.Context(), uid, c.Param("id"), &input))
}

// 路由: GET /api/knowledge/connectors/:id/sources
func (h *knowledgeHandlers) connectorSources(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	sources, err := h.service().ListConnectorSources(c.Request.Context(), uid, c.Param("id"))
	if err != nil {
		respond(c, err)
		return
	}
	if sources == nil {
		sources = []knowledge.Source{}
	}
	c.JSON(http.StatusOK, sources)
}

// referenceOptions lists the current user's enabled connectors, optionally
// expanding sub-sources for ?connectorId=<id> or ?sources=true.
//
// 路由: GET /api/knowledge/reference-options
func (h *knowledgeHandlers) referenceOptions(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	svc := h.service()
	typeMeta := map[string]domain.ConnectorTypeMeta{}
	for _, m := range svc.ConnectorTypes() {
		typeMeta[m.Type] = m
	}

	if targetID := strings.TrimSpace(c.Query("connectorId")); targetID != "" && c.Query("sources") == "true" {
		conn, found := svc.GetConnector(uid, targetID)
		if !found || !conn.Enabled {
			respond(c, apperr.New(apperr.NotFound, knowledge.ErrConnectorNotFound.Error()))
			return
		}
		sources, err := svc.ListConnectorSources(c.Request.Context(), uid, targetID)
		if err != nil {
			respond(c, err)
			return
		}
		out := make([]gin.H, 0, len(sources))
		for _, s := range sources {
			out = append(out, gin.H{"id": s.ID, "name": s.Name, "description": s.Description, "docCount": s.DocCount})
		}
		c.JSON(http.StatusOK, out)
		return
	}

	wantSources := c.Query("sources") == "true"
	connectors := svc.ListConnectors(uid)
	options := make([]gin.H, 0, len(connectors))
	for _, conn := range connectors {
		if !conn.Enabled {
			continue
		}
		meta := typeMeta[conn.Type]
		item := gin.H{
			"id":               conn.ID,
			"name":             conn.Name,
			"type":             conn.Type,
			"typeName":         meta.Name,
			"endpoint":         conn.Endpoint,
			"scoped":           meta.SupportsSubSources,
			"secretConfigured": conn.SecretConfigured,
		}
		if wantSources && meta.SupportsSubSources {
			if sources, err := svc.ListConnectorSources(c.Request.Context(), uid, conn.ID); err == nil {
				srcList := make([]gin.H, 0, len(sources))
				for _, s := range sources {
					srcList = append(srcList, gin.H{"id": s.ID, "name": s.Name, "description": s.Description, "docCount": s.DocCount})
				}
				item["sources"] = srcList
			}
		}
		options = append(options, item)
	}
	c.JSON(http.StatusOK, options)
}

// 路由: POST /api/knowledge/reference-check
func (h *knowledgeHandlers) checkReferences(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var body struct {
		Refs []domain.KnowledgeRef `json:"knowledgeRefs"`
	}
	if !decode(c, &body) {
		return
	}
	if err := domain.ValidateKnowledgeRefs(body.Refs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), knowledge.CallTimeout)
	defer cancel()
	exec := h.executor()
	svc := h.service()
	for _, ref := range body.Refs {
		if ctx.Err() != nil {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "知识服务探测超时"})
			return
		}
		_, _, err := exec.ReferenceTools(ctx, uid, ref)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": ref.ConnectorID + "：" + err.Error()})
			return
		}
		if ref.Mode == "allowlist" && len(ref.Sources) > 0 {
			sources, err := svc.ListConnectorSources(ctx, uid, ref.ConnectorID)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": ref.ConnectorID + "：无法校验子知识库范围"})
				return
			}
			if len(sources) > 0 {
				allowed := map[string]bool{}
				for _, s := range sources {
					allowed[s.ID] = true
				}
				for _, id := range ref.Sources {
					if !allowed[id] {
						c.JSON(http.StatusBadRequest, gin.H{"error": "知识库来源失效或未授权：" + id})
						return
					}
				}
			}
		}
	}
	c.Status(http.StatusNoContent)
}

// 路由: GET /api/knowledge/usage
func (h *knowledgeHandlers) getUsage(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	connectorID := strings.TrimSpace(c.Query("connectorId"))
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	c.JSON(http.StatusOK, h.service().UsageSummary(uid, connectorID, limit))
}

// 路由: GET /api/knowledge/usage/events
func (h *knowledgeHandlers) listUsageEvents(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	filter := domain.KnowledgeUsageEventFilter{
		ConnectorID: strings.TrimSpace(c.Query("connectorId")),
		DocumentID:  strings.TrimSpace(c.Query("documentId")),
		CallerKind:  strings.TrimSpace(c.Query("callerKind")),
		Query:       strings.TrimSpace(c.Query("query")),
		Limit:       50,
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			filter.Limit = parsed
		}
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			filter.Offset = parsed
		}
	}
	c.JSON(http.StatusOK, h.service().UsageEvents(uid, filter))
}

// 路由: DELETE /api/knowledge/usage
func (h *knowledgeHandlers) resetUsage(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	connectorID := strings.TrimSpace(c.Query("connectorId"))
	if err := h.service().ResetUsage(uid, connectorID); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
