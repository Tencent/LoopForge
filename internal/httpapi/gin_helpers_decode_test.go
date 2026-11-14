package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDecodeDefaultLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeJSON := fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2500000))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(largeJSON))

	var v map[string]string
	if decode(c, &v) {
		t.Fatal("expected decode to fail on 2.5MB payload")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var errBody map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody["error"] != "http: request body too large" {
		t.Fatalf("expected 'http: request body too large', got %q", errBody["error"])
	}
}

// A handler that set a stricter limit before decode keeps it: decode wraps the
// body again, and the inner reader still stops at the smaller limit.
func TestDecodeKeepsCallerStricterLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 8<<10))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(payload))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)

	var v map[string]string
	if decode(c, &v) {
		t.Fatal("expected decode to honour the caller's 4 KiB limit")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// decode never inherits a larger limit from a pre-wrapped body; handlers that
// accept more than 2 MiB must say so with decodeLimit.
func TestDecodeDoesNotWidenToCallerLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeJSON := fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2500000))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(largeJSON))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)

	var v map[string]string
	if decode(c, &v) {
		t.Fatal("expected decode to keep its 2 MiB limit")
	}
}

func TestDecodeOptionalAcceptsEmptyBodyWithinDefaultLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, body := range map[string]string{"empty": "", "large": fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2500000))} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(body))
		var v map[string]string
		ok := decodeOptional(c, &v)
		if want := name == "empty"; ok != want {
			t.Fatalf("%s body: decodeOptional = %t, want %t (%s)", name, ok, want, rec.Body.String())
		}
	}
}

func TestDecodeLimitExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeJSON := fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 3000000))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(largeJSON))

	var v map[string]string
	if !decodeLimit(c, &v, 5<<20) {
		t.Fatalf("expected decodeLimit to succeed for 3MB payload with 5MB limit, got: %s", rec.Body.String())
	}
	if len(v["data"]) != 3000000 {
		t.Fatalf("data length mismatch: %d", len(v["data"]))
	}
}
