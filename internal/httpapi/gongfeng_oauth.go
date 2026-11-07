package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"agentflow/internal/gongfeng"

	"github.com/gin-gonic/gin"
)

const gongfengOAuthStateTTL = 10 * time.Minute

var errNoOAuthStateSecret = errors.New("session secret is not configured")

type gongfengOAuthState struct {
	UserID int64  `json:"uid"`
	Exp    int64  `json:"exp"`
	Nonce  string `json:"n"`
	Return string `json:"ret,omitempty"`
}

// gongfengOAuthStatus tells the settings page whether to offer "用工蜂授权".
// Without a configured Gongfeng application the link only leads to an error.
func (h *authHandlers) gongfengOAuthStatus(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]bool{"enabled": gongfeng.ConfigFromEnv().Enabled()})
}

func (h *authHandlers) gongfengOAuthStart(c *gin.Context) {
	cfg := gongfeng.ConfigFromEnv()
	if !cfg.Enabled() {
		gongfengOAuthPage(c, http.StatusServiceUnavailable, "工蜂授权还没配好", "服务没有配置工蜂应用的 Client ID、Secret 和回调地址。")
		return
	}
	user, ok := h.requestUser(c.Request, false)
	if !ok || user == nil || user.IsGuest || user.ID <= 0 {
		gongfengOAuthPage(c, http.StatusUnauthorized, "请先登录", "登录平台后再授权工蜂仓库访问。")
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		gongfengOAuthPage(c, http.StatusInternalServerError, "无法开始授权", "生成校验值失败，请重试。")
		return
	}
	ret := ""
	if strings.TrimSpace(c.Query("return")) == "quick-access" {
		ret = "quick-access"
	}
	state, err := signGongfengOAuthState(gongfengOAuthState{
		UserID: user.ID,
		Exp:    time.Now().Add(gongfengOAuthStateTTL).Unix(),
		Nonce:  hex.EncodeToString(nonce),
		Return: ret,
	}, h.getAuthClient().Config().SessionSecret)
	if err != nil {
		gongfengOAuthPage(c, http.StatusInternalServerError, "无法开始授权", "签名失败，请重试。")
		return
	}
	location, err := gongfeng.AuthorizeURL(cfg, state)
	if err != nil {
		gongfengOAuthPage(c, http.StatusInternalServerError, "无法开始授权", err.Error())
		return
	}
	c.Redirect(http.StatusFound, location)
}

func (h *authHandlers) gongfengOAuthCallback(c *gin.Context) {
	cfg := gongfeng.ConfigFromEnv()
	if !cfg.Enabled() {
		gongfengOAuthPage(c, http.StatusServiceUnavailable, "工蜂授权还没配好", "服务没有配置工蜂应用。")
		return
	}
	if reason := strings.TrimSpace(c.Query("error")); reason != "" {
		gongfengOAuthPage(c, http.StatusBadRequest, "工蜂没有完成授权", html.EscapeString(reason))
		return
	}
	state, err := parseGongfengOAuthState(c.Query("state"), h.getAuthClient().Config().SessionSecret)
	if err != nil || state.UserID <= 0 || time.Now().Unix() > state.Exp {
		gongfengOAuthPage(c, http.StatusBadRequest, "授权已失效", "请回到个人配置，重新点击「用工蜂授权」。")
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		gongfengOAuthPage(c, http.StatusBadRequest, "授权没有返回码", "请回到个人配置，重新点击「用工蜂授权」。")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tok, err := gongfeng.Exchange(ctx, cfg, code)
	if err != nil {
		gongfengOAuthPage(c, http.StatusBadGateway, "换取工蜂令牌失败", html.EscapeString(err.Error()))
		return
	}
	login, err := gongfeng.Username(ctx, cfg, tok.AccessToken)
	if err != nil {
		gongfengOAuthPage(c, http.StatusBadGateway, "读取工蜂账号失败", html.EscapeString(err.Error()))
		return
	}
	stored, ok := h.repo.User(state.UserID)
	if !ok {
		gongfengOAuthPage(c, http.StatusBadRequest, "找不到当前用户", "请重新登录后再授权。")
		return
	}
	if !gongfengLoginMatches(stored.LoginName, login) {
		gongfengOAuthPage(c, http.StatusForbidden, "工蜂账号和当前登录人不一致", "工蜂账号是 "+html.EscapeString(login)+"，当前登录是 "+html.EscapeString(stored.LoginName)+"。请换用同一个账号授权。")
		return
	}
	if _, err := h.repo.SaveGongfengOAuth(stored.ID, tok.AccessToken, tok.RefreshToken, tok.Expiry(time.Now())); err != nil {
		gongfengOAuthPage(c, http.StatusInternalServerError, "保存授权失败", "请稍后重试。")
		return
	}
	c.Redirect(http.StatusFound, gongfengOAuthReturnURL(cfg))
}

func gongfengLoginMatches(platformLogin, gongfengLogin string) bool {
	if strings.TrimSpace(os.Getenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN")) == "1" {
		return true
	}
	local := strings.TrimSpace(os.Getenv("AGENTFLOW_LOCAL_LOGIN_USER"))
	if local != "" && strings.EqualFold(strings.TrimSpace(platformLogin), local) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(platformLogin), strings.TrimSpace(gongfengLogin))
}

// Authorization runs in its own tab, so success lands on a small page that
// tells the opener's tabs over BroadcastChannel and then closes itself. It
// must share the opener's origin, hence the RedirectURI's scheme and host.
func gongfengOAuthReturnURL(cfg gongfeng.Config) string {
	suffix := "/gongfeng-oauth-done.html"
	u, err := url.Parse(cfg.RedirectURI)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return suffix
	}
	return u.Scheme + "://" + u.Host + suffix
}

// The state is signed with the platform session secret. There is no built-in
// fallback key: a key published in the source would let anyone mint a state.
func signGongfengOAuthState(state gongfengOAuthState, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errNoOAuthStateSecret
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encoded))
	return encoded + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func parseGongfengOAuthState(raw, secret string) (gongfengOAuthState, error) {
	if strings.TrimSpace(secret) == "" {
		return gongfengOAuthState{}, errNoOAuthStateSecret
	}
	encoded, sig, ok := strings.Cut(raw, ".")
	if !ok || encoded == "" || sig == "" {
		return gongfengOAuthState{}, os.ErrInvalid
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encoded))
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return gongfengOAuthState{}, os.ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return gongfengOAuthState{}, err
	}
	var state gongfengOAuthState
	if err := json.Unmarshal(body, &state); err != nil {
		return gongfengOAuthState{}, err
	}
	return state, nil
}

func gongfengOAuthPage(c *gin.Context, status int, title, message string) {
	c.Data(status, "text/html; charset=utf-8", []byte("<!doctype html><meta charset=utf-8><title>"+html.EscapeString(title)+"</title><h1>"+html.EscapeString(title)+"</h1><p>"+message+"</p><p><a href='/#/settings'>返回个人配置</a></p>"))
}
