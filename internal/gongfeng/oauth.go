// Package gongfeng talks to the Gongfeng (工蜂) OAuth endpoints used to obtain
// a user-scoped repository credential. Git operations keep using that access
// token as the existing "private" basic-auth password.
package gongfeng

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultAuthorizeURL = "https://git.example.com/oauth/authorize"
	defaultTokenURL     = "https://git.example.com/oauth/token"
	defaultAPIBase      = "https://git.example.com/api/v3"
	// Repository clone and push need both scopes. The application must allow them.
	RepositoryScope = "read_repository write_repository"
	defaultTTL      = 2 * time.Hour
)

// httpClient bounds every call to the Gongfeng OAuth endpoints. A refresh runs
// while the caller holds the user's refresh lock, so it must never hang.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// Config is the OAuth application registered on git.example.com.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AuthorizeURL string
	TokenURL     string
	APIBase      string
}

// Enabled reports whether the process has enough configuration to start a grant.
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.ClientSecret) != "" && strings.TrimSpace(c.RedirectURI) != ""
}

// ConfigFromEnv reads the application credentials. Authorize, token and API
// URLs fall back to git.example.com; the overrides exist so tests can point at a
// local server.
func ConfigFromEnv() Config {
	return Config{
		ClientID:     strings.TrimSpace(os.Getenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(os.Getenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET")),
		RedirectURI:  strings.TrimSpace(os.Getenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI")),
		AuthorizeURL: envOr("AGENTFLOW_GONGFENG_OAUTH_AUTHORIZE_URL", defaultAuthorizeURL),
		TokenURL:     envOr("AGENTFLOW_GONGFENG_OAUTH_TOKEN_URL", defaultTokenURL),
		APIBase:      strings.TrimRight(envOr("AGENTFLOW_GONGFENG_OAUTH_API_BASE", defaultAPIBase), "/"),
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// Token is an OAuth access grant. RefreshToken may be empty when the
// application does not issue one; ExpiresIn is seconds.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Expiry is when the access token should be refreshed. A missing lifetime
// uses the two-hour default from the Gongfeng OAuth documentation.
func (t Token) Expiry(now time.Time) time.Time {
	ttl := time.Duration(t.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return now.Add(ttl)
}

// AuthorizeURL is the browser location that asks the user to approve the app.
func AuthorizeURL(cfg Config, state string) (string, error) {
	if !cfg.Enabled() {
		return "", fmt.Errorf("工蜂 OAuth 未配置")
	}
	u, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("scope", RepositoryScope)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Exchange trades an authorization code for an access token.
func Exchange(ctx context.Context, cfg Config, code string) (Token, error) {
	return requestToken(ctx, cfg, url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {strings.TrimSpace(code)},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {cfg.RedirectURI},
	})
}

// Refresh exchanges a refresh token for a new access token.
func Refresh(ctx context.Context, cfg Config, refreshToken string) (Token, error) {
	tok, err := requestToken(ctx, cfg, url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"refresh_token": {strings.TrimSpace(refreshToken)},
		"grant_type":    {"refresh_token"},
		"redirect_uri":  {cfg.RedirectURI},
	})
	if err != nil {
		return Token{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = strings.TrimSpace(refreshToken)
	}
	return tok, nil
}

// Username returns the Gongfeng login bound to an access token.
func Username(ctx context.Context, cfg Config, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.APIBase+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接工蜂：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("工蜂用户信息返回 HTTP %d", resp.StatusCode)
	}
	var user struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &user); err != nil || strings.TrimSpace(user.Username) == "" {
		return "", fmt.Errorf("工蜂用户信息无法解析")
	}
	return user.Username, nil
}

func requestToken(ctx context.Context, cfg Config, form url.Values) (Token, error) {
	if !cfg.Enabled() {
		return Token{}, fmt.Errorf("工蜂 OAuth 未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("无法连接工蜂：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Token{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("工蜂令牌接口返回 HTTP %d：%s", resp.StatusCode, redact(cfg, truncate(string(body), 180)))
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Token{}, fmt.Errorf("工蜂令牌响应无法解析")
	}
	if raw.AccessToken == "" {
		msg := raw.Description
		if msg == "" {
			msg = raw.Error
		}
		if msg == "" {
			msg = "响应里没有 access_token"
		}
		return Token{}, fmt.Errorf("工蜂没有返回访问令牌：%s", redact(cfg, msg))
	}
	return Token{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, ExpiresIn: raw.ExpiresIn}, nil
}

func redact(cfg Config, msg string) string {
	if cfg.ClientSecret != "" {
		msg = strings.ReplaceAll(msg, cfg.ClientSecret, "***")
	}
	return msg
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
