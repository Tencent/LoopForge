package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestMCPFileUploadDownloadDelete(t *testing.T) {
	store := newSkillFileServer(t)
	server := domain.MCPServerDefinition{ID: "custom", Name: "Custom", Scope: "project", ProjectID: "platform", Transport: "stdio", Command: "python3"}
	if err := store.PutMCPServer(server); err != nil {
		t.Fatal(err)
	}
	h := skillFileHandler(t, store)
	rec := requestMultipart(t, h, http.MethodPost, "/api/mcp-servers/custom/files", func(w *multipart.Writer) { addFilePartWithPath(w, "my-mcp/src/server.py", []byte("print('ok')")) })
	if rec.Code != http.StatusOK {
		t.Fatalf("upload=%d %s", rec.Code, rec.Body.String())
	}
	var saved domain.MCPServerDefinition
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.FileRefs) != 1 || saved.FileRefs[0].Path != "src/server.py" {
		t.Fatalf("refs=%#v", saved.FileRefs)
	}
	large := strings.Repeat("x", (1<<20)+1)
	for _, upload := range []struct {
		name    string
		content []byte
	}{
		{"large.txt", []byte(large)},
		{"bundle.zip", zipBytes(t, map[string]string{"large.txt": large}, nil)},
	} {
		rec := requestMultipart(t, h, http.MethodPost, "/api/mcp-servers/custom/files", func(w *multipart.Writer) {
			addFilePart(w, "files", upload.name, upload.content)
		})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "1MB") {
			t.Fatalf("MCP per-file limit not enforced for %s: %d %s", upload.name, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/mcp-servers/custom/files/src/server.py", nil)
	dl := httptest.NewRecorder()
	h.ServeHTTP(dl, req)
	if dl.Code != 200 || !bytes.Equal(dl.Body.Bytes(), []byte("print('ok')")) {
		t.Fatalf("download=%d %q", dl.Code, dl.Body.Bytes())
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/mcp-servers/custom/files/src/server.py", nil)
	del := httptest.NewRecorder()
	h.ServeHTTP(del, req)
	if del.Code != 200 {
		t.Fatalf("delete=%d %s", del.Code, del.Body.String())
	}
}
