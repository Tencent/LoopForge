package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestOldWorkbenchEntryRedirectsButPreviewRemainsAccessible(t *testing.T) {
	files := fstest.MapFS{
		"native-chat/preview.html": {Data: []byte("preview")},
		"index.html":               {Data: []byte("home")},
	}
	handler := studioFileServer(files)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/chatbot/?session=s-1&projectId=p1&issueId=i1", nil))
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/?chatIssue=i1&chatSession=s-1&projectId=p1#/collaboration/s-1" {
		t.Fatalf("unexpected redirect: %d %q", response.Code, response.Header().Get("Location"))
	}
	preview := httptest.NewRecorder()
	handler.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, "/chatbot/preview.html", nil))
	if preview.Code != http.StatusOK || preview.Body.String() != "preview" {
		t.Fatalf("preview changed: %d %q", preview.Code, preview.Body.String())
	}
}
