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

func TestDecodePreservesExistingMaxBytesReader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeJSON := fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2500000))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(largeJSON))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)

	var v map[string]string
	if !decode(c, &v) {
		t.Fatalf("expected decode to succeed for 2.5MB payload when pre-configured with 4MB limit, got: %s", rec.Body.String())
	}
	if len(v["data"]) != 2500000 {
		t.Fatalf("data length mismatch: %d", len(v["data"]))
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
