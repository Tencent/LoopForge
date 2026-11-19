package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
)

func TestWaitRunReadsOutcomeAndChecksAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, owner := workbenchFixture(t)
	en := engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	api := New(h.repo, en, nil, "", "")
	if err := h.repo.PutUser(domain.User{LoginName: "other-user"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("other-user")
	for _, tc := range []struct {
		status   string
		terminal bool
		timedOut bool
	}{
		{"succeeded", true, false},
		{"failed", true, false},
		{"cancelled", true, false},
		{"waiting", false, false},
		{"paused", false, false},
		{"running", false, true},
		{"queued", false, true},
	} {
		run := domain.Run{ID: "sop-" + tc.status, Status: tc.status, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}}
		if err := h.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
		c, rec := workbenchContext(owner, http.MethodGet, "/api/runs/"+run.ID+"/wait?timeoutSec=0", bytes.NewBuffer(nil))
		api.ServeHTTP(rec, c.Request)
		var got runWaitResult
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("wait %s: %d %s", tc.status, rec.Code, rec.Body)
		}
		if got.RunID != run.ID || got.Status != tc.status || got.Terminal != tc.terminal || got.TimedOut != tc.timedOut {
			t.Errorf("wait %s: %+v", tc.status, got)
		}
	}
	for _, tc := range []struct {
		user domain.User
		path string
		code int
	}{
		{other, "/api/runs/sop-running/wait", http.StatusForbidden},
		{owner, "/api/runs/missing/wait", http.StatusNotFound},
		{owner, "/api/runs/sop-running/wait?timeoutSec=26", http.StatusBadRequest},
	} {
		c, rec := workbenchContext(tc.user, http.MethodGet, tc.path, bytes.NewBuffer(nil))
		api.ServeHTTP(rec, c.Request)
		if rec.Code != tc.code {
			t.Errorf("%s: got %d, want %d (%s)", tc.path, rec.Code, tc.code, rec.Body)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(owner)),
		http.MethodGet, "/api/runs/sop-running/wait", nil)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("cancelled wait: %d %s", rec.Code, rec.Body)
	}
}
