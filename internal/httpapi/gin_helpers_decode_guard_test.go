package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func decodeGuardContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	return c, rec
}

func decodeGuardError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", rec.Body.String(), err)
	}
	return body["error"]
}

// An explicit decodeLimit is enforced, not just the 2 MiB default.
func TestDecodeLimitRejectsBodyOverExplicitLimit(t *testing.T) {
	c, rec := decodeGuardContext(fmt.Sprintf(`{"data":"%s"}`, strings.Repeat("a", 2048)))
	var small map[string]string
	if decodeLimit(c, &small, 1024) || rec.Code != http.StatusBadRequest {
		t.Fatalf("2KB body under 1KB limit = %d", rec.Code)
	}
	if got := decodeGuardError(t, rec); got != "http: request body too large" {
		t.Fatalf("2KB body under 1KB limit error = %q", got)
	}
}

// chat, startGeneration and session PUT raise the limit to
// assistantRequestBodyLimit, not remove it: a body just over it is rejected
// before any work starts.
func TestAssistantHandlersRejectBodyOverAssistantLimit(t *testing.T) {
	h, u := workbenchFixture(t)
	oversized := func() *bytes.Buffer {
		payload := fmt.Sprintf(`{"projectId":"workbench","sessionId":"too-large","messages":[{"role":"user","content":"%s"}]}`,
			strings.Repeat("a", assistantRequestBodyLimit+1024))
		return bytes.NewBufferString(payload)
	}
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		handler func(*gin.Context)
	}{
		{"chat", http.MethodPost, "/api/assistant/chat", h.chat},
		{"startGeneration", http.MethodPost, "/api/assistant/generations", h.startGeneration},
		{"session PUT", http.MethodPut, "/api/assistant/sessions/too-large", h.session},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := workbenchContext(u, tc.method, tc.path, oversized())
			c.Params = gin.Params{{Key: "id", Value: "too-large"}}
			tc.handler(c)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("over-limit body = %d %s", rec.Code, truncateForTest(rec.Body.String()))
			}
			if got := decodeGuardError(t, rec); got != "http: request body too large" {
				t.Fatalf("over-limit body error = %q", got)
			}
		})
	}
	if _, err := loadAssistantConversation(h.repo, u.ID, "too-large"); err == nil {
		t.Fatal("rejected over-limit request still stored a session")
	}
}

// chat itself (not only startGeneration) decodes with the assistant limit: a
// 3 MiB body gets past decoding instead of hitting the 2 MiB default.
func TestAssistantChatDecodesBodyOverDefaultLimit(t *testing.T) {
	h, u := workbenchFixture(t)
	payload := fmt.Sprintf(`{"projectId":"workbench","sessionId":"large-chat","messages":[{"role":"user","content":"%s"}]}`,
		strings.Repeat("a", 3<<20))
	c, rec := workbenchContext(u, http.MethodPost, "/api/assistant/chat", bytes.NewBufferString(payload))
	h.chat(c)
	if strings.Contains(rec.Body.String(), "http: request body too large") {
		t.Fatalf("3MiB chat body hit the default limit: %d %s", rec.Code, truncateForTest(rec.Body.String()))
	}
}

func truncateForTest(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
