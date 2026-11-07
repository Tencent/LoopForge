package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type githubGrant struct {
	id, code      string
	clientID      string
	expires, next time.Time
	interval      time.Duration
	busy          bool
}

const defaultGitHubClientID = "Ov23liPWCuv7rBK2X6H0"

type githubHandlers struct {
	*deps
	client *http.Client
	mu     sync.Mutex
	grants map[int64]*githubGrant
}

func newGitHubHandlers(shared *deps) *githubHandlers {
	return &githubHandlers{deps: shared, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, grants: make(map[int64]*githubGrant)}
}

func (h *githubHandlers) current(c *gin.Context) (int64, bool) {
	c.Header("Cache-Control", "no-store")
	user, ok := h.requestUser(c.Request, false)
	if !ok || user == nil || user.IsGuest || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录 LoopForge，再连接 GitHub"})
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

func (h *githubHandlers) status(c *gin.Context) {
	if _, ok := h.current(c); !ok {
		return
	}
	clientID, err := h.clientID()
	if err != nil {
		respond(c, err)
		return
	}
	admin := h.isPlatformAdmin(c.Request)
	result := gin.H{"enabled": clientID != "", "canConfigure": admin, "managed": strings.TrimSpace(os.Getenv("AGENTFLOW_GITHUB_CLIENT_ID")) != ""}
	if admin {
		result["clientId"] = clientID
	}
	c.JSON(http.StatusOK, result)
}

func (h *githubHandlers) clientID() (string, error) {
	if clientID := strings.TrimSpace(os.Getenv("AGENTFLOW_GITHUB_CLIENT_ID")); clientID != "" {
		return clientID, nil
	}
	return h.repo.UsersRepo.GitHubClientID(defaultGitHubClientID)
}

func (h *githubHandlers) configure(c *gin.Context) {
	if _, ok := h.current(c); !ok {
		return
	}
	if !h.isPlatformAdmin(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅平台管理员可以配置 GitHub 授权应用"})
		return
	}
	if strings.TrimSpace(os.Getenv("AGENTFLOW_GITHUB_CLIENT_ID")) != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "此实例由环境变量管理，请在部署配置中修改 Client ID"})
		return
	}
	var input struct {
		ClientID string `json:"clientId"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 GitHub OAuth App 的 Client ID"})
		return
	}
	input.ClientID = strings.TrimSpace(input.ClientID)
	if input.ClientID != "" && !regexp.MustCompile(`^[A-Za-z0-9_.-]{10,80}$`).MatchString(input.ClientID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Client ID 格式不正确；不要填写 Client Secret 或个人访问令牌"})
		return
	}
	h.mu.Lock()
	err := h.repo.UsersRepo.SetGitHubClientID(input.ClientID)
	if err == nil {
		clear(h.grants)
	}
	h.mu.Unlock()
	if err != nil {
		respond(c, err)
		return
	}
	h.status(c)
}

func (h *githubHandlers) request(c *gin.Context, method, endpoint, token string, form url.Values, output any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "LoopForge")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return errors.New("无法连接 GitHub，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("GitHub 请求失败（HTTP " + strconv.Itoa(response.StatusCode) + "），请检查授权与组织访问策略")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return errors.New("GitHub 返回了无效响应")
	}
	return nil
}

func (h *githubHandlers) start(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	clientID, err := h.clientID()
	if err != nil {
		respond(c, err)
		return
	}
	if clientID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "请管理员配置 GitHub OAuth App 的 Client ID 并启用 Device Flow；用户无需申请 PAT"})
		return
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		respond(c, err)
		return
	}
	grant := &githubGrant{id: hex.EncodeToString(nonce), clientID: clientID, expires: time.Now().Add(15 * time.Minute), next: time.Now().Add(10 * time.Second)}
	h.mu.Lock()
	currentClientID, configErr := h.clientID()
	if configErr != nil || currentClientID != clientID {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "GitHub 应用配置已变更，请重新连接"})
		return
	}
	for owner, pending := range h.grants {
		if time.Now().After(pending.expires) {
			delete(h.grants, owner)
		}
	}
	if pending := h.grants[userID]; (pending != nil && (pending.busy || time.Now().Before(pending.next))) || len(h.grants) >= 1024 {
		h.mu.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "请稍后再发起授权"})
		return
	}
	grant.busy = true
	h.grants[userID] = grant
	h.mu.Unlock()
	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	err = h.request(c, http.MethodPost, "https://github.com/login/device/code", "", url.Values{"client_id": {clientID}, "scope": {"repo"}}, &device)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grants[userID] != grant {
		c.JSON(http.StatusConflict, gin.H{"error": "GitHub 应用配置已变更，请重新连接"})
		return
	}
	if err != nil || device.DeviceCode == "" || device.UserCode == "" || device.ExpiresIn <= 0 || device.ExpiresIn > 1800 || device.VerificationURI != "https://github.com/login/device" {
		delete(h.grants, userID)
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法开始 GitHub 授权，请检查 OAuth App 是否启用 Device Flow 或稍后重试"})
		return
	}
	grant.code = device.DeviceCode
	grant.expires = time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	grant.interval = time.Duration(max(5, device.Interval)) * time.Second
	grant.next = time.Now().Add(grant.interval)
	grant.busy = false
	c.JSON(http.StatusOK, gin.H{"flowId": grant.id, "userCode": device.UserCode, "verificationUri": device.VerificationURI, "expiresIn": device.ExpiresIn, "interval": int(grant.interval.Seconds())})
}

func (h *githubHandlers) poll(c *gin.Context) {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "授权已失效，请重新连接 GitHub"})
		return
	}
	clientID, configErr := h.clientID()
	if configErr != nil || clientID != grant.clientID {
		delete(h.grants, userID)
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "GitHub 应用配置已变更，请重新连接"})
		return
	}
	if grant.busy || time.Now().Before(grant.next) {
		interval := int(grant.interval.Seconds())
		h.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "pending", "interval": interval})
		return
	}
	grant.busy = true
	grant.next = time.Now().Add(grant.interval)
	h.mu.Unlock()
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ExpiresIn   int    `json:"expires_in"`
	}
	err := h.request(c, http.MethodPost, "https://github.com/login/oauth/access_token", "", url.Values{"client_id": {grant.clientID}, "device_code": {grant.code}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &result)
	var identity struct {
		Login string `json:"login"`
	}
	if err == nil && result.AccessToken != "" {
		err = h.request(c, http.MethodGet, "https://api.github.com/user", result.AccessToken, nil, &identity)
		if identity.Login == "" && err == nil {
			err = errors.New("GitHub 未返回账号信息")
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	grant.busy = false
	if h.grants[userID] != grant || time.Now().After(grant.expires) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "授权已失效，请重新连接 GitHub"})
		return
	}
	clientID, configErr = h.clientID()
	if configErr != nil || clientID != grant.clientID {
		delete(h.grants, userID)
		c.JSON(http.StatusConflict, gin.H{"error": "GitHub 应用配置已变更，请重新连接"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if result.Error == "authorization_pending" || result.Error == "slow_down" {
		if result.Error == "slow_down" {
			grant.interval += 5 * time.Second
		}
		grant.next = time.Now().Add(grant.interval)
		c.JSON(http.StatusOK, gin.H{"status": "pending", "interval": int(grant.interval.Seconds())})
		return
	}
	delete(h.grants, userID)
	if result.AccessToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "GitHub 授权被拒绝或已过期，请重新连接"})
		return
	}
	if result.ExpiresIn > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前仅支持非过期 OAuth 令牌，请管理员关闭 OAuth App 的令牌过期选项后重试"})
		return
	}
	saved, err := h.repo.UpdateUserCredentials(userID, repo.UserCredentialUpdate{GitToken: result.AccessToken})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "connected", "login": identity.Login, "user": domain.PublicUser(saved)})
}

func (h *githubHandlers) repositories(c *gin.Context) {
	userID, ok := h.current(c)
	if !ok {
		return
	}
	user, ok := h.repo.User(userID)
	if !ok || user.GitToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先连接 GitHub"})
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效页码"})
		return
	}
	var repositories []struct {
		FullName      string `json:"full_name"`
		HTMLURL       string `json:"html_url"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	_, token := gitwork.SplitToken(user.GitToken)
	err = h.request(c, http.MethodGet, "https://api.github.com/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member&page="+strconv.Itoa(page), token, nil, &repositories)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"repositories": repositories, "hasMore": len(repositories) == 100, "page": page})
}
