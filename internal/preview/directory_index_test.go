package preview

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestPreviewServesNestedDirectoryIndex(t *testing.T) {
	svc, st := cvSPreviewService(t)
	bundle := archiveOf(t, svc.limits(), map[string]string{
		"index.html":       "<html><head></head><body>Root page</body></html>",
		"guide/index.html": `<html><head></head><body>Guide page<img src="diagram.svg"><script src="/assets/app.js"></script></body></html>`,
	})
	svc.store = &countingStore{blobs: map[string][]byte{"site": bundle}, opens: map[string]int{}}
	preview := domain.RunPreview{ID: "nested", RunID: "nested-run", Status: domain.PreviewReady, EntryFile: "index.html", StorageKey: "site", UpdatedAt: time.Now()}
	if err := st.PutRun(domain.Run{ID: preview.RunID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPreview(preview); err != nil {
		t.Fatal(err)
	}
	_, publicURL, err := svc.CreateSession(preview, httptest.NewRequest("GET", "https://preview.example/", nil), "")
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(strings.TrimPrefix(publicURL, "https://preview.example"), "/")
	for _, member := range []string{"guide/", "guide/index.html"} {
		req := httptest.NewRequest(http.MethodGet, publicURL+member, nil)
		req.Header.Set("Accept", "text/html")
		rr := httptest.NewRecorder()
		svc.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Guide page") || strings.Contains(rr.Body.String(), "Root page") {
			t.Fatalf("%s returned %d: %s", member, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `<base href="`+prefix+`/guide/">`) || !strings.Contains(rr.Body.String(), `src="`+prefix+`/assets/app.js"`) {
			t.Fatalf("%s lost relative-directory or root-asset addressing: %s", member, rr.Body.String())
		}
	}
	for _, spa := range []bool{false, true} {
		preview.SPAFallback = spa
		if err := st.PutPreview(preview); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, publicURL+"missing/", nil)
		req.Header.Set("Accept", "text/html")
		rr := httptest.NewRecorder()
		svc.ServeHTTP(rr, req)
		if spa && (rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Root page")) || !spa && rr.Code != http.StatusNotFound {
			t.Errorf("missing directory with SPA=%v: status %d", spa, rr.Code)
		}
	}
}

func TestPreviewRootRedirectPreservesQuery(t *testing.T) {
	svc := &Service{}
	prefix := PreviewBasePath(strings.Repeat("ab", 16))
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, prefix+"?lang=zh&next=%2Fguide", nil))
	if got, want := rr.Header().Get("Location"), prefix+"/?lang=zh&next=%2Fguide"; got != want {
		t.Fatalf("Location=%q, want %q", got, want)
	}
}
