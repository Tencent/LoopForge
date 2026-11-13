package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// A unique process secret is safe for tests; deployments set a stable secret.
var processSessionSecret = func() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}()

// 默认配置常量
const (
	DefaultAuthHost  = ""
	DefaultAuthURI   = ""
	DefaultPhotoURI  = ""
	DefaultAppDomain = ""
	DefaultPaaSID    = ""
	DefaultTimeout   = 5 * time.Second
)

// Config 包含企业 SSO 认证客户端的全部配置。
type Config struct {
	PaaSID        string
	AppToken      string
	AuthHost      string
	AuthURI       string
	PhotoURI      string
	AppDomain     string
	LoginURL      string
	SessionSecret string
	Timeout       time.Duration
}

// Client 是与企业 SSO 认证网关交互的客户端。
type Client struct {
	cfg        Config
	httpClient *http.Client
	photoCache *photoCache
}

// NewClient 根据指定配置创建认证客户端。
func NewClient(cfg Config) *Client {
	if cfg.AuthHost == "" {
		cfg.AuthHost = DefaultAuthHost
	}
	cfg.AuthHost = strings.TrimRight(cfg.AuthHost, "/")
	if cfg.AuthURI == "" {
		cfg.AuthURI = DefaultAuthURI
	}
	if cfg.AuthURI != "" && !strings.HasPrefix(cfg.AuthURI, "/") {
		cfg.AuthURI = "/" + cfg.AuthURI
	}
	if cfg.PhotoURI == "" {
		cfg.PhotoURI = DefaultPhotoURI
	}
	if cfg.PhotoURI != "" && !strings.HasPrefix(cfg.PhotoURI, "/") {
		cfg.PhotoURI = "/" + cfg.PhotoURI
	}
	cfg.PhotoURI = strings.TrimRight(cfg.PhotoURI, "/")
	if cfg.AppDomain == "" {
		cfg.AppDomain = DefaultAppDomain
	}
	if cfg.SessionSecret == "" {
		if cfg.AppToken != "" {
			cfg.SessionSecret = cfg.AppToken
		} else {
			cfg.SessionSecret = processSessionSecret
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		photoCache: newPhotoCache(time.Hour, 2000),
	}
}

// NewClientFromEnv 从环境变量中读取配置并创建 Client。
func NewClientFromEnv() *Client {
	paasID := getEnvAny("AGENTFLOW_SSO_PAAS_ID")
	if paasID == "" {
		paasID = DefaultPaaSID
	}
	appToken := getEnvAny("AGENTFLOW_SSO_APP_TOKEN")
	authHost := getEnvAny("AGENTFLOW_SSO_AUTH_HOST")
	authURI := getEnvAny("AGENTFLOW_SSO_AUTH_URI")
	photoURI := getEnvAny("AGENTFLOW_SSO_PHOTO_URI")
	loginURL := getEnvAny("AGENTFLOW_SSO_LOGIN_URL")
	appDomain := getEnvAny("AGENTFLOW_APP_DOMAIN", "APP_DOMAIN")
	sessionSecret := getEnvAny("AGENTFLOW_SESSION_SECRET", "SESSION_SECRET")

	return NewClient(Config{
		PaaSID:        paasID,
		AppToken:      appToken,
		AuthHost:      authHost,
		AuthURI:       authURI,
		PhotoURI:      photoURI,
		AppDomain:     appDomain,
		LoginURL:      loginURL,
		SessionSecret: sessionSecret,
	})
}

// Config 返回客户端配置的副本。
func (c *Client) Config() Config {
	return c.cfg
}

// SetHTTPClient 允许注入自定义 http.Client（主要用于单测 Mock）。
func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.httpClient = hc
	}
}

// SignUser 使用客户端配置的 SessionSecret 为 User 生成签名 Token。
func (c *Client) SignUser(u *User) (string, error) {
	return SignUser(u, c.cfg.SessionSecret)
}

// ParseSignedUser 解析并校验被 SignUser 签名的 Token。
func (c *Client) ParseSignedUser(tokenStr string) (*User, error) {
	return ParseSignedUser(tokenStr, c.cfg.SessionSecret)
}

// FallbackGuestUser 返回优雅降级后的访客身份。
func FallbackGuestUser() *User {
	return &User{
		ID:          0,
		StaffID:     0,
		LoginName:   "guest",
		ChineseName: "访客",
		DeptID:      0,
		DeptName:    "访客",
		IsGuest:     true,
		AuthTime:    time.Now().UTC(),
	}
}

// LoginURL 返回跳转至企业 SSO 登录页的完整 URL；未配置 SSO 时返回本地登录页。
func (c *Client) LoginURL(returnURL string) string {
	loginURL := strings.TrimSpace(c.cfg.LoginURL)
	if c.cfg.AppToken == "" || loginURL == "" {
		return "/login.html"
	}
	returnURL = strings.TrimSpace(returnURL)
	if returnURL == "" {
		returnURL = fmt.Sprintf("http://%s/", c.cfg.AppDomain)
	}
	appKey := c.cfg.PaaSID
	if appKey == "" {
		appKey = DefaultPaaSID
	}
	sep := "?"
	if strings.Contains(loginURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%soauth=true&appkey=%s&url=%s", loginURL, sep,
		url.QueryEscape(appKey), url.QueryEscape(returnURL))
}

// GenerateRIOHeaders 根据时间戳与随机数计算 RIO 网关认证签名头部。
// 签名规则：SHA256(timestamp + appToken + nonce + timestamp).upper()
func GenerateRIOHeaders(paasID, appToken, timestamp, nonce string) map[string]string {
	signStr := timestamp + appToken + nonce + timestamp
	sum := sha256.Sum256([]byte(signStr))
	sig := strings.ToUpper(hex.EncodeToString(sum[:]))
	return map[string]string{
		"x-rio-paasid":    paasID,
		"x-rio-nonce":     nonce,
		"x-rio-timestamp": timestamp,
		"x-rio-signature": sig,
	}
}

// GenerateRIOHeaders 生成当前时刻的 RIO 头部。
func (c *Client) GenerateRIOHeaders() map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	n, _ := rand.Int(rand.Reader, big.NewInt(9000))
	nonce := fmt.Sprintf("%04d", n.Int64()+1000)
	return GenerateRIOHeaders(c.cfg.PaaSID, c.cfg.AppToken, timestamp, nonce)
}

// newSignedGet 构造带 RIO 网关签名头与指定 Accept 头的 GET 请求。
func (c *Client) newSignedGet(ctx context.Context, reqURL, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.GenerateRIOHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", accept)
	return req, nil
}

// ExchangeCode 通过企业 SSO 认证网关将 code 换取员工信息。
// 遵循降级设计：当网关不可用、未配置凭证或鉴权出错时，记录日志并优雅降级为访客，不抛出阻断异常。
func (c *Client) ExchangeCode(ctx context.Context, code string) (*User, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return FallbackGuestUser(), nil
	}

	if c.cfg.PaaSID == "" || c.cfg.AppToken == "" || c.cfg.AuthHost == "" || c.cfg.AuthURI == "" {
		log.Printf("[auth] SSO gateway is not configured, fallback to guest user")
		return FallbackGuestUser(), nil
	}

	reqURL := fmt.Sprintf("%s%s?code=%s", c.cfg.AuthHost, c.cfg.AuthURI, url.QueryEscape(code))
	req, err := c.newSignedGet(ctx, reqURL, "application/json")
	if err != nil {
		log.Printf("[auth] create request failed: %v, fallback to guest", err)
		return FallbackGuestUser(), nil
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[auth] request SSO endpoint failed: %v, fallback to guest", err)
		return FallbackGuestUser(), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 限制 1MB
	if err != nil {
		log.Printf("[auth] read response body failed: %v, fallback to guest", err)
		return FallbackGuestUser(), nil
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[auth] unexpected status code %d from SSO, body: %s, fallback to guest", resp.StatusCode, string(body))
		return FallbackGuestUser(), nil
	}

	var authResp IOAAuthResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		log.Printf("[auth] decode auth response JSON failed: %v, body: %s, fallback to guest", err, string(body))
		return FallbackGuestUser(), nil
	}

	if authResp.Ret != 0 || authResp.Data == nil {
		log.Printf("[auth] SSO returned failure: Ret=%d ErrCode=%d ErrMsg=%s, fallback to guest", authResp.Ret, authResp.ErrCode, authResp.ErrMsg)
		return FallbackGuestUser(), nil
	}

	chineseName := strings.TrimSpace(authResp.Data.ChineseName)
	if chineseName == "" {
		chineseName = strings.TrimSpace(authResp.Data.LoginName)
	}

	loginName := strings.TrimSpace(authResp.Data.LoginName)

	user := &User{
		StaffID:     authResp.Data.StaffId,
		LoginName:   loginName,
		ChineseName: chineseName,
		DeptID:      authResp.Data.DeptId,
		DeptName:    strings.TrimSpace(authResp.Data.DeptName),
		IsGuest:     false,
		AuthTime:    time.Now().UTC(),
	}

	return user, nil
}

func getEnvAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
