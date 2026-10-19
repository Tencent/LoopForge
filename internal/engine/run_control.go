package engine

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"agentflow/internal/repo"
)

const controlTokenHeader = "X-Agentflow-Control-Token"

// ControlConfig is how an api/worker process advertises and authenticates
// run-control interrupts.
type ControlConfig struct {
	Role        string
	URL         string
	Token       string
	FallbackURL string
	// APIURL is the API listener reachable from this process. Runner workspace
	// operations are forwarded there, not through a sandbox/public MCP address.
	APIURL string
}

// ConfigureControl sets the advertised interrupt URL (workers), shared token,
// and optional static fallback URL used when the owner is not registered.
func (e *Engine) ConfigureControl(cfg ControlConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.controlRole = strings.TrimSpace(cfg.Role)
	e.controlURL = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	e.controlToken = strings.TrimSpace(cfg.Token)
	e.workspaceControlBase = strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/")
	e.fallbackWorkerURL = strings.TrimRight(strings.TrimSpace(cfg.FallbackURL), "/")
	if e.controlClient == nil {
		e.controlClient = &http.Client{Timeout: 800 * time.Millisecond}
	}
}

// SetRunControlNotify wires a test/in-process wakeup. Pause/Cancel still
// persist flags first; a missing listener is harmless because the lease
// heartbeat re-reads the same bits.
func (e *Engine) SetRunControlNotify(fn func(runID string)) {
	e.mu.Lock()
	e.runControlNotify = fn
	e.mu.Unlock()
}

func (e *Engine) notifyRunControl(runID string) {
	e.mu.Lock()
	fn := e.runControlNotify
	fallback := e.fallbackWorkerURL
	token := e.controlToken
	client := e.controlClient
	e.mu.Unlock()
	if strings.TrimSpace(runID) == "" {
		return
	}
	if fn != nil {
		fn(runID)
		return
	}
	urls := e.runControlURLs(runID, fallback)
	if len(urls) == 0 {
		return
	}
	if client == nil {
		client = &http.Client{Timeout: 800 * time.Millisecond}
	}
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(base string) {
			defer wg.Done()
			postRunInterrupt(client, base, runID, token)
		}(u)
	}
	wg.Wait()
}

func (e *Engine) runControlURLs(runID, fallback string) []string {
	if r, ok := e.repo.RunState(runID); ok {
		if u := e.repo.EngineInstanceControlURL(r.OwnerInstanceID); u != "" {
			return []string{u}
		}
	}
	seen := map[string]struct{}{}
	var urls []string
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	for _, u := range e.repo.LiveWorkerControlURLs() {
		add(u)
	}
	add(fallback)
	return urls
}

// StartInstanceHeartbeat publishes this process's interrupt URL so the API can
// address the owning worker. The first beat is synchronous.
func (e *Engine) StartInstanceHeartbeat(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.heartbeatInstance()
	go func() {
		e.mu.Lock()
		interval := e.leaseTTL / 3
		e.mu.Unlock()
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.heartbeatInstance()
			}
		}
	}()
}

func (e *Engine) heartbeatInstance() {
	e.mu.Lock()
	id, role, controlURL, host, ttl := e.instanceID, e.controlRole, e.controlURL, e.workspaceHost, e.leaseTTL
	e.mu.Unlock()
	if controlURL == "" || id == "" {
		return
	}
	if role == "" {
		role = "worker"
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if err := e.repo.HeartbeatEngineInstance(repo.EngineInstance{
		InstanceID: id, Role: role, ControlURL: controlURL, WorkspaceHost: host,
	}, ttl); err != nil {
		log.Printf("engine instance heartbeat: %v", err)
	}
}

// Interrupt cancels the local scheduler for runID when a durable pause/cancel
// flag is already set. It is a no-op when this process does not own the run.
func (e *Engine) Interrupt(runID string) bool {
	if strings.TrimSpace(runID) == "" || !e.runControlRequested(runID) {
		return false
	}
	e.mu.Lock()
	cancel := e.running[runID]
	e.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// HandleRunInterrupt is the worker-side wakeup: POST /internal/runs/{id}/interrupt.
func (e *Engine) HandleRunInterrupt(w http.ResponseWriter, r *http.Request) {
	if !e.controlTokenOK(r.Header.Get(controlTokenHeader)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" {
		http.Error(w, "run id required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"interrupted": e.Interrupt(runID)})
}

func (e *Engine) controlTokenOK(got string) bool {
	e.mu.Lock()
	want := e.controlToken
	e.mu.Unlock()
	if want == "" {
		return true
	}
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func postRunInterrupt(client *http.Client, baseURL, runID, token string) {
	if client == nil || strings.TrimSpace(baseURL) == "" || strings.TrimSpace(runID) == "" {
		return
	}
	u := strings.TrimRight(baseURL, "/") + "/internal/runs/" + url.PathEscape(runID) + "/interrupt"
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		log.Printf("run control notify %s: %v", runID, err)
		return
	}
	if token != "" {
		req.Header.Set(controlTokenHeader, token)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("run control notify %s: %v", runID, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("run control notify %s: worker returned %s", runID, resp.Status)
	}
}

// HTTPRunControlNotify POSTs a single worker. Tests and static fallbacks use it.
func HTTPRunControlNotify(baseURL, token string) func(runID string) {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return func(runID string) {
		postRunInterrupt(client, base, runID, token)
	}
}
