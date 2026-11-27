package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// resourceMeta is the RFC 9728 protected-resource metadata used to locate the
// authorization server that guards an MCP endpoint.
type resourceMeta struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServerMeta is the RFC 8414 / OIDC discovery document subset we consume.
type authServerMeta struct {
	Issuer                      string   `json:"issuer"`
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	RegistrationEndpoint        string   `json:"registration_endpoint"`
	ScopesSupported             []string `json:"scopes_supported"`
	TokenEndpointAuthMethods    []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethods        []string `json:"code_challenge_methods_supported"`
	ResourceIndicatorsSupported bool     `json:"resource_indicators_supported"`
	ClientIDMetadataDocument    bool     `json:"client_id_metadata_document_supported"`
}

// clientRegistration is an RFC 7591 dynamic client registration result.
type clientRegistration struct {
	ClientID                string
	ClientSecret            string
	TokenEndpointAuthMethod string
}

// parseAuthParam extracts a parameter from a WWW-Authenticate style header.
// It tolerates multiple parameters in any order, multiple challenges and
// quoted values containing commas.
func parseAuthParam(header, name string) string {
	header = strings.TrimSpace(header)
	if header == "" || name == "" {
		return ""
	}
	lower := strings.ToLower(header)
	needle := strings.ToLower(name)
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], needle)
		if idx < 0 {
			return ""
		}
		idx += offset
		if idx > 0 {
			prev := header[idx-1]
			if !isAuthParamBoundary(prev) {
				offset = idx + len(needle)
				continue
			}
		}
		j := idx + len(needle)
		for j < len(header) && (header[j] == ' ' || header[j] == '\t') {
			j++
		}
		if j >= len(header) || header[j] != '=' {
			offset = idx + len(needle)
			continue
		}
		j++
		for j < len(header) && (header[j] == ' ' || header[j] == '\t') {
			j++
		}
		if j >= len(header) {
			return ""
		}
		if header[j] == '"' {
			var b strings.Builder
			for k := j + 1; k < len(header); k++ {
				if header[k] == '\\' && k+1 < len(header) {
					b.WriteByte(header[k+1])
					k++
					continue
				}
				if header[k] == '"' {
					return strings.TrimSpace(b.String())
				}
				b.WriteByte(header[k])
			}
			return strings.TrimSpace(b.String())
		}
		k := j
		for k < len(header) && header[k] != ',' && header[k] != ' ' && header[k] != '\t' {
			k++
		}
		return strings.TrimSpace(header[j:k])
	}
	return ""
}

func isAuthParamBoundary(r byte) bool {
	return r == ' ' || r == '\t' || r == ',' || r == ';'
}

// canonicalResource normalizes an MCP endpoint URI for RFC 8707: scheme/host
// are lowercased, the query is preserved, userinfo and fragment are dropped.
func canonicalResource(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.User = nil
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func oauthWellKnownCandidates(base *url.URL, suffix string) []string {
	origin := base.Scheme + "://" + base.Host
	path := strings.TrimRight(base.EscapedPath(), "/")
	var out []string
	if path != "" {
		out = append(out, origin+"/.well-known/"+suffix+path)
	}
	out = append(out, origin+"/.well-known/"+suffix)
	if path != "" {
		out = append(out, strings.TrimRight(base.String(), "/")+"/.well-known/"+suffix)
	}
	return CleanList(out)
}

func fetchOAuthJSON(ctx context.Context, rawURL string, target any) error {
	status, _, body, err := oauthFetch(ctx, http.MethodGet, rawURL, "", nil, "", "")
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return errors.New("响应不是合法 JSON")
	}
	return nil
}

// DiscoverProtectedResource locates the authorization server for an MCP
// endpoint. It first honours the WWW-Authenticate resource_metadata pointer,
// then probes the RFC 9728 well-known locations.
func DiscoverProtectedResource(ctx context.Context, endpoint, wwwAuth string) (*resourceMeta, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("MCP 服务地址无效，无法发现 OAuth 元数据")
	}
	if raw := parseAuthParam(wwwAuth, "resource_metadata"); raw != "" {
		var meta resourceMeta
		if err := fetchOAuthJSON(ctx, raw, &meta); err == nil && len(meta.AuthorizationServers) > 0 {
			if strings.TrimSpace(meta.Resource) == "" {
				meta.Resource = canonicalResource(endpoint)
			}
			return &meta, nil
		}
	}
	for _, candidate := range oauthWellKnownCandidates(u, "oauth-protected-resource") {
		var meta resourceMeta
		if err := fetchOAuthJSON(ctx, candidate, &meta); err != nil {
			continue
		}
		if len(meta.AuthorizationServers) == 0 {
			continue
		}
		if strings.TrimSpace(meta.Resource) == "" {
			meta.Resource = canonicalResource(endpoint)
		}
		return &meta, nil
	}
	// No published metadata: assume the endpoint's own origin is the issuer.
	return &resourceMeta{
		Resource:             canonicalResource(endpoint),
		AuthorizationServers: []string{u.Scheme + "://" + u.Host},
	}, nil
}

// DiscoverAuthServer reads RFC 8414 or OIDC discovery metadata for an issuer.
func DiscoverAuthServer(ctx context.Context, issuer string) (*authServerMeta, error) {
	base, err := url.Parse(strings.TrimSpace(issuer))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("OAuth 授权服务器地址无效")
	}
	candidates := append(
		oauthWellKnownCandidates(base, "oauth-authorization-server"),
		oauthWellKnownCandidates(base, "openid-configuration")...,
	)
	var lastErr error
	for _, candidate := range CleanList(candidates) {
		var meta authServerMeta
		if err := fetchOAuthJSON(ctx, candidate, &meta); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(meta.AuthorizationEndpoint) == "" || strings.TrimSpace(meta.TokenEndpoint) == "" {
			lastErr = errors.New("授权服务器元数据缺少 endpoint")
			continue
		}
		if validateAuthURL(meta.AuthorizationEndpoint) != nil || validateAuthURL(meta.TokenEndpoint) != nil {
			lastErr = errors.New("授权服务器 endpoint 不是 http/https")
			continue
		}
		if meta.Issuer == "" {
			meta.Issuer = strings.TrimRight(issuer, "/")
		}
		return &meta, nil
	}
	if lastErr == nil {
		lastErr = errors.New("未找到授权服务器元数据")
	}
	return nil, lastErr
}

// ProbeWWWAuthenticate makes an unauthenticated initialize call so the gateway
// reveals its WWW-Authenticate challenge.
func ProbeWWWAuthenticate(ctx context.Context, endpoint string) string {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"agentflow","version":"1.0.0"}}}`)
	status, header, _, err := oauthFetch(ctx, http.MethodPost, endpoint, "application/json", payload, "", "")
	if err != nil || header == nil {
		return ""
	}
	if status >= 200 && status < 300 {
		// The endpoint already accepts unauthenticated calls; nothing to do.
		return ""
	}
	return strings.TrimSpace(header.Get("WWW-Authenticate"))
}

// RegisterClient performs RFC 7591 dynamic client registration.
func RegisterClient(ctx context.Context, regEndpoint, redirectURI, clientName string) (*clientRegistration, error) {
	if err := validateAuthURL(regEndpoint); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(clientName)
	if name == "" {
		name = "LoopForge Knowledge"
	}
	payload, err := json.Marshal(map[string]any{
		"client_name":                name,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": OAuthAuthMethodBasic,
	})
	if err != nil {
		return nil, err
	}
	status, _, body, err := oauthFetch(ctx, http.MethodPost, regEndpoint, "application/json", payload, "", "")
	if err != nil {
		return nil, fmt.Errorf("OAuth 动态客户端注册失败：%w", err)
	}
	if status < 200 || status >= 300 {
		if desc := oauthErrorDescription(body); desc != "" {
			return nil, fmt.Errorf("OAuth 动态客户端注册失败（HTTP %d）：%s", status, desc)
		}
		return nil, fmt.Errorf("OAuth 动态客户端注册失败（HTTP %d）", status)
	}
	var parsed struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("OAuth 动态客户端注册响应无法解析")
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("OAuth 动态客户端注册失败：%s", truncateRunes(firstNonEmpty(parsed.ErrorDescription, parsed.Error), 200))
	}
	if strings.TrimSpace(parsed.ClientID) == "" {
		return nil, errors.New("OAuth 动态客户端注册响应缺少 client_id")
	}
	method := strings.TrimSpace(parsed.TokenEndpointAuthMethod)
	if method == "" {
		// The registration request declares client_secret_basic, so an omitted
		// response field means the server kept that default.
		method = OAuthAuthMethodBasic
	}
	return &clientRegistration{
		ClientID:                strings.TrimSpace(parsed.ClientID),
		ClientSecret:            strings.TrimSpace(parsed.ClientSecret),
		TokenEndpointAuthMethod: method,
	}, nil
}
