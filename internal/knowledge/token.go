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
)

const proxyTokenPrefix = "afkb1."

// ProxyTokenTTL bounds how long a run's knowledge token stays valid. It must
// outlive long agent nodes but is not a durable credential.
const ProxyTokenTTL = 48 * time.Hour

// ProxyClaims identify who a proxy token was issued for. Tokens are stateless
// and signed with a key derived from the session secret, so rotating the
// session secret revokes every outstanding proxy token.
type ProxyClaims struct {
	ProjectID string `json:"p,omitempty"`
	UserID    int64  `json:"u,omitempty"`
	AgentID   string `json:"a,omitempty"`
	ExpiresAt int64  `json:"e"`
}

var errInvalidProxyToken = errors.New("invalid knowledge proxy token")

func SignProxyToken(secret string, claims ProxyClaims, now time.Time) (string, error) {
	key := proxyKey(secret)
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

func VerifyProxyToken(secret string, token string, now time.Time) (ProxyClaims, error) {
	key := proxyKey(secret)
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

func proxyKey(secret string) []byte {
	if secret == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("agentflow-knowledge-proxy"))
	return mac.Sum(nil)
}

func sign(key []byte, body string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func SessionSecret() string {
	if s := os.Getenv("AGENTFLOW_SESSION_SECRET"); s != "" {
		return s
	}
	return os.Getenv("SESSION_SECRET")
}
