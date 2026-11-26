package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestOldWorkbenchEntryRedirectsButPreviewRemainsAccessible(t *testing.T) {
	files := fstest.MapFS{
		"native-chat/preview.html":  {Data: []byte("preview")},
		"native-chat/index.html":    {Data: []byte("designer")},
		"native-chat/assets/app.js": {Data: []byte("app")},
		"index.html":                {Data: []byte("home")},
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
	for _, path := range []string{"/chatbot/", "/chatbot", "/chatbot/index.html"} {
		designer := httptest.NewRecorder()
		handler.ServeHTTP(designer, httptest.NewRequest(http.MethodGet, path+"?surface=sop-designer&workflowId=sop-1", nil))
		if designer.Code != http.StatusOK || designer.Body.String() != "designer" || designer.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("designer surface must stay accessible on %s: %d %q", path, designer.Code, designer.Body.String())
		}
		unknown := httptest.NewRecorder()
		handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, path+"?surface=unknown", nil))
		if unknown.Code != http.StatusNotFound {
			t.Fatalf("unknown surface on %s served a document: %d", path, unknown.Code)
		}
	}
}
