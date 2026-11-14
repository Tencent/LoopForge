package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBinaryDownloadPlatform(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantOS   string
		wantArch string
		wantOK   bool
		wantErr  string
	}{
		{name: "defaults", query: "", wantOS: "darwin", wantArch: "amd64", wantOK: true},
		{name: "normalized", query: "?os=%20Linux%20&arch=ARM64", wantOS: "linux", wantArch: "arm64", wantOK: true},
		{name: "invalid chars fall back to defaults", query: "?os=lin-ux&arch=amd_64", wantOS: "darwin", wantArch: "amd64", wantOK: true},
		{name: "windows", query: "?os=windows&arch=amd64", wantOS: "windows", wantArch: "amd64", wantOK: true},
		{name: "unsupported os", query: "?os=freebsd&arch=amd64", wantErr: `unsupported os "freebsd" (darwin, linux, windows)`},
		{name: "unsupported arch", query: "?os=linux&arch=386", wantErr: `unsupported arch "386" (amd64, arm64)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/binary"+tc.query, nil)
			gotOS, gotArch, ok := binaryDownloadPlatform(c)
			if ok != tc.wantOK || gotOS != tc.wantOS || gotArch != tc.wantArch {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", gotOS, gotArch, ok, tc.wantOS, tc.wantArch, tc.wantOK)
			}
			if tc.wantOK {
				if rec.Body.Len() != 0 {
					t.Fatalf("unexpected response body %q", rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400", rec.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != tc.wantErr {
				t.Fatalf("body=%q err=%v want error %q", rec.Body.String(), err, tc.wantErr)
			}
		})
	}
}

// Both binary download handlers must keep rejecting unsupported platforms
// before touching any prebuilt artifact or source build.
func TestBinaryDownloadHandlersRejectUnsupportedPlatform(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"agentflow-runner-freebsd-amd64", "agentflow-gate-mcp-linux-386"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(dir, "agentflow-runner-{os}-{arch}"))
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(dir, "agentflow-gate-mcp-{os}-{arch}"))
	g := gin.New()
	g.GET("/runner", serveRunnerBinary)
	g.GET("/gate", serveGateMCPBinary)
	for _, tc := range []struct{ path, wantErr string }{
		{"/runner?os=freebsd&arch=amd64", `unsupported os "freebsd" (darwin, linux, windows)`},
		{"/gate?os=linux&arch=386", `unsupported arch "386" (amd64, arm64)`},
	} {
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var body map[string]string
		if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["error"] != tc.wantErr {
			t.Fatalf("%s: status=%d body=%q want 400 %q", tc.path, rec.Code, rec.Body.String(), tc.wantErr)
		}
	}
}
