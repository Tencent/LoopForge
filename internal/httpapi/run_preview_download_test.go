package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func previewDownloadFixture(t *testing.T, store *repo.Store) (http.Handler, domain.RunPreview, map[string]string) {
	t.Helper()
	eng := engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	blobs := preview.NewDBStore(store)
	svc := preview.NewService(store, blobs, preview.Config{MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20})
	eng.SetPreview(svc)
	run := domain.Run{
		ID: "run-preview-download", Status: "succeeded", WorkspaceCleanedAt: time.Now().UTC(),
		WorkflowID: "preview-download-sop", Nodes: map[string]*domain.NodeRun{},
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: "preview-download-sop", Name: "前端静态站点交付", Version: 1,
			Preview: &domain.PreviewContract{Enabled: true, Kind: domain.PreviewKindStatic, SourcePath: "dist"},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":     `<html><head></head><body><script src="/assets/app.js"></script></body></html>`,
		"assets/app.js":  `fetch("/api/items")`,
		"assets/app.css": "body{color:red}",
	}
	build := t.TempDir()
	for name, body := range files {
		full := filepath.Join(build, "dist", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(build, ".env"), []byte("must not be downloaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	result, err := preview.ArchiveDir(build, "dist", "index.html", preview.Limits{MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.Put(context.Background(), result.StorageKey, &bundle, result.CompressedSize, result.SHA256); err != nil {
		t.Fatal(err)
	}
	p := domain.RunPreview{
		ID: "preview-download", RunID: run.ID, Kind: domain.PreviewKindStatic, Status: domain.PreviewReady,
		StorageKey: result.StorageKey, ContentSHA256: result.SHA256, EntryFile: "index.html", FileCount: result.FileCount,
		ExpiresAt: time.Now().UTC().Add(time.Hour), BackendURL: "https://backend.test",
	}
	if err := store.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(build); err != nil {
		t.Fatal(err)
	}
	return New(store, eng, tracker.NewService(), "", ""), p, files
}

func assertPreviewDownload(t *testing.T, h http.Handler, p domain.RunPreview, files map[string]string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/"+p.RunID+"/preview/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d %s", rec.Code, rec.Body.String())
	}
	media, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || media != "attachment" || params["filename"] != "preview-"+p.RunID+".zip" {
		t.Fatalf("attachment header: %s, %v", rec.Header().Get("Content-Disposition"), err)
	}
	if rec.Header().Get("Content-Type") != "application/zip" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download headers: %v", rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(files) {
		t.Fatalf("unexpected ZIP file count: %d", len(zr.File))
	}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		_ = r.Close()
		want, exists := files[file.Name]
		if err != nil || !exists || string(body) != want {
			t.Fatalf("download changed original build file %s: %q, %v", file.Name, body, err)
		}
	}
	rangeReq := httptest.NewRequest(http.MethodGet, "/api/runs/"+p.RunID+"/preview/download", nil)
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeRec := httptest.NewRecorder()
	h.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent || !bytes.Equal(rangeRec.Body.Bytes(), rec.Body.Bytes()[:4]) {
		t.Fatalf("range response: %d %q", rangeRec.Code, rangeRec.Body.Bytes())
	}
}

func TestPreviewDownloadAPI(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "preview-download-test-secret")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	h, p, files := previewDownloadFixture(t, store)
	assertPreviewDownload(t, h, p, files)
	for _, tc := range []struct {
		name, status, storageKey string
		expiresAt                time.Time
		want                     int
	}{
		{"pending", domain.PreviewPending, p.StorageKey, p.ExpiresAt, http.StatusConflict},
		{"archiving", domain.PreviewArchiving, p.StorageKey, p.ExpiresAt, http.StatusConflict},
		{"failed", domain.PreviewFailed, p.StorageKey, p.ExpiresAt, http.StatusConflict},
		{"expired", domain.PreviewExpired, p.StorageKey, p.ExpiresAt, http.StatusGone},
		{"deleted", domain.PreviewDeleted, p.StorageKey, p.ExpiresAt, http.StatusGone},
		{"deleting", domain.PreviewDeleting, p.StorageKey, p.ExpiresAt, http.StatusGone},
		{"expired before GC", domain.PreviewReady, p.StorageKey, time.Now().Add(-time.Second), http.StatusGone},
		{"missing blob", domain.PreviewReady, strings.Repeat("f", 64), p.ExpiresAt, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := p
			changed.Status, changed.StorageKey, changed.ExpiresAt = tc.status, tc.storageKey, tc.expiresAt
			if err := store.PutPreview(changed); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/"+p.RunID+"/preview/download", nil))
			if rec.Code != tc.want || rec.Header().Get("Content-Disposition") != "" {
				t.Fatalf("download: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, runID := range []string{"missing-run", "run-without-preview"} {
		if runID == "run-without-preview" {
			if err := store.PutRun(domain.Run{ID: runID, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/"+runID+"/preview/download", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("missing preview: %d %s", rec.Code, rec.Body.String())
		}
	}
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/"+p.RunID+"/preview/download", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download: %d %s", rec.Code, rec.Body.String())
	}
	if err := store.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	signed, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: 1, LoginName: "demo-owner", AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+p.RunID+"/preview/download", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: signed})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("authenticated download: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMySQLPreviewDownloadAPI(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	store, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	h, p, files := previewDownloadFixture(t, store)
	assertPreviewDownload(t, h, p, files)
}
