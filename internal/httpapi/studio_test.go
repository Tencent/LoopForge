package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

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
	css, err := fs.ReadFile(static, "observability.css")
	if err != nil {
		t.Fatal(err)
	}
	cssHash := sha256.Sum256(css)
	if want := `/observability.css?v=` + hex.EncodeToString(cssHash[:8]) + `"`; !strings.Contains(html, want) {
		t.Fatalf("served index must use the activity stylesheet's content hash: want %s, got %s", want, snippet(html, "observability.css"))
	}
}

func TestStudioStylesheetChangesInvalidateURLWithoutEditingIndex(t *testing.T) {
	const index = `<html><head><link rel="stylesheet" href="/observability.css?v=20261031metrics2"></head></html>`
	static := fstest.MapFS{
		"index.html":        {Data: []byte(index)},
		"observability.css": {Data: []byte(`.node-activity-inline{color:gray}`)},
	}
	h := studioFileServer(static)
	previousURL := ""
	for _, css := range []string{
		`.node-activity-inline{color:gray}`,
		`.node-activity-inline.is-running{animation:shimmer 2.4s infinite}`,
	} {
		// The deployment changes only the stylesheet; keep the same HTML and handler.
		static["observability.css"].Data = []byte(css)
		sum := sha256.Sum256([]byte(css))
		url := "/observability.css?v=" + hex.EncodeToString(sum[:8])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("index must be revalidated: status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		if !strings.Contains(rec.Body.String(), `href="`+url+`"`) || strings.Contains(rec.Body.String(), "20261031metrics2") {
			t.Fatalf("handwritten version must be replaced by the current stylesheet hash: %s", rec.Body.String())
		}
		if url == previousURL {
			t.Fatal("changed stylesheet reused its immutable URL")
		}
		asset := httptest.NewRecorder()
		h.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, url, nil))
		if asset.Code != http.StatusOK || asset.Body.String() != css {
			t.Fatalf("stamped URL must serve the matching stylesheet: status=%d body=%q", asset.Code, asset.Body.String())
		}
		previousURL = url
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

func TestStudioPagesShowShortBuildCommit(t *testing.T) {
	t.Setenv("LOOPFORGE_COMMIT", "60FC455aabbccddeeff001122334455667788990")
	static := fstest.MapFS{
		"index.html":  {Data: []byte(`<html><head></head><body><div id="app"></div></body></html>`)},
		"login.html":  {Data: []byte(`<html><body class="login-page"></body></html>`)},
		"vscode.html": {Data: []byte(`<html><body><div id="workbench"></div></body></html>`)},
	}
	h := studioFileServer(static)
	want := `class="loopforge-build" title="镜像版本 60fc455aabbccddeeff001122334455667788990">60fc455</span>`
	for _, path := range []string{"/", "/login.html", "/vscode.html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, want) || strings.Contains(body, "<script>") {
			t.Fatalf("%s should show the short commit without an inline script: %s", path, body)
		}
		if !strings.Contains(body, "position:fixed;right:12px;bottom:8px") {
			t.Fatalf("%s commit label should sit in the corner: %s", path, body)
		}
	}
}

func TestStudioPagesShowCommitBakedByDockerBuild(t *testing.T) {
	t.Setenv("LOOPFORGE_COMMIT", "")
	path := filepath.Join(t.TempDir(), "loopforge-commit")
	if err := os.WriteFile(path, []byte("60fc455aabbccddeeff001122334455667788990\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOPFORGE_COMMIT_FILE", path)
	static := fstest.MapFS{
		"index.html": {Data: []byte(`<html><body></body></html>`)},
	}
	rec := httptest.NewRecorder()
	studioFileServer(static).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `>60fc455</span>`) {
		t.Fatalf("docker-build commit file should show in the corner: %s", rec.Body.String())
	}
}

func TestStudioPagesOmitUnsafeOrMissingBuildCommit(t *testing.T) {
	t.Setenv("LOOPFORGE_COMMIT_FILE", filepath.Join(t.TempDir(), "missing"))
	static := fstest.MapFS{
		"index.html": {Data: []byte(`<html><body></body></html>`)},
	}
	h := studioFileServer(static)
	for _, commit := range []string{"", "60fc45", "<img src=x onerror=alert(1)>", "60fc455;rm", "not-a-sha"} {
		t.Run(commit, func(t *testing.T) {
			t.Setenv("LOOPFORGE_COMMIT", commit)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if strings.Contains(rec.Body.String(), "loopforge-build") || strings.Contains(rec.Body.String(), "onerror") {
				t.Fatalf("commit %q must not be rendered: %s", commit, rec.Body.String())
			}
		})
	}
}

// The OAuth callback lands here in a separate tab; the page must render under
// the global CSP, so its script is external and nothing secret is on it.
func TestStudioServesGongfengOAuthDonePage(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	get := func(path string) (*httptest.ResponseRecorder, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body, _ := io.ReadAll(rec.Body)
		return rec, string(body)
	}
	rec, page := get("/gongfeng-oauth-done.html")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET done page: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("done page must stay under the global CSP, got %q", rec.Header().Get("Content-Security-Policy"))
	}
	if strings.Contains(page, "<script>") || !strings.Contains(page, `<script src="/gongfeng-oauth-done.js" defer></script>`) {
		t.Fatalf("done page must load only its same-origin script: %s", snippet(page, "<script"))
	}
	for _, want := range []string{"工蜂授权已完成", "可以关闭此页面回到原页面继续", `id="closePage"`, "关闭页面", `href="/"`, "打开平台"} {
		if !strings.Contains(page, want) {
			t.Fatalf("done page misses %q", want)
		}
	}
	rec, script := get("/gongfeng-oauth-done.js")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("GET done script: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"new BroadcastChannel('agentflow-gongfeng-oauth')", "postMessage({type:'gongfeng-oauth-completed'})", "window.close()", "setTimeout(showManual,500)"} {
		if !strings.Contains(script, want) {
			t.Fatalf("done script misses %q", want)
		}
	}
	for _, leak := range []string{"location.search", "location.hash", "code", "state", "token"} {
		if strings.Contains(script, leak) {
			t.Fatalf("done script must not touch %q", leak)
		}
	}
}

func TestStudioServesSiteIcons(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	icons := map[string]string{
		"/favicon.svg":          "image/svg+xml",
		"/favicon.png":          "image/png",
		"/favicon.ico":          "image/",
		"/apple-touch-icon.png": "image/png",
	}
	for path, wantType := range icons {
		req := httptest.NewRequest(http.MethodGet, path+"?v=2", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("GET %s: %d (%d bytes); is it in web/embed.go?", path, rec.Code, rec.Body.Len())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, wantType) {
			t.Fatalf("GET %s: Content-Type %q, want %s*", path, got, wantType)
		}
	}
	for _, page := range []string{"/", "/vscode.html"} {
		req := httptest.NewRequest(http.MethodGet, page, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		html := rec.Body.String()
		for _, link := range []string{
			`<link rel="icon" type="image/svg+xml" href="/favicon.svg?v=`,
			`<link rel="apple-touch-icon" href="/apple-touch-icon.png?v=`,
		} {
			if !strings.Contains(html, link) {
				t.Fatalf("%s head missing %s", page, link)
			}
		}
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
