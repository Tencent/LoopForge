package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestServeGateMCPBinarySelectsPlatformArtifact(t *testing.T) {
	dir := t.TempDir()
	linux := filepath.Join(dir, "agentflow-gate-mcp-linux-arm64")
	if err := os.WriteFile(linux, []byte("linux-arm64-gate"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(dir, "agentflow-gate-mcp-{os}-{arch}"))
	g := gin.New()
	g.GET("/runners/agentflow-gate-mcp", serveGateMCPBinary)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runners/agentflow-gate-mcp?os=linux&arch=arm64", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "linux-arm64-gate" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="agentflow-gate-mcp"` {
		t.Fatalf("content disposition=%q", got)
	}
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runners/agentflow-gate-mcp?os=linux&arch=arm64&metadata=1", nil))
	var metadata struct {
		SHA256 string `json:"sha256"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &metadata) != nil {
		t.Fatalf("metadata status=%d body=%q", rec.Code, rec.Body.String())
	}
	want := sha256.Sum256([]byte("linux-arm64-gate"))
	if metadata.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("metadata checksum=%s want=%x", metadata.SHA256, want)
	}
}
