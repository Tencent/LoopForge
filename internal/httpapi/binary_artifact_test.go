package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBinaryArtifactsRejectSymlinkEscapes(t *testing.T) {
	for _, endpoint := range []struct {
		name    string
		env     string
		handler gin.HandlerFunc
	}{
		{"runner", "AGENTFLOW_RUNNER_BINARY", serveRunnerBinary},
		{"gate-mcp", "AGENTFLOW_GATE_MCP_BINARY", serveGateMCPBinary},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			outside := filepath.Join(t.TempDir(), "private.txt")
			if err := os.WriteFile(outside, []byte("private file outside artifact directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(t.TempDir(), "binary-linux-amd64")
			if err := os.Symlink(outside, artifact); err != nil {
				t.Fatal(err)
			}
			t.Setenv(endpoint.env, filepath.Join(filepath.Dir(artifact), "binary-{os}-{arch}"))
			g := gin.New()
			g.GET("/binary", endpoint.handler)
			for _, query := range []string{"", "&metadata=1"} {
				rec := httptest.NewRecorder()
				g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/binary?os=linux&arch=amd64"+query, nil))
				if rec.Code != http.StatusNotFound {
					t.Errorf("query=%q status=%d body=%q; want 404 for an escaping symlink", query, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func TestBinaryArtifactAllowsSymlinkWithinDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("runner release"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("release", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(dir, "current"))
	rec := serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=linux&arch=amd64")
	if rec.Code != http.StatusOK || rec.Body.String() != "runner release" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
