package httpapi

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/auth"

	"github.com/gin-gonic/gin"
)

const authCookieName = "agentflow_user"

// authSessionTTL 是浏览器会话的最长有效期。Cookie 的 MaxAge 与 requestUser 对
// AuthTime 的校验必须共用同一个时长：一旦两者脱节，就会出现 Cookie 一直被续期、
// 而 AuthTime 早已过期的"半失效"状态——/api/auth/current 报告已登录，受保护接口
// 却一律 401，用户既进不去也无法自动重新登录。
const authSessionTTL = 7 * 24 * time.Hour

// authSessionExpired 判断已签名的浏览器会话是否超过 authSessionTTL。
// AuthTime 为零值（历史 Cookie、网关透传身份）时视为未过期，避免误伤。
func authSessionExpired(user *auth.User) bool {
	return user != nil && !user.AuthTime.IsZero() && time.Since(user.AuthTime) > authSessionTTL
}

type authHandlers struct {
	*deps
}

func trustGatewayIdentityHeaders() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("AGENTFLOW_TRUST_IDENTITY_HEADERS")))
	return err == nil && enabled
}

func gatewayLoginName(r *http.Request) string {
	if !trustGatewayIdentityHeaders() || !isLoopbackAddr(r.RemoteAddr) {
		return ""
	}
	login := strings.TrimSpace(getHeaderCaseInsensitive(r, "Staffname"))
	if login == "" {
		login = strings.TrimSpace(getHeaderCaseInsensitive(r, "x-staff-name"))
	}
	return login
}

func secureRequest(r *http.Request) bool {
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("AGENTFLOW_SECURE_COOKIES")))
	return err == nil && enabled
}

// Local acceptance login is opt-in and restricted to direct loopback requests.
func (h *authHandlers) authLocal(c *gin.Context) {
	login := strings.TrimSpace(os.Getenv("AGENTFLOW_LOCAL_LOGIN_USER"))
	enabled := login != "" && isLoopbackAddr(c.Request.RemoteAddr) && c.GetHeader("X-Forwarded-For") == "" && c.GetHeader("Forwarded") == ""
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, map[string]bool{"enabled": enabled})
		return
	}
	origin, err := url.Parse(c.GetHeader("Origin"))
	if !enabled || err != nil || origin.Host != c.Request.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
		c.JSON(http.StatusForbidden, map[string]string{"error": "本地验收登录未启用或请求来源不允许"})
		return
	}
	u, ok := h.repo.UserByLoginName(login)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "本地验收用户不存在"})
		return
	}
	user := &auth.User{ID: u.ID, StaffID: u.StaffID, LoginName: u.LoginName, ChineseName: u.ChineseName, DeptID: u.DeptID, DeptName: u.DeptName, AuthTime: time.Now().UTC(), AvatarURL: auth.AvatarURL(u.LoginName, 100)}
	token, err := h.getAuthClient().SignUser(user)
	if err != nil {
		respond(c, err)
		return
	}
	h.setAuthCookie(c.Writer, c.Request, token)
	c.JSON(http.StatusOK, map[string]any{"user": h.annotateUser(user)})
}

func getHeaderCaseInsensitive(r *http.Request, key string) string {
	if v := r.Header.Get(key); v != "" {
		return v
	}
	keyLower := strings.ToLower(key)
	for k, vals := range r.Header {
		if strings.ToLower(k) == keyLower && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

func (h *authHandlers) getReturnURL(r *http.Request) string {
	if raw := strings.TrimSpace(r.URL.Query().Get("return_url")); raw != "" {
		return raw
	}
	if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
		return ref
	}
	proto := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		proto = "https"
	}
	host := r.Host
	if host == "" {
		host = h.getAuthClient().Config().AppDomain
	}
	return fmt.Sprintf("%s://%s/", proto, host)
}

func (h *authHandlers) authCurrent(c *gin.Context) {
	r := c.Request

	// 1. 优先检查太湖 / AIO-Forward 网关透传的员工身份 Header
	staffName := gatewayLoginName(r)
	if staffName != "" {
		staffID, _ := strconv.Atoi(strings.TrimSpace(getHeaderCaseInsensitive(r, "Staffid")))
		if staffID == 0 {
			staffID, _ = strconv.Atoi(strings.TrimSpace(getHeaderCaseInsensitive(r, "x-staff-id")))
		}
		user := &auth.User{
			StaffID:     staffID,
			LoginName:   staffName,
			ChineseName: staffName,
			IsGuest:     false,
			AuthTime:    time.Now().UTC(),
		}
		if h.repo != nil {
			if existing, ok := h.repo.UserByLoginName(staffName); ok {
				user.ID = existing.ID
				if existing.ChineseName != "" {
					user.ChineseName = existing.ChineseName
				}
				if existing.DeptName != "" {
					user.DeptName = existing.DeptName
				}
				if existing.DeptID > 0 {
					user.DeptID = existing.DeptID
				}
				if existing.StaffID > 0 && user.StaffID == 0 {
					user.StaffID = existing.StaffID
				}
			} else {
				if putErr := h.repo.PutUser(user.ToDomain()); putErr != nil {
					log.Printf("[auth] persist gateway user %s failed: %v", staffName, putErr)
				} else if saved, ok := h.repo.UserByLoginName(staffName); ok {
					user.ID = saved.ID
				}
			}
		}
		user.AvatarURL = auth.AvatarURL(user.LoginName, 100)
		if token, signErr := h.getAuthClient().SignUser(user); signErr == nil && token != "" {
			h.setAuthCookie(c.Writer, c.Request, token)
		}
		c.JSON(http.StatusOK, map[string]any{"user": h.annotateUser(user), "authRequired": authRequired(), "appDomain": h.getAuthClient().Config().AppDomain})
		return
	}

	// 2. 检查业务 Cookie (agentflow_user)
	cookie, err := r.Cookie(authCookieName)
	if err == nil && cookie != nil && cookie.Value != "" {
		user, parseErr := h.getAuthClient().ParseSignedUser(cookie.Value)
		if parseErr == nil && user != nil && !user.IsGuest {
			// 会话超过 authSessionTTL：清掉过期 Cookie 并落到下面的访客分支。
			// 否则这里会用原始 AuthTime 重新签发一个"看起来仍然登录"的 Cookie，
			// 而受保护接口按同一时长判定 401，用户将永久卡在启动失败页面上。
			if authSessionExpired(user) {
				h.clearAuthCookie(c.Writer, r)
			} else {
				if h.repo != nil && user.LoginName != "" {
					if existing, ok := h.repo.UserByLoginName(user.LoginName); ok {
						user.ID = existing.ID
						if existing.ChineseName != "" {
							user.ChineseName = existing.ChineseName
						}
						if existing.DeptName != "" {
							user.DeptName = existing.DeptName
						}
						if existing.DeptID > 0 {
							user.DeptID = existing.DeptID
						}
						if existing.StaffID > 0 && user.StaffID == 0 {
							user.StaffID = existing.StaffID
						}
					} else {
						// 数据库未查到该用户，但前端 Cookie 已经合法登录，执行写表补齐
						if putErr := h.repo.PutUser(user.ToDomain()); putErr != nil {
							log.Printf("[auth] persist cookie user %s failed: %v", user.LoginName, putErr)
						} else if saved, ok := h.repo.UserByLoginName(user.LoginName); ok {
							user.ID = saved.ID
						}
					}
				}
				if user.LoginName != "" {
					user.AvatarURL = auth.AvatarURL(user.LoginName, 100)
				}
				if token, signErr := h.getAuthClient().SignUser(user); signErr == nil && token != "" {
					h.setAuthCookie(c.Writer, c.Request, token)
				}
				c.JSON(http.StatusOK, map[string]any{"user": h.annotateUser(user), "authRequired": authRequired(), "appDomain": h.getAuthClient().Config().AppDomain})
				return
			}
		}
	}

	// 3. 兜底为访客身份，附带登录跳转链接 loginUrl
	returnURL := h.getReturnURL(r)
	loginURL := h.getAuthClient().LoginURL(returnURL)
	c.JSON(http.StatusOK, map[string]any{
		"user":         auth.FallbackGuestUser(),
		"loginUrl":     loginURL,
		"authRequired": authRequired(),
		"appDomain":    h.getAuthClient().Config().AppDomain,
	})
}

func (h *authHandlers) authExchange(c *gin.Context) {
	r := c.Request
	code := strings.TrimSpace(c.Query("code"))
	if code == "" && r.Method == http.MethodPost {
		var req struct {
			Code string `json:"code"`
		}
		if decodeOptional(c, &req) {
			code = strings.TrimSpace(req.Code)
		}
	}
	if code == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}
	user, err := h.getAuthClient().ExchangeCode(r.Context(), code)
	if err != nil || user == nil {
		user = auth.FallbackGuestUser()
	}
	// 当兑换到合法员工身份时，持久化至数据库 users 表
	if user != nil && !user.IsGuest && user.LoginName != "" && h.repo != nil {
		if putErr := h.repo.PutUser(user.ToDomain()); putErr != nil {
			log.Printf("[auth] persist user %s failed: %v", user.LoginName, putErr)
		} else if saved, ok := h.repo.UserByLoginName(user.LoginName); ok {
			user.ID = saved.ID
		}
	}
	if user != nil && !user.IsGuest && user.LoginName != "" {
		user.AvatarURL = auth.AvatarURL(user.LoginName, 100)
	}
	token, signErr := h.getAuthClient().SignUser(user)
	if signErr == nil && token != "" {
		h.setAuthCookie(c.Writer, c.Request, token)
	}
	c.JSON(http.StatusOK, map[string]any{"user": h.annotateUser(user), "authRequired": authRequired(), "appDomain": h.getAuthClient().Config().AppDomain})
}

func (h *authHandlers) authLogout(c *gin.Context) {
	h.clearAuthCookie(c.Writer, c.Request)
	c.JSON(http.StatusOK, map[string]any{"user": auth.FallbackGuestUser()})
}

func (h *authHandlers) setAuthCookie(w http.ResponseWriter, r *http.Request, signedToken string) {
	cookie := &http.Cookie{
		Name:     authCookieName,
		Value:    signedToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
		MaxAge:   int(authSessionTTL.Seconds()),
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	appDomain := h.getAuthClient().Config().AppDomain
	if appDomain != "" && (host == appDomain || strings.HasSuffix(host, "."+appDomain)) {
		cookie.Domain = appDomain
	}
	http.SetCookie(w, cookie)
}

func (h *authHandlers) clearAuthCookie(w http.ResponseWriter, r *http.Request) {
	cookie := &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	appDomain := h.getAuthClient().Config().AppDomain
	if appDomain != "" && (host == appDomain || strings.HasSuffix(host, "."+appDomain)) {
		cookie.Domain = appDomain
	}
	http.SetCookie(w, cookie)
}

func (h *authHandlers) authPhoto(c *gin.Context) {
	username := strings.TrimSpace(c.Param("username"))
	specStr := strings.TrimSpace(c.Param("spec"))

	if username == "" {
		username = strings.TrimSpace(c.Query("username"))
	}
	if specStr == "" {
		specStr = strings.TrimSpace(c.Query("spec"))
	}

	if username == "" {
		// 未显式指定用户名时，尝试拉取当前登录用户的头像
		if curUser, ok := h.requestUser(c.Request, false); ok && curUser != nil && !curUser.IsGuest && curUser.LoginName != "" {
			username = curUser.LoginName
		}
	}
	if username == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "username is required"})
		return
	}

	spec := 100
	if specStr != "" {
		if s, err := strconv.Atoi(specStr); err == nil && s > 0 {
			spec = s
		}
	}

	if h.getAuthClient().Config().AuthHost == "" {
		name, err := auth.SanitizeUsername(username)
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid username"})
			return
		}
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(http.StatusOK, "image/svg+xml", initialsAvatar(name))
		return
	}
	data, contentType, err := h.getAuthClient().FetchPhoto(c.Request.Context(), username, spec)
	if err != nil {
		if errors.Is(err, auth.ErrPhotoNotFound) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "photo not found"})
			return
		}
		if errors.Is(err, auth.ErrInvalidUsername) {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid username"})
			return
		}
		c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, contentType, data)
}
