package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// pendingOAuth is one in-flight authorization request. It is stored server-side
// only (keyed by the state nonce) so the OAuth state parameter stays short and
// carries no client secret.
type pendingOAuth struct {
	UID          int64
	ConnectorID  string
	RedirectURI  string
	CodeVerifier string
	ClientID     string
	ClientSecret string
	TokenURL     string
	AuthMethod   string
	Resource     string
	Scope        string
	ExpiresAt    time.Time

	// Immutable fingerprint of the connector at authorization start. It stays
	// server-side (the state only carries the nonce) and prevents a callback
	// from writing a grant onto a connector that changed meanwhile.
	ConnectorType  string
	EndpointDigest string
	ConfigDigest   string
}

// discoveredOAuth caches one connector's discovery result (process-local).
type discoveredOAuth struct {
	AuthorizationURL   string
	TokenURL           string
	RegistrationURL    string
	Resource           string
	ResourceIndicators bool
	ClientIDMetadata   bool
	Scopes             []string
	TokenAuthMethods   []string
	ExpiresAt          time.Time
}

// OAuthPresetInfo is the UI-facing preset summary.
type OAuthPresetInfo struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Configured  bool     `json:"configured"`
	RedirectURI string   `json:"redirectUri"`
	Scopes      []string `json:"scopes,omitempty"`
}

// Fixed machine codes carried by the completion page. Upstream text is never
// placed in the redirect URL.
const (
	OAuthCallbackCodeFailed           = "oauth_failed"
	OAuthCallbackCodeConnectorChanged = "connector_changed"
	OAuthCallbackCodeStateInvalid     = "state_invalid"
)

// OAuthCallbackError attaches a fixed code to a callback failure while keeping
// the (already redacted) error available for logs and tests.
type OAuthCallbackError struct {
	Code string
	Err  error
}

func (e *OAuthCallbackError) Error() string { return e.Err.Error() }
func (e *OAuthCallbackError) Unwrap() error { return e.Err }

func oauthCallbackError(code string, err error) error {
	if err == nil {
		return nil
	}
	return &OAuthCallbackError{Code: code, Err: err}
}

// redactOAuthSensitive removes raw and URL-encoded forms of one-off values
// (authorization code, PKCE verifier, client secret) from error text.
func redactOAuthSensitive(text string, values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		variants := []string{
			value,
			url.QueryEscape(value),
			url.PathEscape(value),
			url.QueryEscape(url.QueryEscape(value)),
		}
		for _, variant := range variants {
			if variant != "" {
				text = strings.ReplaceAll(text, variant, "***")
			}
		}
	}
	return text
}

func oauthDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// oauthConnectorFingerprint digests the identity-bearing parts of a connector:
// its type, canonical endpoint and the OAuth-related config keys. Name, enabled
// state and unrelated headers are deliberately excluded.
func oauthConnectorFingerprint(conn domain.KnowledgeConnector) (endpointDigest, configDigest string) {
	endpoint := canonicalResource(conn.Endpoint)
	keys := []string{
		"oauthMode", "oauthPreset", "oauthClientId", "oauthAuthorizeUrl",
		"oauthTokenUrl", "oauthScopes", "oauthTokenHeader", "oauthTokenPrefix",
		"oauthResource",
	}
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+conn.Config.Extra[key])
	}
	endpointDigest = oauthDigest(endpoint)
	configDigest = oauthDigest(conn.Type + "|" + endpoint + "|" + strings.Join(pairs, "\x00"))
	return endpointDigest, configDigest
}

// oauthIdentity is the immutable connector identity captured before any
// network call, used to reject work that would land on a changed connector.
type oauthIdentity struct {
	connectorType  string
	endpointDigest string
	configDigest   string
}

func oauthIdentityOf(conn domain.KnowledgeConnector) oauthIdentity {
	endpointDigest, configDigest := oauthConnectorFingerprint(conn)
	return oauthIdentity{connectorType: conn.Type, endpointDigest: endpointDigest, configDigest: configDigest}
}

func (id oauthIdentity) matches(conn domain.KnowledgeConnector) bool {
	if id.connectorType != "" && !strings.EqualFold(id.connectorType, conn.Type) {
		return false
	}
	endpointDigest, configDigest := oauthConnectorFingerprint(conn)
	return id.endpointDigest == endpointDigest && id.configDigest == configDigest
}

func oauthFingerprintMatches(pending pendingOAuth, conn domain.KnowledgeConnector) bool {
	return oauthIdentity{
		connectorType:  pending.ConnectorType,
		endpointDigest: pending.EndpointDigest,
		configDigest:   pending.ConfigDigest,
	}.matches(conn)
}

// casConnectorSecret writes only the sealed secret when the stored ciphertext
// still matches expected, using KnowledgeRepo's narrow update when available.
func (s *Service) casConnectorSecret(ctx context.Context, uid int64, id, expected, sealed string) (bool, error) {
	type secretCAS interface {
		UpdateKnowledgeConnectorSecret(context.Context, int64, string, string, string) (bool, error)
	}
	if cas, ok := s.store.(secretCAS); ok {
		return cas.UpdateKnowledgeConnectorSecret(ctx, uid, id, expected, sealed)
	}
	latest, found := s.store.GetKnowledgeConnector(uid, id)
	if !found {
		return false, ErrConnectorNotFound
	}
	if latest.SecretEnc != expected {
		return false, nil
	}
	latest.SecretEnc = sealed
	latest.SecretConfigured = sealed != ""
	if err := s.store.PutKnowledgeConnector(latest); err != nil {
		return false, err
	}
	return true, nil
}

// casConnectorConfig writes only config_json when the stored config still
// matches expected (compared structurally, so JSON normalization is harmless).
func (s *Service) casConnectorConfig(ctx context.Context, uid int64, id, expected, current string) (bool, error) {
	type configCAS interface {
		UpdateKnowledgeConnectorConfig(context.Context, int64, string, string, string) (bool, error)
	}
	if cas, ok := s.store.(configCAS); ok {
		return cas.UpdateKnowledgeConnectorConfig(ctx, uid, id, expected, current)
	}
	latest, found := s.store.GetKnowledgeConnector(uid, id)
	if !found {
		return false, ErrConnectorNotFound
	}
	expectedJSON, err := json.Marshal(latest.Config)
	if err != nil {
		return false, err
	}
	if string(expectedJSON) != expected {
		return false, nil
	}
	var cfg domain.KnowledgeConnectorConfig
	if err := json.Unmarshal([]byte(current), &cfg); err != nil {
		return false, err
	}
	latest.Config = cfg
	if err := s.store.PutKnowledgeConnector(latest); err != nil {
		return false, err
	}
	return true, nil
}

// clearOAuthState drops pending authorizations, discovery cache and the refresh
// mutex of a deleted connector.
func (s *Service) clearOAuthState(uid int64, connectorID string) {
	key := instanceKey(uid, connectorID)
	s.oauthMu.Lock()
	for nonce, pending := range s.oauthPending {
		if pending.UID == uid && pending.ConnectorID == connectorID {
			delete(s.oauthPending, nonce)
		}
	}
	for cacheKey := range s.oauthCache {
		if strings.HasPrefix(cacheKey, key+"|") {
			delete(s.oauthCache, cacheKey)
		}
	}
	s.oauthMu.Unlock()
	s.sweepOAuthLock(key)
}

func (s *Service) putPending(nonce string, pending pendingOAuth) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.oauthPending == nil {
		s.oauthPending = map[string]pendingOAuth{}
	}
	now := s.now()
	for key, entry := range s.oauthPending {
		if now.After(entry.ExpiresAt) {
			delete(s.oauthPending, key)
		}
	}
	if len(s.oauthPending) >= oauthPendingMax {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range s.oauthPending {
			if oldestKey == "" || entry.ExpiresAt.Before(oldest) {
				oldestKey, oldest = key, entry.ExpiresAt
			}
		}
		if oldestKey != "" {
			delete(s.oauthPending, oldestKey)
		}
	}
	s.oauthPending[nonce] = pending
}

// takePending atomically removes and returns a pending authorization request,
// so a replayed callback can never exchange the same code twice.
func (s *Service) takePending(nonce string) (pendingOAuth, bool) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	pending, ok := s.oauthPending[nonce]
	if !ok {
		return pendingOAuth{}, false
	}
	delete(s.oauthPending, nonce)
	if s.now().After(pending.ExpiresAt) {
		return pendingOAuth{}, false
	}
	return pending, true
}

const oauthDiscoveryTTL = 10 * time.Minute

func (s *Service) cachedDiscovery(key string) (discoveredOAuth, bool) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.oauthCache == nil {
		return discoveredOAuth{}, false
	}
	entry, ok := s.oauthCache[key]
	if !ok {
		return discoveredOAuth{}, false
	}
	if s.now().After(entry.ExpiresAt) {
		delete(s.oauthCache, key)
		return discoveredOAuth{}, false
	}
	return entry, true
}

func (s *Service) cacheDiscovery(key string, entry discoveredOAuth) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.oauthCache == nil {
		s.oauthCache = map[string]discoveredOAuth{}
	}
	now := s.now()
	for cachedKey, cached := range s.oauthCache {
		if now.After(cached.ExpiresAt) {
			delete(s.oauthCache, cachedKey)
		}
	}
	if len(s.oauthCache) >= 256 {
		s.oauthCache = map[string]discoveredOAuth{}
	}
	entry.ExpiresAt = now.Add(oauthDiscoveryTTL)
	s.oauthCache[key] = entry
}

// clearOAuthDiscovery drops cached discovery results for one connector.
func (s *Service) clearOAuthDiscovery(uid int64, connectorID string) {
	prefix := instanceKey(uid, connectorID) + "|"
	s.oauthMu.Lock()
	for key := range s.oauthCache {
		if strings.HasPrefix(key, prefix) {
			delete(s.oauthCache, key)
		}
	}
	s.oauthMu.Unlock()
}

// oauthLockEntry is a reference-counted connector mutex. Entries are removed
// once the last holder/waiter releases, so a deleted connector cannot leak a
// lock forever; refs are only mutated under oauthLocksMu.
type oauthLockEntry struct {
	mu   sync.Mutex
	refs int
}

func (s *Service) acquireOAuthLock(key string) *oauthLockEntry {
	s.oauthLocksMu.Lock()
	value, _ := s.oauthLocks.LoadOrStore(key, &oauthLockEntry{})
	entry := value.(*oauthLockEntry)
	entry.refs++
	s.oauthLocksMu.Unlock()
	entry.mu.Lock()
	return entry
}

func (s *Service) releaseOAuthLock(key string, entry *oauthLockEntry) {
	entry.mu.Unlock()
	s.oauthLocksMu.Lock()
	entry.refs--
	if entry.refs == 0 {
		s.oauthLocks.Delete(key)
	}
	s.oauthLocksMu.Unlock()
}

// sweepOAuthLock removes an idle lock entry explicitly (the refcount already
// removes it on release; this covers entries reached by a delete).
func (s *Service) sweepOAuthLock(key string) {
	s.oauthLocksMu.Lock()
	if value, ok := s.oauthLocks.Load(key); ok {
		if entry, entryOK := value.(*oauthLockEntry); entryOK && entry.refs == 0 {
			s.oauthLocks.Delete(key)
		}
	}
	s.oauthLocksMu.Unlock()
}

// discoverOAuth runs RFC 9728 + RFC 8414 discovery for an MCP endpoint, using
// this process's cache after the first successful probe.
func (s *Service) discoverOAuth(ctx context.Context, saved domain.KnowledgeConnector) (discoveredOAuth, error) {
	endpointDigest, configDigest := oauthConnectorFingerprint(saved)
	key := instanceKey(saved.OwnerUserID, saved.ID) + "|" + endpointDigest + "|" + configDigest
	if entry, ok := s.cachedDiscovery(key); ok {
		return entry, nil
	}
	wwwAuth := ProbeWWWAuthenticate(ctx, saved.Endpoint)
	resMeta, err := DiscoverProtectedResource(ctx, saved.Endpoint, wwwAuth)
	if err != nil {
		return discoveredOAuth{}, err
	}
	if len(resMeta.AuthorizationServers) == 0 {
		return discoveredOAuth{}, apperr.New(apperr.Upstream, "MCP 服务未声明 OAuth 授权服务器")
	}
	as, err := DiscoverAuthServer(ctx, resMeta.AuthorizationServers[0])
	if err != nil {
		return discoveredOAuth{}, err
	}
	scopes := as.ScopesSupported
	if len(scopes) == 0 {
		scopes = resMeta.ScopesSupported
	}
	entry := discoveredOAuth{
		AuthorizationURL:   as.AuthorizationEndpoint,
		TokenURL:           as.TokenEndpoint,
		RegistrationURL:    as.RegistrationEndpoint,
		Resource:           firstNonEmpty(resMeta.Resource, canonicalResource(saved.Endpoint)),
		ResourceIndicators: as.ResourceIndicatorsSupported,
		ClientIDMetadata:   as.ClientIDMetadataDocument,
		Scopes:             scopes,
		TokenAuthMethods:   as.TokenEndpointAuthMethods,
	}
	s.cacheDiscovery(key, entry)
	return entry, nil
}

// StartOAuth prepares an authorization-code + PKCE redirect for one connector.
func (s *Service) StartOAuth(ctx context.Context, uid int64, connectorID, publicURL string) (authorizeURL, redirectURI string, err error) {
	saved, ok := s.store.GetKnowledgeConnector(uid, strings.TrimSpace(connectorID))
	if !ok {
		return "", "", apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	mode := oauthMode(saved.Config)
	if mode == OAuthModeNone {
		return "", "", apperr.New(apperr.Invalid, "该连接器未开启 OAuth 授权")
	}
	// The connector identity is pinned before any network call; every write
	// after discovery/DCR must still match it.
	identity := oauthIdentityOf(saved)
	current, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		return "", "", apperr.New(apperr.Unavailable, "读取连接器凭据失败")
	}
	preset := resolveOAuthPreset(saved.Config)
	envApp := EnvOAuthApp(preset.Key)

	base := strings.TrimRight(firstNonEmpty(publicURL, s.publicBaseURL), "/")
	if base == "" {
		return "", "", apperr.New(apperr.Invalid, "无法确定平台公开访问地址")
	}
	redirectURI = base + "/api/knowledge/oauth/" + url.PathEscape(saved.Type) + "/callback"

	clientID := firstNonEmpty(current.OAuthClientID, saved.Config.Extra["oauthClientId"], envApp.ClientID)
	clientSecret := firstNonEmpty(current.OAuthClientSecret, envApp.ClientSecret)
	authMethod := firstNonEmpty(current.OAuthTokenAuthMethod, preset.AuthMethod)
	authorizeEndpoint := firstNonEmpty(saved.Config.Extra["oauthAuthorizeUrl"], preset.AuthorizeURL)
	tokenEndpoint := firstNonEmpty(current.OAuthTokenURL, saved.Config.Extra["oauthTokenUrl"], preset.TokenURL)
	resource := firstNonEmpty(current.OAuthResource, saved.Config.Extra["oauthResource"])
	scopes := append([]string{}, envApp.Scopes...)
	scopes = append(scopes, preset.Scopes...)
	scopes = append(scopes, SplitOAuthScopes(saved.Config.Extra["oauthScopes"])...)

	registeredClient := false
	if mode == OAuthModeAuto {
		discovered, derr := s.discoverOAuth(ctx, saved)
		if derr != nil {
			return "", "", apperr.New(apperr.Upstream, "OAuth 自动发现失败："+truncateRunes(derr.Error(), 200))
		}
		authorizeEndpoint = discovered.AuthorizationURL
		tokenEndpoint = discovered.TokenURL
		if len(scopes) == 0 {
			scopes = discovered.Scopes
		}
		if resource == "" && discovered.ResourceIndicators {
			resource = discovered.Resource
		}
		if clientID == "" {
			switch {
			case discovered.ClientIDMetadata && cimdClientID(base) != "":
				// Client ID Metadata Document: the metadata URL is the client_id
				// and the AS fetches it. Off unless explicitly enabled.
				clientID = cimdClientID(base)
				authMethod = OAuthAuthMethodNone
			case discovered.RegistrationURL != "":
				reg, rerr := RegisterClient(ctx, discovered.RegistrationURL, redirectURI, "LoopForge Knowledge")
				if rerr != nil {
					return "", "", apperr.New(apperr.Upstream, truncateRunes(rerr.Error(), 200))
				}
				clientID = reg.ClientID
				clientSecret = reg.ClientSecret
				authMethod = firstNonEmpty(reg.TokenEndpointAuthMethod, OAuthAuthMethodBasic)
				// The registration is persisted below, after the identity check,
				// so a connector changed while /register was in flight is never
				// rebased onto the new identity.
				registeredClient = true
			default:
				return "", "", apperr.New(apperr.Invalid, "OAuth 服务未提供客户端注册方式，请在连接器配置中填写 Client ID")
			}
		}
		if strings.TrimSpace(authMethod) == "" {
			picked, perr := pickTokenAuthMethod(discovered.TokenAuthMethods)
			if perr != nil {
				return "", "", apperr.New(apperr.Invalid, perr.Error())
			}
			authMethod = picked
		}
	} else if strings.TrimSpace(authMethod) == "" {
		authMethod = OAuthAuthMethodPost
	}

	authorizeEndpoint = strings.TrimSpace(authorizeEndpoint)
	tokenEndpoint = strings.TrimSpace(tokenEndpoint)
	if authorizeEndpoint == "" || tokenEndpoint == "" {
		return "", "", apperr.New(apperr.Invalid, "缺少 OAuth 授权地址或 Token 地址")
	}
	if err := validateAuthURL(authorizeEndpoint); err != nil {
		return "", "", apperr.New(apperr.Invalid, err.Error())
	}
	if err := validateAuthURL(tokenEndpoint); err != nil {
		return "", "", apperr.New(apperr.Invalid, err.Error())
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return "", "", apperr.New(apperr.Invalid, "缺少 OAuth Client ID，请先配置平台 OAuth 应用")
	}

	scope := strings.Join(CleanList(scopes), " ")
	// Persistence and authorization start share the connector lock with
	// UpdateConnector/DeleteConnector, closing the check-then-act window.
	lockKey := instanceKey(uid, saved.ID)
	lockEntry := s.acquireOAuthLock(lockKey)
	defer s.releaseOAuthLock(lockKey, lockEntry)

	latest, ok := s.store.GetKnowledgeConnector(uid, saved.ID)
	if !ok {
		return "", "", apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	if !identity.matches(latest) {
		return "", "", apperr.New(apperr.Invalid, "oauth connector changed during authorization")
	}
	if registeredClient {
		if perr := s.persistOAuthRegistration(ctx, uid, latest.ID, clientID, clientSecret, authMethod, tokenEndpoint); perr != nil {
			return "", "", perr
		}
	}
	saved, err = s.persistOAuthConfig(ctx, latest, mode, authorizeEndpoint, tokenEndpoint, clientID, scope, preset)
	if err != nil {
		return "", "", err
	}
	endpointDigest, configDigest := oauthConnectorFingerprint(saved)
	verifier, challenge, err := newPKCE()
	if err != nil {
		return "", "", apperr.New(apperr.Unavailable, "安全随机数不可用，无法开始授权")
	}
	nonce, err := randomHex(16)
	if err != nil {
		return "", "", apperr.New(apperr.Unavailable, "安全随机数不可用，无法开始授权")
	}
	expiresAt := s.now().Add(oauthStateTTL)
	signingKey, err := oauthStateSecret()
	if err != nil {
		return "", "", apperr.New(apperr.Unavailable, "OAuth 签名密钥未配置")
	}
	state := signState(signingKey, oauthStatePayload{Nonce: nonce, Exp: expiresAt.Unix()})

	s.putPending(nonce, pendingOAuth{
		UID:            uid,
		ConnectorID:    saved.ID,
		RedirectURI:    redirectURI,
		CodeVerifier:   verifier,
		ClientID:       clientID,
		ClientSecret:   clientSecret,
		TokenURL:       tokenEndpoint,
		AuthMethod:     authMethod,
		Resource:       resource,
		Scope:          scope,
		ExpiresAt:      expiresAt,
		ConnectorType:  saved.Type,
		EndpointDigest: endpointDigest,
		ConfigDigest:   configDigest,
	})

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	if scope != "" {
		query.Set("scope", scope)
	}
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if resource != "" {
		query.Set("resource", resource)
	}
	separator := "?"
	if strings.Contains(authorizeEndpoint, "?") {
		separator = "&"
	}
	authorizeURL = authorizeEndpoint + separator + query.Encode()
	if err := validateAuthURL(authorizeURL); err != nil {
		return "", "", apperr.New(apperr.Invalid, err.Error())
	}
	return authorizeURL, redirectURI, nil
}

// pickTokenAuthMethod chooses a client authentication method advertised by the
// authorization server, preferring basic then post then none.
func pickTokenAuthMethod(supported []string) (string, error) {
	if len(supported) == 0 {
		return OAuthAuthMethodPost, nil
	}
	has := func(want string) bool {
		for _, candidate := range supported {
			if strings.EqualFold(strings.TrimSpace(candidate), want) {
				return true
			}
		}
		return false
	}
	switch {
	case has(OAuthAuthMethodBasic):
		return OAuthAuthMethodBasic, nil
	case has(OAuthAuthMethodPost):
		return OAuthAuthMethodPost, nil
	case has(OAuthAuthMethodNone):
		return OAuthAuthMethodNone, nil
	}
	return "", apperr.New(apperr.Invalid, "OAuth 授权服务器不支持可用的客户端认证方式")
}

// persistOAuthRegistration seals a dynamic client registration result onto the
// connector right away, using the narrow CAS update and preserving any token.
func (s *Service) persistOAuthRegistration(ctx context.Context, uid int64, connectorID, clientID, clientSecret, authMethod, tokenURL string) error {
	for attempt := 0; attempt < 2; attempt++ {
		latest, ok := s.store.GetKnowledgeConnector(uid, connectorID)
		if !ok {
			return apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
		}
		secret, err := OpenSecret(uid, latest.ID, latest.SecretEnc)
		if err != nil {
			return apperr.New(apperr.Unavailable, "读取连接器凭据失败")
		}
		secret.AuthType = domain.ConnectorAuthOAuth
		secret.OAuthClientID = clientID
		secret.OAuthClientSecret = clientSecret
		secret.OAuthTokenURL = tokenURL
		secret.OAuthTokenAuthMethod = authMethod
		sealed, err := SealSecret(uid, latest.ID, secret)
		if err != nil {
			return apperr.New(apperr.Unavailable, "加密 OAuth 客户端凭据失败")
		}
		updated, err := s.casConnectorSecret(ctx, uid, latest.ID, latest.SecretEnc, sealed)
		if err != nil {
			return apperr.New(apperr.Unavailable, "保存 OAuth 客户端凭据失败")
		}
		if updated {
			s.evictInstance(uid, latest.ID)
			return nil
		}
	}
	return apperr.New(apperr.Conflict, "OAuth 客户端注册结果保存冲突，请重试")
}

// persistOAuthConfig writes non-secret OAuth settings into config_json so the
// runtime connector and the UI agree on endpoints, header and scopes. It uses
// the narrow config CAS update and retries once on a concurrent edit.
func (s *Service) persistOAuthConfig(ctx context.Context, saved domain.KnowledgeConnector, mode, authorizeEndpoint, tokenEndpoint, clientID, scope string, preset OAuthProviderPreset) (domain.KnowledgeConnector, error) {
	for attempt := 0; attempt < 2; attempt++ {
		latest, ok := s.store.GetKnowledgeConnector(saved.OwnerUserID, saved.ID)
		if !ok {
			return saved, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
		}
		extra := make(map[string]string, len(latest.Config.Extra)+4)
		for key, value := range latest.Config.Extra {
			extra[key] = value
		}
		changed := false
		set := func(key, value string) {
			if value != "" && extra[key] != value {
				extra[key] = value
				changed = true
			}
		}
		set("oauthMode", mode)
		set("oauthAuthorizeUrl", authorizeEndpoint)
		set("oauthTokenUrl", tokenEndpoint)
		set("oauthClientId", clientID)
		set("oauthScopes", scope)
		if preset.Key != "" {
			header := firstNonEmpty(preset.TokenHeader, "Authorization")
			if extra["oauthTokenHeader"] != header {
				extra["oauthTokenHeader"] = header
				changed = true
			}
			if extra["oauthTokenPrefix"] != preset.TokenPrefix {
				extra["oauthTokenPrefix"] = preset.TokenPrefix
				changed = true
			}
		}
		if !changed {
			return latest, nil
		}
		cfg := latest.Config
		cfg.Extra = extra
		expectedJSON, err := json.Marshal(latest.Config)
		if err != nil {
			return latest, apperr.New(apperr.Unavailable, "序列化 OAuth 配置失败")
		}
		configJSON, err := json.Marshal(cfg)
		if err != nil {
			return latest, apperr.New(apperr.Unavailable, "序列化 OAuth 配置失败")
		}
		updated, err := s.casConnectorConfig(ctx, saved.OwnerUserID, saved.ID, string(expectedJSON), string(configJSON))
		if err != nil {
			return latest, apperr.New(apperr.Unavailable, "保存 OAuth 配置失败")
		}
		if updated {
			latest.Config = cfg
			s.evictInstance(saved.OwnerUserID, saved.ID)
			return latest, nil
		}
	}
	return saved, apperr.New(apperr.Conflict, "OAuth 配置并发更新冲突，请重试")
}

// CompleteOAuthCallback validates state, exchanges the code and seals the new
// grant back onto the connector. The connector fingerprint captured at
// StartOAuth is re-checked before the exchange and again before the write, so a
// callback cannot land a token on a connector whose endpoint/config changed.
func (s *Service) CompleteOAuthCallback(ctx context.Context, connectorType, state, code, errParam string) (string, error) {
	signingKey, err := oauthStateSecret()
	if err != nil {
		return "", oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Unavailable, "OAuth 签名密钥未配置"))
	}
	payload, err := verifyStateAt(signingKey, state, s.now())
	if err != nil {
		return "", oauthCallbackError(OAuthCallbackCodeStateInvalid, apperr.New(apperr.Invalid, "OAuth 授权状态无效或已过期"))
	}
	pending, ok := s.takePending(payload.Nonce)
	if !ok {
		return "", oauthCallbackError(OAuthCallbackCodeStateInvalid, apperr.New(apperr.Invalid, "OAuth 授权请求不存在或已过期"))
	}
	connectorID := pending.ConnectorID
	changedErr := apperr.New(apperr.Invalid, "oauth connector changed during authorization")
	saved, ok := s.store.GetKnowledgeConnector(pending.UID, connectorID)
	if !ok {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error()))
	}
	if connectorType != "" && !strings.EqualFold(strings.TrimSpace(connectorType), pending.ConnectorType) {
		return connectorID, oauthCallbackError(OAuthCallbackCodeConnectorChanged, changedErr)
	}
	if !oauthFingerprintMatches(pending, saved) {
		return connectorID, oauthCallbackError(OAuthCallbackCodeConnectorChanged, changedErr)
	}
	if strings.TrimSpace(errParam) != "" {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Invalid, "授权被拒绝"))
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Invalid, "授权回调缺少 code"))
	}
	current, err := OpenSecret(pending.UID, saved.ID, saved.SecretEnc)
	if err != nil {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Unavailable, "读取连接器凭据失败"))
	}
	token, err := exchangeToken(ctx, tokenRequest{
		TokenURL:     pending.TokenURL,
		GrantType:    "authorization_code",
		Code:         code,
		RedirectURI:  pending.RedirectURI,
		CodeVerifier: pending.CodeVerifier,
		ClientID:     pending.ClientID,
		ClientSecret: pending.ClientSecret,
		AuthMethod:   pending.AuthMethod,
		Resource:     pending.Resource,
		Scope:        pending.Scope,
	})
	if err != nil {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, oauthUpstreamError(err, current, pending.ClientSecret, code, pending.CodeVerifier))
	}
	// Re-read under the shared connector lock: a refresh or an edit may have
	// happened while the token endpoint was in flight.
	lockKey := instanceKey(pending.UID, connectorID)
	lockEntry := s.acquireOAuthLock(lockKey)
	defer s.releaseOAuthLock(lockKey, lockEntry)

	latest, ok := s.store.GetKnowledgeConnector(pending.UID, connectorID)
	if !ok {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error()))
	}
	if !oauthFingerprintMatches(pending, latest) {
		return connectorID, oauthCallbackError(OAuthCallbackCodeConnectorChanged, changedErr)
	}
	latestSecret, err := OpenSecret(pending.UID, latest.ID, latest.SecretEnc)
	if err != nil {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Unavailable, "读取连接器凭据失败"))
	}
	latestSecret.AuthType = domain.ConnectorAuthOAuth
	latestSecret.Token = token.AccessToken
	if token.RefreshToken != "" {
		latestSecret.RefreshToken = token.RefreshToken
	}
	if expiry := tokenExpiry(token, s.now()); expiry != nil {
		latestSecret.ExpiresAt = expiry.Format(time.RFC3339)
	} else {
		latestSecret.ExpiresAt = ""
	}
	latestSecret.OAuthClientID = pending.ClientID
	latestSecret.OAuthClientSecret = pending.ClientSecret
	latestSecret.OAuthTokenURL = pending.TokenURL
	latestSecret.OAuthTokenAuthMethod = pending.AuthMethod
	latestSecret.OAuthResource = pending.Resource

	sealed, err := SealSecret(pending.UID, latest.ID, latestSecret)
	if err != nil {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Unavailable, "加密 OAuth 凭据失败"))
	}
	updated, err := s.casConnectorSecret(ctx, pending.UID, latest.ID, latest.SecretEnc, sealed)
	if err != nil {
		return connectorID, oauthCallbackError(OAuthCallbackCodeFailed, apperr.New(apperr.Unavailable, "保存 OAuth 授权失败"))
	}
	if !updated {
		return connectorID, oauthCallbackError(OAuthCallbackCodeConnectorChanged, apperr.New(apperr.Conflict, "oauth connector changed during authorization"))
	}
	s.evictInstance(pending.UID, latest.ID)
	return connectorID, nil
}

// EnsureValidSecret returns a usable secret, refreshing it when it expires (or
// when forceRefresh is set after an upstream 401). Refreshes are serialized per
// connector and double-check the database, because providers such as Feishu
// rotate refresh tokens on every use.
func (s *Service) EnsureValidSecret(ctx context.Context, uid int64, connectorID string, forceRefresh bool, failedToken string) (domain.KnowledgeConnectorSecret, error) {
	saved, ok := s.store.GetKnowledgeConnector(uid, strings.TrimSpace(connectorID))
	if !ok {
		return domain.KnowledgeConnectorSecret{}, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	if oauthMode(saved.Config) == OAuthModeNone {
		return OpenSecret(uid, saved.ID, saved.SecretEnc)
	}
	current, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		return domain.KnowledgeConnectorSecret{}, apperr.New(apperr.Unavailable, "读取连接器凭据失败")
	}
	if !forceRefresh && !s.oauthTokenExpired(current) {
		return current, nil
	}

	lockKey := instanceKey(uid, saved.ID)
	lockEntry := s.acquireOAuthLock(lockKey)
	defer s.releaseOAuthLock(lockKey, lockEntry)

	// Double-check after acquiring the lock: a concurrent goroutine may have
	// already rotated the token while we waited.
	latest, ok := s.store.GetKnowledgeConnector(uid, saved.ID)
	if !ok {
		return domain.KnowledgeConnectorSecret{}, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
	}
	refreshed, err := OpenSecret(uid, latest.ID, latest.SecretEnc)
	if err != nil {
		return domain.KnowledgeConnectorSecret{}, apperr.New(apperr.Unavailable, "读取连接器凭据失败")
	}
	if forceRefresh && failedToken != "" && refreshed.Token != "" &&
		refreshed.Token != failedToken && !s.oauthTokenExpired(refreshed) {
		// Another caller already replaced the token that just failed; reuse it
		// instead of rotating a second time.
		return refreshed, nil
	}
	if refreshed.Token != "" && refreshed.Token != current.Token && !s.oauthTokenExpired(refreshed) {
		// A different writer may have stored an already-expired token; only a
		// usable one may skip the refresh below.
		return refreshed, nil
	}
	if !forceRefresh && !s.oauthTokenExpired(refreshed) {
		return refreshed, nil
	}
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		return refreshed, apperr.New(apperr.Forbidden, "OAuth 授权已失效，请重新授权")
	}

	preset := resolveOAuthPreset(latest.Config)
	tokenURL := firstNonEmpty(refreshed.OAuthTokenURL, latest.Config.Extra["oauthTokenUrl"], preset.TokenURL)
	authMethod := firstNonEmpty(refreshed.OAuthTokenAuthMethod, preset.AuthMethod, OAuthAuthMethodPost)
	clientID := firstNonEmpty(refreshed.OAuthClientID, latest.Config.Extra["oauthClientId"], EnvOAuthApp(preset.Key).ClientID)
	clientSecret := firstNonEmpty(refreshed.OAuthClientSecret, EnvOAuthApp(preset.Key).ClientSecret)
	if tokenURL == "" || clientID == "" {
		return refreshed, apperr.New(apperr.Invalid, "OAuth 配置不完整，请重新授权")
	}
	token, err := refreshToken(ctx, tokenRequest{
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthMethod:   authMethod,
		RefreshToken: refreshed.RefreshToken,
		Resource:     refreshed.OAuthResource,
	})
	if err != nil {
		return refreshed, oauthUpstreamError(err, refreshed)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return refreshed, apperr.New(apperr.Upstream, "OAuth 刷新未返回访问令牌")
	}
	return s.commitRefreshedSecret(ctx, uid, latest.ID, token, tokenURL, authMethod, clientID, clientSecret, refreshed)
}

// applyRefreshedToken merges a token endpoint response onto a secret read after
// the network call. Only OAuth credential fields are replaced.
func (s *Service) applyRefreshedToken(base domain.KnowledgeConnectorSecret, token tokenResponse, tokenURL, authMethod, clientID, clientSecret string) domain.KnowledgeConnectorSecret {
	out := base
	out.AuthType = domain.ConnectorAuthOAuth
	out.Token = token.AccessToken
	if token.RefreshToken != "" {
		out.RefreshToken = token.RefreshToken
	}
	if token.ExpiresIn > 0 {
		out.ExpiresAt = s.now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	} else {
		out.ExpiresAt = ""
	}
	out.OAuthTokenURL = tokenURL
	out.OAuthTokenAuthMethod = authMethod
	out.OAuthClientID = clientID
	if out.OAuthClientSecret == "" {
		out.OAuthClientSecret = clientSecret
	}
	return out
}

// commitRefreshedSecret re-reads the record after the refresh network call and
// CASes the new ciphertext. Name/endpoint/config are never written back, and a
// concurrent writer's newer valid token wins instead of being rotated again.
func (s *Service) commitRefreshedSecret(ctx context.Context, uid int64, connectorID string, token tokenResponse, tokenURL, authMethod, clientID, clientSecret string, fallback domain.KnowledgeConnectorSecret) (domain.KnowledgeConnectorSecret, error) {
	last := fallback
	for attempt := 0; attempt < 2; attempt++ {
		latest, ok := s.store.GetKnowledgeConnector(uid, connectorID)
		if !ok {
			return last, apperr.New(apperr.NotFound, ErrConnectorNotFound.Error())
		}
		current, err := OpenSecret(uid, latest.ID, latest.SecretEnc)
		if err != nil {
			return last, apperr.New(apperr.Unavailable, "读取连接器凭据失败")
		}
		last = current
		if attempt > 0 && strings.TrimSpace(current.Token) != "" && current.Token != token.AccessToken && !s.oauthTokenExpired(current) {
			// Another writer already produced a newer usable token; drop ours to
			// avoid burning a second refresh token.
			return current, nil
		}
		merged := s.applyRefreshedToken(current, token, tokenURL, authMethod, clientID, clientSecret)
		sealed, err := SealSecret(uid, latest.ID, merged)
		if err != nil {
			return merged, apperr.New(apperr.Unavailable, "加密 OAuth 凭据失败")
		}
		updated, err := s.casConnectorSecret(ctx, uid, latest.ID, latest.SecretEnc, sealed)
		if err != nil {
			return merged, apperr.New(apperr.Unavailable, "保存 OAuth 凭据失败")
		}
		if updated {
			s.evictInstance(uid, latest.ID)
			return merged, nil
		}
		last = merged
	}
	return last, apperr.New(apperr.Conflict, "OAuth 凭证并发更新冲突，请重试")
}

// oauthTokenExpired reports whether the access token is missing or within the
// refresh skew (60s) of expiry.
func (s *Service) oauthTokenExpired(secret domain.KnowledgeConnectorSecret) bool {
	if strings.TrimSpace(secret.Token) == "" {
		return true
	}
	expiry := parseSecretExpiry(secret.ExpiresAt)
	if expiry == nil {
		return false
	}
	return !s.now().Add(oauthRefreshSkew).Before(*expiry)
}

// OAuthPresets lists built-in providers with their configuration state.
func (s *Service) OAuthPresets() []OAuthPresetInfo {
	base := strings.TrimRight(s.publicBaseURL, "/")
	redirect := base + "/api/knowledge/oauth/" + domain.ConnectorTypeMCP + "/callback"
	presets := BuiltinOAuthPresets()
	out := make([]OAuthPresetInfo, 0, len(presets))
	for _, preset := range presets {
		app := EnvOAuthApp(preset.Key)
		scopes := append([]string{}, preset.Scopes...)
		scopes = append(scopes, app.Scopes...)
		out = append(out, OAuthPresetInfo{
			Key:         preset.Key,
			Name:        preset.Name,
			Configured:  app.ClientID != "",
			RedirectURI: redirect,
			Scopes:      CleanList(scopes),
		})
	}
	return out
}

// decorateOAuth projects the sealed secret into client-safe OAuth status
// fields without ever exposing tokens or client secrets.
func (s *Service) decorateOAuth(conn domain.KnowledgeConnector) domain.KnowledgeConnector {
	mode := oauthMode(conn.Config)
	if mode == OAuthModeNone {
		conn.OAuthMode = ""
		return conn
	}
	conn.OAuthMode = mode
	secret, err := OpenSecret(conn.OwnerUserID, conn.ID, conn.SecretEnc)
	if err != nil {
		conn.OAuthStatus = "unauthorized"
		return conn
	}
	conn.OAuthClientSecretConfigured = strings.TrimSpace(secret.OAuthClientSecret) != ""
	if expiry := parseSecretExpiry(secret.ExpiresAt); expiry != nil {
		conn.OAuthExpiresAt = expiry
	}
	switch {
	case strings.TrimSpace(secret.Token) == "":
		conn.OAuthStatus = "unauthorized"
	case s.oauthTokenExpired(secret):
		conn.OAuthStatus = "expired"
	default:
		conn.OAuthStatus = "authorized"
	}
	return conn
}

// randomHex returns n cryptographically random bytes hex encoded. A failing
// entropy source is reported instead of falling back to predictable values.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(oauthRandReader, buf); err != nil {
		return "", fmt.Errorf("生成随机值失败：%w", err)
	}
	return hex.EncodeToString(buf), nil
}
