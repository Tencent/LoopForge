package httpapi

import (
	"errors"
	"log"
	"mime"
	"net/http"
	"strings"

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

const reservedPreviewHostNotice = "This hostname is reserved for previews by AGENTFLOW_PREVIEW_PUBLIC_URL; studio pages and APIs are not served here.\n" +
	"Online previews stay disabled until AGENTFLOW_PREVIEW_FRAME_ANCESTORS lists the exact studio origins.\n" +
	"If this is the studio, set AGENTFLOW_PREVIEW_PUBLIC_URL to a dedicated preview hostname, or list this origin in AGENTFLOW_PREVIEW_FRAME_ANCESTORS.\n"

func (h *previewsHandlers) previewHost() gin.HandlerFunc {
	return func(c *gin.Context) {
		svc := h.engine.Preview()
		if svc == nil || !svc.Enabled() {
			c.Next()
			return
		}
		if _, _, ok := preview.ParsePreviewPath(c.Request.URL.Path); !ok {
			if target, stray := svc.StrayNavigationTarget(c.Request); svc.IsPreviewHost(c.Request) && stray {
				c.Redirect(http.StatusSeeOther, target)
				c.Abort()
				return
			}
			if svc.IsPreviewHost(c.Request) && svc.IsolationError() != nil {
				// Still reserved, but explain it: an operator who pointed this
				// variable at the studio must see why every page is missing.
				c.String(http.StatusNotFound, reservedPreviewHostNotice)
				c.Abort()
				return
			}
			if svc.IsPreviewHost(c.Request) || strings.HasPrefix(c.Request.URL.Path, preview.HTTPPathPrefix) {
				// The preview hostname must never expose the studio or its APIs.
				c.AbortWithStatus(http.StatusNotFound)
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

func (h *previewsHandlers) downloadRunPreview(c *gin.Context) {
	svc := h.engine.Preview()
	if svc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview is not enabled"})
		return
	}
	runID := c.Param("id")
	if _, ok := h.repo.Run(runID); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	p, ok := h.repo.PreviewByRun(runID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview not found"})
		return
	}
	file, err := svc.OpenDownload(c.Request.Context(), p)
	if err != nil {
		switch {
		case errors.Is(err, preview.ErrPreviewNotReady):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, preview.ErrPreviewExpired):
			c.JSON(http.StatusGone, gin.H{"error": err.Error()})
		case errors.Is(err, preview.ErrPreviewNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "preview bundle not found"})
		default:
			log.Printf("preview download %s: %v", runID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "preview download failed"})
		}
		return
	}
	defer file.Close()
	name := "preview-" + runID + ".zip"
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, name, p.UpdatedAt, file)
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
		status, body := previewErrorResponse(err)
		c.JSON(status, body)
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
		status, body := previewErrorResponse(err)
		c.JSON(status, body)
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
		} else {
			// The backend was saved; tell the studio why no session followed.
			_, resp["sessionError"] = previewErrorResponse(err)
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

// previewErrorResponse maps session and backend failures to a status and a
// stable code, so the studio can explain the fix while keeping the archive's
// download and backend actions available.
func previewErrorResponse(err error) (int, gin.H) {
	switch {
	case errors.Is(err, preview.ErrStudioBackend):
		return http.StatusBadRequest, gin.H{"error": err.Error(), "code": "preview_backend_on_studio"}
	case errors.Is(err, domain.ErrInvalidPreviewBackendURL):
		return http.StatusBadRequest, gin.H{"error": err.Error()}
	case errors.Is(err, preview.ErrIsolatedOriginRequired):
		return http.StatusConflict, gin.H{"error": err.Error(), "code": "preview_isolation_required"}
	default:
		return http.StatusConflict, gin.H{"error": err.Error()}
	}
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
