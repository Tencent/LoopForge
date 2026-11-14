package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// This exercises a real archived bundle through the platform's preview
// middleware. The fixture parent uses the studio root's production middleware
// (including its CSP) and a real signed viewer cookie, not a mock API.
func TestPreviewIsolationBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := runtime.FindChromium(); err != nil {
		t.Skip("chromium not installed")
	}
	t.Setenv("AGENTFLOW_LOCAL_SANDBOX", "local")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "preview-isolation-test-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "preview-viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := st.UserByLoginName("preview-viewer")
	signed, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: viewer.ID, LoginName: viewer.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	t.Cleanup(server.Close)
	studio := "http://" + server.Listener.Addr().String()
	previewHost := strings.Replace(server.Listener.Addr().String(), "127.0.0.1", "localhost", 1)
	en := engine.New(st, runtime.NewManager(t.TempDir()))
	svc := preview.NewService(st, nil, preview.Config{PublicScheme: "http", PublicHost: previewHost,
		FrameAncestors: studio, SessionTTL: time.Hour, DefaultRetention: time.Hour})
	en.SetPreview(svc)
	run := domain.Run{ID: "preview-isolation", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	p := domain.RunPreview{ID: run.ID, RunID: run.ID, Status: domain.PreviewPending,
		SourcePath: "dist", EntryFile: "index.html", SPAFallback: true}
	if err := st.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	studioJSON, _ := json.Marshal(studio)
	files := map[string]string{
		// The producer can suppress the convenience backend interceptor. The
		// boundary must hold for native fetch, not depend on an injected shim.
		"index.html": `<html><head><script data-af-preview-backend></script><link rel="stylesheet" href="./style.css"></head><body><pre id="result">pending</pre><script type="module" src="./app.js"></script></body></html>`,
		"style.css":  `body { --preview-loaded: yes; }`,
		"dep.js":     `export const loaded = true;`,
		"app.js": `import { loaded } from './dep.js';
const studio = ` + string(studioJSON) + `;
const result = {module: loaded, css: getComputedStyle(document.body).getPropertyValue('--preview-loaded').trim() === 'yes'};
if (parent !== window) {
  try { void parent.document.body; result.parent = false; } catch { result.parent = true; }
  try { await parent.fetch(studio + '/api/auth/tokens', {method:'POST', headers:{'Content-Type':'application/json'}, body:'{"name":"preview-attack"}'}); result.parentFetch = false; } catch { result.parentFetch = true; }
} else { result.parent = true; result.parentFetch = true; }
try { await fetch(studio + '/api/auth/tokens', {method:'POST', credentials:'include', headers:{'Content-Type':'application/json'}, body:'{"name":"preview-attack"}'}); result.fetch = false; } catch { result.fetch = true; }
try { await fetch(studio + '/api/auth/tokens', {method:'POST', credentials:'include', mode:'no-cors', body:'attack'}); result.noCors = false; } catch { result.noCors = true; }
const image = new Image();
result.image = await new Promise(resolve => { image.onload = () => resolve(false); image.onerror = () => resolve(true); image.src = studio + '/api/auth/tokens?image=1'; });
history.pushState({}, '', '/nested');
await new Promise(resolve => setTimeout(resolve, 20));
history.pushState({}, '', '/other');
await new Promise(resolve => setTimeout(resolve, 20));
await new Promise(resolve => { addEventListener('popstate', () => setTimeout(resolve, 20), {once:true}); history.back(); });
result.back = location.pathname.endsWith('/nested') && location.pathname.startsWith('/preview/');
result.route = location.pathname.endsWith('/nested') && location.pathname.startsWith('/preview/');
document.querySelector('#result').textContent = JSON.stringify(result);
parent.postMessage(result, studio);`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, "dist", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.ArchiveLocal(context.Background(), run, p, workspace); err != nil {
		t.Fatal(err)
	}
	p, _ = st.Preview(p.ID)
	handler := New(st, en, nil, "", "")
	var previewURL string
	var currentPreviewURL atomic.Value
	var tokenRequests atomic.Int32
	// Register the fixture on New's router so it cannot bypass securityHeaders.
	// Use an external script, as the studio CSP deliberately forbids inline JS.
	handler.GET("/", func(c *gin.Context) {
		http.SetCookie(c.Writer, &http.Cookie{Name: authCookieName, Value: signed, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		c.Data(http.StatusOK, "text/html", []byte(`<html><body><pre id="result">pending</pre><script src="/preview-test.js"></script></body></html>`))
	})
	handler.GET("/preview-test.js", func(c *gin.Context) {
		urlJSON, _ := json.Marshal(currentPreviewURL.Load().(string))
		c.Data(http.StatusOK, "application/javascript", []byte(fmt.Sprintf(`
(async () => {
  addEventListener('securitypolicyviolation', e => {
    document.querySelector('#result').textContent = 'parent CSP blocked ' + e.effectiveDirective;
  });
  const current = await fetch('/api/auth/current').then(r => r.json());
  if (current.user?.loginName !== 'preview-viewer') { document.querySelector('#result').textContent = 'viewer not authenticated'; return; }
  const url = %s;
  addEventListener('message', e => {
    if (e.origin !== new URL(url).origin || e.source !== document.querySelector('iframe')?.contentWindow) return;
    document.querySelector('#result').textContent = JSON.stringify(e.data);
  });
  const frame = document.createElement('iframe');
  frame.sandbox = 'allow-scripts allow-forms allow-modals allow-downloads allow-same-origin';
  frame.src = url;
  document.body.append(frame);
})();`, urlJSON)))
	})
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == server.Listener.Addr().String() && r.URL.Path == "/api/auth/tokens" {
			tokenRequests.Add(1)
		}
		handler.ServeHTTP(w, r)
	})
	server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	browserID := fmt.Sprintf("preview-isolation-%d", time.Now().UnixNano())
	sb, err := runtime.NewManager(t.TempDir()).Create(ctx, browserID, "test", domain.SandboxProfile{Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close(context.Background())
	if runtime.EnsureSandboxBrowser(ctx, sb, sb.Workspace(), browserID, nil) == nil {
		t.Fatal("Chromium could not start")
	}
	for _, backend := range []string{"", "https://api.example.test"} {
		_, previewURL, err = svc.CreateSession(p, httptest.NewRequest("POST", studio+"/api/runs/"+run.ID+"/preview/session", nil), backend)
		if err != nil {
			t.Fatal(err)
		}
		currentPreviewURL.Store(previewURL)
		for _, target := range []string{studio + "/", previewURL} {
			state, err := runtime.HandleBrowser(ctx, browserID, domain.BrowserRequest{Operation: "navigate", URL: target, WithSnapshot: true})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for (state.Snapshot == nil || !strings.Contains(state.Snapshot.Text, `"route":`)) && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				state, err = runtime.HandleBrowser(ctx, browserID, domain.BrowserRequest{Operation: "snapshot"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if state.Snapshot == nil {
				t.Fatalf("missing snapshot: %+v", state)
			}
			out := state.Snapshot.Text
			for _, key := range []string{"module", "css", "parent", "parentFetch", "fetch", "noCors", "image", "route", "back"} {
				if !strings.Contains(out, `"`+key+`":true`) || strings.Contains(out, `"`+key+`":false`) {
					t.Fatalf("backend=%q target=%s: %s not verified\n%s", backend, target, key, out)
				}
			}
		}
	}
	if got := tokenRequests.Load(); got != 0 {
		t.Fatalf("preview reached the studio token API %d times", got)
	}
}
