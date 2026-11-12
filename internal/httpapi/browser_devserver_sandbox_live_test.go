//go:build sandboxlive

package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// The dev server binds 127.0.0.1 inside the sandbox and the port is not
// published. The preview reaches it through the browser sidecar, which
// shares that network namespace, the same way a run does.
func TestDevServerPreviewReachesDockerSandbox(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	const (
		runID     = "live-devpreview"
		port      = "4173"
		image     = "agentflow-browser-c34051af5914b90c"
		sandbox   = "lf-devpreview-sandbox"
		sidecar   = "lf-devpreview-browser"
		pageToken = "sandbox-live-7f3a"
		fileToken = "workspace-live-marker"
	)
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Fatalf("browser sidecar image %s is not present", image)
	}

	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	run, ok := a.repo.RunState("shared-run")
	if !ok {
		t.Fatal("fixture run missing")
	}
	run.ID = runID
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeCloud
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}

	// The Docker VM shares the home directory. macOS temp dirs and /tmp are
	// not visible inside containers, so the server script and workspace have
	// to live under the home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "tmp", "lf-devpreview-live")
	_ = os.RemoveAll(root)
	ws := filepath.Join(root, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.WriteFile(filepath.Join(ws, "index.html"), []byte("<p>"+fileToken+"</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	serverJS := `const http=require("http");
const token=` + "`" + pageToken + "`" + `;
http.createServer((req,res)=>{
  if(req.url==="/headers"){
    const names=["host","cookie","x-forwarded-for","x-real-ip","x-app-trace","authorization"];
    const out={}; for(const n of names) out[n]=req.headers[n]||"";
    res.setHeader("Content-Type","application/json"); res.end(JSON.stringify(out)); return;
  }
  if(req.url==="/src/main.ts"){
    res.setHeader("Content-Type","text/javascript"); res.end('export default "from-sandbox"'); return;
  }
  res.setHeader("Content-Type","text/html; charset=utf-8");
  res.setHeader("Set-Cookie","sid=live; Path=/");
  res.end('<!doctype html><html><head></head><body><p>'+token+'</p><script type="module" src="/src/main.ts"></script></body></html>');
}).listen(4173,"127.0.0.1");`
	script := filepath.Join(root, "server.js")
	if err := os.WriteFile(script, []byte(serverJS), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := func() {
		_ = exec.Command("docker", "rm", "-f", sidecar, sandbox).Run()
	}
	rm()
	t.Cleanup(rm)
	if out, err := exec.Command("docker", "run", "-d", "--name", sandbox,
		"-v", script+":/server.js:ro", "--entrypoint", "node", "node:22-bookworm-slim", "/server.js").CombinedOutput(); err != nil {
		t.Fatalf("start sandbox: %v: %s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	if state, _ := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", sandbox).Output(); strings.TrimSpace(string(state)) != "running" {
		logs, _ := exec.Command("docker", "logs", sandbox).CombinedOutput()
		t.Fatalf("sandbox %s: %s", strings.TrimSpace(string(state)), logs)
	}
	if out, err := exec.Command("docker", "run", "-d", "--name", sidecar, "--init",
		"--label", "agentflow.browser.run="+runID,
		"--label", "agentflow.browser.owner="+sandbox,
		"--network", "container:"+sandbox,
		"--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--entrypoint", "sleep",
		"-v", ws+":/workspace",
		image, "600").CombinedOutput(); err != nil {
		t.Fatalf("start sidecar: %v: %s", err, out)
	}

	h := &browserHandlers{deps: a.deps}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	current := owner
	g := gin.New()
	g.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(current)))
	})
	g.Use(h.devServerHost())
	prefix := "/devpreview/" + runID + "/" + port
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

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("sandbox port is reachable on the host; the preview would not be proving the network namespace")
	}
	if published, err := exec.Command("docker", "port", sandbox).Output(); err != nil || strings.TrimSpace(string(published)) != "" {
		t.Fatalf("sandbox published ports: %q err=%v", published, err)
	}

	rec := serve(http.MethodGet, prefix+"/", map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, pageToken) || !strings.Contains(body, `src="`+prefix+`/src/main.ts"`) || !strings.Contains(body, "data-af-dev-server") {
		t.Fatalf("page: %d %s", rec.Code, body)
	}
	if cookie := rec.Header().Get("Set-Cookie"); cookie != "af_devpreview_"+runID+"_"+port+"_sid=live; Path="+prefix+"/" {
		t.Fatalf("cookie %q", cookie)
	}

	rec = serve(http.MethodGet, prefix+"/src/main.ts", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `export default "from-sandbox"`) {
		t.Fatalf("module: %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(http.MethodGet, prefix+"/headers", map[string]string{
		"Cookie":          "af_devpreview_" + runID + "_" + port + "_sid=live; platform_session=secret",
		"X-Forwarded-For": "203.0.113.8",
		"X-Real-IP":       "203.0.113.9",
		"X-App-Trace":     "trace-from-browser",
		"Authorization":   "Basic cGxhdGZvcm06cGFzcw==",
		"Origin":          "http://platform.test",
		"Referer":         "http://platform.test" + prefix + "/admin",
	})
	if rec.Code != 200 {
		t.Fatalf("headers: %d %s", rec.Code, rec.Body.String())
	}
	var seen map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &seen); err != nil {
		t.Fatal(err)
	}
	if seen["host"] != "localhost:"+port || seen["cookie"] != "sid=live" || seen["x-forwarded-for"] != "" || seen["x-real-ip"] != "" || seen["authorization"] != "" || seen["x-app-trace"] != "trace-from-browser" {
		t.Fatalf("upstream headers %+v", seen)
	}

	rec = serve(http.MethodGet, "/devpreview/"+runID+"/file/workspace/index.html", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), fileToken) {
		t.Fatalf("workspace file: %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(http.MethodGet, "/devpreview/"+runID+"/4174/", map[string]string{"Sec-Fetch-Mode": "navigate"})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "没有正在监听") {
		t.Fatalf("idle port: %d %s", rec.Code, rec.Body.String())
	}

	current = outsider
	if rec = serve(http.MethodGet, prefix+"/", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider: %d %s", rec.Code, rec.Body.String())
	}
}
