package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"agentflow/internal/codexoauth"
	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// codexDeviceClient is the OpenAI device sign-in (codexoauth.Client).
type codexDeviceClient interface {
	StartDevice(ctx context.Context) (codexoauth.DeviceStart, error)
	PollDevice(ctx context.Context, d codexoauth.DeviceStart) (codexoauth.Tokens, bool, error)
}

type codexGrant struct {
	id       string
	device   codexoauth.DeviceStart
	interval time.Duration
	expires  time.Time
	next     time.Time
	busy     bool
}

const (
	codexDeviceMinInterval = 3 * time.Second
	codexDevicePollTimeout = 30 * time.Second
)

// codexLoginHandlers signs a user in to ChatGPT for Codex. The full login,
// refresh token included, stays on the platform; responses carry only the
// account email and status.
type codexLoginHandlers struct {
	*deps
	client codexDeviceClient
	mu     sync.Mutex
	grants map[int64]*codexGrant
}

func newCodexLoginHandlers(shared *deps) *codexLoginHandlers {
	return &codexLoginHandlers{deps: shared, client: codexoauth.NewClient(os.Getenv("AGENTFLOW_CODEX_AUTH_BASE_URL")), grants: make(map[int64]*codexGrant)}
}

func (h *codexLoginHandlers) current(c *gin.Context) (int64, bool) {
	c.Header("Cache-Control", "no-store")
	user, ok := h.requestUser(c.Request, false)
	if !ok || user == nil || user.IsGuest || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录 LoopForge，再登录 ChatGPT"})
		return 0, false
	}
	if c.Request.Method != http.MethodGet {
		origin, err := url.Parse(c.GetHeader("Origin"))
		if err != nil || origin.Host != c.Request.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
			c.JSON(http.StatusForbidden, gin.H{"error": "请求来源不允许"})
			return 0, false
		}
	}
	return user.ID, true
}

// codexLoginEmail is the ChatGPT account of the user's own login. The user
// directory is visible to everyone, so the email is returned only here.
func codexLoginEmail(user domain.User) string {
	auth, err := codexoauth.ParseAuthFile(user.CodexChatGPTLogin)
	if err != nil {
		return ""
	}
	return auth.Email()
}

func (h *codexLoginHandlers) reply(c *gin.Context, status string, userID int64) {
	saved, _ := h.repo.User(userID)
	c.JSON(http.StatusOK, gin.H{"status": status, "email": codexLoginEmail(saved), "user": domain.PublicUser(saved)})
}

func (h *codexLoginHandlers) status(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	user, _ := h.repo.User(userID)
	public := domain.PublicUser(user)
	c.JSON(http.StatusOK, gin.H{
		"configured":  public.CodexChatGPTLoginConfigured,
		"email":       codexLoginEmail(user),
		"loginStatus": public.CodexChatGPTLoginStatus,
	})
}

// paste stores an auth.json produced by `codex login` elsewhere.
func (h *codexLoginHandlers) paste(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	var input struct {
		AuthJSON string `json:"authJson"`
	}
	if !decode(c, &input) {
		return
	}
	if _, err := h.repo.SaveCodexChatGPTLogin(userID, input.AuthJSON); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.reply(c, "connected", userID)
}

func (h *codexLoginHandlers) signOut(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	h.mu.Lock()
	delete(h.grants, userID)
	h.mu.Unlock()
	if _, err := h.repo.ClearCodexChatGPTLogin(userID); err != nil {
		respond(c, err)
		return
	}
	h.reply(c, "signed_out", userID)
}

func (h *codexLoginHandlers) start(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		respond(c, err)
		return
	}
	now := time.Now()
	grant := &codexGrant{id: hex.EncodeToString(nonce), busy: true, expires: now.Add(15 * time.Minute)}
	h.mu.Lock()
	for owner, pending := range h.grants {
		if now.After(pending.expires) {
			delete(h.grants, owner)
		}
	}
	if pending := h.grants[userID]; (pending != nil && pending.busy) || len(h.grants) >= 1024 {
		h.mu.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "请稍后再发起登录"})
		return
	}
	h.grants[userID] = grant
	h.mu.Unlock()
	device, err := h.client.StartDevice(c.Request.Context())
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grants[userID] != grant {
		c.JSON(http.StatusConflict, gin.H{"error": "登录已取消，请重新发起"})
		return
	}
	if err != nil {
		delete(h.grants, userID)
		if errors.Is(err, codexoauth.ErrDeviceNotEnabled) {
			c.JSON(http.StatusConflict, gin.H{"error": "该 ChatGPT 账号未开启设备码登录：请在 ChatGPT 安全设置中开启，或粘贴本机 ~/.codex/auth.json", "code": "not_enabled"})
			return
		}
		log.Printf("codex device login for user %d: start: %v", userID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法连接 OpenAI 登录服务，请稍后重试"})
		return
	}
	grant.device = device
	grant.interval = max(device.Interval, codexDeviceMinInterval)
	grant.expires = time.Now().Add(device.ExpiresIn)
	grant.next = time.Now().Add(grant.interval)
	grant.busy = false
	c.JSON(http.StatusOK, gin.H{
		"flowId": grant.id, "userCode": device.UserCode, "verificationUri": device.VerificationURL,
		"expiresIn": int(device.ExpiresIn / time.Second), "interval": int(grant.interval / time.Second),
	})
}

func (h *codexLoginHandlers) poll(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	var input struct {
		FlowID string `json:"flowId"`
	}
	if !decode(c, &input) {
		return
	}
	h.mu.Lock()
	grant := h.grants[userID]
	if grant == nil || grant.id != input.FlowID || time.Now().After(grant.expires) {
		h.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "登录已失效，请重新发起", "code": "expired"})
		return
	}
	if grant.busy || time.Now().Before(grant.next) {
		interval := int(grant.interval / time.Second)
		h.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "pending", "interval": interval})
		return
	}
	grant.busy = true
	device := grant.device
	h.mu.Unlock()
	pollCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), codexDevicePollTimeout)
	tokens, authorized, err := h.client.PollDevice(pollCtx, device)
	cancel()
	h.mu.Lock()
	grant.busy = false
	grant.next = time.Now().Add(grant.interval)
	if h.grants[userID] != grant {
		h.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "登录已取消，请重新发起", "code": "expired"})
		return
	}
	var oauthErr *codexoauth.Error
	if (err == nil && !authorized) || (err != nil && !errors.As(err, &oauthErr)) {
		if err != nil {
			log.Printf("codex device login for user %d: poll: %v", userID, err)
		}
		interval := int(grant.interval / time.Second)
		h.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "pending", "interval": interval})
		return
	}
	delete(h.grants, userID)
	h.mu.Unlock()
	if err != nil {
		switch oauthErr.Code {
		case "access_denied", "authorization_declined":
			c.JSON(http.StatusBadRequest, gin.H{"error": "已在 ChatGPT 拒绝授权", "code": "denied"})
		case "expired_token", "deviceauth_expired":
			c.JSON(http.StatusBadRequest, gin.H{"error": "登录已过期，请重新发起", "code": "expired"})
		default:
			log.Printf("codex device login for user %d: exchange: %v", userID, err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "OpenAI 登录服务返回错误，请稍后重试"})
		}
		return
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "OpenAI 未返回完整的登录凭证，请重试"})
		return
	}
	content, err := codexoauth.NewAuthFile(tokens, time.Now()).Encode()
	if err == nil {
		_, err = h.repo.SaveCodexChatGPTLogin(userID, content)
	}
	if err != nil {
		respond(c, err)
		return
	}
	h.reply(c, "connected", userID)
}

func (h *codexLoginHandlers) cancel(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	h.mu.Lock()
	delete(h.grants, userID)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}
