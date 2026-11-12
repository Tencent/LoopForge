package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRunnerManagementAndMachineLifecycle(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	runner, _ := f.s.Runner(runnerID)
	runner.OwnerUserID = f.user.ID
	runner.RunnerVersion = domain.RunnerVersionDefaultSelection
	if err := f.s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	userCases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/runners?mine=1", nil, 200},
		{http.MethodPut, "/api/runners/default", map[string]any{"runnerId": "missing"}, 404},
		{http.MethodPut, "/api/runners/default", map[string]any{"runnerId": runnerID}, 200},
		{http.MethodPut, "/api/runners/" + runnerID + "/run-limit", map[string]any{}, 400},
		{http.MethodPut, "/api/runners/missing/run-limit", map[string]any{"runLimit": 1}, 404},
		{http.MethodPut, "/api/runners/" + runnerID + "/run-limit", map[string]any{"runLimit": 2}, 200},
		{http.MethodPut, "/api/runners/runtimes", map[string]any{"runnerId": runnerID, "provider": "mock"}, 400},
		{http.MethodPut, "/api/runners/runtimes", map[string]any{"runnerId": "missing", "provider": "mock", "status": "online"}, 404},
		{http.MethodPut, "/api/runners/runtimes", map[string]any{"runnerId": runnerID, "provider": "mock", "status": "disabled", "authMode": "local"}, 200},
		{http.MethodPut, "/api/runners/default", map[string]any{"runnerId": ""}, 200},
	}
	for i, tc := range userCases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("user case %d %s = %d want %d: %s", i, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	auth := map[string]string{"Authorization": "Bearer " + session}
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, "/runners/heartbeat", map[string]any{"version": "1.2.3", "runtimes": []any{map[string]any{"provider": "mock", "status": "online"}}}, 200},
		{http.MethodPut, "/runners/previews/missing/content", nil, 404},
		{http.MethodPost, "/runners/previews/missing/complete", map[string]any{"ignored": true}, 200},
	} {
		rec := requestWithHeaders(t, f.h, tc.method, tc.path, tc.body, auth)
		if rec.Code != tc.want {
			t.Errorf("%s = %d want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	created := cvBRequest(t, f, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock", "prompt": "do work", "runnerId": runnerID,
	}, true)
	if created.Code != 201 {
		t.Fatalf("create task = %d: %s", created.Code, created.Body.String())
	}
	var task domain.RunnerTask
	if err := json.Unmarshal(created.Body.Bytes(), &task); err != nil || task.ID == "" {
		t.Fatalf("task: %v %s", err, created.Body.String())
	}
	claim := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"mock"}, "capacity": 1, "noWait": true}, auth)
	if claim.Code != 200 {
		t.Fatalf("claim = %d: %s", claim.Code, claim.Body.String())
	}
	machineCases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/runners/tasks/" + task.ID, nil, 200},
		{http.MethodPost, "/runners/tasks/" + task.ID + "/start", nil, 200},
		{http.MethodPost, "/runners/tasks/" + task.ID + "/lease/renew", nil, 200},
		{http.MethodPost, "/runners/tasks/" + task.ID + "/events", map[string]any{"events": []any{}}, 200},
		{http.MethodPost, "/runners/tasks/" + task.ID + "/events", map[string]any{"events": []any{map[string]any{"seq": 1, "data": "hello"}}}, 200},
		{http.MethodGet, "/api/runners/tasks/" + task.ID + "/events", nil, 200},
		{http.MethodGet, "/runners/tasks/" + task.ID + "/control", nil, 200},
		{http.MethodPost, "/runners/tasks/" + task.ID + "/complete", map[string]any{"status": "success", "result": "done", "outputs": map[string]any{"files": map[string]string{"out.txt": "ok"}}}, 200},
	}
	for i, tc := range machineCases {
		rec := requestWithHeaders(t, f.h, tc.method, tc.path, tc.body, auth)
		if rec.Code != tc.want {
			t.Errorf("machine case %d %s = %d want %d: %s", i, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/disconnect", nil, auth); rec.Code != 200 {
		t.Errorf("disconnect = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/runners/"+runnerID, nil, true); rec.Code != 200 {
		t.Errorf("remove = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunnerNormalizationAndBindCodeBranches(t *testing.T) {
	reported := []domain.RunnerRuntime{{Provider: "codebuddy", Status: "online"}, {Provider: "codex", Status: "online"}, {Provider: "opencode", Status: "online"}}
	existing := []domain.RunnerRuntime{{Provider: "codebuddy", Status: domain.RuntimeStatusDisabled, AuthMode: domain.RuntimeAuthLocal}, {Provider: "codex", AuthMode: domain.RuntimeAuthPersonal}, {Provider: "opencode", AuthMode: domain.RuntimeAuthLocal}}
	got := normalizeRunnerRuntimes(reported, existing)
	if got[0].Status != domain.RuntimeStatusDisabled || got[0].AuthMode != domain.RuntimeAuthLocal || got[1].AuthMode != domain.RuntimeAuthPersonal || got[2].AuthMode != domain.RuntimeAuthPersonal {
		t.Fatalf("normalized = %+v", got)
	}
	f := cvBNewHTTPFixture(t)
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runners/bind-codes", nil, true); rec.Code != 200 {
		t.Fatalf("bind code = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunnerErrorAndRegistrationBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	auth := map[string]string{"Authorization": "Bearer " + session}
	owned, _ := f.s.Runner(runnerID)
	owned.OwnerUserID = f.user.ID
	if err := f.s.PutRunner(owned); err != nil {
		t.Fatal(err)
	}

	if rec := cvBRequest(t, f, http.MethodGet, "/api/runners/default", nil, true); rec.Code != 200 {
		t.Errorf("default = %d: %s", rec.Code, rec.Body.String())
	}
	foreignID, _ := registerTestRunner(t, f.h)
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodDelete, "/api/runners/missing", nil, 200},
		{http.MethodDelete, "/api/runners/" + foreignID, nil, 403},
		{http.MethodPut, "/api/runners/" + foreignID + "/run-limit", map[string]any{"runLimit": 2}, 403},
		{http.MethodPut, "/api/runners/" + runnerID + "/run-limit", map[string]any{"runLimit": -1}, 400},
		{http.MethodPut, "/api/runners/" + runnerID + "/run-limit", map[string]any{"runLimit": 100000}, 400},
		{http.MethodPut, "/api/runners/default", map[string]any{"runnerId": foreignID}, 403},
		{http.MethodPut, "/api/runners/runtimes", map[string]any{"runnerId": runnerID, "provider": "mock", "status": "nonsense"}, 400},
		{http.MethodPut, "/api/runners/runtimes", map[string]any{"runnerId": foreignID, "provider": "mock", "status": "online"}, 403},
		{http.MethodPost, "/api/runners/tasks", map[string]any{"prompt": "x"}, 400},
		{http.MethodPost, "/api/runners/tasks", map[string]any{"provider": "mock"}, 400},
		{http.MethodGet, "/api/runners/tasks", nil, 200},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, "/runners/tasks/claim", map[string]any{}, 400},
		{http.MethodPost, "/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"none"}, "noWait": true}, 204},
		{http.MethodPost, "/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"none"}, "preflight": true, "noWait": true}, 204},
		{http.MethodGet, "/api/runners/tasks/missing", nil, 404},
		{http.MethodGet, "/api/runners/tasks/missing/events", nil, 404},
		{http.MethodPost, "/runners/tasks/missing/start", nil, 409},
		{http.MethodPost, "/runners/tasks/missing/lease/renew", nil, 409},
		{http.MethodPost, "/runners/tasks/missing/events", map[string]any{"events": []any{}}, 404},
		{http.MethodPost, "/runners/tasks/missing/complete", map[string]any{}, 409},
		{http.MethodGet, "/runners/tasks/missing/control", nil, 404},
	} {
		rec := requestWithHeaders(t, f.h, tc.method, tc.path, tc.body, auth)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	bind := cvBRequest(t, f, http.MethodPost, "/api/runners/bind-codes", nil, true)
	var bindOut struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(bind.Body.Bytes(), &bindOut); err != nil || bindOut.Code == "" {
		t.Fatalf("bind response: %v %s", err, bind.Body.String())
	}
	registerBody := map[string]any{"name": "bound", "maxConcurrency": 99, "runtimes": []any{map[string]any{"provider": "codebuddy"}}}
	rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/register", registerBody, map[string]string{"X-Bootstrap-Token": bindOut.Code})
	if rec.Code != 200 {
		t.Fatalf("bind registration = %d: %s", rec.Code, rec.Body.String())
	}
	if replay := requestWithHeaders(t, f.h, http.MethodPost, "/runners/register", registerBody, map[string]string{"X-Bootstrap-Token": bindOut.Code}); replay.Code != 401 {
		t.Errorf("bind replay = %d: %s", replay.Code, replay.Body.String())
	}
}

func TestRunnerDirectAuthenticationBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runnersHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerBindCodes: map[string]runnerBindCode{}}
	for _, tc := range []struct {
		body string
		call func(*gin.Context)
	}{
		{"", h.runnerDefault},
		{"", h.runnerRemove},
		{`{"runLimit":1}`, h.runnerRunLimitSet},
		{`{"runnerId":""}`, h.runnerDefaultSet},
		{`{"runnerId":"r","provider":"mock","status":"online"}`, h.runnerRuntimeStatus},
		{"", h.issueRunnerBindCode},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: "missing"}}
		tc.call(c)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("direct auth status = %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestRunnerTaskValidationAndVisibilityBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	otherID, otherSession := registerTestRunner(t, f.h)
	auth := map[string]string{"Authorization": "Bearer " + session}
	otherAuth := map[string]string{"Authorization": "Bearer " + otherSession}

	create := func(idSuffix string) domain.RunnerTask {
		rec := cvBRequest(t, f, http.MethodPost, "/api/runners/tasks", map[string]any{
			"provider": "mock", "prompt": "task " + idSuffix, "runnerId": runnerID, "maxAttempts": 99,
		}, true)
		var task domain.RunnerTask
		if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &task) != nil {
			t.Fatalf("create task = %d: %s", rec.Code, rec.Body.String())
		}
		return task
	}
	task := create("visible")
	for _, path := range []string{"/api/runners/tasks/" + task.ID, "/api/runners/tasks/" + task.ID + "/events"} {
		if rec := requestWithHeaders(t, f.h, http.MethodGet, path, nil, otherAuth); rec.Code != 404 {
			t.Errorf("foreign visibility %s = %d", path, rec.Code)
		}
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/events", map[string]any{"events": []any{}}, auth); rec.Code != 409 {
		t.Errorf("unleased events = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := requestWithHeaders(t, f.h, http.MethodGet, "/runners/tasks/"+task.ID+"/control", nil, auth); rec.Code != 409 {
		t.Errorf("unleased control = %d: %s", rec.Code, rec.Body.String())
	}

	claim := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"mock"}, "capacity": 1, "noWait": true}, auth)
	if claim.Code != 200 {
		t.Fatalf("claim = %d: %s", claim.Code, claim.Body.String())
	}
	events := make([]map[string]any, 501)
	for i := range events {
		events[i] = map[string]any{"seq": i + 1}
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/events", map[string]any{"events": events}, auth); rec.Code != 400 {
		t.Errorf("large events = %d", rec.Code)
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/events", map[string]any{"events": []any{map[string]any{"seq": 1}}}, auth); rec.Code != 200 {
		t.Errorf("default event fields = %d: %s", rec.Code, rec.Body.String())
	}
	manyArtifacts := map[string]string{}
	for i := 0; i < 65; i++ {
		manyArtifacts[strconv.Itoa(i)] = "x"
	}
	for _, body := range []any{
		map[string]any{"artifacts": manyArtifacts},
		map[string]any{"artifacts": map[string]string{"x": strings.Repeat("x", (1<<20)+1)}},
		map[string]any{"outputs": map[string]any{"recovered": make([]any, 65)}},
	} {
		if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/complete", body, auth); rec.Code != 400 {
			t.Errorf("invalid completion = %d: %s", rec.Code, rec.Body.String())
		}
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/lease/renew", nil, otherAuth); rec.Code != 409 {
		t.Errorf("foreign renew = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodPost, "/api/runners/tasks/"+task.ID+"/cancel", nil, true); rec.Code != 200 {
		t.Errorf("cancel = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/lease/renew", nil, auth); rec.Code != 200 {
		t.Errorf("cancelled renew = %d: %s", rec.Code, rec.Body.String())
	}
	_ = otherID
}

func TestRunnerInternalHelperBranches(t *testing.T) {
	waiters := newRunnerWaiterRegistry()
	w := waiters.register([]string{"Mock"})
	waiters.notify([]string{"mock"})
	select {
	case <-w.ch:
	case <-time.After(time.Second):
		t.Fatal("waiter not notified")
	}
	waiters.notify([]string{"mock"})
	waiters.notify([]string{"mock"})
	waiters.unregister(w)
	if !containsStringFold([]string{"A"}, "a") || containsStringFold([]string{"A"}, "b") {
		t.Fatal("containsStringFold")
	}
	for path, want := range map[string]bool{
		"/runners/x": true, "/api/runners/register": true, "/api/runners/tasks/id": true,
		"/api/runners/tasks/id/start": true, "/api/runners/tasks/id/cancel": false,
		"/api/runners/tasks": false, "/api/other": false,
	} {
		if got := isRunnerAgentPath(path); got != want {
			t.Errorf("isRunnerAgentPath(%q)=%v want %v", path, got, want)
		}
	}
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "25ms")
	t.Setenv("AGENTFLOW_RUNNER_DB_POLL_INTERVAL", "5ms")
	if runnerLongPollTimeout() != 25*time.Millisecond || runnerDBPollInterval() != 5*time.Millisecond {
		t.Fatal("runner duration environment")
	}
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "bad")
	t.Setenv("AGENTFLOW_RUNNER_DB_POLL_INTERVAL", "0")
	if runnerLongPollTimeout() != runnerLongPollDefault || runnerDBPollInterval() != runnerDBPollDefault {
		t.Fatal("runner duration defaults")
	}
}

func TestRunnerLongPollAndPreflightBranches(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "20ms")
	t.Setenv("AGENTFLOW_RUNNER_DB_POLL_INTERVAL", "5ms")
	f := cvBNewHTTPFixture(t)
	_, session := registerTestRunner(t, f.h)
	auth := map[string]string{"Authorization": "Bearer " + session}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"absent"}, "capacity": 1,
	}, auth); rec.Code != 204 {
		t.Errorf("timed poll = %d: %s", rec.Code, rec.Body.String())
	}
	created := cvBRequest(t, f, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock", "prompt": "preflight",
	}, true)
	if created.Code != 201 {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"}, "capacity": 1, "preflight": true, "noWait": true,
	}, auth); rec.Code != 200 || !strings.Contains(rec.Body.String(), "taskAvailable") {
		t.Errorf("preflight = %d: %s", rec.Code, rec.Body.String())
	}
}
