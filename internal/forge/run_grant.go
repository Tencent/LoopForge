package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

const RunMCPPath = "/internal/forgejo/run/mcp"

// RunGrant authorizes PR submission only during one active node attempt.
// The API checks the frozen run, user and branch after verifying the signature.
type RunGrant struct {
	RunID      string `json:"r"`
	NodeID     string `json:"n"`
	UserID     int64  `json:"u"`
	Generation int    `json:"g"`
	Attempt    int    `json:"a"`
	ExpiresAt  int64  `json:"e"`
}

var errRunGrant = errors.New("无效或已过期的 Forgejo 节点凭据")

func SignRunGrant(grant RunGrant, now time.Time) (string, error) {
	if runGrantSecret() == "" || grant.RunID == "" || grant.NodeID == "" || grant.UserID <= 0 || grant.Attempt <= 0 || grant.Generation <= 0 {
		return "", errRunGrant
	}
	grant.ExpiresAt = now.Add(48 * time.Hour).Unix()
	raw, err := json.Marshal(grant)
	if err != nil {
		return "", err
	}
	body := "lfforge1." + base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + runGrantSignature(body), nil
}

func VerifyRunGrant(token string, now time.Time) (RunGrant, error) {
	var grant RunGrant
	if runGrantSecret() == "" || len(token) > 4096 || !strings.HasPrefix(token, "lfforge1.") {
		return grant, errRunGrant
	}
	index := strings.LastIndexByte(token, '.')
	if index <= len("lfforge1.") || !hmac.Equal([]byte(token[index+1:]), []byte(runGrantSignature(token[:index]))) {
		return grant, errRunGrant
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token[:index], "lfforge1."))
	if err != nil || json.Unmarshal(raw, &grant) != nil || grant.ExpiresAt <= now.Unix() ||
		grant.RunID == "" || grant.NodeID == "" || grant.UserID <= 0 || grant.Attempt <= 0 || grant.Generation <= 0 {
		return RunGrant{}, errRunGrant
	}
	return grant, nil
}

func runGrantSecret() string {
	if secret := strings.TrimSpace(os.Getenv("AGENTFLOW_SESSION_SECRET")); secret != "" {
		return secret
	}
	return strings.TrimSpace(os.Getenv("SESSION_SECRET"))
}

func runGrantSignature(body string) string {
	mac := hmac.New(sha256.New, []byte(runGrantSecret()))
	_, _ = mac.Write([]byte("loopforge-forgejo-node|" + body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
