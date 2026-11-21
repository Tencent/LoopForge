package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/engine"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestPreviewStudioCSP(t *testing.T) {
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, tc := range []struct {
		name, publicURL, parents, origin string
	}{
		{"disabled", "", "", ""},
		{"unconfigured", "", "https://studio.example", ""},
		{"https", "https://preview.example", "https://studio.example", "https://preview.example"},
		{"http with port", "http://preview.example:8090", "http://studio.example:8090", "http://preview.example:8090"},
		{"origin only", "https://preview.example:8443/ignored?query=1#fragment", "https://studio.example http://localhost:8090", "https://preview.example:8443"},
		{"same hostname", "https://studio.example", "https://studio.example", ""},
		{"same hostname other port", "http://studio.example:8443", "https://studio.example", ""},
		{"same hostname canonicalized", "https://STUDIO.example.", "https://studio.example", ""},
		{"missing parents", "https://preview.example", "", ""},
		{"wildcard parent", "https://preview.example", "https://*.example", ""},
		{"wildcard preview", "https://*.example", "https://studio.example", ""},
		{"credentials", "https://user@preview.example", "https://studio.example", ""},
		{"invalid scheme", "javascript://preview.example", "https://studio.example", ""},
		{"directive injection", "https://preview.example;frame-src *", "https://studio.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTFLOW_PREVIEW_PUBLIC_URL", tc.publicURL)
			t.Setenv("AGENTFLOW_PREVIEW_FRAME_ANCESTORS", tc.parents)
			en := engine.New(st, runtime.NewManager(t.TempDir()))
			if tc.name != "disabled" {
				en.SetPreview(preview.NewService(st, nil, preview.LoadConfig()))
			}
			// The middleware must use the service's config, not reload the env
			// independently of the gateway and session URL signer.
			t.Setenv("AGENTFLOW_PREVIEW_PUBLIC_URL", "https://unrelated.example")
			handler := New(st, en, nil, "", "")
			for _, path := range []string{"/", "/index.html", "/?view=runs", "/login.html", "/chatbot/", "/chatbot/preview.html", "/vscode.html", "/vscode-web/", "/extensions/", "/api/health"} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "https://studio.example"+path, nil)
				req.Header.Set("X-Forwarded-Host", "spoofed.example")
				req.Header.Set("X-Forwarded-Proto", "http")
				handler.ServeHTTP(rec, req)
				policy := rec.Header().Get("Content-Security-Policy")
				directives := make(map[string]string)
				for _, directive := range strings.Split(policy, ";") {
					name, value, _ := strings.Cut(strings.TrimSpace(directive), " ")
					directives[name] = value
				}
				studio := req.URL.Path == "/" || req.URL.Path == "/index.html"
				wantFrames := "'self' blob:"
				if studio && tc.origin != "" {
					wantFrames += " " + tc.origin
				}
				wantConnect := "'self'"
				if path == "/chatbot/preview.html" {
					wantFrames = "" // This document falls back to default-src 'none'.
					wantConnect = "'none'"
				}
				if directives["frame-src"] != wantFrames || directives["connect-src"] != wantConnect {
					t.Errorf("%s: unexpected frame/connect policy: %s", path, policy)
				}
				if studio {
					if rec.Code != http.StatusOK || directives["script-src"] != "'self'" || directives["frame-ancestors"] != "'none'" || rec.Header().Get("X-Frame-Options") != "DENY" {
						t.Errorf("%s: studio headers changed: status=%d headers=%v", path, rec.Code, rec.Header())
					}
				}
			}
		})
	}
}

// TestPreviewStudioCSPAfterLateSetPreview attaches the preview service only
// after New has built the routes. The studio page must still allow the
// preview origin in frame-src, otherwise the embedded preview iframe is
// blocked by the platform's own CSP.
func TestPreviewStudioCSPAfterLateSetPreview(t *testing.T) {
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	t.Setenv("AGENTFLOW_PREVIEW_PUBLIC_URL", "https://preview.example")
	t.Setenv("AGENTFLOW_PREVIEW_FRAME_ANCESTORS", "https://studio.example")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	en := engine.New(st, runtime.NewManager(t.TempDir()))
	handler := New(st, en, nil, "", "")
	frameSrc := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://studio.example/", nil))
		for _, directive := range strings.Split(rec.Header().Get("Content-Security-Policy"), ";") {
			if name, value, ok := strings.Cut(strings.TrimSpace(directive), " "); ok && name == "frame-src" {
				return value
			}
		}
		return ""
	}
	if got := frameSrc(); got != "'self' blob:" {
		t.Fatalf("frame-src before SetPreview = %q, want %q", got, "'self' blob:")
	}
	en.SetPreview(preview.NewService(st, nil, preview.LoadConfig()))
	if got, want := frameSrc(), "'self' blob: https://preview.example"; got != want {
		t.Fatalf("frame-src after SetPreview = %q, want %q", got, want)
	}
}
