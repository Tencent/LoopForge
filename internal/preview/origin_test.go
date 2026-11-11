package preview

import (
	"errors"
	"net/http/httptest"
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
