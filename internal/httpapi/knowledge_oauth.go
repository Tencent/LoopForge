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

const knowledgeOAuthBrowserCookie = "loopforge_kb_oauth_nonce"

func knowledgeOAuthBrowserCookieName(nonce string) string {
	return knowledgeOAuthBrowserCookie + "_" + nonce
}

// 路由: POST /api/knowledge/oauth/:type/authorize
func (h *knowledgeHandlers) oauthAuthorize(c *gin.Context) {
	uid, ok := h.ownerUserID(c)
	if !ok {
		return
	}
	var payload knowledgeOAuthAuthorizeRequest
	if !decode(c, &payload) {
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
	if parsed, err := url.Parse(authorizeURL); err == nil {
		if nonce, err := knowledge.VerifyOAuthStateNonce(parsed.Query().Get("state")); err == nil && nonce != "" {
			http.SetCookie(c.Writer, &http.Cookie{
				Name:     knowledgeOAuthBrowserCookieName(nonce),
				Value:    nonce,
				Path:     "/api/knowledge/oauth",
				MaxAge:   600,
				HttpOnly: true,
				Secure:   c.Request.TLS != nil || strings.HasPrefix(strings.ToLower(redirectURI), "https://"),
				SameSite: http.SameSiteLaxMode,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"authorizeUrl": authorizeURL, "redirectUri": redirectURI})
}

// 路由: GET /api/knowledge/oauth/callback 与 GET /api/knowledge/oauth/:type/callback
// The callback is reached in a fresh tab from the authorization server, so it
// must work without a platform session; state and the SameSite=Lax browser
// binding cookie together bind the callback to the initiating browser.
func (h *knowledgeHandlers) oauthCallback(c *gin.Context) {
	errParam := strings.TrimSpace(c.Query("error"))
	if desc := strings.TrimSpace(c.Query("error_description")); desc != "" {
		if errParam == "" {
			errParam = desc
		} else {
			errParam += ": " + desc
		}
	}
	state := strings.TrimSpace(c.Query("state"))
	nonce, stateErr := knowledge.VerifyOAuthStateNonce(state)
	if stateErr != nil || strings.TrimSpace(nonce) == "" {
		c.Redirect(http.StatusSeeOther, "/knowledge-oauth-done.html?status=error&code="+knowledge.OAuthCallbackCodeStateInvalid)
		return
	}
	cookieName := knowledgeOAuthBrowserCookieName(nonce)
	cookieNonce, cookieErr := c.Cookie(cookieName)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/api/knowledge/oauth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil || strings.HasPrefix(strings.ToLower(knowledgePublicBaseURL(c)), "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	if cookieErr != nil || strings.TrimSpace(cookieNonce) == "" || cookieNonce != nonce {
		c.Redirect(http.StatusSeeOther, "/knowledge-oauth-done.html?status=error&code="+knowledge.OAuthCallbackCodeStateInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), knowledgeCallbackTimeout)
	defer cancel()
	_, err := h.service().CompleteOAuthCallback(ctx, c.Param("type"), state, c.Query("code"), errParam)
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
// When LOOPFORGE_PUBLIC_BASE_URL is not configured, it uses the direct request
// scheme and Host header without trusting unverified X-Forwarded-* headers.
func knowledgePublicBaseURL(c *gin.Context) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("LOOPFORGE_PUBLIC_BASE_URL")), "/"); v != "" {
		return v
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(c.Request.Host)
	return scheme + "://" + host
}

func knowledgeOAuthRedirectURI(c *gin.Context, connectorType string) string {
	typ := strings.TrimSpace(connectorType)
	if typ == "" {
		typ = domain.ConnectorTypeMCP
	}
	return knowledgePublicBaseURL(c) + "/api/knowledge/oauth/" + url.PathEscape(typ) + "/callback"
}
