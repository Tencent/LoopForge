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

func TestWindowsHelperDownloadsAndChecksums(t *testing.T) {
	for _, helper := range []struct {
		name    string
		env     string
		handler gin.HandlerFunc
	}{
		{"agentflow-runner", "AGENTFLOW_RUNNER_BINARY", serveRunnerBinary},
		{"agentflow-gate-mcp", "AGENTFLOW_GATE_MCP_BINARY", serveGateMCPBinary},
	} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(helper.name+"/"+arch, func(t *testing.T) {
				dir := t.TempDir()
				payload := []byte("windows-" + arch + "-" + helper.name)
				path := filepath.Join(dir, helper.name+"-windows-"+arch)
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(helper.env, filepath.Join(dir, helper.name+"-{os}-{arch}"))
				router := gin.New()
				router.GET("/helper", helper.handler)
				url := "/helper?os=windows&arch=" + arch
				download := httptest.NewRecorder()
				router.ServeHTTP(download, httptest.NewRequest(http.MethodGet, url, nil))
				if download.Code != http.StatusOK || download.Body.String() != string(payload) {
					t.Fatalf("download status=%d body=%q", download.Code, download.Body.String())
				}
				if got := download.Header().Get("Content-Disposition"); got != `attachment; filename="`+helper.name+`.exe"` {
					t.Fatalf("download filename = %q", got)
				}
				metadata := httptest.NewRecorder()
				router.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, url+"&metadata=1", nil))
				var body struct {
					SHA256 string `json:"sha256"`
				}
				if metadata.Code != http.StatusOK || json.Unmarshal(metadata.Body.Bytes(), &body) != nil {
					t.Fatalf("metadata status=%d body=%q", metadata.Code, metadata.Body.String())
				}
				expected := sha256.Sum256(payload)
				if body.SHA256 != hex.EncodeToString(expected[:]) {
					t.Fatal("metadata checksum differs from downloaded Windows helper")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				missing := httptest.NewRecorder()
				router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, url, nil))
				if missing.Code != http.StatusNotFound {
					t.Fatalf("missing Windows helper status=%d", missing.Code)
				}
			})
		}
	}
}

func TestWindowsDownloadsUseStandardDeploymentPatterns(t *testing.T) {
	for _, helper := range []struct {
		name    string
		env     string
		handler gin.HandlerFunc
	}{
		{"agentflow-runner", "AGENTFLOW_RUNNER_BINARY", serveRunnerBinary},
		{"agentflow-gate-mcp", "AGENTFLOW_GATE_MCP_BINARY", serveGateMCPBinary},
	} {
		t.Run(helper.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, helper.name+"-windows-amd64.exe")
			if err := os.WriteFile(binary, []byte("windows-native-binary"), 0600); err != nil {
				t.Fatal(err)
			}
			// compose.yaml and scripts/dev.sh use extensionless patterns.
			t.Setenv(helper.env, filepath.Join(dir, helper.name+"-{os}-{arch}"))
			router := gin.New()
			router.GET("/download", helper.handler)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/download?os=windows&arch=amd64", nil))
			if rec.Code != http.StatusOK || rec.Body.String() != "windows-native-binary" {
				t.Fatalf("standard deployment could not serve Windows binary: %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}
