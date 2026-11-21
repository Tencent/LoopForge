package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const runnerLoginLeaseEnv = "AGENTFLOW_RUNNER_LOGIN_TOKEN"

var runnerLoginLeases sync.Map

type runnerLoginLease struct {
	id        string
	manager   *runnerLoginManager
	source    runnerLoginSource
	snapshot  runnerLoginSnapshot
	mu        sync.Mutex
	requests  map[string]runnerLoginSnapshot
	replies   map[string][]byte
	server    *http.Server
	endpoint  string
	closed    atomic.Bool
	protected []string
}

func runnerLoginNonce() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func newRunnerLoginLease(ctx context.Context, manager *runnerLoginManager, source runnerLoginSource, docker bool) (*runnerLoginLease, error) {
	snapshot, err := manager.current(ctx, source)
	if err != nil {
		return nil, err
	}
	if source.provider == "claude-code" && !snapshot.expiry.IsZero() && time.Until(snapshot.expiry) < 2*time.Minute {
		snapshot, err = manager.renew(ctx, source, snapshot)
		if err != nil {
			return nil, err
		}
	}
	id, err := runnerLoginNonce()
	if err != nil {
		return nil, err
	}
	lease := &runnerLoginLease{id: id, manager: manager, source: source, snapshot: snapshot,
		requests: map[string]runnerLoginSnapshot{id: snapshot}, replies: make(map[string][]byte)}
	_, statePath, err := manager.entry(source)
	if err != nil {
		return nil, err
	}
	lease.protected = []string{source.path, filepath.Dir(statePath)}
	if source.origin != "" && source.origin != source.path {
		lease.protected = append(lease.protected, source.origin)
	}
	if source.provider == "codex" {
		address, host := "127.0.0.1:0", "127.0.0.1"
		if docker {
			address = "0.0.0.0:0"
			host, _, err = conversationDockerAccess(ctx)
			if err != nil {
				return nil, err
			}
		}
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			return nil, err
		}
		lease.endpoint = "http://" + net.JoinHostPort(host, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)) + "/runner-login/" + id + "/refresh"
		lease.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second,
			Handler: http.HandlerFunc(lease.serveRefresh)}
		go lease.server.Serve(listener)
	}
	runnerLoginLeases.Store(id, lease)
	return lease, nil
}

func (l *runnerLoginLease) close() {
	if l.closed.Swap(true) {
		return
	}
	runnerLoginLeases.Delete(l.id)
	if l.server != nil {
		_ = l.server.Close()
	}
}

func (l *runnerLoginLease) environment() map[string]string {
	env := map[string]string{runnerLoginLeaseEnv: l.id}
	if l.source.provider == "codex" {
		env["CODEX_REFRESH_TOKEN_URL_OVERRIDE"] = l.endpoint
	} else {
		env["CLAUDE_CODE_OAUTH_TOKEN"] = l.snapshot.access
		env["CLAUDE_CODE_OAUTH_REFRESH_TOKEN"] = ""
		env["CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH"] = "1"
		env["CLAUDE_CODE_ENTRYPOINT"] = "local-agent"
		env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"] = "1"
	}
	return env
}

func codexLeaseCredential(snapshot runnerLoginSnapshot, nonce string) ([]byte, error) {
	var host struct {
		Tokens struct {
			ID      string `json:"id_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
		Last json.RawMessage `json:"last_refresh"`
	}
	if json.Unmarshal(snapshot.raw, &host) != nil {
		return nil, errors.New("invalid Codex login")
	}
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{
		"access_token": snapshot.access, "refresh_token": nonce, "id_token": host.Tokens.ID, "account_id": host.Tokens.Account,
	}}
	if len(host.Last) > 0 {
		doc["last_refresh"] = host.Last
	}
	return json.Marshal(doc)
}

func (l *runnerLoginLease) credential() ([]byte, error) {
	if l.source.provider == "codex" {
		return codexLeaseCredential(l.snapshot, l.id)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(l.snapshot.raw, &doc) != nil {
		return nil, errors.New("invalid Claude login")
	}
	var oauth map[string]json.RawMessage
	if json.Unmarshal(doc["claudeAiOauth"], &oauth) != nil {
		return nil, errors.New("invalid Claude OAuth login")
	}
	delete(oauth, "refreshToken")
	raw, _ := json.Marshal(oauth)
	return json.Marshal(map[string]json.RawMessage{"claudeAiOauth": raw})
}

func (l *runnerLoginLease) renewClaude(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed.Load() {
		return "", errors.New("Runner login lease ended")
	}
	updated, err := l.manager.renew(ctx, l.source, l.snapshot)
	if err != nil {
		return "", err
	}
	l.snapshot = updated
	return updated.access, nil
}

func (l *runnerLoginLease) serveRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.Path != "/runner-login/"+l.id+"/refresh" || l.closed.Load() {
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
	var request struct {
		Grant string `json:"grant_type"`
		Nonce string `json:"refresh_token"`
	}
	if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &request) != nil || request.Grant != "refresh_token" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_request"}`)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed.Load() {
		http.NotFound(w, r)
		return
	}
	if cached := l.replies[request.Nonce]; cached != nil {
		_, _ = w.Write(cached) // Retrying a lost response never repeats the issuer exchange.
		return
	}
	stale, valid := l.requests[request.Nonce]
	if !valid {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	updated, err := l.manager.renew(r.Context(), l.source, stale)
	if err != nil {
		// Do not echo issuer bodies, token material or private filesystem paths.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
		return
	}
	nonce, err := runnerLoginNonce()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var doc struct {
		Tokens struct {
			ID string `json:"id_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(updated.raw, &doc)
	reply, _ := json.Marshal(map[string]string{"access_token": updated.access, "id_token": doc.Tokens.ID, "refresh_token": nonce})
	// Bound replay history per task. The latest two generations cover a
	// transport retry; expired historical handles cannot start another refresh.
	if len(l.requests) >= 4 {
		l.requests = map[string]runnerLoginSnapshot{}
		l.replies = map[string][]byte{}
	}
	l.requests[nonce], l.replies[request.Nonce] = updated, reply
	l.snapshot = updated
	_, _ = w.Write(reply)
}

func bindRunnerLoginLease(command CommandSpec) CommandSpec {
	id := command.environment[runnerLoginLeaseEnv]
	delete(command.environment, runnerLoginLeaseEnv)
	if id == "" {
		return command
	}
	value, exists := runnerLoginLeases.Load(id)
	if !exists {
		return command
	}
	lease := value.(*runnerLoginLease)
	if lease.source.provider != command.Provider || lease.closed.Load() {
		return command
	}
	command.runnerLogin = lease
	if command.Provider != "claude-code" {
		return command
	}
	if valueAfter(command.Argv, "--input-format") == "stream-json" {
		return command
	}
	command.Argv = append([]string(nil), command.Argv...)
	// Provider invocation contracts put the print prompt last. Already
	// interactive conversations keep their caller-owned initialization/input.
	if len(command.Argv) > 1 && !strings.HasPrefix(command.Argv[len(command.Argv)-1], "-") {
		command.runnerLoginPrompt = command.Argv[len(command.Argv)-1]
		command.Argv = append([]string(nil), command.Argv[:len(command.Argv)-1]...)
	}
	command.Argv = insertBeforeFlag(dropArgWithValue(command.Argv, "--input-format"), "--", "--input-format", "stream-json")
	command.runnerLoginInitialize = true
	return command
}

func valueAfter(argv []string, key string) string {
	for i, arg := range argv {
		if arg == key && i+1 < len(argv) {
			return argv[i+1]
		}
		if strings.HasPrefix(arg, key+"=") {
			return strings.TrimPrefix(arg, key+"=")
		}
	}
	return ""
}
