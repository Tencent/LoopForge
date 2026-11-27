package knowledge

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSSRFIPBoundariesAndLocalHostDetection(t *testing.T) {
	alwaysBlocked := []string{
		"169.254.169.254",
		"fe80::1",
		"100.64.0.1",
		"100.100.100.200",
		"198.18.0.1",
		"64:ff9b::1",
		"0.0.0.0",
		"224.0.0.1",
		"ff02::1",
	}
	if !isAlwaysBlockedIP(nil) {
		t.Fatal("nil IP must be blocked")
	}
	for _, raw := range alwaysBlocked {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("failed to parse %q", raw)
		}
		if !isAlwaysBlockedIP(ip) || !isBlockedConnectorIP(ip, true) || !isBlockedConnectorIP(ip, false) {
			t.Fatalf("expected IP %s to always be blocked", raw)
		}
	}

	privateIPs := []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "fc00::1"}
	for _, raw := range privateIPs {
		ip := net.ParseIP(raw)
		if isBlockedConnectorIP(ip, true) {
			t.Fatalf("private IP %s should be allowed when allowLocalOrPrivate=true", raw)
		}
		if !isBlockedConnectorIP(ip, false) {
			t.Fatalf("private IP %s should be blocked when allowLocalOrPrivate=false", raw)
		}
	}

	// Loopback policy: allowed with allowLocalOrPrivate=true; when false, governed by insecure flag.
	for _, raw := range []string{"127.0.0.1", "::1"} {
		ip := net.ParseIP(raw)
		if isBlockedConnectorIP(ip, true) {
			t.Fatalf("loopback %s should be allowed when allowLocalOrPrivate=true", raw)
		}
		t.Setenv("LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE", "")
		if !isBlockedConnectorIP(ip, false) {
			t.Fatalf("loopback %s should be blocked when insecure opt-in is off", raw)
		}
		t.Setenv("LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE", "1")
		if isBlockedConnectorIP(ip, false) {
			t.Fatalf("loopback %s should be allowed when insecure opt-in is 1", raw)
		}
	}

	// Public IPs are always allowed.
	for _, raw := range []string{"8.8.8.8", "93.184.216.34", "2001:4860:4860::8888"} {
		ip := net.ParseIP(raw)
		if isBlockedConnectorIP(ip, false) || isBlockedConnectorIP(ip, true) {
			t.Fatalf("public IP %s should not be blocked", raw)
		}
	}

	if err := validateConnectorEndpointIP("://bad", true, "MCP"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for unparseable URL, got %v", err)
	}
	if err := validateConnectorEndpointIP("http://169.254.169.254/latest", true, "MCP"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for metadata URL, got %v", err)
	}
	if err := validateConnectorEndpointIP("https://93.184.216.34/mcp", false, "MCP"); err != nil {
		t.Fatalf("expected public IP allowed, got %v", err)
	}

	localHostCases := []struct {
		host string
		want bool
	}{
		{"", false},
		{"localhost:8080", true},
		{"host.docker.internal", true},
		{"[::1]:27124", true},
		{"192.168.1.5", true},
		{"169.254.169.254", false},
		{"example.com", false},
	}
	for _, tc := range localHostCases {
		if got := isLocalOrPrivateHost(tc.host); got != tc.want {
			t.Fatalf("isLocalOrPrivateHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestSSRFGuardedDialContextAndNativeHTTPClient(t *testing.T) {
	ctx := context.Background()
	dial := newSSRFGuardedDialContext(0, false, "测试")
	if _, err := dial(ctx, "tcp", "no-port"); err == nil || !strings.Contains(err.Error(), "地址无效") {
		t.Fatalf("expected invalid addr error, got %v", err)
	}
	if _, err := dial(ctx, "tcp", "169.254.169.254:80"); err == nil || !strings.Contains(err.Error(), "不允许访问") {
		t.Fatalf("expected metadata IP rejection, got %v", err)
	}
	t.Setenv("LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE", "")
	if _, err := dial(ctx, "tcp", "127.0.0.1:80"); err == nil || !strings.Contains(err.Error(), "不允许访问") {
		t.Fatalf("expected loopback rejection when allowLocalOrPrivate=false, got %v", err)
	}

	if _, err := newNativeHTTPClientWithPolicy(0, "://bad", false, true); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for malformed endpoint, got %v", err)
	}
	if _, err := newNativeHTTPClientWithPolicy(0, "http://169.254.169.254", false, true); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for metadata endpoint, got %v", err)
	}
	if _, err := newNativeHTTPClientWithPolicy(0, "https://example.com", true, true); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for public host with insecure TLS, got %v", err)
	}

	client, err := newNativeHTTPClientWithPolicy(0, "https://127.0.0.1:27124", true, true)
	if err != nil {
		t.Fatalf("expected local insecure TLS client, got %v", err)
	}
	tr := client.Transport.(*http.Transport)
	if client.Timeout != nativeDefaultTimeout || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected client TLS/timeout configuration: timeout=%v tls=%+v", client.Timeout, tr.TLSClientConfig)
	}

	// Same-origin redirects succeed up to nativeMaxRedirects and fail beyond it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok-1":
			http.Redirect(w, r, "/ok-2", http.StatusFound)
		case "/ok-2":
			w.WriteHeader(http.StatusOK)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()

	plainClient, err := newNativeHTTPClient(5*time.Second, srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := plainClient.Get(srv.URL + "/ok-1")
	if err != nil {
		t.Fatalf("same-origin redirect should succeed: %v", err)
	}
	resp.Body.Close()

	loopResp, err := plainClient.Get(srv.URL + "/loop")
	if loopResp != nil {
		loopResp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "重定向次数超过") {
		t.Fatalf("expected max redirect error, got %v", err)
	}
}

func TestDoRequestWithDockerFallback(t *testing.T) {
	origHook := dockerEnvCheckHook
	t.Cleanup(func() { dockerEnvCheckHook = origHook })

	if isConnectionRefusedErr(nil) || isConnectionRefusedErr(errors.New("timeout")) {
		t.Fatal("unexpected true from isConnectionRefusedErr")
	}
	if !isConnectionRefusedErr(syscall.ECONNREFUSED) || !isConnectionRefusedErr(errors.New("dial tcp: Connection Refused")) {
		t.Fatal("expected true from isConnectionRefusedErr")
	}

	var seenHosts []string
	var seenBody string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seenHosts = append(seenHosts, r.URL.Host)
			if r.URL.Hostname() == "127.0.0.1" {
				return nil, syscall.ECONNREFUSED
			}
			if r.Body != nil {
				b, _ := io.ReadAll(r.Body)
				seenBody = string(b)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	// Non-docker env does not retry host.docker.internal.
	dockerEnvCheckHook = func() bool { return false }
	req1, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:6806/ping", nil)
	if _, err := doRequestWithDockerFallback(client, req1); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("expected original connrefused when not in docker, got %v", err)
	}

	// Docker env + loopback POST with GetBody retries host.docker.internal:6806 with body preserved.
	dockerEnvCheckHook = func() bool { return true }
	seenHosts = nil
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:6806/api", strings.NewReader(`{"hello":1}`))
	resp, err := doRequestWithDockerFallback(client, req2)
	if err != nil {
		t.Fatalf("expected docker fallback retry to succeed, got %v", err)
	}
	resp.Body.Close()
	if len(seenHosts) != 2 || seenHosts[0] != "127.0.0.1:6806" || seenHosts[1] != "host.docker.internal:6806" || seenBody != `{"hello":1}` {
		t.Fatalf("unexpected docker fallback behavior: hosts=%v body=%q", seenHosts, seenBody)
	}

	// Loopback without explicit port retries host.docker.internal without colon.
	seenHosts = nil
	req3, _ := http.NewRequest(http.MethodGet, "http://localhost/ping", nil)
	// Make localhost fail with connrefused too via custom transport.
	clientLocal := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seenHosts = append(seenHosts, r.URL.Host)
			if r.URL.Hostname() == "localhost" {
				return nil, syscall.ECONNREFUSED
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
		}),
	}
	resp3, err := doRequestWithDockerFallback(clientLocal, req3)
	if err != nil {
		t.Fatalf("expected localhost fallback retry to succeed, got %v", err)
	}
	resp3.Body.Close()
	if len(seenHosts) != 2 || seenHosts[1] != "host.docker.internal" {
		t.Fatalf("expected host.docker.internal without port, got %v", seenHosts)
	}
}

func TestCommonNativeHelpersChunkingAndSnippets(t *testing.T) {
	if _, err := validateNativeBaseURL("", "", "Test"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty URL without default, got %v", err)
	}
	if got, err := validateNativeBaseURL("https://kb.example.com/v1/#frag", "", "Test"); err != nil || got != "https://kb.example.com/v1" {
		t.Fatalf("unexpected validateNativeBaseURL: %q err=%v", got, err)
	}

	if got := stripTokenPrefix("Token abc"); got != "abc" {
		t.Fatalf("expected stripped Token prefix, got %q", got)
	}
	if !parseBoolExtra(map[string]string{"flag": "YES"}, "flag", false) || parseBoolExtra(map[string]string{"flag": "0"}, "flag", true) || !parseBoolExtra(nil, "flag", true) {
		t.Fatal("unexpected parseBoolExtra behavior")
	}
	if normalizeExtraMap(map[string]string{" ": "v", "k": " "}) != nil {
		t.Fatal("expected nil for empty normalized extra map")
	}

	ids := extractSearchKBIDs(map[string]any{"sources": []string{"s1"}})
	if len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("unexpected extractSearchKBIDs: %v", ids)
	}
	if got := extractReadDocID(map[string]any{"doc_id": "d-9"}); got != "d-9" {
		t.Fatalf("unexpected extractReadDocID: %q", got)
	}
	if got := extractReadDocID(nil); got != "" {
		t.Fatalf("expected empty docID, got %q", got)
	}

	// splitDocumentChunks: empty, paragraph merge/flush, huge paragraph line split, huge line rune split.
	if splitDocumentChunks("  ", 10) != nil {
		t.Fatal("expected nil chunks for whitespace text")
	}
	hugeText := "Para1\r\n\r\nPara2\n\nLineA\nLineB\nVeryLongWord1234567890"
	chunks := splitDocumentChunks(hugeText, 10)
	if len(chunks) < 4 {
		t.Fatalf("expected >=4 chunks from splitting, got %v", chunks)
	}

	// buildReadDocumentResult: negative offset, limit>50, multi-word query filter, ExtraFields.
	res, err := buildReadDocumentResult(nativeReadDocParams{
		ConnectionID: "c-1",
		SourceID:     "src-1",
		SourceName:   "Docs",
		DocumentID:   "doc-1",
		FileName:     "guide.md",
		Content:      "Alpha beta\n\nGamma delta",
		Offset:       -3,
		Limit:        100,
		Query:        "alpha beta",
		ExtraFields:  map[string]any{"url": "https://example.com/guide"},
		Started:      time.Now(),
	})
	if err != nil || res.Hits != 1 || !strings.Contains(res.Text, "https://example.com/guide") {
		t.Fatalf("unexpected buildReadDocumentResult: %+v err=%v", res, err)
	}

	paged, total, page, ps := paginateSlice([]int{1, 2, 3}, 5, 0)
	if total != 3 || page != 5 || ps != 20 || len(paged) != 0 {
		t.Fatalf("unexpected out-of-range paginateSlice: paged=%v total=%d page=%d ps=%d", paged, total, page, ps)
	}

	if extractKeywordSnippet("", "q", 10) != "" {
		t.Fatal("expected empty snippet for empty content")
	}
	long := strings.Repeat("a", 40) + "TargetKeyword" + strings.Repeat("b", 40)
	snip := extractKeywordSnippet(long, "targetkeyword", 20)
	if !strings.HasPrefix(snip, "…") || !strings.HasSuffix(snip, "…") || !strings.Contains(snip, "Target") {
		t.Fatalf("expected middle snippet with ellipses, got %q", snip)
	}
	if noMatch := extractKeywordSnippet("short text", "missing", 5); noMatch != "short…" {
		t.Fatalf("expected clipped fallback snippet, got %q", noMatch)
	}
}

func TestUsageRuleMCPServerDefAndRegistryScopeHelpers(t *testing.T) {
	rule := UsageRule()
	if rule.ID != domain.KnowledgeUsageRuleID || rule.Priority != 50 || !strings.Contains(rule.Content, "[[knowledge:<id>]]") {
		t.Fatalf("unexpected UsageRule: %+v", rule)
	}

	defDefault := MCPServerDefinition("https://host.example.com/", "", "", "", "tok-1")
	if defDefault.ID != domain.KnowledgeMCPServerID || defDefault.URL != "https://host.example.com"+ProxyPath || defDefault.Headers["Authorization"] != "Bearer tok-1" {
		t.Fatalf("unexpected default MCPServerDefinition: %+v", defDefault)
	}
	defCustom := MCPServerDefinition("https://host.example.com", "srv-9", "Custom", "Desc", "tok-2")
	if defCustom.ID != "srv-9" || defCustom.Name != "Custom" || defCustom.Description != "Desc" {
		t.Fatalf("unexpected custom MCPServerDefinition: %+v", defCustom)
	}

	reg := NewRegistry()
	reg.Register(nil)
	if len(reg.Types()) != 0 {
		t.Fatal("nil factory should be ignored")
	}
	if _, ok := reg.Factory("missing"); ok {
		t.Fatal("missing factory should return false")
	}

	emptyAllow := SourceScope{Mode: "allowlist"}
	if _, err := emptyAllow.FilterIDs(nil); err == nil {
		t.Fatal("expected error for empty allowlist")
	}
	if got, err := (SourceScope{Mode: "allowlist", Sources: []string{"s1", "s2"}}).FilterIDs(nil); err != nil || len(got) != 2 {
		t.Fatalf("expected allowlist expansion, got %v err=%v", got, err)
	}
	if _, err := (SourceScope{Mode: "allowlist", Sources: []string{"s1"}}).FilterIDs([]string{"s2"}); err == nil {
		t.Fatal("expected out-of-scope FilterIDs error")
	}

	if clipText("  hello world  ", 5) != "hello…" || truncateRunes("abcdef", 3) != "abc…" {
		t.Fatal("unexpected text truncation helper output")
	}
	if normalizeHeaderMap(map[string]string{" ": "v"}) != nil {
		t.Fatal("expected nil header map when all keys blank")
	}
}
