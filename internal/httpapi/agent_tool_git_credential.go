package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// agentGitCredentialMethod is the private JSON-RPC method the workspace's
// credential helper and gh/glab wrappers call through the session relay. It
// is not a tool and never appears in tools/list.
const agentGitCredentialMethod = "agentflow/gitCredential"

// gitCredentialResult follows Git's credential attributes. An empty result
// means "no credential here": Git then tries its next helper or fails.
type gitCredentialResult struct {
	Username          string `json:"username,omitempty"`
	Password          string `json:"password,omitempty"`
	PasswordExpiryUTC int64  `json:"password_expiry_utc,omitempty"`
	Platform          string `json:"platform,omitempty"`
}

// gitCredential hands the current speaker's token for exactly one HTTPS host
// to a workspace turn that is still running. It answers empty instead of an
// error so a helper never blocks Git on a host the platform does not serve.
func (h *agentToolGateway) gitCredential(ctx context.Context, s *AgentToolSession, raw json.RawMessage) gitCredentialResult {
	var req struct {
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		Caller   string `json:"caller"`
	}
	if len(raw) > 4<<10 || json.Unmarshal(raw, &req) != nil {
		return gitCredentialResult{}
	}
	caller := strings.ToLower(strings.TrimSpace(req.Caller))
	switch caller {
	case "git", "gh", "glab":
	default:
		caller = "manual"
	}
	outcome := "granted"
	host, err := gitwork.HTTPSHostScope(req.Host)
	defer func() {
		log.Printf("git credential %s: session=%s speaker=%d host=%q caller=%s", outcome, s.ChatSessionID, s.UserID, req.Host, caller)
	}()
	switch {
	case s.RuntimeMode != domain.AssistantModeWorkspace || h.repo == nil:
		outcome = "refused (not a workspace turn)"
		return gitCredentialResult{}
	case !h.nativeToolTurnActive(ctx, s):
		outcome = "refused (turn not active)"
		return gitCredentialResult{}
	case !strings.EqualFold(strings.TrimSpace(req.Protocol), "https") || err != nil:
		outcome = "refused (not an https host)"
		return gitCredentialResult{}
	}
	cred, ok := h.repo.GitHTTPCredentialForHost(ctx, s.UserID, host)
	if !ok || cred.Token == "" {
		outcome = "not configured"
		return gitCredentialResult{}
	}
	result := gitCredentialResult{Username: cred.GitUsername(), Password: cred.Token, Platform: cred.Platform}
	s.rememberGitCredential(host, cred.Platform, result.Username, cred.Token)
	if !cred.ExpiresAt.IsZero() {
		result.PasswordExpiryUTC = cred.ExpiresAt.Unix()
	}
	return result
}

func (s *AgentToolSession) rememberGitCredential(host, platform, username, token string) {
	gitSecretRedactions.add(username, token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gitHosts == nil {
		s.gitHosts = map[string]string{}
	}
	s.gitHosts[host] = platform
}

// gitSecretRedactions holds every Git token handed to a workspace turn. Any
// participant, transcript or log may show what the agent printed, so the
// tokens are redacted from all assistant output, not only the speaker's.
var gitSecretRedactions = &gitSecretRegistry{values: map[string]time.Time{}}

const (
	gitSecretRetention = 24 * time.Hour
	gitSecretLimit     = 4096
)

type gitSecretRegistry struct {
	mu     sync.Mutex
	values map[string]time.Time
	sorted []string
	pruned time.Time
}

func (r *gitSecretRegistry) add(username, token string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, form := range gitSecretForms(username, token) {
		r.values[form] = now
	}
	r.refreshLocked(now, true)
}

// redact replaces each issued token, longest first so a token containing
// another is replaced whole.
func (r *gitSecretRegistry) redact(text, mask string) string {
	if text == "" {
		return text
	}
	r.mu.Lock()
	r.refreshLocked(time.Now(), false)
	secrets := r.sorted
	r.mu.Unlock()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			text = strings.ReplaceAll(text, secret, mask)
		}
	}
	return text
}

func (r *gitSecretRegistry) refreshLocked(now time.Time, changed bool) {
	if !changed && now.Sub(r.pruned) < time.Minute {
		return
	}
	r.pruned = now
	forms := make([]string, 0, len(r.values))
	for form, issued := range r.values {
		if now.Sub(issued) > gitSecretRetention {
			delete(r.values, form)
			continue
		}
		forms = append(forms, form)
	}
	if len(forms) > gitSecretLimit {
		sort.Slice(forms, func(i, j int) bool { return r.values[forms[i]].After(r.values[forms[j]]) })
		for _, form := range forms[gitSecretLimit:] {
			delete(r.values, form)
		}
		forms = forms[:gitSecretLimit]
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	r.sorted = forms
}

// gitSecretForms are the ways a token shows up in command output: as is,
// URL-escaped in a remote URL, and inside a Basic authorization header
// (base64 of "user:token", padding trimmed so both padded and unpadded
// encodings match).
func gitSecretForms(username, token string) []string {
	token = strings.TrimSpace(token)
	if len(token) < 8 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(form string) {
		if len(form) >= 8 && !seen[form] {
			seen[form] = true
			out = append(out, form)
		}
	}
	add(token)
	add(url.QueryEscape(token))
	add(url.PathEscape(token))
	for _, user := range []string{username, domain.GitTokenHTTPUser, "oauth2"} {
		if user != "" {
			add(strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(user+":"+token)), "="))
		}
	}
	return out
}

// noteGitScrubbed records credentials the execution host removed after a CLI
// of this turn finished; the turn's reply mentions them once.
func (s *AgentToolSession) noteGitScrubbed(items []string) {
	if s == nil || len(items) == 0 {
		return
	}
	log.Printf("git credentials scrubbed: session=%s speaker=%d items=%s", s.ChatSessionID, s.UserID, strings.Join(items, ", "))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		if !slices.Contains(s.gitScrubbed, item) {
			s.gitScrubbed = append(s.gitScrubbed, item)
		}
	}
}

func (s *AgentToolSession) gitScrubNote() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.gitScrubbed) == 0 {
		return ""
	}
	items := s.gitScrubbed
	if len(items) > 5 {
		items = append(items[:5:5], fmt.Sprintf("等 %d 处", len(s.gitScrubbed)))
	}
	return "已清理 Agent 写入工作区的 Git 凭据（" + strings.Join(items, "、") + "）。令牌由平台按需提供，不要写入文件或 remote URL。"
}

// requestedGitHosts maps each host a token was handed out for onto its platform.
func (s *AgentToolSession) requestedGitHosts() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.gitHosts))
	for host, platform := range s.gitHosts {
		out[host] = platform
	}
	return out
}
