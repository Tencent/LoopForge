package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// knowledgeOAuthAuthorizeRequest is the JSON body for POST authorize.
type knowledgeOAuthAuthorizeRequest struct {
	ConnectorID string `json:"connectorId"`
}

// 路由: GET /api/knowledge/oauth/presets
func (h *knowledgeHandlers) oauthPresets(c *gin.Context) {
	if _, ok := h.ownerUserID(c); !ok {
		return
	}
	svc := h.service()
	presets := svc.OAuthPresets()
	c.JSON(http.StatusOK, gin.H{
		"presets":     presets,
		"redirectUri": knowledgeOAuthRedirectURI(c, domain.ConnectorTypeMCP),
	})
}

// 路由: POST /api/knowledge/oauth/:type/authorize（GET 同路径直接 302）
func (h *knowledgeHandlers) oauthAuthorize(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var payload knowledgeOAuthAuthorizeRequest
	if c.Request.Method == http.MethodGet {
		payload.ConnectorID = c.Query("connectorId")
	} else if !decode(c, &payload) {
		return
	}
	connectorID := strings.TrimSpace(firstNonEmpty(payload.ConnectorID, c.Query("connectorId")))
	if connectorID == "" {
		respond(c, apperr.New(apperr.Invalid, "缺少 connectorId"))
		return
	}
	authorizeURL, redirectURI, err := h.service().StartOAuth(c.Request.Context(), uid, connectorID, knowledgePublicBaseURL(c))
	if err != nil {
		respond(c, err)
		return
	}
	if c.Request.Method == http.MethodGet {
		c.Redirect(http.StatusFound, authorizeURL)
		return
	}
	c.JSON(http.StatusOK, gin.H{"authorizeUrl": authorizeURL, "redirectUri": redirectURI})
}

// 路由: GET /api/knowledge/oauth/callback 与 GET /api/knowledge/oauth/:type/callback
// The callback is reached in a fresh tab from the authorization server, so it
// must work without a platform session; state is the credential.
func (h *knowledgeHandlers) oauthCallback(c *gin.Context) {
	errParam := strings.TrimSpace(c.Query("error"))
	if desc := strings.TrimSpace(c.Query("error_description")); desc != "" {
		if errParam == "" {
			errParam = desc
		} else {
			errParam += ": " + desc
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), knowledgeCallbackTimeout)
	defer cancel()
	_, err := h.service().CompleteOAuthCallback(ctx, c.Param("type"), c.Query("state"), c.Query("code"), errParam)
	location := "/knowledge-oauth-done.html?status=ok"
	if err != nil {
		location = "/knowledge-oauth-done.html?status=error&code=" + knowledgeOAuthErrorCode(err)
	}
	c.Redirect(http.StatusSeeOther, location)
}

// knowledgeOAuthErrorCode maps a callback failure to a fixed machine code; the
// redirect never carries upstream error text.
func knowledgeOAuthErrorCode(err error) string {
	var callbackErr *knowledge.OAuthCallbackError
	if errors.As(err, &callbackErr) && callbackErr.Code != "" {
		return callbackErr.Code
	}
	return knowledge.OAuthCallbackCodeFailed
}

const knowledgeCallbackTimeout = 30 * time.Second

// knowledgePublicBaseURL resolves the browser-facing origin for redirect URIs.
func knowledgePublicBaseURL(c *gin.Context) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("LOOPFORGE_PUBLIC_BASE_URL")), "/"); v != "" {
		return v
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		if i := strings.Index(proto, ","); i >= 0 {
			proto = proto[:i]
		}
		proto = strings.ToLower(strings.TrimSpace(proto))
		if proto == "http" || proto == "https" {
			scheme = proto
		}
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if i := strings.Index(host, ","); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = c.Request.Host
	}
	return scheme + "://" + host
}

func knowledgeOAuthRedirectURI(c *gin.Context, connectorType string) string {
	typ := strings.TrimSpace(connectorType)
	if typ == "" {
		typ = domain.ConnectorTypeMCP
	}
	return knowledgePublicBaseURL(c) + "/api/knowledge/oauth/" + url.PathEscape(typ) + "/callback"
}
