package knowledge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func capabilityTestConn() domain.KnowledgeConnection {
	return domain.KnowledgeConnection{
		ID:       "conn-1",
		Name:     "测试连接",
		Provider: domain.KnowledgeProviderWeKnora,
		URL:      "http://weknora.test:8080",
		Token:    "test-key",
		Enabled:  true,
	}
}

func capabilityTestClient(t *testing.T, handler http.Handler) *WeKnoraClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewWeKnoraClientWithConfig(WeKnoraClientConfig{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

func assertCapabilitySet(t *testing.T, caps domain.ConnectionCapabilities, want domain.CapabilityState) {
	t.Helper()
	if strings.TrimSpace(caps.CheckedAt) == "" {
		t.Error("CheckedAt must be set")
	}
	for _, capability := range domain.KnowledgeCapabilities {
		status, ok := caps.Capabilities[capability]
		if !ok {
			t.Fatalf("capability %s missing from set", capability)
		}
		if status.Status != want {
			t.Errorf("capability %s status = %q (reason %q), want %q", capability, status.Status, status.Reason, want)
		}
		if want != domain.CapabilityAvailable && strings.TrimSpace(status.Reason) == "" {
			t.Errorf("capability %s must carry a reason when %q", capability, want)
		}
	}
}

func writeCapabilityError(w http.ResponseWriter, status int, body string) {
	if body != "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func TestCapabilityProbeAllAvailable(t *testing.T) {
	client := capabilityTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/chunks/"+weknoraSentinelID {
			writeCapabilityError(w, http.StatusOK, `{"success":true,"total":0,"data":[]}`)
			return
		}
		writeCapabilityError(w, http.StatusNotFound, `{"error":{"code":"not_found","message":"资源不存在"}}`)
	}))

	caps := probeConnectionCapabilitiesWithin(context.Background(), capabilityTestConn(), client, 2*time.Second)
	assertCapabilitySet(t, caps, domain.CapabilityAvailable)

	// The default entry point must use the production probe timeout.
	caps = probeConnectionCapabilities(context.Background(), capabilityTestConn(), client)
	assertCapabilitySet(t, caps, domain.CapabilityAvailable)
}

func TestCapabilityProbeScopeDenied(t *testing.T) {
	client := capabilityTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/chunks/") {
			writeCapabilityError(w, http.StatusUnauthorized, `{"error":"invalid api key"}`)
			return
		}
		writeCapabilityError(w, http.StatusForbidden, `{"error":"missing required scope"}`)
	}))

	caps := probeConnectionCapabilitiesWithin(context.Background(), capabilityTestConn(), client, 2*time.Second)
	assertCapabilitySet(t, caps, domain.CapabilityUnavailable)
}

func TestCapabilityProbeMissingRoutes(t *testing.T) {
	client := capabilityTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch:
			writeCapabilityError(w, http.StatusNotFound, "404 page not found")
		case r.Method == http.MethodPut:
			writeCapabilityError(w, http.StatusMethodNotAllowed, "method not allowed")
		case strings.HasSuffix(r.URL.Path, "/reparse"):
			writeCapabilityError(w, http.StatusNotImplemented, "")
		default:
			writeCapabilityError(w, http.StatusNotFound, "404 page not found")
		}
	}))

	caps := probeConnectionCapabilitiesWithin(context.Background(), capabilityTestConn(), client, 2*time.Second)
	assertCapabilitySet(t, caps, domain.CapabilityUnavailable)
}

func TestCapabilityProbeUnknownOnServerErrorAndTimeout(t *testing.T) {
	client := capabilityTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reparse"):
			time.Sleep(150 * time.Millisecond)
			writeCapabilityError(w, http.StatusNotFound, `{"error":{"code":"not_found","message":"资源不存在"}}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/chunks/"):
			writeCapabilityError(w, http.StatusInternalServerError, `{"error":{"code":"internal","message":"boom"}}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/knowledge/") && strings.HasSuffix(r.URL.Path, "/preview"):
			writeCapabilityError(w, http.StatusGatewayTimeout, `{"error":{"code":"timeout","message":"gateway timeout"}}`)
		default:
			writeCapabilityError(w, http.StatusBadGateway, `{"error":{"code":"bad_gateway","message":"down"}}`)
		}
	}))

	caps := probeConnectionCapabilitiesWithin(context.Background(), capabilityTestConn(), client, 30*time.Millisecond)
	assertCapabilitySet(t, caps, domain.CapabilityUnknown)
}

func TestCapabilityProbeProviderGate(t *testing.T) {
	lightcloud := capabilityTestConn()
	lightcloud.Provider = domain.KnowledgeProviderLightCloud
	caps := probeConnectionCapabilitiesWithin(context.Background(), lightcloud, nil, time.Second)
	assertCapabilitySet(t, caps, domain.CapabilityUnavailable)
	for _, capability := range domain.KnowledgeCapabilities {
		if got := caps.Capabilities[capability].Reason; got != capabilityUnsupportedProviderReason {
			t.Errorf("capability %s reason = %q, want %q", capability, got, capabilityUnsupportedProviderReason)
		}
	}

	// A weknora connection without a dialed client can only be unknown.
	caps = probeConnectionCapabilitiesWithin(context.Background(), capabilityTestConn(), nil, time.Second)
	assertCapabilitySet(t, caps, domain.CapabilityUnknown)
}

func TestCapabilityClassifyProbe(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want domain.CapabilityState
	}{
		{"success", nil, domain.CapabilityAvailable},
		{"bad request", &UpstreamError{Status: http.StatusBadRequest, Structured: true}, domain.CapabilityAvailable},
		{"structured not found", &UpstreamError{Status: http.StatusNotFound, Structured: true}, domain.CapabilityAvailable},
		{"unexpected status", &UpstreamError{Status: http.StatusFound}, domain.CapabilityUnknown},
		{"server error", &UpstreamError{Status: http.StatusBadGateway}, domain.CapabilityUnknown},
		{"network failure", errors.New("dial tcp: connection refused"), domain.CapabilityUnknown},
		{"timeout", context.DeadlineExceeded, domain.CapabilityUnknown},
	}
	for _, tc := range cases {
		if got := classifyCapabilityProbe(tc.err).Status; got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCapabilityUnavailableErrorContract(t *testing.T) {
	status := domain.CapabilityStatus{Status: domain.CapabilityUnavailable, Reason: "缺少 manage_kbs 权限"}
	err := capabilityUnavailableError("conn-1", domain.CapKBManage, status)
	appErr, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error kind = %T, want *apperr.Error", err)
	}
	if appErr.Kind != apperr.Unavailable {
		t.Errorf("kind = %v, want Unavailable", appErr.Kind)
	}
	want := map[string]any{
		"code":         "capability_unavailable",
		"capability":   "kb_manage",
		"connectionId": "conn-1",
		"status":       "unavailable",
		"reason":       "缺少 manage_kbs 权限",
	}
	for key, value := range want {
		if got := appErr.Fields[key]; got != value {
			t.Errorf("Fields[%q] = %v, want %v", key, got, value)
		}
	}
	if !strings.Contains(appErr.Message, "kb_manage") {
		t.Errorf("message %q should name the capability", appErr.Message)
	}
}

func TestCapabilityCacheTTLAndFingerprint(t *testing.T) {
	cache := newCapabilityCache()
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	var calls int
	probe := func(context.Context) domain.ConnectionCapabilities {
		calls++
		return capabilitiesWithReason(domain.CapabilityAvailable, "")
	}
	ctx := context.Background()
	conn := capabilityTestConn()

	if caps := cache.get(ctx, conn, probe); caps.Capabilities[domain.CapKBManage].Status != domain.CapabilityAvailable {
		t.Fatalf("first probe status = %q", caps.Capabilities[domain.CapKBManage].Status)
	}
	if calls != 1 {
		t.Fatalf("calls after first get = %d, want 1", calls)
	}
	cache.get(ctx, conn, probe)
	if calls != 1 {
		t.Fatalf("cached get re-probed: calls = %d", calls)
	}

	now = now.Add(4 * time.Minute)
	cache.get(ctx, conn, probe)
	if calls != 1 {
		t.Fatalf("entry evicted before TTL: calls = %d", calls)
	}
	now = now.Add(2 * time.Minute)
	cache.get(ctx, conn, probe)
	if calls != 2 {
		t.Fatalf("expired entry not re-probed: calls = %d", calls)
	}

	// Same URL with trailing slash or /api/v1 suffix is the same identity.
	normalized := conn
	normalized.URL = "http://weknora.test:8080/api/v1/"
	cache.get(ctx, normalized, probe)
	if calls != 2 {
		t.Fatalf("equivalent URL re-probed: calls = %d", calls)
	}

	mutations := []func(domain.KnowledgeConnection) domain.KnowledgeConnection{
		func(c domain.KnowledgeConnection) domain.KnowledgeConnection { c.Token = "rotated"; return c },
		func(c domain.KnowledgeConnection) domain.KnowledgeConnection { c.URL = "http://other.test:8080"; return c },
		func(c domain.KnowledgeConnection) domain.KnowledgeConnection { c.Provider = domain.KnowledgeProviderLightCloud; return c },
		func(c domain.KnowledgeConnection) domain.KnowledgeConnection { c.Enabled = false; return c },
	}
	for i, mutate := range mutations {
		before := calls
		cache.get(ctx, mutate(conn), probe)
		if calls != before+1 {
			t.Fatalf("mutation %d did not invalidate cache: calls = %d, want %d", i, calls, before+1)
		}
	}

	cache.invalidate("conn-1")
	before := calls
	cache.get(ctx, conn, probe)
	if calls != before+1 {
		t.Fatalf("invalidate did not drop entry: calls = %d", calls)
	}

	// A connection without an id shares the "default" cache slot.
	legacy := conn
	legacy.ID = ""
	cache.get(ctx, legacy, probe)
	cache.get(ctx, legacy, probe)
	if calls != before+2 {
		t.Fatalf("legacy connection caching = %d calls, want %d", calls, before+2)
	}
	cache.invalidate("")
	cache.get(ctx, legacy, probe)
	if calls != before+3 {
		t.Fatalf("invalidate(\"\") did not drop default entry: calls = %d", calls)
	}
}

func TestCapabilityCacheSingleflight(t *testing.T) {
	cache := newCapabilityCache()
	conn := capabilityTestConn()
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	probe := func(context.Context) domain.ConnectionCapabilities {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return capabilitiesWithReason(domain.CapabilityAvailable, "")
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]domain.ConnectionCapabilities, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = cache.get(context.Background(), conn, probe)
		}(i)
	}
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waited := cache.get(ctx, conn, probe)
	if got := waited.Capabilities[domain.CapKBManage].Status; got != domain.CapabilityUnknown {
		t.Fatalf("canceled waiter status = %q, want unknown", got)
	}

	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("probe ran %d times, want 1", got)
	}
	for i, caps := range results {
		if len(caps.Capabilities) != len(domain.KnowledgeCapabilities) {
			t.Fatalf("worker %d got %d capabilities, want %d", i, len(caps.Capabilities), len(domain.KnowledgeCapabilities))
		}
	}
}

func TestCapabilityCacheDemoteDenied(t *testing.T) {
	cache := newCapabilityCache()
	ctx := context.Background()
	conn := capabilityTestConn()
	calls := 0
	probe := func(context.Context) domain.ConnectionCapabilities {
		calls++
		return capabilitiesWithReason(domain.CapabilityAvailable, "")
	}
	cache.get(ctx, conn, probe)
	if calls != 1 {
		t.Fatalf("initial probe calls = %d, want 1", calls)
	}

	status, ok := cache.demoteDenied(conn, domain.CapReparse, &UpstreamError{Status: http.StatusForbidden, Structured: true, Message: "forbidden"})
	if !ok || status.Status != domain.CapabilityUnavailable || strings.TrimSpace(status.Reason) == "" {
		t.Fatalf("demote = (%+v, %v), want unavailable with reason", status, ok)
	}
	noProbe := func(context.Context) domain.ConnectionCapabilities {
		t.Fatal("cache hit after demote must not re-probe")
		return domain.ConnectionCapabilities{}
	}
	caps := cache.get(ctx, conn, noProbe)
	if got := caps.Capabilities[domain.CapReparse].Status; got != domain.CapabilityUnavailable {
		t.Errorf("reparse status = %q, want unavailable", got)
	}
	if got := caps.Capabilities[domain.CapKBManage].Status; got != domain.CapabilityAvailable {
		t.Errorf("kb_manage status = %q, want available", got)
	}

	if _, ok := cache.demoteDenied(conn, domain.CapKBManage, &UpstreamError{Status: http.StatusNotFound, Structured: true}); ok {
		t.Error("structured 404 must not demote a capability")
	}
	if _, ok := cache.demoteDenied(conn, domain.CapKBManage, &UpstreamError{Status: http.StatusBadGateway}); ok {
		t.Error("5xx must not demote a capability")
	}
	if _, ok := cache.demoteDenied(conn, domain.CapKBManage, errors.New("plain error")); ok {
		t.Error("non-upstream error must not demote a capability")
	}

	uncached := conn
	uncached.ID = "conn-missing"
	if _, ok := cache.demoteDenied(uncached, domain.CapDocWrite, &UpstreamError{Status: http.StatusForbidden}); !ok {
		t.Error("denial must be reported even without a cached entry")
	}
}

func TestCapabilitySentinelProbePaths(t *testing.T) {
	probes := weKnoraCapabilityProbes()
	if len(probes) != len(domain.KnowledgeCapabilities) {
		t.Fatalf("probe count = %d, want %d", len(probes), len(domain.KnowledgeCapabilities))
	}
	seen := map[domain.KnowledgeCapability]bool{}
	for _, probe := range probes {
		seen[probe.capability] = true
		if !strings.Contains(probe.path, weknoraSentinelID) {
			t.Errorf("probe %s path %q does not target the sentinel id", probe.capability, probe.path)
		}
	}
	for _, capability := range domain.KnowledgeCapabilities {
		if !seen[capability] {
			t.Errorf("capability %s has no probe", capability)
		}
	}
}
