package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

const managementSentinel = "00000000-0000-0000-0000-000000000000"

// weKnoraMock is a minimal WeKnora REST instance: it answers the management
// endpoints the HTTP layer proxies and the sentinel capability probes.
type weKnoraMock struct {
	mu    sync.Mutex
	deny  bool
	flaky bool
	seen  []string
}

func (m *weKnoraMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.seen = append(m.seen, r.Method+" "+r.URL.Path)
	m.mu.Unlock()
	if m.deny {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"success":false,"error":"forbidden"}`)
		return
	}
	path := r.URL.Path
	if strings.Contains(path, managementSentinel) {
		if m.flaky {
			// A bare 404 makes the capability probe conclude "unavailable"
			// while every real read endpoint keeps working.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// A structured 400 proves the route exists and the key passed the gate.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"success":false,"error":"invalid payload"}`)
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "/api/v1/knowledge-bases":
		writeEnvelope(w, []map[string]any{
			{"id": "kb-1", "name": "团队库", "description": "共享", "knowledge_count": 2},
			{"id": "kb-2", "name": "私有库", "knowledge_count": 0},
		})
	case r.Method == http.MethodPost && path == "/api/v1/knowledge-bases":
		writeEnvelope(w, map[string]any{"id": "kb-new", "name": "新建库"})
	case strings.HasPrefix(path, "/api/v1/knowledge-bases/"):
		rest := strings.TrimPrefix(path, "/api/v1/knowledge-bases/")
		parts := strings.Split(rest, "/")
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			writeEnvelope(w, map[string]any{"id": parts[0], "name": "团队库", "description": "共享"})
		case len(parts) == 1 && r.Method == http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Upstream PATCH keeps omitted fields; emulate that so a monitor
			// assertion can catch a handler that zeroes the description.
			name, description := "团队库", "共享"
			if v, ok := body["name"]; ok {
				name = fmt.Sprint(v)
			}
			if v, ok := body["description"]; ok {
				description = fmt.Sprint(v)
			}
			writeEnvelope(w, map[string]any{"id": parts[0], "name": name, "description": description})
		case len(parts) == 1 && r.Method == http.MethodDelete:
			writeEnvelope(w, map[string]any{"id": parts[0]})
		case len(parts) == 2 && parts[1] == "knowledge" && r.Method == http.MethodGet:
			writeEnvelope(w, []map[string]any{
				{"id": "doc-1", "knowledge_base_id": parts[0], "title": "手册", "parse_status": "completed"},
			})
		case len(parts) == 3 && parts[1] == "knowledge" && parts[2] == "file" && r.Method == http.MethodPost:
			writeEnvelope(w, map[string]any{"id": "doc-up", "knowledge_base_id": parts[0], "title": "上传文档", "parse_status": "pending"})
		case len(parts) == 2 && parts[1] == "hybrid-search" && r.Method == http.MethodPost:
			writeEnvelope(w, []map[string]any{{
				"id": "chunk-1", "content": "命中内容", "knowledge_id": "doc-1",
				"knowledge_title": "手册", "score": 0.9, "chunk_index": 0,
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"error":"not found"}`)
		}
	case strings.HasPrefix(path, "/api/v1/knowledge/manual/"):
		writeEnvelope(w, map[string]any{"id": strings.TrimPrefix(path, "/api/v1/knowledge/manual/"), "knowledge_base_id": "kb-1", "title": "手工文档"})
	case strings.HasPrefix(path, "/api/v1/knowledge/"):
		rest := strings.TrimPrefix(path, "/api/v1/knowledge/")
		switch {
		case strings.HasSuffix(rest, "/preview"):
			body := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case strings.HasSuffix(rest, "/download"):
			body := []byte(`<html><body><script>alert(1)</script></body></html>`)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case strings.HasSuffix(rest, "/reparse"):
			writeEnvelope(w, map[string]any{"id": strings.TrimSuffix(rest, "/reparse")})
		case r.Method == http.MethodGet:
			docID := rest
			kbID := "kb-1"
			if docID == "doc-2" {
				kbID = "kb-2"
			}
			writeEnvelope(w, map[string]any{"id": docID, "knowledge_base_id": kbID, "title": "手册", "parse_status": "completed"})
		case r.Method == http.MethodDelete:
			writeEnvelope(w, map[string]any{"id": rest})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"error":"not found"}`)
		}
	case strings.HasPrefix(path, "/api/v1/chunks/"):
		writeEnvelopeTotal(w, []map[string]any{{"id": "chunk-1", "content": "第一段", "chunk_index": 0, "start_at": 0, "end_at": 3}}, 1)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"success":false,"error":"not found"}`)
	}
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func writeEnvelopeTotal(w http.ResponseWriter, data any, total int64) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data, "total": total})
}

func newKnowledgeManagementServer(t *testing.T) (*repo.Store, http.Handler, domain.KnowledgeSettings, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	good := &weKnoraMock{}
	goodServer := httptest.NewServer(good)
	t.Cleanup(goodServer.Close)
	bad := &weKnoraMock{deny: true}
	badServer := httptest.NewServer(bad)
	t.Cleanup(badServer.Close)
	flaky := &weKnoraMock{flaky: true}
	flakyServer := httptest.NewServer(flaky)
	t.Cleanup(flakyServer.Close)

	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	settings := domain.KnowledgeSettings{
		Enabled: true, Token: "proxy-signing-secret", WriteEnabled: true,
		Connections: []domain.KnowledgeConnection{
			{ID: "wk-1", Name: "主库", Provider: domain.KnowledgeProviderWeKnora, URL: goodServer.URL, Token: "wk-secret-token", Enabled: true},
			{ID: "wk-bad", Name: "无权限库", Provider: domain.KnowledgeProviderWeKnora, URL: badServer.URL, Token: "bad-secret-token", Enabled: true},
			{ID: "wk-flaky", Name: "探针异常库", Provider: domain.KnowledgeProviderWeKnora, URL: flakyServer.URL, Token: "flaky-secret-token", Enabled: true},
		},
		ProjectSources: map[string][]string{"p1": {"wk-1::kb-1", "wk-flaky::kb-1"}},
	}
	if err := s.PutKnowledgeSettings(settings); err != nil {
		t.Fatal(err)
	}
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return s, handler, settings, logs
}

func multipartUpload(t *testing.T, h http.Handler, path, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, content)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertCapabilitiesAvailable(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var env struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode capabilities: %v body=%s", err, rec.Body.String())
	}
	statuses := capabilityStatuses(env.Capabilities)
	if nested, ok := env.Capabilities["capabilities"].(map[string]any); ok {
		statuses = capabilityStatuses(nested)
	}
	for _, name := range []string{"kb_manage", "doc_write", "chunk_read", "preview_download", "reparse"} {
		if got := statuses[name]; got != "available" {
			t.Fatalf("capability %s = %q (body=%s)", name, got, rec.Body.String())
		}
	}
}

func capabilityStatuses(raw map[string]any) map[string]string {
	out := map[string]string{}
	for name, value := range raw {
		if item, ok := value.(map[string]any); ok {
			status, _ := item["status"].(string)
			out[name] = status
		}
	}
	return out
}

func TestKnowledgeManagement(t *testing.T) {
	s, h, settings, logs := newKnowledgeManagementServer(t)

	t.Run("explicit connection is required", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodPost, "/api/knowledge/sources?projectId=p1", `{"name":"新库"}`, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "请指定知识库连接") {
			t.Fatalf("missing connectionId = %d %s", rec.Code, rec.Body.String())
		}
		rec = doJSON(t, h, http.MethodPost, "/api/knowledge/sources?projectId=p1", `{"connectionId":"wk-1","name":"新库"}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"wk-1::kb-new"`) {
			t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("check returns capabilities without credentials", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodPost, "/api/knowledge/check", `{"connectionId":"wk-1"}`, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("check = %d %s", rec.Code, body)
		}
		assertCapabilitiesAvailable(t, rec)
		if strings.Contains(body, "wk-secret-token") || strings.Contains(body, settings.Connections[0].Token) {
			t.Fatalf("check leaked the token: %s", body)
		}
		rec = doJSON(t, h, http.MethodGet, "/api/knowledge/connections/wk-1/capabilities", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("capabilities = %d %s", rec.Code, rec.Body.String())
		}
		assertCapabilitiesAvailable(t, rec)
	})

	t.Run("missing capability becomes 503", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodPost, "/api/knowledge/sources?projectId=p1", `{"connectionId":"wk-bad","name":"禁写库"}`, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("denied capability = %d %s", rec.Code, body)
		}
		if !strings.Contains(body, `"code":"capability_unavailable"`) || !strings.Contains(body, `"capability":"kb_manage"`) {
			t.Fatalf("missing capability payload: %s", body)
		}
	})

	t.Run("source detail and patch", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodGet, "/api/knowledge/sources/wk-1::kb-1", "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"wk-1::kb-1"`) {
			t.Fatalf("source = %d %s", rec.Code, rec.Body.String())
		}
		rec = doJSON(t, h, http.MethodPatch, "/api/knowledge/sources/wk-1::kb-1", `{"name":"改名库"}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "改名库") {
			t.Fatalf("patch = %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"description":"共享"`) {
			t.Fatalf("partial patch cleared the description: %s", rec.Body.String())
		}
		rec = doJSON(t, h, http.MethodPatch, "/api/knowledge/sources/wk-1::kb-1", `{"description":"仅描述"}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"description":"仅描述"`) || !strings.Contains(rec.Body.String(), `"name":"团队库"`) {
			t.Fatalf("description-only patch = %d %s", rec.Code, rec.Body.String())
		}
		rec = doJSON(t, h, http.MethodPatch, "/api/knowledge/sources/wk-1::kb-1", `{"description":""}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"description":""`) {
			t.Fatalf("explicit clear = %d %s", rec.Code, rec.Body.String())
		}
		if rec := doJSON(t, h, http.MethodPatch, "/api/knowledge/sources/wk-1::kb-1", `{}`, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("empty patch must be rejected: %d %s", rec.Code, rec.Body.String())
		}
		rec = doJSON(t, h, http.MethodPut, "/api/knowledge/sources/wk-1::kb-1", `{"name":"全量库"}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"全量库"`) || !strings.Contains(rec.Body.String(), `"description":""`) {
			t.Fatalf("put replaces both fields = %d %s", rec.Code, rec.Body.String())
		}
		if rec := doJSON(t, h, http.MethodGet, "/api/knowledge/sources?connectionId=default", "", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("legacy default must not be accepted alongside explicit connections: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("document detail and chunks", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodGet, "/api/knowledge/documents/wk-1::doc-1?projectId=p1", "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"knowledgeBaseId":"wk-1::kb-1"`) {
			t.Fatalf("document = %d %s", rec.Code, rec.Body.String())
		}
		rec = doJSON(t, h, http.MethodGet, "/api/knowledge/documents/wk-1::doc-1/chunks?projectId=p1&page=1", "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"第一段"`) {
			t.Fatalf("chunks = %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("preview and download sanitize headers", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodGet, "/api/knowledge/documents/wk-1::doc-1/preview?projectId=p1", "", map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != http.StatusOK {
			t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Fatalf("preview content-type = %q", got)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("preview security headers = %v", rec.Header())
		}
		if rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("preview must not be gzipped: %v", rec.Header())
		}
		if got := rec.Header().Get("Content-Length"); got == "" || got != fmt.Sprint(rec.Body.Len()) {
			t.Fatalf("preview content-length = %q body=%d", got, rec.Body.Len())
		}

		rec = doJSON(t, h, http.MethodGet, "/api/knowledge/documents/wk-1::doc-1/download?projectId=p1", "", map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("download = %d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Fatalf("download disposition = %q", rec.Header().Get("Content-Disposition"))
		}
	})

	t.Run("out of scope document is forbidden", func(t *testing.T) {
		rec := doJSON(t, h, http.MethodGet, "/api/knowledge/documents/wk-1::doc-2?projectId=p1", "", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("scope = %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("writes are audited", func(t *testing.T) {
		if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/sources?projectId=p1", `{"connectionId":"wk-1","name":"审计库"}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("create for audit = %d %s", rec.Code, rec.Body.String())
		}
		rec := multipartUpload(t, h, "/api/knowledge/sources/wk-1::kb-1/upload?projectId=p1", "manual.md", "# 文档")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"wk-1::doc-up"`) {
			t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
		}
		if rec := doJSON(t, h, http.MethodDelete, "/api/knowledge/documents/wk-1::doc-1?projectId=p1", "", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
		}
		if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/documents/wk-1::doc-1/reparse?projectId=p1", "", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("reparse = %d %s", rec.Code, rec.Body.String())
		}
		if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/sources/wk-flaky::kb-1/url?projectId=p1", `{"url":"https://example.com/doc"}`, nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("failed url import = %d %s", rec.Code, rec.Body.String())
		}
		if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/sources?projectId=p1", `{"connectionId":"wk-flaky","name":"失败库"}`, nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("failed create = %d %s", rec.Code, rec.Body.String())
		}
		var usage struct {
			Activity []knowledgeActivity `json:"activity"`
		}
		if err := json.Unmarshal(doJSON(t, h, http.MethodGet, "/api/knowledge/usage?projectId=p1", "", nil).Body.Bytes(), &usage); err != nil {
			t.Fatalf("decode usage: %v", err)
		}
		byTool := map[string][]knowledgeActivity{}
		for _, item := range usage.Activity {
			byTool[item.Tool] = append(byTool[item.Tool], item)
		}
		for _, tool := range []string{"admin:kb.create", "admin:doc.upload", "admin:doc.delete", "admin:doc.reparse", "admin:doc.url"} {
			if len(byTool[tool]) == 0 {
				t.Fatalf("usage missing %s: %+v", tool, usage.Activity)
			}
		}
		requireAudit := func(tool string, ok bool, target string) {
			t.Helper()
			for _, item := range byTool[tool] {
				if item.OK == ok && item.Query == target {
					if !ok && item.Error == "" {
						t.Fatalf("%s failure has no error: %+v", tool, item)
					}
					return
				}
			}
			t.Fatalf("%s(ok=%v,target=%q) missing: %+v", tool, ok, target, byTool[tool])
		}
		requireAudit("admin:kb.create", true, "wk-1::kb-new")
		requireAudit("admin:kb.create", false, "失败库")
		requireAudit("admin:doc.url", false, "wk-flaky::kb-1")
		requireAudit("admin:doc.upload", true, "wk-1::doc-up")
		if !strings.Contains(logs.String(), "knowledge.management.audit") {
			t.Fatalf("structured audit log missing: %s", logs.String())
		}
		if !strings.Contains(logs.String(), `"action":"kb.create"`) {
			t.Fatalf("structured audit action missing: %s", logs.String())
		}
	})

	t.Run("upload enforces the file limit", func(t *testing.T) {
		if rec := multipartUpload(t, h, "/api/knowledge/sources/wk-1::kb-1/upload?projectId=p1", "limit.md", strings.Repeat("a", maxKnowledgeUpload)); rec.Code != http.StatusOK {
			t.Fatalf("exact limit rejected: %d %s", rec.Code, rec.Body.String())
		}
		longName := strings.Repeat("n", 200) + ".md"
		if rec := multipartUpload(t, h, "/api/knowledge/sources/wk-1::kb-1/upload?projectId=p1", longName, strings.Repeat("a", maxKnowledgeUpload)); rec.Code != http.StatusOK {
			t.Fatalf("long filename rejected: %d %s", rec.Code, rec.Body.String())
		}
		if rec := multipartUpload(t, h, "/api/knowledge/sources/wk-1::kb-1/upload?projectId=p1", "over.md", strings.Repeat("a", maxKnowledgeUpload+1)); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("over limit = %d %s", rec.Code, rec.Body.String())
		}
		// A body past the envelope trips MaxBytesReader while parsing, a
		// different branch from the explicit file size check above.
		if rec := multipartUpload(t, h, "/api/knowledge/sources/wk-1::kb-1/upload?projectId=p1", "huge.md", strings.Repeat("a", maxKnowledgeUpload+2<<20)); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("envelope over limit = %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("read only MCP path survives probe failures", func(t *testing.T) {
		// wk-flaky answers every real read but returns a bare 404 to each
		// sentinel probe, so its management capabilities are unavailable.
		caps := doJSON(t, h, http.MethodGet, "/api/knowledge/connections/wk-flaky/capabilities", "", nil)
		if caps.Code != http.StatusOK || !strings.Contains(caps.Body.String(), `"status":"unavailable"`) {
			t.Fatalf("flaky capabilities = %d %s", caps.Code, caps.Body.String())
		}
		if rec := doJSON(t, h, http.MethodGet, "/api/knowledge/sources?connectionId=wk-flaky", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"wk-flaky::kb-1"`) {
			t.Fatalf("flaky source list = %d %s", rec.Code, rec.Body.String())
		}
		token, err := knowledge.SignProxyToken(settings, knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call := func(body string) *httptest.ResponseRecorder {
			return doJSON(t, h, http.MethodPost, knowledge.ProxyPath, body, map[string]string{"Authorization": "Bearer " + token})
		}
		for _, tc := range []struct{ name, args, want string }{
			{"kb_list_sources", `{}`, "wk-flaky::kb-1"},
			{"kb_search", `{"query":"手册","sources":["wk-flaky::kb-1"]}`, "命中内容"},
		} {
			text, isErr, msg := rpcText(t, call(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tc.name+`","arguments":`+tc.args+`}}`))
			if isErr {
				t.Fatalf("%s failed: %s", tc.name, msg)
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("%s missing %q: %s", tc.name, tc.want, text)
			}
		}
		raw := "af_test_management_token"
		sum := sha256.Sum256([]byte(raw))
		if err := s.PutUser(domain.User{ID: 42, LoginName: "management-tester"}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutAPIToken(domain.APIToken{ID: "tok-management", UserID: 42, Name: "test", Prefix: "af_test", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
			t.Fatal(err)
		}
		rec := doJSON(t, h, http.MethodGet, "/api/knowledge/reference-options?projectId=p1&sources=true", "", map[string]string{"Authorization": "Bearer " + raw})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"wk-flaky::kb-1"`) {
			t.Fatalf("reference options = %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestCORSAllowsKnowledgePatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(cors())
	router.PATCH("/api/knowledge/sources/:id", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/knowledge/sources/kb-1", nil)
	req.Host = "console.example.com"
	req.Header.Set("Origin", "http://console.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", recorder.Code)
	}
	allowed := recorder.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowed, http.MethodPatch) {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PATCH", allowed)
	}
}

func TestKnowledgeCapabilitiesForceRefreshRequiresAdmin(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	// 路由层的全局鉴权会先把未登录请求拦成 401，这里直接驱动 handler，
	// 单独验证 refresh 分支的管理员门禁（受保护路径本身由路由测试覆盖）。
	h := &knowledgeHandlers{deps: &deps{}}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/knowledge/connections/wk-1/capabilities?refresh=1", nil)

	h.connectionCapabilities(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("force refresh without admin = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "只有平台管理员可以强制刷新") {
		t.Fatalf("force refresh message = %s", recorder.Body.String())
	}
}
