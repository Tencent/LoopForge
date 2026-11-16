package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	oidcLoginTTL = 10 * time.Minute
	oidcTimeout  = 10 * time.Second
)

func (h *authHandlers) oidcAvailable(r *http.Request) bool {
	return h.oidc.MatchesHost(r.Host) && (!h.oidc.Secure() || secureRequest(r))
}

func (h *authHandlers) oidcStart(c *gin.Context) {
	if !h.oidcAvailable(c.Request) {
		c.Status(http.StatusNotFound)
		return
	}
	oidcResponseHeaders(c)
	returnPath := h.oidcReturnPath(c.Request)
	ctx, cancel := context.WithTimeout(c.Request.Context(), oidcTimeout)
	defer cancel()

	cookieName := h.oidcBrowserCookieName()
	browser, _ := c.Cookie(cookieName)
	if len(browser) != 43 {
		browser = newOIDCValue()
	}
	state, nonce, verifier := newOIDCValue(), newOIDCValue(), newOIDCValue()
	location, err := h.oidc.AuthorizationURL(ctx, state, nonce, verifier)
	if err != nil {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(returnPath, "unavailable"))
		return
	}
	now := time.Now().UTC()
	login := repo.OIDCLogin{
		State: oidcDigest(state), Browser: oidcDigest(browser), Nonce: nonce, Verifier: verifier,
		ReturnPath: returnPath, ExpiresAt: now.Add(oidcLoginTTL),
	}
	if err := h.repo.CreateOIDCLogin(ctx, login, now); err != nil {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(returnPath, "unavailable"))
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: cookieName, Value: browser, Path: "/", HttpOnly: true, Secure: h.oidc.Secure(),
		SameSite: http.SameSiteLaxMode, MaxAge: int(oidcLoginTTL.Seconds()),
	})
	c.Redirect(http.StatusFound, location)
}

func (h *authHandlers) oidcCallback(c *gin.Context) {
	if !h.oidcAvailable(c.Request) {
		c.Status(http.StatusNotFound)
		return
	}
	oidcResponseHeaders(c)
	browser, _ := c.Cookie(h.oidcBrowserCookieName())
	state := c.Query("state")
	if len(state) != 43 || len(browser) != 43 {
		c.Redirect(http.StatusSeeOther, oidcLoginPage("/", "invalid"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), oidcTimeout)
	defer cancel()
	login, err := h.repo.ConsumeOIDCLogin(ctx, oidcDigest(state), oidcDigest(browser), time.Now().UTC())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Redirect(http.StatusSeeOther, oidcLoginPage("/", "invalid"))
		return
	}
	if err != nil {
		c.Redirect(http.StatusSeeOther, oidcLoginPage("/", "unavailable"))
		return
	}
	code := c.Query("code")
	if code == "" || c.Query("error") != "" {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(login.ReturnPath, "invalid"))
		return
	}
	issuer, subject, err := h.oidc.Exchange(ctx, code, login.Nonce, login.Verifier)
	if err != nil {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(login.ReturnPath, "invalid"))
		return
	}
	user, err := h.repo.UserByOIDCIdentity(ctx, issuer, subject)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(login.ReturnPath, "unlinked"))
		return
	}
	if err != nil {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(login.ReturnPath, "unavailable"))
		return
	}
	token, err := h.getAuthClient().SignUser(domainUserToAuth(user))
	if err != nil {
		c.Redirect(http.StatusSeeOther, oidcLoginPage(login.ReturnPath, "unavailable"))
		return
	}
	h.setAuthCookie(c.Writer, c.Request, token)
	c.Redirect(http.StatusSeeOther, login.ReturnPath)
}

func (h *authHandlers) oidcBrowserCookieName() string {
	if h.oidc.Secure() {
		return "__Host-agentflow_oidc"
	}
	return "agentflow_oidc"
}

func newOIDCValue() string {
	var value [32]byte
	rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func oidcDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func oidcResponseHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
}

func (h *authHandlers) oidcReturnPath(r *http.Request) string {
	for _, candidate := range []string{r.URL.Query().Get("return_url"), r.Referer()} {
		if path := oidcSameOriginPath(h.oidc.Origin(), candidate); path != "" {
			return path
		}
	}
	return "/"
}

func oidcSameOriginPath(origin, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	if u.IsAbs() || u.Host != "" {
		if u.Scheme == "" || !strings.EqualFold(u.Scheme+"://"+u.Host, origin) {
			return ""
		}
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") ||
		strings.ContainsAny(u.Path, "\\\r\n\t") || u.Path == "/login.html" || strings.HasPrefix(u.Path, "/api/auth/") {
		return ""
	}
	u.Scheme, u.Host = "", ""
	return u.String()
}

func oidcLoginPage(path, reason string) string {
	query := url.Values{}
	if path != "/" {
		query.Set("return_url", path)
	}
	if reason != "" {
		query.Set("oidc_error", reason)
	}
	if len(query) == 0 {
		return "/login.html"
	}
	return "/login.html?" + query.Encode()
}
