package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func bindCLIInstallScript(t *testing.T, headers map[string]string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	t.Setenv("AGENTFLOW_SECURE_COOKIES", "")
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/runners/install.sh", nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	c.Request = req
	serveBindCLIScript(c)
	return rec, rec.Body.String()
}

func TestBindCLIScriptFetchedOverHTTPSRefusesDowngrade(t *testing.T) {
	rec, body := bindCLIInstallScript(t, map[string]string{"X-Forwarded-Proto": "https"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(body, "\nSTRICT_HTTPS=1\n") {
		t.Fatal("a script fetched through an HTTPS proxy must default to strict HTTPS")
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/x-shellscript") {
		t.Fatalf("content type: %q", got)
	}
}

func TestBindCLIScriptFetchedOverIntranetStaysFlexible(t *testing.T) {
	_, body := bindCLIInstallScript(t, nil)
	if !strings.Contains(body, "\nSTRICT_HTTPS=0\n") {
		t.Fatal("an intranet install over a bare IP must still be able to fall back to HTTP")
	}
}