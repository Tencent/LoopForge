package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// 桥接页在控制台源（:18081）下发起跨源兑换，因此这条路径的预检、匿名可达性
// 与“仅收窄到 POST”的 CORS 头都属于对外契约，需要 HTTP 层兜住。
func TestConsoleSSORedeemPreflightAndPublicAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")

	const consoleOrigin = "http://localhost:18081"
	do := func(method, path, body, origin string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, "http://localhost"+path, nil)
		} else {
			r = httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
			r.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 预检必须放行，否则浏览器连 POST 都不会发。
	w := do(http.MethodOptions, "/api/knowledge/weknora/console-sso/redeem", "", consoleOrigin)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != consoleOrigin {
		t.Fatalf("allow-origin = %q, want %q", got, consoleOrigin)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") || strings.Contains(got, "DELETE") {
		t.Fatalf("allow-methods must be narrowed to POST, got %q", got)
	}

	// 兑换接口本身是公开路径（票据即凭据）：伪造票据得到 403，而不是被登录态拦成 401。
	w = do(http.MethodPost, "/api/knowledge/weknora/console-sso/redeem", `{"ticket":"bogus"}`, consoleOrigin)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bogus ticket: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "票据") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}

	// 跨源放行只属于这条路径，其它知识库接口的跨源请求仍然拒绝。
	w = do(http.MethodGet, "/api/knowledge/settings", "", consoleOrigin)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "cross-origin") {
		t.Fatalf("cross-origin must stay denied: %d %s", w.Code, w.Body.String())
	}

	// 签发接口仍然要求平台管理员会话，且不接受控制台源的跨源调用。
	w = do(http.MethodPost, "/api/knowledge/weknora/console-sso", `{"connectionId":"","consoleOrigin":"http://localhost:8091"}`, consoleOrigin)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("issue without a session must be rejected: %d %s", w.Code, w.Body.String())
	}
}
