package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/apperr"

	"github.com/gin-gonic/gin"
)

func TestRespondMapsAppErrKinds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		err    error
		status int
	}{
		{apperr.New(apperr.Invalid, "bad"), http.StatusBadRequest},
		{apperr.New(apperr.NotFound, "missing"), http.StatusNotFound},
		{apperr.New(apperr.Conflict, "dup").With("id", "kbs-1"), http.StatusConflict},
		{apperr.New(apperr.Forbidden, "no"), http.StatusForbidden},
		{apperr.New(apperr.Unavailable, "off"), http.StatusServiceUnavailable},
		{fmt.Errorf("wrapped: %w", apperr.New(apperr.Upstream, "down")), http.StatusBadGateway},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		respond(c, tc.err)
		if rec.Code != tc.status {
			t.Fatalf("%v: status %d, want %d", tc.err, rec.Code, tc.status)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		ae, _ := apperr.As(tc.err)
		if body["error"] != ae.Message {
			t.Fatalf("%v: body %v", tc.err, body)
		}
		if ae.Kind == apperr.Conflict && body["id"] != "kbs-1" {
			t.Fatalf("extra fields should be merged into the body: %v", body)
		}
	}
}
