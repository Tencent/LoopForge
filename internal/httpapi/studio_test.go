package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	webassets "agentflow/web"
)

func TestStudioIndexStampsAssetsWithContentHash(t *testing.T) {
	static, err := fs.Sub(webassets.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(static, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(js)
	want := hex.EncodeToString(sum[:8])
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /: %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if !strings.Contains(html, "/app.js?v="+want) {
		t.Fatalf("index.html should stamp app.js with content hash %s, got snippet around script: %s", want, snippet(html, "app.js"))
	}
	if strings.Contains(html, "/app.js?v=20260820s6") || strings.Contains(html, "/app.js?v=20260820s5") {
		t.Fatal("hand-edited cache-bust versions should be replaced")
	}
}

func TestStudioVscodePageStampsAssetsWithContentHash(t *testing.T) {
	static, err := fs.Sub(webassets.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	js, err := fs.ReadFile(static, "vscode.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(js)
	want := hex.EncodeToString(sum[:8])
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/vscode.html", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /vscode.html: %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	if !strings.Contains(html, "/vscode.js?v="+want) {
		t.Fatalf("vscode.html should stamp vscode.js with content hash %s, got snippet around script: %s", want, snippet(html, "vscode.js"))
	}
	if !strings.Contains(html, `id="workbench"`) || !strings.Contains(html, "/vscode.js?v="+want) {
		t.Fatal("vscode.html should host the vscode-web workbench")
	}
	if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("vscode.html must be embeddable same-origin, got X-Frame-Options=%q", rec.Header().Get("X-Frame-Options"))
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Fatalf("vscode.html CSP should allow same-origin framing, got %q", rec.Header().Get("Content-Security-Policy"))
	}
}

func TestStudioIndexInjectsAppDomain(t *testing.T) {
	t.Setenv("AGENTFLOW_APP_DOMAIN", "loopforge-test.example.com")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /: %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	html := string(body)
	want := `<meta name="agentflow-app-domain" content="loopforge-test.example.com"></head>`
	if !strings.Contains(html, want) {
		t.Fatalf("expected injected domain meta %q, got snippet: %s", want, snippet(html, "agentflow-app-domain"))
	}
	// The CSP forbids inline scripts, so nothing may be injected as one.
	if strings.Contains(html, "<script>") {
		t.Fatalf("index.html carries an inline script the CSP would block: %s", snippet(html, "<script>"))
	}
}

func snippet(s, needle string) string {
	i := strings.Index(s, needle)
	if i < 0 {
		return "<missing>"
	}
	start := i - 20
	if start < 0 {
		start = 0
	}
	end := i + 40
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
