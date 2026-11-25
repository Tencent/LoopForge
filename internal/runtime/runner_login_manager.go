package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A source is owned by the Runner. Sandbox credential files are never sources.
type runnerLoginSource struct {
	provider, path string
	origin         string
	identity       func([]byte) string
}

type runnerLoginSnapshot struct {
	raw                       []byte
	identity, access, refresh string
	expiry                    time.Time
}

type runnerLoginEntry struct {
	gate          chan struct{}
	managedDigest string
}

type runnerLoginManager struct {
	stateDir string
	client   *http.Client
	refresh  func(context.Context, string, []byte) ([]byte, error)
	mu       sync.Mutex
	entries  map[string]*runnerLoginEntry
}

var nativeRunnerLogins = &runnerLoginManager{}

const claudeRunnerOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e" // gitleaks:allow public OAuth application ID shipped by the official Claude Code CLI.

func loginDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func fileRunnerLoginSource(login conversationLoginSpec, configDir string) runnerLoginSource {
	path := filepath.Join(configDir, login.file)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	origin := path
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(parent, filepath.Base(path))
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	provider := "codex"
	if login.binding == BindClaudeCode {
		provider = "claude-code"
	}
	source := runnerLoginSource{provider: provider, path: path, origin: origin}
	source.identity = func(raw []byte) string {
		if provider == "codex" {
			var doc struct {
				Tokens struct {
					ID      string `json:"id_token"`
					Access  string `json:"access_token"`
					Account string `json:"account_id"`
				} `json:"tokens"`
			}
			if json.Unmarshal(raw, &doc) != nil {
				return ""
			}
			id := jwtRunnerPrincipal(doc.Tokens.ID)
			if id == "" {
				id = jwtRunnerPrincipal(doc.Tokens.Access)
			}
			if id == "" || doc.Tokens.Account == "" {
				return ""
			}
			return id + "\x00" + doc.Tokens.Account
		}
		// This is trusted host metadata, never a sandbox .claude.json. An
		// absent anchor does not authorize adopting an external token change.
		for _, profile := range []string{filepath.Join(configDir, ".claude.json"), filepath.Join(filepath.Dir(configDir), ".claude.json")} {
			data, err := os.ReadFile(profile)
			if err != nil || len(data) > 1<<20 {
				continue
			}
			var doc struct {
				Account struct {
					User string `json:"accountUuid"`
					Org  string `json:"organizationUuid"`
				} `json:"oauthAccount"`
			}
			if json.Unmarshal(data, &doc) == nil && doc.Account.User != "" {
				return doc.Account.User + "\x00" + doc.Account.Org
			}
		}
		return ""
	}
	return source
}

// JWTs are decoded here only from a trusted host source or a TLS-authenticated
// issuer response. This function must never validate sandbox-supplied tokens.
func jwtRunnerPrincipal(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Subject string `json:"sub"`
		Auth    struct {
			User string `json:"chatgpt_user_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	if claims.Subject == "" {
		return claims.Auth.User
	}
	return claims.Subject + "\x00" + claims.Auth.User
}

func parseRunnerOAuth(provider string, raw []byte) (runnerLoginSnapshot, error) {
	snapshot := runnerLoginSnapshot{raw: bytes.Clone(raw)}
	var doc struct {
		Tokens struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
		Claude struct {
			Access  string `json:"accessToken"`
			Refresh string `json:"refreshToken"`
			Expiry  int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &doc) != nil {
		return snapshot, errors.New("invalid Runner login")
	}
	switch provider {
	case "codex":
		snapshot.access, snapshot.refresh = doc.Tokens.Access, doc.Tokens.Refresh
		parts := strings.Split(snapshot.access, ".")
		if len(parts) == 3 {
			data, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				Expiry int64 `json:"exp"`
			}
			if json.Unmarshal(data, &claims) == nil && claims.Expiry > 0 {
				snapshot.expiry = time.Unix(claims.Expiry, 0)
			}
		}
	case "claude-code":
		snapshot.access, snapshot.refresh = doc.Claude.Access, doc.Claude.Refresh
		if doc.Claude.Expiry > 0 {
			snapshot.expiry = time.UnixMilli(doc.Claude.Expiry)
		}
	default:
		return snapshot, errors.New("unsupported managed Runner login")
	}
	if snapshot.access == "" || snapshot.refresh == "" {
		return snapshot, errors.New("Runner OAuth login has no token pair")
	}
	return snapshot, nil
}

func (source runnerLoginSource) read() (runnerLoginSnapshot, error) {
	if source.origin != "" {
		resolved, err := filepath.EvalSymlinks(source.origin)
		if err != nil {
			return runnerLoginSnapshot{}, err
		}
		if resolved != source.path {
			return runnerLoginSnapshot{}, errors.New("Runner login source changed; restart this task")
		}
	}
	raw, err := readRunnerLoginFile(filepath.Dir(source.path), filepath.Base(source.path))
	if err != nil {
		return runnerLoginSnapshot{}, err
	}
	snapshot, err := parseRunnerOAuth(source.provider, raw)
	if err == nil && source.identity != nil {
		snapshot.identity = source.identity(raw)
	}
	return snapshot, err
}

func (m *runnerLoginManager) entry(source runnerLoginSource) (*runnerLoginEntry, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	root := m.stateDir
	if root == "" {
		root = strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_AUTH_DIR"))
		if root == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				return nil, "", err
			}
			root = filepath.Join(cache, "LoopForge", "runner-auth")
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, "", errors.New("Runner auth state must be a private directory")
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, "", err
	}
	if m.entries == nil {
		m.entries = make(map[string]*runnerLoginEntry)
	}
	key := loginDigest([]byte(source.provider + "\x00" + source.path))
	e := m.entries[key]
	if e == nil {
		e = &runnerLoginEntry{gate: make(chan struct{}, 1)}
		m.entries[key] = e
	}
	return e, filepath.Join(root, key), nil
}

func (m *runnerLoginManager) locked(ctx context.Context, source runnerLoginSource, fn func(*runnerLoginEntry, string) (runnerLoginSnapshot, error)) (runnerLoginSnapshot, error) {
	e, path, err := m.entry(source)
	if err != nil {
		return runnerLoginSnapshot{}, err
	}
	select {
	case e.gate <- struct{}{}:
	case <-ctx.Done():
		return runnerLoginSnapshot{}, ctx.Err()
	}
	defer func() { <-e.gate }()
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return runnerLoginSnapshot{}, err
	}
	defer file.Close()
	for {
		if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return runnerLoginSnapshot{}, err
		}
		select {
		case <-ctx.Done():
			return runnerLoginSnapshot{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn(e, path+".pending")
}

type runnerLoginPending struct {
	Expected string          `json:"expected"`
	Identity string          `json:"identity"`
	Login    json.RawMessage `json:"login,omitempty"`
}

func writeRunnerLoginPending(path string, pending runnerLoginPending) error {
	return writeRunnerLoginState(path, pending)
}

func writeRunnerLoginState(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".runner-auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type runnerLoginReceipt struct {
	Digest   string `json:"digest"`
	Identity string `json:"identity"`
}

func runnerLoginReceiptPath(pendingPath string) string {
	return strings.TrimSuffix(pendingPath, ".pending") + ".receipt"
}

func recordRunnerLogin(e *runnerLoginEntry, pendingPath string, snapshot runnerLoginSnapshot) error {
	receipt := runnerLoginReceipt{Digest: loginDigest(snapshot.raw), Identity: snapshot.identity}
	if err := writeRunnerLoginState(runnerLoginReceiptPath(pendingPath), receipt); err != nil {
		return errors.New("cannot record canonical Runner refresh")
	}
	e.managedDigest = receipt.Digest
	// The receipt contains only a hash. Do not retain a second live refresh
	// token after the host file has been durably updated.
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *runnerLoginManager) current(ctx context.Context, source runnerLoginSource) (runnerLoginSnapshot, error) {
	return m.locked(ctx, source, func(e *runnerLoginEntry, pendingPath string) (runnerLoginSnapshot, error) {
		current, _, err := m.recover(source, e, pendingPath)
		return current, err
	})
}

// A journal without Login means an exchange may have consumed the old refresh
// token. Access remains usable, but that exact pair must not be exchanged again.
func (m *runnerLoginManager) recover(source runnerLoginSource, e *runnerLoginEntry, pendingPath string) (runnerLoginSnapshot, bool, error) {
	current, err := source.read()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(pendingPath)
			_ = os.Remove(runnerLoginReceiptPath(pendingPath))
			e.managedDigest = ""
		}
		return current, false, err
	} // A logout must never resurrect a pending login.
	if raw, err := os.ReadFile(runnerLoginReceiptPath(pendingPath)); err == nil {
		var receipt runnerLoginReceipt
		if len(raw) > 16<<10 || json.Unmarshal(raw, &receipt) != nil {
			return current, false, errors.New("invalid Runner refresh receipt")
		}
		if receipt.Digest == loginDigest(current.raw) && receipt.Identity == current.identity {
			e.managedDigest = receipt.Digest
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return current, false, errors.New("cannot read Runner refresh receipt")
	}
	data, err := os.ReadFile(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return current, false, nil
	}
	if err != nil || len(data) > 2<<20 {
		return current, false, errors.New("cannot read pending Runner refresh")
	}
	var pending runnerLoginPending
	if json.Unmarshal(data, &pending) != nil {
		return current, false, errors.New("invalid pending Runner refresh")
	}
	if bytes.Equal(current.raw, pending.Login) {
		return current, false, recordRunnerLogin(e, pendingPath, current)
	}
	if pending.Expected != loginDigest(current.raw) || pending.Identity != current.identity {
		return current, false, nil
	}
	if len(pending.Login) == 0 {
		return current, true, nil
	}
	if _, err := parseRunnerOAuth(source.provider, pending.Login); err != nil {
		return current, false, err
	}
	if pending.Identity != "" && source.identity(pending.Login) != pending.Identity {
		return current, false, errors.New("pending Runner refresh belongs to another account")
	}
	if err := replaceRunnerLoginFile(filepath.Dir(source.path), filepath.Base(source.path), current.raw, pending.Login); err != nil {
		return current, false, err
	}
	updated, err := source.read()
	if err == nil && bytes.Equal(updated.raw, pending.Login) {
		err = recordRunnerLogin(e, pendingPath, updated)
	}
	return updated, false, err
}

func (m *runnerLoginManager) renew(ctx context.Context, source runnerLoginSource, stale runnerLoginSnapshot) (runnerLoginSnapshot, error) {
	return m.locked(ctx, source, func(e *runnerLoginEntry, pendingPath string) (runnerLoginSnapshot, error) {
		current, uncertain, err := m.recover(source, e, pendingPath)
		if err != nil {
			return current, err
		}
		if current.identity != stale.identity {
			return current, errors.New("Runner account changed; restart this task")
		}
		if !bytes.Equal(current.raw, stale.raw) {
			if current.identity == "" && e.managedDigest != loginDigest(current.raw) {
				return current, errors.New("Runner login changed without an account anchor; restart this task")
			}
			return current, nil // Another task or the host refreshed first.
		}
		if uncertain {
			return current, errors.New("Runner refresh outcome is unknown; update the host login before retrying")
		}
		if err := ctx.Err(); err != nil {
			return current, err
		}
		if err := writeRunnerLoginPending(pendingPath, runnerLoginPending{Expected: loginDigest(current.raw), Identity: current.identity}); err != nil {
			return current, errors.New("cannot journal Runner refresh")
		}
		// Once an exchange has started, a cancelled sandbox must not discard a
		// successful rotation. Bound the trusted refresh independently.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		refresh := m.refresh
		if refresh == nil {
			refresh = m.refreshOAuth
		}
		canonical, err := refresh(refreshCtx, source.provider, current.raw)
		if err != nil {
			var rejected *runnerRefreshRejected
			if errors.As(err, &rejected) {
				_ = os.Remove(pendingPath) // The issuer definitively rejected the exchange.
			}
			return runnerLoginSnapshot{}, err
		}
		pending := runnerLoginPending{Expected: loginDigest(current.raw), Identity: current.identity, Login: canonical}
		if err := writeRunnerLoginPending(pendingPath, pending); err != nil {
			return runnerLoginSnapshot{}, errors.New("cannot preserve refreshed Runner login")
		}
		if _, err := parseRunnerOAuth(source.provider, canonical); err != nil {
			return runnerLoginSnapshot{}, err
		}
		if current.identity != "" && source.identity(canonical) != current.identity {
			return runnerLoginSnapshot{}, errors.New("refreshed Runner login belongs to another account")
		}
		if _, err := source.read(); err != nil {
			return runnerLoginSnapshot{}, errors.New("Runner login source changed during refresh; response retained privately")
		}
		if err := replaceRunnerLoginFile(filepath.Dir(source.path), filepath.Base(source.path), current.raw, canonical); err != nil {
			return runnerLoginSnapshot{}, errors.New("refreshed Runner login is pending recovery")
		}
		updated, err := source.read()
		if err != nil {
			return updated, errors.New("Runner login removed during refresh")
		}
		if !bytes.Equal(updated.raw, canonical) {
			if updated.identity == "" || updated.identity != current.identity {
				return updated, errors.New("Runner account changed during refresh")
			}
			return updated, nil // Keep the received pair private; host stays authoritative.
		}
		return updated, recordRunnerLogin(e, pendingPath, updated)
	})
}

type runnerRefreshRejected struct{ error }

func (m *runnerLoginManager) refreshOAuth(ctx context.Context, provider string, original []byte) ([]byte, error) {
	snapshot, err := parseRunnerOAuth(provider, original)
	if err != nil {
		return nil, err
	}
	endpoint, clientID := "https://auth.openai.com/oauth/token", "app_EMoamEEZ73f0CkXaXp7hrann"
	body := map[string]any{"grant_type": "refresh_token", "refresh_token": snapshot.refresh}
	if provider == "claude-code" {
		endpoint, clientID = "https://platform.claude.com/v1/oauth/token", claudeRunnerOAuthClientID
		var doc struct {
			OAuth struct {
				Scopes []string `json:"scopes"`
			} `json:"claudeAiOauth"`
		}
		_ = json.Unmarshal(original, &doc)
		if len(doc.OAuth.Scopes) > 0 {
			body["scope"] = strings.Join(doc.OAuth.Scopes, " ")
		}
	} else {
		var doc struct {
			Tokens struct {
				Provenance *struct {
					Issuer string `json:"issuer"`
					Client string `json:"client_id"`
				} `json:"oauth_provenance"`
			} `json:"tokens"`
		}
		_ = json.Unmarshal(original, &doc)
		if provenance := doc.Tokens.Provenance; provenance != nil && provenance.Issuer != "" && provenance.Client != "" {
			endpoint, clientID = strings.TrimRight(provenance.Issuer, "/")+"/oauth/token", provenance.Client
		} else if override := strings.TrimSpace(os.Getenv("CODEX_APP_SERVER_LOGIN_CLIENT_ID")); override != "" {
			clientID = override
		}
		if override := strings.TrimSpace(os.Getenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE")); override != "" {
			endpoint = override // Trusted Runner environment, never a sandbox value.
		}
	}
	body["client_id"] = clientID
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, &runnerRefreshRejected{errors.New("invalid Runner OAuth endpoint")}
	}
	req.Header.Set("Content-Type", "application/json")
	client := m.client
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Runner login refresh unavailable; credentials retained")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || resp.StatusCode != http.StatusOK {
		failure := fmt.Errorf("Runner login refresh rejected (HTTP %d); credentials retained", resp.StatusCode)
		if err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout {
			return nil, &runnerRefreshRejected{failure}
		}
		return nil, failure
	}
	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
		Expires int64  `json:"expires_in"`
		Scope   string `json:"scope"`
	}
	if json.Unmarshal(data, &tokens) != nil || tokens.Access == "" {
		return nil, errors.New("invalid Runner OAuth response")
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(original, &doc)
	key, accessKey, refreshKey := "tokens", "access_token", "refresh_token"
	if provider == "claude-code" {
		key, accessKey, refreshKey = "claudeAiOauth", "accessToken", "refreshToken"
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(doc[key], &values) != nil {
		return nil, errors.New("invalid Runner OAuth record")
	}
	values[accessKey], _ = json.Marshal(tokens.Access)
	if tokens.Refresh != "" {
		values[refreshKey], _ = json.Marshal(tokens.Refresh)
	}
	if provider == "codex" {
		if tokens.ID != "" {
			values["id_token"], _ = json.Marshal(tokens.ID)
		}
		doc["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	} else {
		if tokens.Expires <= 0 {
			return nil, errors.New("Runner OAuth response has no expiry")
		}
		values["expiresAt"], _ = json.Marshal(time.Now().Add(time.Duration(tokens.Expires) * time.Second).UnixMilli())
		if tokens.Scope != "" {
			values["scopes"], _ = json.Marshal(strings.Fields(tokens.Scope))
		}
	}
	doc[key], _ = json.Marshal(values)
	return json.Marshal(doc)
}
