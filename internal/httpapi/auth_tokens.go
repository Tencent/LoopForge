package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type requestUserContextKey struct{}

func authRequired() bool {
	value := strings.TrimSpace(os.Getenv("AGENTFLOW_AUTH_REQUIRED"))
	if value == "" {
		return false
	}
	required, err := strconv.ParseBool(value)
	return err == nil && required
}

type tokenHandlers struct {
	*deps
}

// userAccessAuth protects public API and MCP calls when explicitly enabled.
// Studio assets and the login exchange remain public so a guest can reach the
// sign-in flow. Token-management handlers always enforce interactive login.
func (h *tokenHandlers) userAccessAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		// Always attach the signed-in actor when present. LocalInvoker and
		// background generation hops reuse this request context; without the
		// key, login-gated tools (runs_group_invite, etc.) see an anonymous
		// call and return 请先登录 even though the outer /mcp or generation
		// request was authenticated via cookie.
		user, ok := h.requestUser(c.Request, true)
		if authRequired() && protectedAccessPath(c.Request.URL.Path) && !ok {
			c.Header("WWW-Authenticate", `Bearer realm="AgentFlow"`)
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			c.Abort()
			return
		}
		if ok {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, user))
		}
		c.Next()
	}
}

func protectedAccessPath(path string) bool {
	if path == "/mcp" {
		return true
	}
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	// Runner-agent endpoints carry their own bootstrap/session credentials.
	if isRunnerAgentPath(path) {
		return false
	}
	switch path {
	case "/api/health", "/api/auth/password", "/api/auth/setup", "/api/auth/register", "/api/auth/current", "/api/auth/exchange", "/api/auth/logout", "/api/auth/local", "/api/auth/photo":
		return false
	default:
		if strings.HasPrefix(path, "/api/auth/photo/") {
			return false
		}
		return path != "/api/auth/tokens" && !strings.HasPrefix(path, "/api/auth/tokens/")
	}
}

// requestUser 挂 deps（token 鉴权与多个领域共用）。It resolves a signed
// browser session or a user API token. Gateway identity headers are resolved
// by authCurrent, which exchanges them for the signed HttpOnly browser cookie.
func (d deps) requestUser(r *http.Request, allowBearer bool) (*auth.User, bool) {
	if current, ok := r.Context().Value(requestUserContextKey{}).(*auth.User); ok && current != nil && !current.IsGuest {
		return current, true
	}
	if allowBearer {
		if raw, ok := bearerToken(r); ok {
			sum := sha256.Sum256([]byte(raw))
			token, valid := d.repo.ActiveAPITokenByHash(hex.EncodeToString(sum[:]), time.Now().UTC())
			if valid {
				if stored, exists := d.repo.User(token.UserID); exists {
					return domainUserToAuth(stored), true
				}
			}
			return nil, false
		}
	}
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	user, err := d.getAuthClient().ParseSignedUser(cookie.Value)
	if err != nil || user == nil || user.IsGuest || strings.TrimSpace(user.LoginName) == "" {
		return nil, false
	}
	if authSessionExpired(user) {
		return nil, false
	}
	if stored, exists := d.repo.UserByLoginName(user.LoginName); exists {
		user.ID = stored.ID
		user.StaffID = stored.StaffID
		user.ChineseName = stored.ChineseName
		user.DeptID = stored.DeptID
		user.DeptName = stored.DeptName
	}
	return user, true
}

func bearerToken(r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, raw, ok := strings.Cut(value, " ")
	raw = strings.TrimSpace(raw)
	return raw, ok && strings.EqualFold(scheme, "Bearer") && raw != ""
}

func domainUserToAuth(user domain.User) *auth.User {
	return &auth.User{
		ID:              user.ID,
		StaffID:         user.StaffID,
		LoginName:       user.LoginName,
		ChineseName:     user.ChineseName,
		DeptID:          user.DeptID,
		DeptName:        user.DeptName,
		IsGuest:         false,
		IsPlatformAdmin: repo.IsAllowlistedPlatformAdmin(user.LoginName) || domain.IsPlatformAdmin(user),
		AuthTime:        time.Now().UTC(),
	}
}

// annotateUser fills IsPlatformAdmin from the store / builtin allowlist.
// The flag is computed on every response so a cookie never becomes the source of truth.
func (d deps) annotateUser(user *auth.User) *auth.User {
	if user == nil || user.IsGuest {
		return user
	}
	if d.repo != nil {
		user.IsPlatformAdmin = d.repo.IsPlatformAdmin(user.LoginName)
		return user
	}
	user.IsPlatformAdmin = repo.IsAllowlistedPlatformAdmin(user.LoginName)
	return user
}

func (d deps) isPlatformAdmin(r *http.Request) bool {
	user, ok := d.requestUser(r, true)
	if !ok || user == nil {
		return false
	}
	if d.repo != nil {
		return d.repo.IsPlatformAdmin(user.LoginName)
	}
	return repo.IsAllowlistedPlatformAdmin(user.LoginName)
}

// 路由: g.GET("/api/auth/tokens", h.authTokens)
//
//	g.POST("/api/auth/tokens", h.authTokens)
func (h *tokenHandlers) authTokens(c *gin.Context) {
	user, ok := h.interactiveUser(c.Request)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理个人 Token"})
		return
	}
	if user.ID <= 0 {
		if err := h.repo.PutUser(user.ToDomain()); err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存登录用户失败"})
			return
		}
		if stored, exists := h.repo.UserByLoginName(user.LoginName); exists {
			user.ID = stored.ID
		}
	}

	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, h.repo.APITokens(user.ID))
	case http.MethodPost:
		var input struct {
			Name          string `json:"name"`
			ExpiresInDays int    `json:"expiresInDays"`
		}
		if !decode(c, &input) {
			return
		}
		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "Token 名称不能为空"})
			return
		}
		if len([]rune(name)) > 128 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "Token 名称不能超过 128 个字符"})
			return
		}
		days := input.ExpiresInDays
		if days == 0 {
			days = 90
		}
		if days < 1 || days > 365 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "有效期必须在 1 到 365 天之间"})
			return
		}
		plain, err := newAPIToken()
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "生成 Token 失败"})
			return
		}
		now := time.Now().UTC()
		expires := now.Add(time.Duration(days) * 24 * time.Hour)
		sum := sha256.Sum256([]byte(plain))
		token := domain.APIToken{
			ID:        "tok-" + randomID(),
			UserID:    user.ID,
			Name:      name,
			Prefix:    tokenPrefix(plain),
			TokenHash: hex.EncodeToString(sum[:]),
			ExpiresAt: &expires,
			CreatedAt: now,
		}
		if err := h.repo.PutAPIToken(token); err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存 Token 失败"})
			return
		}
		c.JSON(http.StatusCreated, map[string]any{"token": plain, "metadata": token})
	}
}

// 路由: g.DELETE("/api/auth/tokens/:id", h.authToken)
func (h *tokenHandlers) authToken(c *gin.Context) {
	user, ok := h.interactiveUser(c.Request)
	if !ok {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后管理个人 Token"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "Token ID 不能为空"})
		return
	}
	if !h.repo.RevokeAPIToken(id, user.ID, time.Now().UTC()) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "Token 不存在或已撤销"})
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func newAPIToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "afp_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func tokenPrefix(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}
