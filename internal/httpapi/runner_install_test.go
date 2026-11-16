package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	deployassets "agentflow/deploy"
	"github.com/gin-gonic/gin"
)

func TestWindowsInstallerServesEmbeddedScript(t *testing.T) {
	g := gin.New()
	g.GET("/runners/install.ps1", serveBindCLIWindowsScript)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runners/install.ps1", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("installer response: status=%d headers=%v", rec.Code, rec.Header())
	}
	if len(rec.Body.Bytes()) == 0 || !bytes.Equal(rec.Body.Bytes(), deployassets.BindCLIWindowsScript()) {
		t.Fatal("installer response differs from embedded asset")
	}
	// BOM 让下载到磁盘的中文注释同时兼容 Windows PowerShell 5.1 和 PowerShell 7。
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte{0xef, 0xbb, 0xbf}) {
		t.Fatal("PowerShell installer must preserve UTF-8 BOM")
	}
}
