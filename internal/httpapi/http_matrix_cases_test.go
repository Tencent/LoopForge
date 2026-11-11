package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

type cvBHTTPFixture struct {
	h                    http.Handler
	s                    *repo.Store
	e                    *engine.Engine
	user                 domain.User
	notificationProvider *cvBNotificationProviderRound2
}

func cvBNewHTTPFixture(t *testing.T) cvBHTTPFixture {
	t.Helper()
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "cvb-coverage-session-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	user, ok := s.User(1)
	if !ok {
		t.Fatal("OpenTest user 1 missing")
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	// HTTP unit tests must never bootstrap or send through the real bot provider.
	providerErr := errors.New("notification provider unavailable in unit test")
	provider := &cvBNotificationProviderRound2{err: providerErr, sendErr: providerErr}
	notifications := notification.New(s)
	notifications.Provider = provider
	return cvBHTTPFixture{h: NewWithNotifications(s, e, tracker.NewService(), "", "", notifications), s: s, e: e, user: user, notificationProvider: provider}
}

func cvBRequest(t *testing.T, f cvBHTTPFixture, method, path string, body any, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req = authedRequest(t, req, f.user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestProjectBusinessAndNotificationHTTP(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	cases := []struct {
		method string
		path   string
		body   any
		auth   bool
		want   int
	}{
		{http.MethodGet, "/api/projects", nil, false, 200},
		{http.MethodGet, "/api/projects/missing", nil, false, 404},
		{http.MethodPost, "/api/projects", map[string]any{}, true, 400},
		{http.MethodPost, "/api/projects", map[string]any{"id": "cvb-project", "name": "CVB", "ownerId": 999999}, true, 400},
		{http.MethodPost, "/api/projects", map[string]any{"id": "cvb-project", "name": "CVB", "members": []any{map[string]any{"userId": 999999}}}, true, 400},
		{http.MethodPost, "/api/projects", map[string]any{"id": "cvb-project", "name": "CVB", "defaultRepositoryId": "missing"}, true, 400},
		{http.MethodPost, "/api/projects", map[string]any{"id": "cvb-project", "name": "CVB", "ownerId": 1}, true, 200},
		{http.MethodGet, "/api/projects/cvb-project", nil, false, 200},
		{http.MethodGet, "/api/business-domains", nil, true, 200},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "bad id", "name": ""}, true, 400},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "cvb-domain", "name": "CVB domain", "managerIds": []int64{1, 1}}, true, 400},
		{http.MethodPut, "/api/business-domains", map[string]any{"id": "cvb-domain", "name": "CVB domain", "managerIds": []int64{1}}, true, 200},
		{http.MethodPut, "/api/business-domains?createOnly=true", map[string]any{"id": "cvb-domain", "name": "CVB domain"}, true, 409},
		{http.MethodGet, "/api/business-domain-report?businessDomainId=missing", nil, true, 404},
		{http.MethodGet, "/api/business-domain-report?timeZone=Local", nil, true, 400},
		{http.MethodGet, "/api/business-domain-report?grain=hour", nil, true, 400},
		{http.MethodGet, "/api/business-domain-report?from=bad", nil, true, 400},
		{http.MethodGet, "/api/business-domain-report?from=2026-10-30T00:00:00Z&to=2026-10-29T00:00:00Z", nil, true, 400},
		{http.MethodGet, "/api/business-domain-report?grain=week&timeZone=Asia%2FShanghai", nil, true, 200},
		{http.MethodGet, "/api/notification-bots?projectId=missing", nil, true, 404},
		{http.MethodPost, "/api/notification-bots", map[string]any{}, true, 400},
		{http.MethodPost, "/api/notification-bots", map[string]any{"projectId": "cvb-project", "name": "bot", "botId": "b"}, true, 400},
		{http.MethodPost, "/api/notification-bots", map[string]any{"projectId": "cvb-project", "name": "bot", "botId": "b", "secret": "s"}, true, 200},
		{http.MethodGet, "/api/notification-deliveries?projectId=cvb-project", nil, true, 200},
		{http.MethodPost, "/api/notification-deliveries/missing/retry?projectId=cvb-project", nil, true, 404},
		{http.MethodPost, "/api/notification-preview", map[string]any{"projectId": "cvb-project", "binding": map[string]any{"scope": "node", "template": "{{status}}"}}, true, 400},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, tc.auth)
		if rec.Code != tc.want {
			t.Errorf("case %d %s %s: status %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestCatalogIssueRunAndSpaceValidationHTTP(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	cases := []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodGet, "/api/repositories", nil, 200},
		{http.MethodGet, "/api/repositories/missing", nil, 404},
		{http.MethodPost, "/api/repositories", map[string]any{}, 400},
		{http.MethodGet, "/api/agents/missing", nil, 404},
		{http.MethodPut, "/api/agents", map[string]any{}, 200},
		{http.MethodDelete, "/api/agents/missing", nil, 404},
		{http.MethodPut, "/api/rules", map[string]any{}, 400},
		{http.MethodDelete, "/api/rules?id=missing", nil, 404},
		{http.MethodPut, "/api/skills", map[string]any{}, 400},
		{http.MethodDelete, "/api/skills?id=missing", nil, 404},
		{http.MethodPut, "/api/dockerfiles", map[string]any{}, 400},
		{http.MethodPut, "/api/environments", map[string]any{}, 400},
		{http.MethodPut, "/api/sandboxes", map[string]any{}, 400},
		{http.MethodPut, "/api/workflows", map[string]any{}, 200},
		{http.MethodGet, "/api/workflows/missing", nil, 404},
		{http.MethodGet, "/api/issues?limit=bad&offset=-1", nil, 400},
		{http.MethodPost, "/api/issues", map[string]any{}, 400},
		{http.MethodGet, "/api/issues/missing", nil, 404},
		{http.MethodPost, "/api/issues/missing/comments", map[string]any{}, 404},
		{http.MethodGet, "/api/runs", nil, 200},
		{http.MethodPost, "/api/runs", map[string]any{}, 400},
		{http.MethodGet, "/api/runs/missing", nil, 404},
		{http.MethodDelete, "/api/runs/missing", nil, 400},
		{http.MethodPost, "/api/runs/missing/eval", map[string]any{}, 404},
		{http.MethodGet, "/api/runs/missing/eval", nil, 404},
		{http.MethodGet, "/api/runs/missing/evals", nil, 404},
		{http.MethodPost, "/api/runs/missing/eval/apply", map[string]any{}, 404},
		{http.MethodGet, "/api/runs/missing/preview", nil, 404},
		{http.MethodDelete, "/api/runs/missing/preview", nil, 404},
		{http.MethodGet, "/api/runs/missing/workspace", nil, 404},
		{http.MethodPost, "/api/runs/missing/workspace/takeover", nil, 404},
		{http.MethodPost, "/api/runs/missing/workspace/release", nil, 404},
		{http.MethodPost, "/api/runs/missing/workspace/restore", nil, 404},
		{http.MethodGet, "/api/runs/missing/workspace/preparation", nil, 404},
		{http.MethodGet, "/api/spaces", nil, 400},
		{http.MethodPost, "/api/spaces", map[string]any{}, 400},
		{http.MethodGet, "/api/spaces/missing", nil, 404},
		{http.MethodPatch, "/api/spaces/missing", map[string]any{}, 404},
		{http.MethodPost, "/api/spaces/missing/requirement", map[string]any{}, 404},
		{http.MethodGet, "/api/spaces/missing/members", nil, 404},
		{http.MethodGet, "/api/spaces/missing/threads", nil, 404},
		{http.MethodGet, "/api/spaces/missing/artifacts", nil, 404},
		{http.MethodGet, "/api/spaces/missing/lanes", nil, 404},
		{http.MethodGet, "/api/spaces/missing/usage", nil, 404},
		{http.MethodGet, "/api/rooms", nil, 400},
		{http.MethodPost, "/api/rooms", map[string]any{}, 400},
		{http.MethodGet, "/api/rooms/missing", nil, 404},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("case %d %s %s: status %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestAuthRunnerScheduleSyncValidationHTTP(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	cases := []struct {
		method string
		path   string
		body   any
		auth   bool
		want   int
	}{
		{http.MethodGet, "/api/auth/current", nil, false, 200},
		{http.MethodGet, "/api/auth/local", nil, false, 200},
		{http.MethodPost, "/api/auth/local", map[string]any{}, false, 403},
		{http.MethodGet, "/api/auth/tokens", nil, false, 401},
		{http.MethodPost, "/api/auth/tokens", map[string]any{}, true, 400},
		{http.MethodDelete, "/api/auth/tokens/missing", nil, true, 404},
		{http.MethodGet, "/api/users", nil, false, 200},
		{http.MethodPut, "/api/users/bad/credentials", map[string]any{}, true, 400},
		{http.MethodPut, "/api/users/999/credentials", map[string]any{}, true, 403},
		{http.MethodGet, "/api/schedule-jobs", nil, true, 200},
		{http.MethodPost, "/api/schedule-jobs", map[string]any{}, true, 400},
		{http.MethodDelete, "/api/schedule-jobs/missing", nil, true, 400},
		{http.MethodPost, "/api/schedule-jobs/missing/run", nil, true, 409},
		{http.MethodGet, "/api/sync/config", nil, true, 200},
		{http.MethodGet, "/api/sync/logs", nil, true, 200},
		{http.MethodGet, "/api/sync/status", nil, true, 200},
		{http.MethodPost, "/api/sync/create", map[string]any{}, true, 400},
		{http.MethodPost, "/api/sync/create", map[string]any{"issueId": "missing", "target": "tapd"}, true, 404},
		{http.MethodGet, "/api/runners", nil, true, 200},
		{http.MethodGet, "/api/runners/default", nil, true, 200},
		{http.MethodPost, "/api/runners/register", map[string]any{}, false, 503},
		{http.MethodPost, "/api/runners/deregister", map[string]any{}, false, 401},
		{http.MethodPost, "/api/runners/disconnect", map[string]any{}, false, 401},
		{http.MethodPost, "/api/runners/heartbeat", map[string]any{}, false, 401},
		{http.MethodGet, "/api/runners/tasks", nil, true, 200},
		{http.MethodPost, "/api/runners/tasks", map[string]any{}, true, 400},
		{http.MethodPost, "/api/runners/tasks/claim", map[string]any{}, false, 401},
		{http.MethodGet, "/api/runners/tasks/missing", nil, true, 401},
		{http.MethodPost, "/api/runners/tasks/missing/cancel", nil, true, 200},
		{http.MethodPost, "/api/runners/tasks/missing/start", nil, false, 401},
		{http.MethodPost, "/api/runners/tasks/missing/lease/renew", nil, false, 401},
		{http.MethodPost, "/api/runners/tasks/missing/events", nil, false, 401},
		{http.MethodPost, "/api/runners/tasks/missing/complete", nil, false, 401},
		{http.MethodGet, "/api/runners/tasks/missing/control", nil, false, 401},
		{http.MethodPut, "/api/runners/runtimes", map[string]any{}, true, 400},
		{http.MethodPost, "/api/otel/not-traces", nil, false, 404},
	}
	for i, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, tc.auth)
		if rec.Code != tc.want {
			t.Errorf("case %d %s %s: status %d want %d: %s", i, tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestRunTaskEvalSuccessHTTP(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{
		ID: "cvb-run", Status: "succeeded", WorkflowID: "cvb-wf",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "cvb-wf", Name: "CVB", Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "a"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {Status: "succeeded", Output: "done"}},
		Artifacts:        []domain.Artifact{{Name: "report", MediaType: "text/markdown", Content: "# report"}},
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutTask(domain.Task{ID: "cvb-task", RunID: run.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	rec := domain.RunEvalRecord{
		ID: "cvb-eval", RunID: run.ID, Status: domain.RunEvalSucceeded, Score: 88, Passed: true,
		Result: domain.RunEvalResult{Score: 88, Passed: true, Summary: "ok"},
	}
	if err := f.s.PutRunEval(rec); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/tasks", nil, 200},
		{http.MethodGet, "/api/tasks/cvb-task", nil, 200},
		{http.MethodGet, "/api/tasks/missing", nil, 404},
		{http.MethodGet, "/api/workflow-metrics?projectId=platform", nil, 200},
		{http.MethodGet, "/api/analytics?origin=bad", nil, 400},
		{http.MethodGet, "/api/analytics?from=bad", nil, 400},
		{http.MethodGet, "/api/analytics?to=bad", nil, 400},
		{http.MethodGet, "/api/runs/cvb-run", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run/telemetry?expanded=true", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run/logs", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run/logs.txt", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run/eval", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run/evals", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run/eval/stream", nil, 200},
		{http.MethodPost, "/api/runs/cvb-run/eval/apply", map[string]any{}, 400},
		{http.MethodPost, "/api/runs/cvb-run/signals", map[string]any{}, 400},
		{http.MethodPost, "/api/runs/cvb-run/cancel", nil, 400},
		{http.MethodPost, "/api/runs/cvb-run/pause", nil, 400},
		{http.MethodPost, "/api/runs/cvb-run/resume", map[string]any{}, 400},
		{http.MethodPost, "/api/runs/cvb-run/refresh-sop", nil, 400},
		{http.MethodGet, "/api/runs/cvb-run/preview", nil, 404},
		{http.MethodDelete, "/api/runs/cvb-run/preview", nil, 404},
		{http.MethodDelete, "/api/runs/cvb-run/preview/session/missing", nil, 404},
	}
	for i, tc := range cases {
		got := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if got.Code != tc.want {
			t.Errorf("case %d %s %s = %d want %d: %s", i, tc.method, tc.path, got.Code, tc.want, got.Body.String())
		}
	}
}
