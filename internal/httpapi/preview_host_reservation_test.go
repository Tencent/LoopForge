package httpapi

import (
	"context"
	"encoding/json"
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
)

type reservationEnv struct {
	handler http.Handler
	store   *repo.Store
	svc     *preview.Service
	cookie  *http.Cookie
}

// newReservationEnv builds the production router from environment settings,
// with a signed viewer cookie, so the checks run through every middleware.
func newReservationEnv(t *testing.T, publicURL, ancestors, studioURL string) reservationEnv {
	t.Helper()
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "preview-reservation-test-secret")
	t.Setenv("AGENTFLOW_PREVIEW_PUBLIC_URL", publicURL)
	t.Setenv("AGENTFLOW_PREVIEW_FRAME_ANCESTORS", ancestors)
	t.Setenv("AGENTFLOW_PUBLIC_URL", studioURL)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "reservation-viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := st.UserByLoginName("reservation-viewer")
	signed, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: viewer.ID, LoginName: viewer.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	en := engine.New(st, runtime.NewManager(t.TempDir()))
	svc := preview.NewService(st, nil, preview.LoadConfig())
	en.SetPreview(svc)
	return reservationEnv{handler: New(st, en, nil, "", ""), store: st, svc: svc,
		cookie: &http.Cookie{Name: authCookieName, Value: signed}}
}

func (e reservationEnv) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e reservationEnv) archive(t *testing.T, runID, backend string) {
	t.Helper()
	run := domain.Run{ID: runID, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := e.store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	p := domain.RunPreview{ID: runID, RunID: runID, Status: domain.PreviewPending, SourcePath: "dist", EntryFile: "index.html"}
	if err := e.store.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html><body>reserved</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ArchiveLocal(context.Background(), run, p, workspace); err != nil {
		t.Fatal(err)
	}
	// Simulate a backend stored before the upgrade, bypassing today's checks.
	if backend != "" {
		if err := e.store.UpdatePreviewBackendURL(p.ID, backend); err != nil {
			t.Fatal(err)
		}
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return out
}

// A missing or invalid FRAME_ANCESTORS only disables sessions. The preview
// hostname must keep returning 404 for studio pages and APIs, even with a
// valid studio cookie.
func TestPreviewHostReservedWithoutFrameAncestors(t *testing.T) {
	for _, ancestors := range []string{"https://studio.example.test", "", "'self'", "https://*.example.test", "'self' https://studio.example.test"} {
		t.Run(ancestors, func(t *testing.T) {
			env := newReservationEnv(t, "https://preview.example.test", ancestors, "")
			complete := env.svc.IsolationError() == nil
			if complete != (ancestors == "https://studio.example.test") {
				t.Fatalf("unexpected isolation state: %v", env.svc.IsolationError())
			}
			for _, path := range []string{"/", "/index.html", "/login.html", "/api/health", "/api/auth/current", "/api/bootstrap", "/mcp", "/chatbot/"} {
				for _, host := range []string{"https://preview.example.test", "http://PREVIEW.example.test.:8090"} {
					rec := env.do(t, http.MethodGet, host+path, "")
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s%s = %d, want 404", host, path, rec.Code)
					}
					// An incomplete setup explains the reservation to operators.
					if explained := strings.Contains(rec.Body.String(), "reserved for previews"); explained == complete {
						t.Errorf("%s%s: explanation=%v with complete=%v: %q", host, path, explained, complete, rec.Body.String())
					}
				}
			}
			// Positive control: the cookie is valid on the studio hostname.
			rec := env.do(t, http.MethodGet, "https://studio.example.test/api/auth/current", "")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "reservation-viewer") {
				t.Fatalf("studio rejected the viewer: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Before isolation, AGENTFLOW_PREVIEW_PUBLIC_URL was documented as the studio's
// own public origin. When the settings identify it as a studio, keep serving it.
func TestPreviewLegacyStudioPublicURLKeepsStudio(t *testing.T) {
	for _, tc := range []struct{ name, publicURL, ancestors, studioURL string }{
		{"listed as frame ancestor", "https://studio.example.test", "https://studio.example.test", ""},
		{"studio public URL", "https://studio.example.test", "", "https://studio.example.test"},
		{"studio public URL with self", "https://studio.example.test", "'self'", "https://studio.example.test/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newReservationEnv(t, tc.publicURL, tc.ancestors, tc.studioURL)
			for _, path := range []string{"/", "/api/health", "/api/auth/current"} {
				if rec := env.do(t, http.MethodGet, "https://studio.example.test"+path, ""); rec.Code != http.StatusOK {
					t.Errorf("studio %s = %d, want 200", path, rec.Code)
				}
			}
			env.archive(t, "legacy-run", "")
			rec := env.do(t, http.MethodPost, "https://studio.example.test/api/runs/legacy-run/preview/session", "")
			if rec.Code != http.StatusConflict || decodeBody(t, rec)["code"] != "preview_isolation_required" {
				t.Fatalf("session without isolation: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Session failures carry stable codes. The archive's download and backend
// repair keep working without a session.
func TestPreviewSessionFailuresKeepArchiveActions(t *testing.T) {
	t.Run("isolation not configured", func(t *testing.T) {
		env := newReservationEnv(t, "https://preview.example.test", "", "")
		env.archive(t, "unconfigured", "")
		base := "https://studio.example.test/api/runs/unconfigured/preview"
		if rec := env.do(t, http.MethodGet, base, ""); rec.Code != http.StatusOK || decodeBody(t, rec)["status"] != "ready" {
			t.Fatalf("metadata %d %s", rec.Code, rec.Body.String())
		}
		rec := env.do(t, http.MethodPost, base+"/session", "")
		body := decodeBody(t, rec)
		if rec.Code != http.StatusConflict || body["code"] != "preview_isolation_required" || !strings.Contains(body["error"].(string), "FRAME_ANCESTORS is empty") {
			t.Fatalf("session %d %s", rec.Code, rec.Body.String())
		}
		if rec := env.do(t, http.MethodGet, base+"/download", ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("download %d %s", rec.Code, rec.Header().Get("Content-Type"))
		}
		// Saving a backend that cannot be validated is refused with the same code.
		rec = env.do(t, http.MethodPut, base+"/backend", `{"backendUrl":"https://api.example.test"}`)
		if rec.Code != http.StatusConflict || decodeBody(t, rec)["code"] != "preview_isolation_required" {
			t.Fatalf("backend without isolation %d %s", rec.Code, rec.Body.String())
		}
		// Clearing succeeds and reports why no session followed.
		rec = env.do(t, http.MethodPut, base+"/backend", `{"backendUrl":""}`)
		body = decodeBody(t, rec)
		sessionErr, _ := body["sessionError"].(map[string]any)
		if rec.Code != http.StatusOK || body["session"] != nil || sessionErr["code"] != "preview_isolation_required" || sessionErr["error"] == "" {
			t.Fatalf("clear backend %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("stored studio backend", func(t *testing.T) {
		env := newReservationEnv(t, "https://preview.example.test", "https://studio.example.test", "")
		env.archive(t, "legacy-backend", "https://studio.example.test")
		base := "https://studio.example.test/api/runs/legacy-backend/preview"
		rec := env.do(t, http.MethodPost, base+"/session", "")
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != "preview_backend_on_studio" {
			t.Fatalf("stored studio backend %d %s", rec.Code, rec.Body.String())
		}
		rec = env.do(t, http.MethodPost, base+"/session", `{"backendUrl":"https://studio.example.test:8443/api"}`)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != "preview_backend_on_studio" {
			t.Fatalf("explicit studio backend %d %s", rec.Code, rec.Body.String())
		}
		if rec := env.do(t, http.MethodGet, base+"/download", ""); rec.Code != http.StatusOK {
			t.Fatalf("download %d", rec.Code)
		}
		rec = env.do(t, http.MethodPut, base+"/backend", `{"backendUrl":"https://studio.example.test/v2"}`)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["code"] != "preview_backend_on_studio" {
			t.Fatalf("studio backend saved %d %s", rec.Code, rec.Body.String())
		}
		// Clearing the stored backend repairs the preview from the studio UI.
		rec = env.do(t, http.MethodPut, base+"/backend", `{"backendUrl":""}`)
		body := decodeBody(t, rec)
		session, _ := body["session"].(map[string]any)
		if rec.Code != http.StatusOK || body["sessionError"] != nil || !strings.HasPrefix(session["url"].(string), "https://preview.example.test/preview/") {
			t.Fatalf("clear backend %d %s", rec.Code, rec.Body.String())
		}
		page := env.do(t, http.MethodGet, session["url"].(string), "")
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "reserved") {
			t.Fatalf("repaired preview %d %s", page.Code, page.Body.String())
		}
		rec = env.do(t, http.MethodPost, base+"/session", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("session after repair %d %s", rec.Code, rec.Body.String())
		}
	})
}
