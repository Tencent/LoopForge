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

// decodeOptional only tolerates an empty body; anything else that fails to
// parse must still answer 400 with the decoder error, like decode does.
func TestDecodeOptionalRejectsMalformedBodyWith400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(`{"data":`))

	var v map[string]string
	if decodeOptional(c, &v) {
		t.Fatal("expected decodeOptional to reject a truncated JSON body")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var errBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody["error"] == "" {
		t.Fatalf("expected a JSON error body, got %q (%v)", rec.Body.String(), err)
	}
}

// An oversized optional body is rejected with the same 400 and message as
// decode, not silently accepted or reported differently.
func TestDecodeOptionalTooLargeAnswersBodyTooLarge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2500000))))

	var v map[string]string
	if decodeOptional(c, &v) {
		t.Fatal("expected decodeOptional to reject a 2.5 MB body")
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

// Like decode, decodeOptional keeps its own 2 MiB cap even when the caller
// already wrapped the body with a larger MaxBytesReader.
func TestDecodeOptionalDoesNotWidenToCallerLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2500000))))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)

	var v map[string]string
	if decodeOptional(c, &v) {
		t.Fatal("expected decodeOptional to keep its 2 MiB limit")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// Bodies within the default cap still decode through both helpers.
func TestDecodeHelpersAcceptBodyWithinDefaultLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 1<<20))
	for name, fn := range map[string]func(*gin.Context, any) bool{"decode": decode, "decodeOptional": decodeOptional} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/test", strings.NewReader(payload))
		var v map[string]string
		if !fn(c, &v) {
			t.Fatalf("%s rejected a 1 MiB body: %s", name, rec.Body.String())
		}
		if len(v["data"]) != 1<<20 {
			t.Fatalf("%s data length = %d", name, len(v["data"]))
		}
	}
}
