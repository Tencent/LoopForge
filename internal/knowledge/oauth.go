package knowledge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// OAuth modes stored in KnowledgeConnectorConfig.Extra["oauthMode"].
const (
	OAuthModeNone   = "none"
	OAuthModeAuto   = "auto"
	OAuthModePreset = "preset"
	OAuthModeCustom = "custom"
)

// Token endpoint client authentication methods.
const (
	OAuthAuthMethodPost  = "client_secret_post"
	OAuthAuthMethodBasic = "client_secret_basic"
	OAuthAuthMethodNone  = "none"
	OAuthAuthMethodJSON  = "json_body" // Feishu-style JSON body token endpoint
)

const (
	oauthHTTPTimeout = 15 * time.Second
	oauthMaxBody     = 64 * 1024
	oauthStateTTL    = 10 * time.Minute
	oauthRefreshSkew = 60 * time.Second
	oauthPendingMax  = 1024
)

// OAuthProviderPreset describes one well-known upstream OAuth application.
// Presets only carry endpoints and defaults; credentials come from the
// environment (LOOPFORGE_OAUTH_<KEY>_CLIENT_ID/_CLIENT_SECRET/_SCOPES) or from
// the connector's Extra overrides.
type OAuthProviderPreset struct {
	Key          string
	Name         string
	AuthorizeURL string
	TokenURL     string
	Scopes       []string
	AuthMethod   string
	TokenHeader  string
	TokenPrefix  string
	SupportsDCR  bool
	ScopesEnv    string
}

// BuiltinOAuthPresets returns the built-in provider presets in UI order.
func BuiltinOAuthPresets() []OAuthProviderPreset {
	return []OAuthProviderPreset{
		{
			Key:          "feishu",
			Name:         "飞书 / Lark",
			AuthorizeURL: "https://accounts.feishu.cn/open-apis/authen/v1/authorize",
			TokenURL:     "https://mcp.feishu.cn/oauth/token",
			AuthMethod:   OAuthAuthMethodJSON,
			TokenHeader:  "X-Lark-MCP-UAT",
			TokenPrefix:  "",
			SupportsDCR:  true,
			ScopesEnv:    "LOOPFORGE_OAUTH_FEISHU_SCOPES",
		},
		{
			Key:          "tencent_docs",
			Name:         "腾讯文档",
			AuthorizeURL: "https://docs.qq.com/oauth/v2/authorize",
			TokenURL:     "https://docs.qq.com/oauth/v2/token",
			Scopes:       []string{"all"},
			AuthMethod:   OAuthAuthMethodPost,
			TokenHeader:  "Authorization",
			TokenPrefix:  "",
			SupportsDCR:  false,
			ScopesEnv:    "LOOPFORGE_OAUTH_TENCENT_DOCS_SCOPES",
		},
		{
			Key:          "lexiang",
			Name:         "乐享知识库",
			AuthorizeURL: "https://lexiang.tencent.com/oauth2/authorize",
			TokenURL:     "https://lexiang.tencent.com/oauth2/token",
			Scopes:       []string{"knowledge:read"},
			AuthMethod:   OAuthAuthMethodPost,
			TokenHeader:  "Authorization",
			TokenPrefix:  "Bearer ",
			SupportsDCR:  false,
			ScopesEnv:    "LOOPFORGE_OAUTH_LEXIANG_SCOPES",
		},
		{
			Key:          "notion",
			Name:         "Notion",
			AuthorizeURL: "https://api.notion.com/v1/oauth/authorize",
			TokenURL:     "https://api.notion.com/v1/oauth/token",
			AuthMethod:   OAuthAuthMethodBasic,
			TokenHeader:  "Authorization",
			TokenPrefix:  "Bearer ",
			SupportsDCR:  false,
			ScopesEnv:    "LOOPFORGE_OAUTH_NOTION_SCOPES",
		},
	}
}

// FindOAuthPreset looks up a built-in preset by key (case-insensitive).
func FindOAuthPreset(key string) (OAuthProviderPreset, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, p := range BuiltinOAuthPresets() {
		if p.Key == key {
			return p, true
		}
	}
	return OAuthProviderPreset{}, false
}

// OAuthAppCredentials is one platform-registered OAuth application.
type OAuthAppCredentials struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// EnvOAuthApp reads LOOPFORGE_OAUTH_<KEY>_CLIENT_ID/_CLIENT_SECRET/_SCOPES for
// the given preset key. Missing values simply mean "not configured".
func EnvOAuthApp(preset string) OAuthAppCredentials {
	key := strings.ToUpper(strings.TrimSpace(preset))
	replacer := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	key = replacer.Replace(key)
	return OAuthAppCredentials{
		ClientID:     strings.TrimSpace(os.Getenv("LOOPFORGE_OAUTH_" + key + "_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(os.Getenv("LOOPFORGE_OAUTH_" + key + "_CLIENT_SECRET")),
		Scopes:       SplitOAuthScopes(os.Getenv("LOOPFORGE_OAUTH_" + key + "_SCOPES")),
	}
}

// SplitOAuthScopes splits a comma/space separated scope list.
func SplitOAuthScopes(raw string) []string {
	return CleanList(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}))
}

// oauthRandReader is the entropy source for PKCE verifiers and state nonces.
// It is a variable so tests can substitute a failing reader; there is
// deliberately no time-based fallback (authorization must fail closed).
var oauthRandReader io.Reader = rand.Reader

// newPKCE returns an RFC 7636 S256 verifier (43 chars) and its challenge.
func newPKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(oauthRandReader, buf); err != nil {
		return "", "", fmt.Errorf("生成 PKCE 校验值失败：%w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// oauthStatePayload is the signed part of the OAuth state parameter. The
// connector identity lives in the server-side pending table keyed by Nonce, so
// the state itself stays short (<120 chars).
type oauthStatePayload struct {
	Nonce string `json:"n"`
	Exp   int64  `json:"e"`
}

func oauthStateSecret() (string, error) {
	material, err := platformSecretMaterial()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("loopforge/knowledge/oauth-state/v1\x00" + material))
	return hex.EncodeToString(sum[:]), nil
}

// signState returns "<nonce>.<exp-base36>.<hmac-hex>".
func signState(secret string, payload oauthStatePayload) string {
	raw := payload.Nonce + "." + strconv.FormatInt(payload.Exp, 36)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	return raw + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifyState checks the signature and expiry of a state value.
func verifyState(secret, state string) (oauthStatePayload, error) {
	return verifyStateAt(secret, state, time.Now())
}

func verifyStateAt(secret, state string, now time.Time) (oauthStatePayload, error) {
	parts := strings.Split(strings.TrimSpace(state), ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return oauthStatePayload{}, errors.New("授权状态格式无效")
	}
	raw := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		return oauthStatePayload{}, errors.New("授权状态签名无效")
	}
	exp, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil {
		return oauthStatePayload{}, errors.New("授权状态格式无效")
	}
	if now.Unix() > exp {
		return oauthStatePayload{}, errors.New("授权状态已过期")
	}
	return oauthStatePayload{Nonce: parts[0], Exp: exp}, nil
}

// VerifyOAuthStateNonce validates a signed OAuth state token and returns its nonce.
func VerifyOAuthStateNonce(state string) (string, error) {
	secret, err := oauthStateSecret()
	if err != nil {
		return "", err
	}
	payload, err := verifyState(secret, state)
	if err != nil {
		return "", err
	}
	return payload.Nonce, nil
}

// validateAuthURL accepts only absolute http(s) URLs, blocking javascript:,
// file: and other schemes used for redirect-based credential exfiltration.
func validateAuthURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("OAuth 地址无法解析")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("OAuth 地址仅支持 http/https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return errors.New("OAuth 地址缺少主机名")
	}
	return nil
}

// oauthAllowLoopbackForTest lets httptest servers through the SSRF guard.
// Production callers must use LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE=1 instead.
var oauthAllowLoopbackForTest bool

func oauthInsecureAllowed() bool {
	if oauthAllowLoopbackForTest {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// blockedOAuthIP rejects loopback, private, link-local (169.254.0.0/16),
// CGNAT (100.64.0.0/10), unspecified, multicast, benchmarking and NAT64
// addresses unless the test hook is enabled.
func blockedOAuthIP(ip net.IP) bool {
	return isBlockedConnectorIP(ip, false)
}

var (
	oauthHTTPOnce   sync.Once
	oauthHTTPShared *http.Client
)

// oauthHTTPClient returns the shared hardened client for OAuth discovery,
// registration and token calls (15s timeout, 64KB bodies, SSRF-guarded dial,
// same-origin redirects only).
func oauthHTTPClient() *http.Client {
	oauthHTTPOnce.Do(func() {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		oauthHTTPShared = &http.Client{
			Timeout: oauthHTTPTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("OAuth 请求重定向次数过多")
				}
				if len(via) > 0 {
					origin := via[0].URL
					if origin == nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
						return errors.New("OAuth 请求禁止跨源重定向")
					}
				}
				return validateAuthURL(req.URL.String())
			},
			Transport: &http.Transport{
				// No environment proxy: the destination must always be resolved
				// and dialled by this repo's SSRF guard, never handed to a proxy.
				Proxy:                 nil,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: oauthHTTPTimeout,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					host, port, err := net.SplitHostPort(addr)
					if err != nil {
						return nil, errors.New("OAuth 地址无效")
					}
					if ip := net.ParseIP(host); ip != nil {
						if blockedOAuthIP(ip) {
							return nil, errors.New("OAuth 地址不允许访问")
						}
						return dialer.DialContext(ctx, network, addr)
					}
					ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
					if err != nil {
						return nil, errors.New("OAuth 地址无法解析")
					}
					for _, candidate := range ips {
						if blockedOAuthIP(candidate.IP) {
							continue
						}
						return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
					}
					return nil, errors.New("OAuth 地址不允许访问")
				},
			},
		}
	})
	return oauthHTTPShared
}

// oauthFetch performs one HTTP request with the hardened client and returns
// status, headers and a body capped at 64KB.
func oauthFetch(ctx context.Context, method, rawURL, contentType string, payload []byte, basicUser, basicPass string) (int, http.Header, []byte, error) {
	if err := validateAuthURL(rawURL); err != nil {
		return 0, nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, strings.NewReader(string(payload)))
	if err != nil {
		return 0, nil, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loopforge-knowledge-oauth/1.0")
	if basicUser != "" || basicPass != "" {
		req.SetBasicAuth(basicUser, basicPass)
	}
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

// tokenRequest is one authorization_code or refresh_token exchange.
type tokenRequest struct {
	TokenURL     string
	GrantType    string
	Code         string
	RedirectURI  string
	CodeVerifier string
	ClientID     string
	ClientSecret string
	AuthMethod   string
	RefreshToken string
	Resource     string
	Scope        string
}

// tokenResponse is the normalized successful token endpoint payload.
type tokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
}

// exchangeToken calls the provider token endpoint. It supports form bodies,
// Feishu-style JSON bodies and client_secret_basic, and tolerates both
// top-level and legacy {"code":0,"data":{...}} response shapes.
func exchangeToken(ctx context.Context, req tokenRequest) (tokenResponse, error) {
	tokenURL := strings.TrimSpace(req.TokenURL)
	if tokenURL == "" {
		return tokenResponse{}, errors.New("缺少 OAuth Token 地址")
	}
	grant := firstNonEmpty(req.GrantType, "authorization_code")
	method := strings.ToLower(strings.TrimSpace(req.AuthMethod))
	if method == "" {
		method = OAuthAuthMethodPost
	}

	values := map[string]string{
		"grant_type": grant,
	}
	if req.Code != "" {
		values["code"] = req.Code
	}
	if req.RedirectURI != "" {
		values["redirect_uri"] = req.RedirectURI
	}
	if req.CodeVerifier != "" {
		values["code_verifier"] = req.CodeVerifier
	}
	if req.ClientID != "" {
		values["client_id"] = req.ClientID
	}
	if req.Resource != "" {
		values["resource"] = req.Resource
	}
	if req.RefreshToken != "" {
		values["refresh_token"] = req.RefreshToken
	}
	if req.Scope != "" {
		values["scope"] = req.Scope
	}

	var body []byte
	var contentType string
	var basicUser, basicPass string
	switch method {
	case OAuthAuthMethodJSON:
		payload := map[string]string{}
		for k, v := range values {
			payload[k] = v
		}
		if req.ClientSecret != "" {
			payload["client_secret"] = req.ClientSecret
		}
		body, _ = json.Marshal(payload)
		contentType = "application/json"
	case OAuthAuthMethodBasic:
		basicUser, basicPass = req.ClientID, req.ClientSecret
		body = []byte(encodeOAuthForm(values))
		contentType = "application/x-www-form-urlencoded"
	default: // client_secret_post / none
		if req.ClientSecret != "" {
			values["client_secret"] = req.ClientSecret
		}
		body = []byte(encodeOAuthForm(values))
		contentType = "application/x-www-form-urlencoded"
	}

	status, _, respBody, err := oauthFetch(ctx, http.MethodPost, tokenURL, contentType, body, basicUser, basicPass)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("OAuth 令牌请求失败：%w", err)
	}
	if status < 200 || status >= 300 {
		if desc := oauthErrorDescription(respBody); desc != "" {
			return tokenResponse{}, fmt.Errorf("OAuth 令牌请求失败（HTTP %d）：%s", status, desc)
		}
		return tokenResponse{}, fmt.Errorf("OAuth 令牌请求失败（HTTP %d）", status)
	}
	return parseTokenResponse(respBody)
}

func encodeOAuthForm(values map[string]string) string {
	form := url.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	return form.Encode()
}

// parseTokenResponse accepts the standard flat shape, Feishu's nested data
// shape, and treats a non-zero `code` as an error.
func parseTokenResponse(raw []byte) (tokenResponse, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return tokenResponse{}, errors.New("OAuth 令牌响应无法解析")
	}
	if rawCode, ok := top["code"]; ok {
		if code := jsonInt(rawCode); code != 0 {
			desc := firstNonEmpty(jsonString(top["msg"]), jsonString(top["message"]), jsonString(top["error_description"]), jsonString(top["error"]))
			if desc == "" {
				desc = fmt.Sprintf("code=%d", code)
			}
			return tokenResponse{}, fmt.Errorf("OAuth 令牌接口返回错误：%s", truncateRunes(desc, 200))
		}
	}
	if errText := jsonString(top["error"]); errText != "" {
		desc := firstNonEmpty(jsonString(top["error_description"]), errText)
		return tokenResponse{}, fmt.Errorf("OAuth 令牌接口返回错误：%s", truncateRunes(desc, 200))
	}

	candidates := []map[string]json.RawMessage{top}
	if nested, ok := top["data"]; ok {
		var data map[string]json.RawMessage
		if json.Unmarshal(nested, &data) == nil {
			candidates = append(candidates, data)
		}
	}
	for _, m := range candidates {
		access := strings.TrimSpace(jsonString(m["access_token"]))
		if access == "" {
			continue
		}
		out := tokenResponse{
			AccessToken:  access,
			RefreshToken: strings.TrimSpace(jsonString(m["refresh_token"])),
			ExpiresIn:    jsonInt(m["expires_in"]),
			Scope:        strings.TrimSpace(jsonString(m["scope"])),
		}
		if out.ExpiresIn <= 0 {
			out.ExpiresIn = jsonInt(m["expires_in_seconds"])
		}
		return out, nil
	}
	if desc := oauthErrorDescription(raw); desc != "" {
		return tokenResponse{}, fmt.Errorf("OAuth 令牌接口返回错误：%s", desc)
	}
	return tokenResponse{}, errors.New("OAuth 令牌响应缺少 access_token")
}

func oauthErrorDescription(raw []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if code := jsonInt(m["code"]); code != 0 {
		return truncateRunes(firstNonEmpty(jsonString(m["msg"]), jsonString(m["message"]), fmt.Sprintf("code=%d", code)), 200)
	}
	return truncateRunes(firstNonEmpty(jsonString(m["error_description"]), jsonString(m["message"]), jsonString(m["error"]), jsonString(m["msg"])), 200)
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func jsonInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return 0
}

// refreshToken exchanges a refresh token for a fresh access token. Providers
// that do not rotate the refresh token keep the previous value.
func refreshToken(ctx context.Context, req tokenRequest) (tokenResponse, error) {
	req.GrantType = "refresh_token"
	return exchangeToken(ctx, req)
}

// tokenExpiry converts expires_in into an absolute UTC timestamp.
func tokenExpiry(resp tokenResponse, now time.Time) *time.Time {
	if resp.ExpiresIn <= 0 {
		return nil
	}
	exp := now.Add(time.Duration(resp.ExpiresIn) * time.Second).UTC()
	return &exp
}

// oauthTokenHeader resolves the header carrying the access token.
func oauthTokenHeader(cfg domain.KnowledgeConnectorConfig) (header, prefix string) {
	header = strings.TrimSpace(cfg.Extra["oauthTokenHeader"])
	if header == "" {
		header = "Authorization"
	}
	prefix, ok := cfg.Extra["oauthTokenPrefix"]
	if !ok {
		prefix = "Bearer "
	}
	return header, prefix
}

func oauthMode(cfg domain.KnowledgeConnectorConfig) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.Extra["oauthMode"]))
	if mode == "" {
		return OAuthModeNone
	}
	return mode
}

func oauthModeActive(cfg domain.KnowledgeConnectorConfig) bool {
	return oauthMode(cfg) != OAuthModeNone
}

func resolveOAuthPreset(cfg domain.KnowledgeConnectorConfig) OAuthProviderPreset {
	if p, ok := FindOAuthPreset(cfg.Extra["oauthPreset"]); ok {
		return p
	}
	return OAuthProviderPreset{}
}

// cimdEnabled reports whether Client ID Metadata Documents may be used as the
// client_id. Disabled by default until the platform serves the document.
func cimdEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOOPFORGE_OAUTH_CIMD_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// cimdClientID returns the public metadata document URL to use as client_id,
// or "" when the feature is off or the platform has no HTTPS base URL.
func cimdClientID(baseURL string) string {
	if !cimdEnabled() {
		return ""
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(baseURL, "https://") {
		return ""
	}
	return baseURL + "/oauth-client-metadata.json"
}

// parseSecretExpiry parses the sealed secret's RFC3339 expiry.
func parseSecretExpiry(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return &t
	}
	return nil
}

// oauthUpstreamError turns an exchange failure into a user-facing error with
// any credential material redacted.
func oauthUpstreamError(err error, secret domain.KnowledgeConnectorSecret, extra ...string) error {
	if err == nil {
		return nil
	}
	message := RedactSecrets(err.Error(), secret)
	message = redactOAuthSensitive(message, extra...)
	return apperr.New(apperr.Upstream, truncateRunes(message, 300))
}
