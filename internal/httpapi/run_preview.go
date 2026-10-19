package httpapi

import (
	"errors"
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/preview"

	"github.com/gin-gonic/gin"
)

// previewsHandlers owns the run preview plane: the preview host middleware
// mounted before routing, preview lookup by run, and preview session
// lifecycle. Runner-machine upload endpoints stay on runnersHandlers.
type previewsHandlers struct {
	*deps
}

func (h *previewsHandlers) previewHost() gin.HandlerFunc {
	return func(c *gin.Context) {
		svc := h.engine.Preview()
		if svc == nil || !svc.Enabled() {
			c.Next()
			return
		}
		if _, _, ok := preview.ParsePreviewPath(c.Request.URL.Path); !ok {
			if target, stray := svc.StrayNavigationTarget(c.Request); stray {
				c.Redirect(http.StatusSeeOther, target)
				c.Abort()
				return
			}
			c.Next()
			return
		}
		svc.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

func (h *previewsHandlers) runPreview(c *gin.Context) {
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview is not enabled"})
		return
	}
	runID := c.Param("id")
	if _, ok := h.repo.Run(runID); !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	p, ok := h.repo.PreviewByRun(runID)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}
	c.JSON(http.StatusOK, publicPreview(p))
}

func (h *previewsHandlers) runPreviewSession(c *gin.Context) {
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview is not enabled"})
		return
	}
	runID := c.Param("id")
	if _, ok := h.repo.Run(runID); !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	p, ok := h.repo.PreviewByRun(runID)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}
	var body struct {
		BackendURL *string `json:"backendUrl"`
	}
	if !decodeOptional(c, &body) {
		return
	}
	backendURL := ""
	if body.BackendURL != nil {
		backendURL = *body.BackendURL
	} else {
		backendURL = p.BackendURL
	}
	session, url, err := svc.CreateSession(p, c.Request, backendURL)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, domain.ErrInvalidPreviewBackendURL) {
			status = http.StatusBadRequest
		}
		c.JSON(status, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"id":         session.ID,
		"previewId":  session.PreviewID,
		"url":        url,
		"backendUrl": session.BackendURL,
		"expiresAt":  session.ExpiresAt,
	})
}

func (h *previewsHandlers) updatePreviewBackend(c *gin.Context) {
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview is not enabled"})
		return
	}
	runID := c.Param("id")
	if _, ok := h.repo.Run(runID); !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	p, ok := h.repo.PreviewByRun(runID)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}
	var body struct {
		BackendURL string `json:"backendUrl"`
	}
	if !decode(c, &body) {
		return
	}
	updated, err := svc.SetPreviewBackend(p.ID, body.BackendURL)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, domain.ErrInvalidPreviewBackendURL) {
			status = http.StatusBadRequest
		}
		c.JSON(status, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"preview": publicPreview(updated),
	}
	if updated.Serveable() {
		session, url, err := svc.CreateSession(updated, c.Request, updated.BackendURL)
		if err == nil {
			resp["session"] = map[string]any{
				"id":         session.ID,
				"previewId":  session.PreviewID,
				"url":        url,
				"backendUrl": session.BackendURL,
				"expiresAt":  session.ExpiresAt,
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

func (h *previewsHandlers) deletePreviewSession(c *gin.Context) {
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview is not enabled"})
		return
	}
	if err := svc.RevokeSession(c.Param("sid")); err != nil {
		respond(c, err)
		return
	}
	respond(c, nil)
}

func (h *previewsHandlers) deletePreview(c *gin.Context) {
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview is not enabled"})
		return
	}
	p, ok := h.repo.PreviewByRun(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "preview not found"})
		return
	}
	respond(c, svc.DeletePreview(p))
}

func publicPreview(p domain.RunPreview) map[string]any {
	out := map[string]any{
		"id":             p.ID,
		"runId":          p.RunID,
		"kind":           p.Kind,
		"status":         p.Status,
		"sourcePath":     p.SourcePath,
		"entryFile":      p.EntryFile,
		"spaFallback":    p.SPAFallback,
		"backendUrl":     p.BackendURL,
		"fileCount":      p.FileCount,
		"compressedSize": p.CompressedSize,
		"expandedSize":   p.ExpandedSize,
		"expiresAt":      p.ExpiresAt,
		"lastAccessedAt": p.LastAccessedAt,
		"createdAt":      p.CreatedAt,
		"updatedAt":      p.UpdatedAt,
	}
	if p.ErrorText != "" {
		out["error"] = p.ErrorText
	}
	return out
}
