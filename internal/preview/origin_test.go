package preview

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPreviewRequiresIsolatedOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, host, ancestors string
	}{
		{"unconfigured", "", ""},
		{"same host", "studio.example", "https://studio.example"},
		{"same host different port", "studio.example:8443", "https://studio.example"},
		{"same host canonicalized", "STUDIO.example.", "https://studio.example"},
		{"self", "preview.example", "'self'"},
		{"wildcard", "preview.example", "https://*.example"},
		{"parent path", "preview.example", "https://studio.example/app"},
		{"parent credentials", "preview.example", "https://user@studio.example"},
		{"parent query", "preview.example", "https://studio.example?x=1"},
		{"parent missing", "preview.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{cfg: Config{PublicScheme: "https", PublicHost: tc.host, FrameAncestors: tc.ancestors}}
			req := httptest.NewRequest("GET", "https://studio.example/", nil)
			if err := s.validateSessionOrigin(req); !errors.Is(err, ErrIsolatedOriginRequired) {
				t.Fatalf("unsafe configuration accepted: %v", err)
			}
			if got := s.PublicURL(strings.Repeat("a", 32), req); got != "" {
				t.Fatalf("unsafe public URL: %s", got)
			}
		})
	}
}

func TestPreviewOriginAndBackendBoundaries(t *testing.T) {
	s := &Service{cfg: Config{PublicScheme: "https", PublicHost: "preview.example:8443", FrameAncestors: "https://studio.example http://localhost:8090"}}
	req := httptest.NewRequest("GET", "http://studio.example/", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("X-Forwarded-Host", "studio.example")
	if err := s.validateSessionOrigin(req); err != nil {
		t.Fatal(err)
	}
	if got := s.PublicURL("token", req); got != "https://preview.example:8443/preview/token/" {
		t.Fatalf("request changed configured origin: %s", got)
	}
	if err := s.validateSessionOrigin(httptest.NewRequest("GET", "https://other.example/", nil)); err == nil {
		t.Fatal("unlisted studio accepted")
	}
	for _, backend := range []string{"https://studio.example/api", "http://STUDIO.example.:8080", "http://localhost:9999"} {
		if err := s.validateBackendOrigin(backend); !errors.Is(err, domain.ErrInvalidPreviewBackendURL) {
			t.Errorf("studio backend %q accepted: %v", backend, err)
		}
	}
	for _, backend := range []string{"", "https://api.example/v1"} {
		if err := s.validateBackendOrigin(backend); err != nil {
			t.Errorf("backend %q rejected: %v", backend, err)
		}
	}
	for _, target := range []string{"https://preview.example:8443/", "http://PREVIEW.example.:8090/"} {
		if !s.IsPreviewHost(httptest.NewRequest("GET", target, nil)) {
			t.Errorf("preview hostname not reserved: %s", target)
		}
	}
}

// Missing or invalid embedding settings disable sessions, but the configured
// preview hostname must stay reserved: it may still hold preview documents.
func TestPreviewHostStaysReservedWithoutEmbedding(t *testing.T) {
	for _, ancestors := range []string{"", "'self'", "https://*.example", "https://studio.example/app", "'self' https://studio.example"} {
		t.Run(ancestors, func(t *testing.T) {
			s := &Service{cfg: Config{PublicScheme: "https", PublicHost: "preview.example", FrameAncestors: ancestors}}
			for _, target := range []string{"https://preview.example/", "http://PREVIEW.example.:8090/api/health"} {
				if !s.IsPreviewHost(httptest.NewRequest("GET", target, nil)) {
					t.Errorf("preview hostname released: %s", target)
				}
			}
			if s.IsPreviewHost(httptest.NewRequest("GET", "https://studio.example/", nil)) {
				t.Error("studio hostname reserved")
			}
			if got := s.ReservedHost(); got != "preview.example" {
				t.Errorf("ReservedHost = %q", got)
			}
			if err := s.IsolationError(); !errors.Is(err, ErrIsolatedOriginRequired) {
				t.Errorf("incomplete configuration accepted: %v", err)
			}
			if got := s.PublicOrigin(); got != "" {
				t.Errorf("incomplete configuration widened frame-src: %s", got)
			}
			if err := s.validateSessionOrigin(httptest.NewRequest("GET", "https://studio.example/", nil)); !errors.Is(err, ErrIsolatedOriginRequired) {
				t.Errorf("session allowed: %v", err)
			}
		})
	}
}

// Older deployments pointed AGENTFLOW_PREVIEW_PUBLIC_URL at the studio. A
// hostname known to be a studio must never be reserved, or the studio 404s.
func TestPreviewNeverReservesStudioHostname(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"listed in frame ancestors", Config{PublicHost: "studio.example", FrameAncestors: "https://studio.example"}},
		{"listed among other ancestors", Config{PublicHost: "studio.example:8443", FrameAncestors: "https://other.example 'self' https://STUDIO.example."}},
		{"studio public URL", Config{PublicHost: "studio.example", StudioURL: "https://studio.example/app/"}},
		{"studio public URL without scheme", Config{PublicHost: "studio.example", StudioURL: "studio.example:8090"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.PublicScheme = "https"
			s := &Service{cfg: tc.cfg}
			if s.IsPreviewHost(httptest.NewRequest("GET", "https://studio.example/api/health", nil)) {
				t.Fatal("studio hostname reserved for previews")
			}
			if got := s.ReservedHost(); got != "" {
				t.Fatalf("ReservedHost = %q", got)
			}
			if err := s.IsolationError(); !errors.Is(err, ErrIsolatedOriginRequired) {
				t.Fatalf("studio hostname accepted as preview origin: %v", err)
			}
		})
	}
	// The studio's public URL is also a studio hostname for backends.
	s := &Service{cfg: Config{PublicScheme: "https", PublicHost: "preview.example",
		FrameAncestors: "https://studio.example", StudioURL: "https://app.example"}}
	if err := s.IsolationError(); err != nil {
		t.Fatal(err)
	}
	if err := s.validateBackendOrigin("https://app.example/api"); !errors.Is(err, ErrStudioBackend) || !errors.Is(err, domain.ErrInvalidPreviewBackendURL) {
		t.Fatalf("studio public URL accepted as backend: %v", err)
	}
}

func TestPreviewGatewayNeedsCompleteIsolation(t *testing.T) {
	s, st := cvSPreviewService(t)
	run := domain.Run{ID: "incomplete-origin"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	p := domain.RunPreview{ID: run.ID, RunID: run.ID, Status: domain.PreviewPending, SourcePath: "dist", EntryFile: "index.html"}
	if err := st.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html><body>isolated</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveLocal(context.Background(), run, p, workspace); err != nil {
		t.Fatal(err)
	}
	p, _ = st.Preview(p.ID)
	_, link, err := s.CreateSession(p, httptest.NewRequest("POST", "https://studio.example/api", nil), "")
	if err != nil {
		t.Fatal(err)
	}
	serve := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
		return w
	}
	if w := serve(); w.Code != 200 || !strings.Contains(w.Body.String(), "isolated") {
		t.Fatalf("complete isolation should serve the session: %d %s", w.Code, w.Body.String())
	}
	// A session minted before the embedding settings were removed is not served.
	s.cfg.FrameAncestors = ""
	if w := serve(); w.Code != 404 || strings.Contains(w.Body.String(), "isolated") {
		t.Fatalf("session served without complete isolation: %d %s", w.Code, w.Body.String())
	}
}

func TestPreviewBackendCanBeClearedWithoutIsolation(t *testing.T) {
	s, st := cvSPreviewService(t)
	p := domain.RunPreview{ID: "legacy-backend", RunID: "legacy-backend", Status: domain.PreviewReady, StorageKey: "blob", BackendURL: "https://studio.example"}
	if err := st.PutRun(domain.Run{ID: p.RunID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	s.cfg.FrameAncestors = ""
	if _, err := s.SetPreviewBackend(p.ID, "https://api.example"); !errors.Is(err, ErrIsolatedOriginRequired) {
		t.Fatalf("unvalidated backend saved: %v", err)
	}
	updated, err := s.SetPreviewBackend(p.ID, "")
	if err != nil || updated.BackendURL != "" {
		t.Fatalf("clear backend: %+v %v", updated, err)
	}
	if stored, _ := st.Preview(p.ID); stored.BackendURL != "" {
		t.Fatalf("backend not cleared: %q", stored.BackendURL)
	}
}

func TestPreviewRevalidatesStoredBackend(t *testing.T) {
	s, st := cvSPreviewService(t)
	run := domain.Run{ID: "stored-origin"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	p := domain.RunPreview{ID: "stored-origin", RunID: run.ID, Status: domain.PreviewReady, StorageKey: "blob"}
	if err := st.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	token, hash, err := newOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutPreviewSession(domain.PreviewSession{PreviewID: p.ID, TokenHash: hash,
		BackendURL: "https://studio.example", ExpiresAt: s.clock().Add(s.cfg.SessionTTL)}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://preview.example"+PreviewBasePath(token)+"/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "studio hostname") {
		t.Fatalf("old unsafe session was not rejected: %d %s", w.Code, w.Body.String())
	}
}
