package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// TestStaticRoutes locks the Studio static-file routing behavior on the gin
// engine: the stamped index renders with an explicit 200 even though gin's
// NoRoute preset is 404, and missing assets still return a real 404.
func TestStaticRoutes(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	for _, tc := range []struct {
		path     string
		wantCT   string
		wantCode int
	}{
		{"/", "text/html", 200},
		{"/index.html", "text/html", 200},
		{"/vscode.html", "text/html", 200},
		{"/vscode.js", "javascript", 200},
		{"/vscode-web/out/vs/loader.js", "javascript", 200},
		{"/extensions/agentflow-workspace/package.json", "json", 200},
		{"/app.js", "javascript", 200},
		{"/app/start.js", "javascript", 200},
		{"/deployment-dashboard.css", "text/css", 200},
		{"/no-such-file.js", "", 404},
		{"/no/such/dir/", "", 404},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.wantCode {
			t.Errorf("GET %s -> %d, want %d", tc.path, rec.Code, tc.wantCode)
		}
		if tc.wantCode == 200 && !strings.Contains(rec.Header().Get("Content-Type"), tc.wantCT) {
			t.Errorf("GET %s ct=%s, want contains %s", tc.path, rec.Header().Get("Content-Type"), tc.wantCT)
		}
		if tc.wantCode == 404 {
			body, _ := io.ReadAll(rec.Body)
			if len(body) == 0 {
				t.Errorf("GET %s 404 with empty body", tc.path)
			}
		}
	}
}
