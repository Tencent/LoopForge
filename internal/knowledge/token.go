package knowledge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const proxyTokenPrefix = "afkb1."

// ProxyTokenTTL bounds how long a run's knowledge token stays valid. It must
// outlive long agent nodes but is not a durable credential.
const ProxyTokenTTL = 48 * time.Hour

// ProxyClaims identify who a proxy token was issued for. Tokens are stateless
// and signed with a key derived from the upstream token, so rotating the
// upstream token revokes every outstanding proxy token.
type ProxyClaims struct {
	Ref       *domain.KnowledgeRef `json:"ref,omitempty"`
	Attempt   int                  `json:"attempt,omitempty"`
	RunID     string               `json:"run,omitempty"`
	NodeID    string               `json:"node,omitempty"`
	ProjectID string               `json:"p,omitempty"`
	UserID    int64                `json:"u,omitempty"`
	AgentID   string               `json:"a,omitempty"`
	ExpiresAt int64                `json:"e"`
}

var errInvalidProxyToken = errors.New("invalid knowledge proxy token")

func SignProxyToken(settings domain.KnowledgeSettings, claims ProxyClaims, now time.Time) (string, error) {
	if settings.Token == "" && (claims.Ref == nil || claims.Ref.ServerID == domain.KnowledgeMCPServerID) {
		return "", ErrNotConfigured
	}
	key := proxyKey(settings)
	if key == nil {
		return "", ErrNotConfigured
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now.Add(ProxyTokenTTL).Unix()
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := proxyTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + sign(key, body), nil
}

func VerifyProxyToken(settings domain.KnowledgeSettings, token string, now time.Time) (ProxyClaims, error) {
	key := proxyKey(settings)
	token = strings.TrimSpace(token)
	if key == nil || !strings.HasPrefix(token, proxyTokenPrefix) {
		return ProxyClaims{}, errInvalidProxyToken
	}
	dot := strings.LastIndexByte(token, '.')
	if dot <= len(proxyTokenPrefix) {
		return ProxyClaims{}, errInvalidProxyToken
	}
	body, sig := token[:dot], token[dot+1:]
	if !hmac.Equal([]byte(sig), []byte(sign(key, body))) {
		return ProxyClaims{}, errInvalidProxyToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(body, proxyTokenPrefix))
	if err != nil {
		return ProxyClaims{}, errInvalidProxyToken
	}
	var claims ProxyClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ProxyClaims{}, errInvalidProxyToken
	}
	if claims.ExpiresAt <= now.Unix() {
		return ProxyClaims{}, errors.New("knowledge proxy token expired")
	}
	return claims, nil
}

func proxyKey(settings domain.KnowledgeSettings) []byte {
	upstream := strings.TrimSpace(settings.Token)

	secret := strings.TrimSpace(os.Getenv("AGENTFLOW_SESSION_SECRET"))
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("SESSION_SECRET"))
	}
	if upstream == "" {
		if secret == "" {
			return nil
		}
		upstream = secret
		secret = "external-knowledge-only"
	}
	mac := hmac.New(sha256.New, []byte(upstream))
	mac.Write([]byte("agentflow-knowledge-proxy|" + secret))
	return mac.Sum(nil)
}

func sign(key []byte, body string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
