package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestPreviewDisabledMissingRunAndMissingPreview(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/runs/missing/preview", nil},
		{http.MethodGet, "/api/runs/missing/preview/download", nil},
		{http.MethodPost, "/api/runs/missing/preview/session", nil},
		{http.MethodPut, "/api/runs/missing/preview/backend", map[string]any{"backendUrl": "https://example.test"}},
		{http.MethodDelete, "/api/runs/missing/preview", nil},
		{http.MethodDelete, "/api/preview-sessions/missing", nil},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != http.StatusNotFound {
			t.Errorf("disabled %s = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	eng := engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	eng.SetPreview(preview.NewService(store, preview.NewDBStore(store), preview.Config{
		PublicScheme: "http", MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20,
		DefaultRetention: time.Hour, FinalizationTimeout: time.Minute, SessionTTL: time.Hour,
	}))
	handler := New(store, eng, tracker.NewService(), "", "")
	run := domain.Run{ID: "cvb-r2-preview-run", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	_, runnerSession := registerTestRunner(t, handler)
	request := func(method, path string) int {
		rec := requestWithHeaders(t, handler, method, path, nil, nil)
		return rec.Code
	}
	for _, path := range []string{
		"/api/runs/missing/preview", "/api/runs/missing/preview/download",
		"/api/runs/missing/preview/session", "/api/runs/missing/preview/backend",
	} {
		method := http.MethodGet
		if path[len(path)-7:] == "session" {
			method = http.MethodPost
		} else if path[len(path)-7:] == "backend" {
			method = http.MethodPut
		}
		if code := request(method, path); code != http.StatusNotFound {
			t.Errorf("missing run %s = %d", path, code)
		}
	}
	for _, path := range []string{
		"/api/runs/" + run.ID + "/preview", "/api/runs/" + run.ID + "/preview/download",
		"/api/runs/" + run.ID + "/preview/session", "/api/runs/" + run.ID + "/preview/backend",
	} {
		method := http.MethodGet
		if path[len(path)-7:] == "session" {
			method = http.MethodPost
		} else if path[len(path)-7:] == "backend" {
			method = http.MethodPut
		}
		if code := request(method, path); code != http.StatusNotFound {
			t.Errorf("missing preview %s = %d", path, code)
		}
	}
	out := publicPreview(domain.RunPreview{ID: "p", RunID: run.ID, ErrorText: "failed"})
	if out["error"] != "failed" {
		t.Fatal("public preview omitted error")
	}
	runnerHeaders := map[string]string{"Authorization": "Bearer " + runnerSession}
	if rec := requestWithHeaders(t, handler, http.MethodPut, "/runners/previews/missing/content", nil, runnerHeaders); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing upload token = %d: %s", rec.Code, rec.Body.String())
	}
	runnerHeaders["X-Preview-Upload-Token"] = "invalid"
	if rec := requestWithHeaders(t, handler, http.MethodPut, "/runners/previews/missing/content", nil, runnerHeaders); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid upload = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceRestoreStatusActivityAndSync(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/runs/missing/workspace/restore"},
		{http.MethodPost, "/api/runs/missing/workspace/use"},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, nil, true)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	run := domain.Run{ID: "cvb-r2-restore", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: f.user.ID},
		Nodes:            map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/workspace/use", nil, true); rec.Code != http.StatusOK {
		t.Errorf("activity = %d: %s", rec.Code, rec.Body.String())
	}
	if state, err := f.e.WorkspaceRestoreStatus(run.ID); err == nil && state.Status != "" {
		t.Logf("initial restore state: %+v", state)
	}
}
