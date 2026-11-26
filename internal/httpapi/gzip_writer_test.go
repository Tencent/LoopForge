package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// http.FileServer drops Content-Encoding on its error pages (Go 1.23+). The
// body must then stay uncompressed, or clients get gzip bytes with no header
// telling them to decode them: every unknown /api path and missing asset
// answered with an unreadable 404.
func TestGzipKeepsBodiesReadableWhenHandlersDropTheEncoding(t *testing.T) {
	router := gin.New()
	router.Use(gzipMaybe())
	router.GET("/json", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	router.GET("/empty", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.NoRoute(func(c *gin.Context) { http.FileServer(http.Dir(t.TempDir())).ServeHTTP(c.Writer, c.Request) })
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	missing := get("/api/projects/a%2Fb")
	if missing.Code != http.StatusNotFound || missing.Header().Get("Content-Encoding") != "" || missing.Body.String() != "404 page not found\n" {
		t.Fatalf("missing: %d %#v %q", missing.Code, missing.Header(), missing.Body.String())
	}

	ok := get("/json")
	if ok.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("json not compressed: %#v", ok.Header())
	}
	zr, err := gzip.NewReader(ok.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); string(body) != `{"ok":true}` {
		t.Fatalf("json body %q", body)
	}

	// A compressed response without a body is still a valid gzip stream.
	empty := get("/empty")
	if empty.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("empty headers: %#v", empty.Header())
	}
	if zr, err := gzip.NewReader(empty.Body); err != nil {
		t.Fatalf("empty body is not gzip: %v", err)
	} else if body, _ := io.ReadAll(zr); len(body) != 0 {
		t.Fatalf("empty body %q", body)
	}
}
