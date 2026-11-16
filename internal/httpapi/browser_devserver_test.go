package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// fakeHostBrowser registers a host browser for runID the way a host driver
// does, so dev server requests are served from this machine's loopback.
func fakeHostBrowser(t *testing.T, runID string) {
	t.Helper()
	fakeHostBrowserIn(t, runID, "")
}

// fakeHostBrowserIn also records the workspace the browser serves file:// from.
func fakeHostBrowserIn(t *testing.T, runID, workspace string) {
	t.Helper()
	devtools := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(devtools.Close)
	_, rawPort, _ := net.SplitHostPort(strings.TrimPrefix(devtools.URL, "http://"))
	port, _ := strconv.Atoi(rawPort)
	dir := filepath.Join(os.TempDir(), "agentflow-browser", runID)
	_ = os.RemoveAll(dir)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "owner": os.Getpid(), "webSocket": "ws://127.0.0.1:" + rawPort, "port": port, "workspace": workspace})
	if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDevServerPreviewProxiesTheSandboxPort(t *testing.T) {
	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	h := &browserHandlers{deps: a.deps}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	fakeHostBrowser(t, "shared-run")

	var seen *http.Request
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/", Domain: "localhost"})
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>app</title></head><body><script type="module" src="/src/main.ts"></script></body></html>`))
		case "/login":
			http.Redirect(w, r, "http://localhost:"+r.Host[strings.LastIndex(r.Host, ":")+1:]+"/home?x=1", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write([]byte(`export default 1`))
			if r.URL.Path == "/src/app.ts" {
				_, _ = w.Write([]byte(`;import "/src/b.ts"`))
			}
		}
	}))
	defer dev.Close()
	_, devPort, _ := net.SplitHostPort(strings.TrimPrefix(dev.URL, "http://"))
	prefix := "/devpreview/shared-run/" + devPort

	g := gin.New()
	user := owner
	g.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(user)))
	})
	g.Use(h.devServerHost())
	g.NoRoute(func(c *gin.Context) { c.String(404, "platform") })
	serve := func(method, path string, header map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://platform.test"+path, nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve("GET", prefix, nil); rec.Code != 302 || rec.Header().Get("Location") != prefix+"/" {
		t.Fatalf("bare prefix: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec := serve("GET", prefix+"/", map[string]string{"Accept": "text/html"})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "data-af-dev-server") || !strings.Contains(body, "data-af-preview-history") {
		t.Fatalf("document: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `src="`+prefix+`/src/main.ts"`) || strings.Contains(body, "<base") {
		t.Fatalf("module references must move under the prefix: %s", body)
	}
	if rec.Header().Get("X-Frame-Options") != "" || rec.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("headers: %v", rec.Header())
	}
	if cookie := rec.Header().Get("Set-Cookie"); cookie != "af_devpreview_shared-run_"+devPort+"_sid=1; Path="+prefix+"/" {
		t.Fatalf("cookie: %q", cookie)
	}

	// A module import from the page comes back under the prefix by Referer.
	rec = serve("GET", "/src/main.ts?v=2", map[string]string{"Referer": "http://platform.test" + prefix + "/", "Sec-Fetch-Site": "same-origin"})
	if rec.Code != 307 || rec.Header().Get("Location") != prefix+"/src/main.ts?v=2" {
		t.Fatalf("stray import: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serve("GET", "/src/main.ts", map[string]string{"Referer": "http://platform.test/chatbot/", "Sec-Fetch-Site": "same-origin"}); rec.Code != 404 || rec.Body.String() != "platform" {
		t.Fatalf("platform request rerouted: %d", rec.Code)
	}
	if rec := serve("GET", "/src/main.ts", map[string]string{"Referer": "http://evil.test" + prefix + "/", "Sec-Fetch-Site": "cross-site"}); rec.Code != 404 {
		t.Fatalf("cross-site request rerouted: %d", rec.Code)
	}

	if rec := serve("GET", prefix+"/src/app.ts", nil); !strings.Contains(rec.Body.String(), `import "`+prefix+`/src/b.ts"`) {
		t.Fatalf("module imports: %s", rec.Body.String())
	}

	rec = serve("GET", prefix+"/login", nil)
	if rec.Code != 302 || rec.Header().Get("Location") != prefix+"/home?x=1" {
		t.Fatalf("redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = serve("GET", prefix+"/src/main.ts", map[string]string{
		// Only this run's cookies reach its dev server: not the platform's,
		// not another run's on the same port, not the old port-only names.
		"Cookie": "af_devpreview_shared-run_" + devPort + "_sid=1; " + authCookieName + "=secret; " +
			"af_devpreview_other-run_" + devPort + "_leak=2; afdev" + devPort + "_old=3",
		"Origin":           "http://platform.test",
		"Referer":          "http://platform.test" + prefix + "/admin?a=1",
		"Staffname":        "bob",
		"X-Forwarded-For":  "10.0.0.1",
		"X-Requested-With": "XMLHttpRequest",
		"Authorization":    "Basic cGxhdGZvcm06cGFzcw==",
	})
	if rec.Code != 200 || rec.Body.String() != "export default 1" {
		t.Fatalf("module: %d %s", rec.Code, rec.Body.String())
	}
	if got := seen.Header.Get("Cookie"); got != "sid=1" {
		t.Fatalf("upstream cookie %q", got)
	}
	if seen.Host != "localhost:"+devPort || seen.Header.Get("Origin") != "http://localhost:"+devPort || seen.Header.Get("Referer") != "http://localhost:"+devPort+"/admin?a=1" {
		t.Fatalf("upstream host/origin/referer: %q %q %q", seen.Host, seen.Header.Get("Origin"), seen.Header.Get("Referer"))
	}
	for _, name := range []string{"Staffname", "X-Forwarded-For", "Authorization"} {
		if seen.Header.Get(name) != "" {
			t.Fatalf("upstream got %s", name)
		}
	}
	if seen.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		t.Fatal("app headers must pass")
	}

	if rec := serve("GET", prefix+"/", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-Websocket-Version": "13"}); rec.Code != 501 {
		t.Fatalf("websocket: %d", rec.Code)
	}

	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	_, idlePort, _ := net.SplitHostPort(closed.Addr().String())
	closed.Close()
	rec = serve("GET", "/devpreview/shared-run/"+idlePort+"/", map[string]string{"Sec-Fetch-Mode": "navigate"})
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "没有正在监听") || !strings.Contains(rec.Body.String(), `http-equiv="refresh"`) {
		t.Fatalf("idle port: %d %s", rec.Code, rec.Body.String())
	}

	user = outsider
	if rec := serve("GET", prefix+"/", nil); rec.Code != 403 {
		t.Fatalf("outsider: %d", rec.Code)
	}
}

func TestParseDevServerPath(t *testing.T) {
	for _, tc := range []struct {
		path  string
		run   string
		port  int
		rest  string
		slash bool
		ok    bool
	}{
		{"/devpreview/run-1/5173/src/a.ts", "run-1", 5173, "src/a.ts", true, true},
		{"/devpreview/run-1/5173/", "run-1", 5173, "", true, true},
		{"/devpreview/run-1/5173", "run-1", 5173, "", false, true},
		{"/devpreview/run-1/05173/", "", 0, "", false, false},
		{"/devpreview/run-1/70000/", "", 0, "", false, false},
		{"/devpreview/run 1/80/", "", 0, "", false, false},
		{"/devpreview/run-1", "", 0, "", false, false},
		{"/preview/run-1/80/", "", 0, "", false, false},
	} {
		run, port, rest, slash, ok := parseDevServerPath(tc.path)
		if run != tc.run || port != tc.port || rest != tc.rest || slash != tc.slash || ok != tc.ok {
			t.Fatalf("%s: %q %d %q %v %v", tc.path, run, port, rest, slash, ok)
		}
	}
}

func TestDevServerETagsCarryTheRewriteVersion(t *testing.T) {
	if got := devServerETag(`W/"12-abc"`); got != `W/"12-abc-af1"` {
		t.Fatal(got)
	}
	if got := devServerUpstreamETags([]string{`W/"12-abc-af1", "old"`, `"x-af1"`}); got != `W/"12-abc", "x"` {
		t.Fatal(got)
	}
	if got := devServerUpstreamETags([]string{`"old"`}); got != "" {
		t.Fatal(got)
	}
}

func TestRewriteDevServerCookie(t *testing.T) {
	prefix := "/devpreview/r/3000"
	for raw, want := range map[string]string{
		"a=1; Path=/; Domain=localhost; HttpOnly": "af_devpreview_r_3000_a=1; Path=/devpreview/r/3000/; HttpOnly",
		"a=1; path=/api; SameSite=Lax":            "af_devpreview_r_3000_a=1; Path=/devpreview/r/3000/api; SameSite=Lax",
		"a=1":                                     "af_devpreview_r_3000_a=1",
	} {
		if got := rewriteDevServerCookie(raw, prefix); got != want {
			t.Fatalf("%q: %q", raw, got)
		}
	}
	_ = domain.BrowserRequest{}
}

func TestDevServerPreviewServesWorkspaceFiles(t *testing.T) {
	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	h := &browserHandlers{deps: a.deps}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	ws := t.TempDir()
	for name, body := range map[string]string{"demo.html": `<link href="style.css"><h1>demo</h1>`, "style.css": "h1{}", "site/index.html": "<p>site</p>"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(ws, name)), 0o755)
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeHostBrowserIn(t, "shared-run", ws)

	g := gin.New()
	user := owner
	g.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(user)))
	})
	g.Use(h.devServerHost())
	g.NoRoute(func(c *gin.Context) { c.String(404, "platform") })
	serve := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest(method, "http://platform.test"+path, nil))
		return rec
	}
	prefix := "/devpreview/shared-run/file" + ws

	rec := serve("GET", prefix+"/demo.html")
	if rec.Code != 200 || rec.Body.String() != `<link href="style.css"><h1>demo</h1>` || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("page: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("headers: %v", rec.Header())
	}
	if rec := serve("GET", prefix+"/style.css"); rec.Code != 200 || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("stylesheet: %d %v", rec.Code, rec.Header())
	}
	if rec := serve("GET", prefix+"/site"); rec.Code != 301 || rec.Header().Get("Location") != prefix+"/site/" {
		t.Fatalf("directory: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serve("GET", prefix+"/site/"); rec.Code != 200 || rec.Body.String() != "<p>site</p>" {
		t.Fatalf("index: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve("HEAD", prefix+"/site/"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("head: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve("GET", prefix+"/missing.html"); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(ws), "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := serve("GET", prefix+"/../secret.txt"); rec.Code != 403 {
		t.Fatalf("outside: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve("POST", prefix+"/demo.html"); rec.Code != 405 {
		t.Fatalf("post: %d", rec.Code)
	}
	user = outsider
	if rec := serve("GET", prefix+"/demo.html"); rec.Code != 403 {
		t.Fatalf("outsider: %d", rec.Code)
	}
}

func TestParseDevFilePath(t *testing.T) {
	for _, tc := range []struct {
		path, run, file string
		ok              bool
	}{
		{"/devpreview/run-1/file/workspace/demo.html", "run-1", "/workspace/demo.html", true},
		{"/devpreview/run-1/file/workspace/site/", "run-1", "/workspace/site/", true},
		{"/devpreview/run-1/file/workspace/a/../b.html", "run-1", "/workspace/b.html", true},
		{"/devpreview/run-1/file/", "", "", false},
		{"/devpreview/run-1/file", "", "", false},
		{"/devpreview/run-1/5173/file/a", "", "", false},
		{"/devpreview/run 1/file/a", "", "", false},
	} {
		run, file, ok := parseDevFilePath(tc.path)
		if run != tc.run || file != tc.file || ok != tc.ok {
			t.Fatalf("%s: %q %q %v", tc.path, run, file, ok)
		}
	}
}
