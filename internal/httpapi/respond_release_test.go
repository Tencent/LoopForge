package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
)

// A failed OpenSandbox release keeps the run so the delete can be repeated;
// the response must say so (503 + Retry-After) instead of a 400 that reads as
// a permanent request error.
func TestRespondMapsExecutionResourceReleaseToRetryable503(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	err := fmt.Errorf("%w；运行记录已保留，请稍后重试删除", fmt.Errorf("%w：OpenSandbox DELETE: HTTP 500", engine.ErrExecutionResourceRelease))
	respond(c, err)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("release failure = %d Retry-After=%q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	var body map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["retryable"] != true || body["error"] != err.Error() {
		t.Fatalf("body = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	respond(c, errors.New("run x is running: cancel it before deleting"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ordinary delete refusal = %d, want 400", rec.Code)
	}
}
