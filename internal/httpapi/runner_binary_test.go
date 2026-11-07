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

func init() { gin.SetMode(gin.TestMode) }

// serveRunnerBinaryViaGin routes the request through a minimal gin engine.
func serveRunnerBinaryViaGin(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	g := gin.New()
	g.GET("/deploy/agentflow-runner", serveRunnerBinary)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServeRunnerBinarySelectsPlatformArtifact(t *testing.T) {
	dir := t.TempDir()
	linux := filepath.Join(dir, "agentflow-runner-linux-amd64")
	if err := os.WriteFile(linux, []byte("linux-amd64-runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(dir, "agentflow-runner-{os}-{arch}"))

	rec := serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=linux&arch=amd64")
	if rec.Code != http.StatusOK || rec.Body.String() != "linux-amd64-runner" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="agentflow-runner"` {
		t.Fatalf("content disposition = %q", got)
	}
}

func TestServeRunnerBinaryMetadataMatchesDownload(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("new runner binary")
	if err := os.WriteFile(filepath.Join(dir, "agentflow-runner-linux-amd64"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(dir, "agentflow-runner-{os}-{arch}"))
	rec := serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=linux&arch=amd64&metadata=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var metadata struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(payload)
	if metadata.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("checksum=%s want=%x", metadata.SHA256, want)
	}
}

func TestServeRunnerBinaryReportsMissingPlatformArtifact(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(t.TempDir(), "agentflow-runner-{os}-{arch}"))
	rec := serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=darwin&arch=arm64")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestRunnerSourceDigestDetectsContentChangeWithPreservedMtime(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"cmd/agentflow-runner", "cmd/agentflow-gate-mcp", "internal/runner"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "internal/runner/runner.go")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runnerSourceDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := runnerSourceDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("source changed without cache key changing")
	}
}
