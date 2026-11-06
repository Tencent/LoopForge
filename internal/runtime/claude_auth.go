package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Claude Code reads a saved key from one of two variables, each sent as a
// different header: ANTHROPIC_API_KEY as x-api-key (the official API) and
// ANTHROPIC_AUTH_TOKEN as "Authorization: Bearer" (LLM gateways). Only one is
// set, since a gateway may reject an unexpected x-api-key.
const (
	claudeOfficialBaseURL = "https://api.anthropic.com"
	claudeAuthProbeTTL    = 10 * time.Minute
)

// claudeAuthProbe reports whether baseURL accepts key with the given scheme
// ("bearer" or "x-api-key"). ok is false when the probe could not decide.
// It is a variable so tests can stub the network.
var claudeAuthProbe = probeClaudeAuth

var claudeAuthCache sync.Map // sha256(baseURL, key) -> claudeAuthChoice

type claudeAuthChoice struct {
	apiKey  bool
	expires time.Time
}

// ClaudeCodeCredentialEnvironment maps a saved Claude Code key onto the
// variables the CLI reads. Without a base URL the official API takes
// ANTHROPIC_API_KEY. With a custom base URL the Bearer token is tried first;
// when the gateway rejects it but accepts x-api-key, ANTHROPIC_API_KEY is used
// instead. An undecided probe (network error, unexpected status) keeps the
// Bearer token.
func ClaudeCodeCredentialEnvironment(apiKey, baseURL string) map[string]string {
	apiKey = strings.TrimSpace(apiKey)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return map[string]string{"ANTHROPIC_API_KEY": apiKey, "ANTHROPIC_BASE_URL": claudeOfficialBaseURL}
	}
	if claudeGatewayWantsAPIKey(apiKey, baseURL) {
		return map[string]string{"ANTHROPIC_API_KEY": apiKey, "ANTHROPIC_BASE_URL": baseURL}
	}
	return map[string]string{"ANTHROPIC_AUTH_TOKEN": apiKey, "ANTHROPIC_BASE_URL": baseURL}
}

func claudeGatewayWantsAPIKey(apiKey, baseURL string) bool {
	if apiKey == "" {
		return false
	}
	sum := sha256.Sum256([]byte(baseURL + "\x00" + apiKey))
	cacheKey := hex.EncodeToString(sum[:])
	now := time.Now()
	if cached, ok := claudeAuthCache.Load(cacheKey); ok {
		if choice := cached.(claudeAuthChoice); now.Before(choice.expires) {
			return choice.apiKey
		}
	}
	useAPIKey := false
	if accepted, ok := claudeAuthProbe(baseURL, apiKey, "bearer"); ok && !accepted {
		if accepted, ok := claudeAuthProbe(baseURL, apiKey, "x-api-key"); ok && accepted {
			useAPIKey = true
		}
	}
	claudeAuthCache.Store(cacheKey, claudeAuthChoice{apiKey: useAPIKey, expires: now.Add(claudeAuthProbeTTL)})
	return useAPIKey
}

// probeClaudeAuth posts an empty Messages request: gateways authenticate
// before validating the body, so 401/403 means the header was rejected and
// any other HTTP answer (typically 400) means it was accepted. No tokens are
// spent.
func probeClaudeAuth(baseURL, apiKey, scheme string) (accepted, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", strings.NewReader("{}"))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if scheme == "bearer" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	} else {
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, true
	case resp.StatusCode >= 500:
		return false, false
	default:
		return true, true
	}
}
