package knowledge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const proxyTokenPrefix = "afkb1."

// ProxyTokenTTL bounds how long a run's knowledge proxy token stays valid.
const ProxyTokenTTL = 48 * time.Hour

// ProxyClaims identify the run, node, user owner, and bound KnowledgeRef a
// stateless proxy token grants read-only access for.
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

// SignProxyToken mints a stateless HMAC-SHA256 bearer token for /runners/knowledge/mcp.
func SignProxyToken(claims ProxyClaims, now time.Time) (string, error) {
	if claims.Ref == nil || strings.TrimSpace(claims.Ref.ConnectorID) == "" {
		return "", errors.New("missing knowledge connector reference")
	}
	key, err := proxySigningKey()
	if err != nil {
		return "", err
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now.Add(ProxyTokenTTL).Unix()
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := proxyTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + signProxyBody(key, body), nil
}

// VerifyProxyToken validates signature and expiry on an afkb1. bearer token.
func VerifyProxyToken(token string, now time.Time) (ProxyClaims, error) {
	key, err := proxySigningKey()
	if err != nil {
		return ProxyClaims{}, err
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, proxyTokenPrefix) {
		return ProxyClaims{}, errInvalidProxyToken
	}
	dot := strings.LastIndexByte(token, '.')
	if dot <= len(proxyTokenPrefix) {
		return ProxyClaims{}, errInvalidProxyToken
	}
	body, sig := token[:dot], token[dot+1:]
	if !hmac.Equal([]byte(sig), []byte(signProxyBody(key, body))) {
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
	if claims.Ref == nil || strings.TrimSpace(claims.Ref.ConnectorID) == "" {
		return ProxyClaims{}, errInvalidProxyToken
	}
	return claims, nil
}

func proxySigningKey() ([]byte, error) {
	material, err := platformSecretMaterial()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte("loopforge/knowledge/proxy-token/v1\x00" + material))
	return sum[:], nil
}

func signProxyBody(key []byte, body string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
