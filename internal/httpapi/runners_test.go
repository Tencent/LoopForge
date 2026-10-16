package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// requestWithHeaders issues a JSON request with extra headers (runner session
// or bootstrap tokens) and returns the raw recorder.
func requestWithHeaders(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newRunnerTestServer(t *testing.T) http.Handler {
	t.Helper()
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 42, LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	return New(s, e, tracker.NewService(), "", "")
}

func registerTestRunner(t *testing.T, h http.Handler) (runnerID, session string) {
	t.Helper()
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	body := map[string]any{
		"name":           "test-mac",
		"os":             "darwin",
		"arch":           "arm64",
		"version":        "0.1.0",
		"maxConcurrency": 1,
		"runtimes": []domain.RunnerRuntime{
			{Provider: "mock", BinaryPath: "(builtin)", Status: "online"},
		},
	}
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", body, map[string]string{"X-Bootstrap-Token": "boot-secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("register: status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		RunnerID     string `json:"runnerId"`
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RunnerID == "" || out.SessionToken == "" {
		t.Fatalf("register response missing fields: %s", rec.Body.String())
	}
	return out.RunnerID, out.SessionToken
}

func TestRunnerBindCodeRegistersUnderIssuingUser(t *testing.T) {
	h := newRunnerTestServer(t)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "")

	// The bind code requires an interactive login; use the local acceptance
	// login with a freshly created user. Local login is loopback-only.
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "alice")
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/local", bytes.NewReader([]byte("{}")))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Origin", "http://example.com")
	loginReq.RemoteAddr = "127.0.0.1:60000"
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("local login: %d %s", loginRec.Code, loginRec.Body.String())
	}
	rec := loginRec
	var cookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == authCookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("login cookie missing")
	}

	// Issue a one-time bind code.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/bind-codes", map[string]any{}, map[string]string{"Cookie": authCookieName + "=" + cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("issue bind code: %d %s", rec.Code, rec.Body.String())
	}
	var issued struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("bind code response: %s", rec.Body.String())
	}

	// Register the machine with the bind code; it lands under the user.
	body := map[string]any{"name": "bind-code-mac", "os": "darwin", "arch": "arm64", "maxConcurrency": 1}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", body, map[string]string{"X-Bootstrap-Token": issued.Code})
	if rec.Code != http.StatusOK {
		t.Fatalf("register with bind code: %d %s", rec.Code, rec.Body.String())
	}
	runnersRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners?mine=1", nil, map[string]string{"Cookie": authCookieName + "=" + cookie})
	if runnersRec.Code != http.StatusOK {
		t.Fatalf("mine=1: %d %s", runnersRec.Code, runnersRec.Body.String())
	}
	var runners []domain.Runner
	if err := json.Unmarshal(runnersRec.Body.Bytes(), &runners); err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || runners[0].Name != "bind-code-mac" {
		t.Fatalf("mine=1 should list the bound machine: %+v", runners)
	}

	// The code is strictly one-time: a replay is rejected.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", body, map[string]string{"X-Bootstrap-Token": issued.Code})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed bind code register status = %d, want 401", rec.Code)
	}
}

func TestRunnerDeregisterRemovesMachineAndSelections(t *testing.T) {
	h := newRunnerTestServer(t)

	// Log in as alice, issue a bind code, and register her machine with it.
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "alice")
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/local", bytes.NewReader([]byte("{}")))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Origin", "http://example.com")
	loginReq.RemoteAddr = "127.0.0.1:60000"
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("local login: %d %s", loginRec.Code, loginRec.Body.String())
	}
	var cookie string
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == authCookieName {
			cookie = c.Value
		}
	}
	userAuth := map[string]string{"Cookie": authCookieName + "=" + cookie}
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/bind-codes", map[string]any{}, userAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue bind code: %d %s", rec.Code, rec.Body.String())
	}
	var issued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("bind code response: %s", rec.Body.String())
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{
		"name": "dereg-mac", "os": "darwin", "arch": "arm64", "maxConcurrency": 1,
	}, map[string]string{"X-Bootstrap-Token": issued.Code})
	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var registered struct {
		RunnerID     string `json:"runnerId"`
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	auth := map[string]string{"Authorization": "Bearer " + registered.SessionToken}

	// Alice selects the machine as her default execution machine.
	rec = requestWithHeaders(t, h, http.MethodPut, "/api/runners/default", map[string]any{"runnerId": registered.RunnerID}, userAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("set default: %d %s", rec.Code, rec.Body.String())
	}

	// Graceful deregister removes the machine and the default selection.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/deregister", map[string]any{}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("deregister: %d %s", rec.Code, rec.Body.String())
	}
	listRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners", nil, nil)
	var runners []domain.Runner
	if err := json.Unmarshal(listRec.Body.Bytes(), &runners); err != nil {
		t.Fatal(err)
	}
	if len(runners) != 0 {
		t.Fatalf("machine still listed after deregister: %+v", runners)
	}

	// The stale session is rejected (the record is gone).
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/heartbeat", map[string]any{}, auth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-deregister heartbeat status = %d, want 401", rec.Code)
	}
}

func TestRunnerDisconnectKeepsRegistrationAndSession(t *testing.T) {
	h := newRunnerTestServer(t)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	runnerID, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}

	// A normal daemon stop marks presence offline without destroying the
	// registration. The same session can heartbeat after a service restart.
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/disconnect", map[string]any{}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body.String())
	}
	listRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners", nil, nil)
	var runners []domain.Runner
	if err := json.Unmarshal(listRec.Body.Bytes(), &runners); err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || runners[0].ID != runnerID || runners[0].Status != domain.RunnerStatusOffline {
		t.Fatalf("disconnect should retain an offline machine: %+v", runners)
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/heartbeat", map[string]any{
		"status": "idle", "runningTasks": 0, "version": "0.2.0",
	}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat after reconnect: %d %s", rec.Code, rec.Body.String())
	}
	listRec = requestWithHeaders(t, h, http.MethodGet, "/api/runners", nil, nil)
	if err := json.Unmarshal(listRec.Body.Bytes(), &runners); err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || runners[0].RunnerVersion != "0.2.0" {
		t.Fatalf("runner version after heartbeat: %+v", runners)
	}
}

func currentTestRunnerID(t *testing.T, h http.Handler, auth map[string]string) string {
	t.Helper()
	rec := requestWithHeaders(t, h, http.MethodGet, "/api/runners", nil, auth)
	var runners []domain.Runner
	if err := json.Unmarshal(rec.Body.Bytes(), &runners); err != nil || len(runners) != 1 {
		t.Fatalf("runner list: %s", rec.Body.String())
	}
	return runners[0].ID
}

func TestRunnerRegisterRejectsBadBootstrapToken(t *testing.T) {
	h := newRunnerTestServer(t)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{"name": "x"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token register status = %d, want 401", rec.Code)
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{"name": "x"}, map[string]string{"X-Bootstrap-Token": "wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-token register status = %d, want 401", rec.Code)
	}
}

func TestRunnerRegisterDisabledWithoutBootstrapToken(t *testing.T) {
	h := newRunnerTestServer(t)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "")
	// No credential at all and registration not configured: 503.
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{"name": "x"}, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("register without credential or bootstrap token status = %d, want 503", rec.Code)
	}
	// A presented-but-invalid credential is always 401.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{"name": "x"}, map[string]string{"X-Bootstrap-Token": "anything"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("register with invalid token status = %d, want 401", rec.Code)
	}
}

func TestRunnerTaskEndToEnd(t *testing.T) {
	h := newRunnerTestServer(t)
	// Keep the long poll short so the empty-claim check does not stall.
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "200ms")
	runnerID, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}

	// Heartbeat keeps the runner online.
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/heartbeat", map[string]any{
		"status":       "idle",
		"runningTasks": 0,
		"runtimes":     []domain.RunnerRuntime{{Provider: "mock", BinaryPath: "(builtin)", Status: "online"}},
	}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body.String())
	}

	// The runner appears in the management list without session material.
	list := requestEmptyJSON[[]domain.Runner](t, h, http.MethodGet, "/api/runners", http.StatusOK)
	if len(list) != 1 || list[0].ID != runnerID {
		t.Fatalf("unexpected runner list: %+v", list)
	}
	if list[0].SessionTokenHash != "" || list[0].SessionExpiresAt != nil {
		t.Fatal("runner list leaked session material")
	}

	// Create a task (management API).
	created := requestJSON[domain.RunnerTask](t, h, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock",
		"prompt":   "hello runner",
	}, http.StatusCreated)
	if created.ID == "" || created.Status != domain.RunnerTaskQueued {
		t.Fatalf("unexpected created task: %+v", created)
	}

	// Claim: the long poll returns immediately because a task is queued.
	claimBody := map[string]any{
		"availableRuntimes": []string{"mock"},
		"capacity":          1,
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/claim", claimBody, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body.String())
	}
	var claimed struct {
		Task domain.RunnerTask `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.Task.ID != created.ID || claimed.Task.Status != domain.RunnerTaskLeased || claimed.Task.LeaseOwner != runnerID {
		t.Fatalf("unexpected claimed task: %+v", claimed.Task)
	}

	// A second claim finds nothing and returns 204.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/claim", claimBody, auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("second claim status = %d, want 204", rec.Code)
	}

	// Start.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/start", map[string]any{}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	// Batched events are deduplicated by seq.
	events := map[string]any{"events": []domain.RunnerTaskEvent{
		{Seq: 1, Type: "stdout", Data: "line-1"},
		{Seq: 2, Type: "stdout", Data: "line-2"},
	}}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/events", events, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("events: %d %s", rec.Code, rec.Body.String())
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/events", events, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("events retry: %d %s", rec.Code, rec.Body.String())
	}
	eventsRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners/tasks/"+created.ID+"/events", nil, auth)
	if eventsRec.Code != http.StatusOK {
		t.Fatalf("get events: %d %s", eventsRec.Code, eventsRec.Body.String())
	}
	var stored []domain.RunnerTaskEvent
	if err := json.Unmarshal(eventsRec.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d events, want 2 (retry must not duplicate)", len(stored))
	}

	// Complete.
	exit := 0
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/complete", map[string]any{
		"status":   "success",
		"exitCode": exit,
		"result":   "MOCK hello runner",
	}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	finalRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners/tasks/"+created.ID, nil, auth)
	if finalRec.Code != http.StatusOK {
		t.Fatalf("get final task: %d %s", finalRec.Code, finalRec.Body.String())
	}
	var final domain.RunnerTask
	if err := json.Unmarshal(finalRec.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != domain.RunnerTaskSuccess || final.Result != "MOCK hello runner" {
		t.Fatalf("unexpected final task: %+v", final)
	}
}

func TestRunnerTaskCancelPropagatesThroughLease(t *testing.T) {
	h := newRunnerTestServer(t)
	_, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}

	created := requestJSON[domain.RunnerTask](t, h, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock",
		"prompt":   "work",
	}, http.StatusCreated)

	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"},
		"capacity":          1,
	}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body.String())
	}
	_ = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/start", map[string]any{}, auth)

	// Management cancel flags the live task.
	_ = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/cancel", map[string]any{}, nil)

	// The runner learns about the cancel on its next lease renewal.
	var renewed struct {
		DesiredState string `json:"desiredState"`
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+created.ID+"/lease/renew", map[string]any{}, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &renewed); err != nil {
		t.Fatal(err)
	}
	if renewed.DesiredState != domain.RunnerTaskDesiredCancel {
		t.Fatalf("desiredState = %q, want cancel", renewed.DesiredState)
	}

	// Canceling a queued task cancels it outright.
	queued := requestJSON[domain.RunnerTask](t, h, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock",
		"prompt":   "never runs",
	}, http.StatusCreated)
	_ = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+queued.ID+"/cancel", map[string]any{}, nil)
	finalRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners/tasks/"+queued.ID, nil, auth)
	if finalRec.Code != http.StatusOK {
		t.Fatalf("get canceled task: %d %s", finalRec.Code, finalRec.Body.String())
	}
	var final domain.RunnerTask
	if err := json.Unmarshal(finalRec.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != domain.RunnerTaskCanceled {
		t.Fatalf("queued cancel status = %s, want canceled", final.Status)
	}
}

func TestRunnerObservationUploadIsIdempotent(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	runnerID, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}
	runID := "run-observable"
	if err := s.PutRun(domain.Run{
		ID: runID, Status: "running", Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{
		ID: "rt-observation", RunnerID: runnerID, Provider: "codex", Status: domain.RunnerTaskRunning,
		LeaseOwner: runnerID, RunID: runID, NodeID: "work", MaxAttempts: 1,
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	usage := domain.TokenUsage{InputTokens: 12, OutputTokens: 5, TotalTokens: 17}
	observation := domain.TelemetryEvent{
		Category: "usage", Name: "usage.final", Phase: "finish", Message: "final model usage",
		Provider: "codex", Model: "gpt-test", Usage: &usage,
		Source: domain.TelemetrySource{Layer: "executor", Adapter: "codex-jsonl-v1", Provider: "codex", Protocol: "jsonl"},
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"events": []domain.RunnerTaskEvent{{Seq: 1, Type: "observation", Data: string(raw), Ts: time.Now().UTC()}}}
	for attempt := 0; attempt < 2; attempt++ {
		rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/"+task.ID+"/events", body, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("events attempt %d: %d %s", attempt+1, rec.Code, rec.Body.String())
		}
	}
	stored := s.RunnerTaskEvents(task.ID)
	if len(stored) != 1 || stored[0].Seq != 1 || stored[0].Type != "observation" {
		t.Fatalf("stored runner events = %+v, want one idempotent observation", stored)
	}
}

func TestRunnerSessionRequiredForTaskAPI(t *testing.T) {
	h := newRunnerTestServer(t)
	registerTestRunner(t, h)

	// No session: claim is rejected.
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"},
		"capacity":          1,
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated claim status = %d, want 401", rec.Code)
	}

	// A user token (afp_) is not a runner session.
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"},
		"capacity":          1,
	}, map[string]string{"Authorization": "Bearer afp_not-a-runner"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("user-token claim status = %d, want 401", rec.Code)
	}
}

func TestRunnerTaskClaimLongPollWaitsForNewTask(t *testing.T) {
	h := newRunnerTestServer(t)
	_, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}

	// Long poll in the background; it must not return 204 before the task
	// arrives, and it should wake up shortly after creation.
	type claimResult struct {
		rec *httptest.ResponseRecorder
	}
	done := make(chan claimResult, 1)
	go func() {
		done <- claimResult{requestWithHeaders(t, h, http.MethodPost, "/api/runners/tasks/claim", map[string]any{
			"availableRuntimes": []string{"mock"},
			"capacity":          1,
		}, auth)}
	}()

	time.Sleep(300 * time.Millisecond)
	created := requestJSON[domain.RunnerTask](t, h, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock",
		"prompt":   "wake up",
	}, http.StatusCreated)

	select {
	case result := <-done:
		if result.rec.Code != http.StatusOK {
			t.Fatalf("long-poll claim status = %d body %s", result.rec.Code, result.rec.Body.String())
		}
		var claimed struct {
			Task domain.RunnerTask `json:"task"`
		}
		if err := json.Unmarshal(result.rec.Body.Bytes(), &claimed); err != nil {
			t.Fatal(err)
		}
		if claimed.Task.ID != created.ID {
			t.Fatalf("long poll claimed %s, want %s", claimed.Task.ID, created.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("long-poll claim did not return after task creation")
	}
}

func TestRunnerTaskPreflightLeavesTaskQueued(t *testing.T) {
	h := newRunnerTestServer(t)
	runnerID, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}
	created := requestJSON[domain.RunnerTask](t, h, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock", "prompt": "lazy update", "runnerId": runnerID,
	}, http.StatusCreated)
	request := map[string]any{"availableRuntimes": []string{"mock"}, "capacity": 1, "preflight": true}
	preflight := requestWithHeaders(t, h, http.MethodPost, "/runners/tasks/claim", request, auth)
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"taskAvailable":true`) {
		t.Fatalf("preflight: status=%d body=%s", preflight.Code, preflight.Body.String())
	}
	list := requestJSON[[]domain.RunnerTask](t, h, http.MethodGet, "/api/runners/tasks", nil, http.StatusOK)
	if len(list) != 1 || list[0].ID != created.ID || list[0].Status != domain.RunnerTaskQueued {
		t.Fatalf("preflight leased task: %+v", list)
	}
	claim := requestWithHeaders(t, h, http.MethodPost, "/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"}, "capacity": 1, "noWait": true,
	}, auth)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim: status=%d body=%s", claim.Code, claim.Body.String())
	}
	var response struct {
		Task domain.RunnerTask `json:"task"`
	}
	if err := json.Unmarshal(claim.Body.Bytes(), &response); err != nil || response.Task.ID != created.ID {
		t.Fatalf("claimed task: %+v, err=%v", response.Task, err)
	}
	noTask := requestWithHeaders(t, h, http.MethodPost, "/runners/tasks/claim", map[string]any{
		"availableRuntimes": []string{"mock"}, "capacity": 1, "noWait": true,
	}, auth)
	if noTask.Code != http.StatusNoContent {
		t.Fatalf("empty non-blocking claim: status=%d body=%s", noTask.Code, noTask.Body.String())
	}
}

func TestRunnerTaskClaimFindsTaskInsertedByAnotherProcess(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "2s")
	t.Setenv("AGENTFLOW_RUNNER_DB_POLL_INTERVAL", "25ms")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	_, session := registerTestRunner(t, h)
	auth := map[string]string{"Authorization": "Bearer " + session}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- requestWithHeaders(t, h, http.MethodPost, "/runners/tasks/claim", map[string]any{
			"availableRuntimes": []string{"mock"}, "capacity": 1,
		}, auth)
	}()
	time.Sleep(75 * time.Millisecond)
	// Write directly to the durable queue without notifying this Server's
	// process-local waiter, matching the production worker/API split.
	task := domain.RunnerTask{ID: "rt-other-process", Provider: "mock", Status: domain.RunnerTaskQueued, MaxAttempts: 1}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}

	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("cross-process claim status=%d body=%s", rec.Code, rec.Body.String())
		}
		var claimed struct {
			Task domain.RunnerTask `json:"task"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &claimed); err != nil {
			t.Fatal(err)
		}
		if claimed.Task.ID != task.ID {
			t.Fatalf("claimed %s, want %s", claimed.Task.ID, task.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("long poll did not discover cross-process queue insertion")
	}
}

func TestIsRunnerAgentPath(t *testing.T) {
	agentPaths := []string{
		"/runners/install.sh",
		"/runners/agentflow-runner",
		"/runners/agentflow-gate-mcp",
		"/runners/health",
		"/runners/register",
		"/runners/heartbeat",
		"/runners/tasks/claim",
		"/runners/tasks/rt-1/events",
		"/api/runners/register",
		"/api/runners/disconnect",
		"/api/runners/heartbeat",
		"/api/runners/tasks/claim",
		"/api/runners/tasks/rt-1",
		"/api/runners/tasks/rt-1/start",
		"/api/runners/tasks/rt-1/lease/renew",
		"/api/runners/tasks/rt-1/events",
		"/api/runners/tasks/rt-1/complete",
		"/api/runners/tasks/rt-1/control",
	}
	for _, p := range agentPaths {
		if !isRunnerAgentPath(p) {
			t.Fatalf("isRunnerAgentPath(%q) = false, want true", p)
		}
	}
	managementPaths := []string{
		"/api/runners",
		"/api/runners/tasks",
		"/api/runners/tasks/rt-1/cancel",
		"/api/runs",
		"/api/runnersX",
	}
	for _, p := range managementPaths {
		if isRunnerAgentPath(p) {
			t.Fatalf("isRunnerAgentPath(%q) = true, want false", p)
		}
	}
	_ = os.Environ
}

func TestRunnerMachinePlaneNeedsNoUserIdentityButKeepsRunnerAuth(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "10ms")
	dir := t.TempDir()
	binary := filepath.Join(dir, "agentflow-runner-linux-amd64")
	if err := os.WriteFile(binary, []byte("runner-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	gateBinary := filepath.Join(dir, "agentflow-gate-mcp-linux-amd64")
	if err := os.WriteFile(gateBinary, []byte("gate-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(dir, "agentflow-runner-{os}-{arch}"))
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(dir, "agentflow-gate-mcp-{os}-{arch}"))

	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "preview", "secret")

	for _, path := range []string{"/runners/install.sh", "/runners/health", "/runners/agentflow-runner?os=linux&arch=amd64", "/runners/agentflow-gate-mcp?os=linux&arch=amd64"} {
		rec := requestWithHeaders(t, h, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("public machine asset %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	body := map[string]any{"name": "isolated-linux", "os": "linux", "arch": "amd64", "maxConcurrency": 1, "runtimes": []domain.RunnerRuntime{{Provider: "terminal", Status: "online"}}}
	registered := requestWithHeaders(t, h, http.MethodPost, "/runners/register", body, map[string]string{"X-Bootstrap-Token": "boot-secret"})
	if registered.Code != http.StatusOK {
		t.Fatalf("machine registration: status=%d body=%s", registered.Code, registered.Body.String())
	}
	var session struct {
		Token string `json:"sessionToken"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &session); err != nil || session.Token == "" {
		t.Fatalf("registration response: %s", registered.Body.String())
	}

	unauthorized := requestWithHeaders(t, h, http.MethodPost, "/runners/heartbeat", map[string]any{}, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("heartbeat without runner token: status=%d", unauthorized.Code)
	}
	auth := map[string]string{"Authorization": "Bearer " + session.Token}
	heartbeat := requestWithHeaders(t, h, http.MethodPost, "/runners/heartbeat", map[string]any{"runtimes": []domain.RunnerRuntime{{Provider: "terminal", Status: "online"}}}, auth)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("session heartbeat: status=%d body=%s", heartbeat.Code, heartbeat.Body.String())
	}
	claim := requestWithHeaders(t, h, http.MethodPost, "/runners/tasks/claim", map[string]any{"availableRuntimes": []string{"terminal"}, "capacity": 1}, auth)
	if claim.Code != http.StatusNoContent {
		t.Fatalf("session claim: status=%d body=%s", claim.Code, claim.Body.String())
	}

	management := requestWithHeaders(t, h, http.MethodGet, "/api/runners", nil, nil)
	if management.Code != http.StatusUnauthorized {
		t.Fatalf("browser management unexpectedly public: status=%d body=%s", management.Code, management.Body.String())
	}
	for _, path := range []string{"/runners/bind-codes", "/runners/default", "/runners/runtimes", "/runners/tasks", "/runners/tasks/task-1/cancel"} {
		rec := requestWithHeaders(t, h, http.MethodPost, path, map[string]any{}, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("management endpoint leaked into machine plane %s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

// 直接派发执行机器任务等于在别人的机器上跑任意提示词，只有平台管理员可以用；
// 执行机器也只能读派给自己的任务，且拿不到任务里的凭证。
func TestRunnerTaskManagementRequiresAdminAndOwnership(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 42, LoginName: "alice"}); err != nil {
		t.Fatal(err)
	}
	raw := "af_test_alice_token"
	sum := sha256.Sum256([]byte(raw))
	if err := s.PutAPIToken(domain.APIToken{ID: "tok-alice", UserID: 42, Name: "t", Prefix: "af_test", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	alice := map[string]string{"Authorization": "Bearer " + raw}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/runners/tasks"},
		{http.MethodPost, "/api/runners/tasks"},
		{http.MethodPost, "/api/runners/tasks/rt-x/cancel"},
	} {
		rec := requestWithHeaders(t, h, tc.method, tc.path, map[string]any{"provider": "mock", "prompt": "rm -rf ~"}, alice)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s as non-admin = %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	ownerID, ownerSession := registerTestRunner(t, h)
	_, otherSession := registerTestRunner(t, h)
	task := domain.RunnerTask{
		ID: "rt-owned", RunnerID: ownerID, Provider: "mock", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: "https://git.example.com/a/b.git", GitToken: "git-secret"}}},
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/runners/tasks/rt-owned", "/api/runners/tasks/rt-owned/events"} {
		if rec := requestWithHeaders(t, h, http.MethodGet, path, nil, map[string]string{"Authorization": "Bearer " + otherSession}); rec.Code != http.StatusNotFound {
			t.Fatalf("other runner GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := requestWithHeaders(t, h, http.MethodGet, "/api/runners/tasks/rt-owned", nil, map[string]string{"Authorization": "Bearer " + ownerSession})
	if rec.Code != http.StatusOK {
		t.Fatalf("owner GET = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "git-secret") {
		t.Fatalf("task read leaked credentials: %s", rec.Body.String())
	}
}

// 用户可以移除自己已经离线、不再使用的机器；在线机器和别人的机器不能移除。
func TestRunnerRemoveOnlyOwnOfflineMachine(t *testing.T) {
	h := newRunnerTestServer(t)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "alice")
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/local", bytes.NewReader([]byte("{}")))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Origin", "http://example.com")
	loginReq.RemoteAddr = "127.0.0.1:60000"
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("local login: %d %s", loginRec.Code, loginRec.Body.String())
	}
	var cookie string
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == authCookieName {
			cookie = c.Value
		}
	}
	userAuth := map[string]string{"Cookie": authCookieName + "=" + cookie}
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/bind-codes", map[string]any{}, userAuth)
	var issued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("bind code response: %d %s", rec.Code, rec.Body.String())
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{
		"name": "old-mac", "os": "darwin", "arch": "arm64", "maxConcurrency": 1,
	}, map[string]string{"X-Bootstrap-Token": issued.Code})
	var registered struct {
		RunnerID     string `json:"runnerId"`
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil || registered.RunnerID == "" {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	poolRunnerID, _ := registerTestRunner(t, h)

	if rec = requestWithHeaders(t, h, http.MethodDelete, "/api/runners/"+registered.RunnerID, nil, userAuth); rec.Code != http.StatusConflict {
		t.Fatalf("remove online machine status = %d, want 409", rec.Code)
	}
	if rec = requestWithHeaders(t, h, http.MethodDelete, "/api/runners/"+poolRunnerID, nil, userAuth); rec.Code != http.StatusForbidden {
		t.Fatalf("remove someone else's machine status = %d, want 403", rec.Code)
	}
	requestWithHeaders(t, h, http.MethodPost, "/api/runners/disconnect", map[string]any{}, map[string]string{"Authorization": "Bearer " + registered.SessionToken})
	if rec = requestWithHeaders(t, h, http.MethodDelete, "/api/runners/"+registered.RunnerID, nil, userAuth); rec.Code != http.StatusOK {
		t.Fatalf("remove offline machine: %d %s", rec.Code, rec.Body.String())
	}
	var mine []domain.Runner
	listRec := requestWithHeaders(t, h, http.MethodGet, "/api/runners?mine=1", nil, userAuth)
	if err := json.Unmarshal(listRec.Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	if len(mine) != 0 {
		t.Fatalf("machine still listed after remove: %+v", mine)
	}
}

func TestRunnerRunLimitOnlyOwnerCanSet(t *testing.T) {
	h := newRunnerTestServer(t)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "alice")
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/local", bytes.NewReader([]byte("{}")))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Origin", "http://example.com")
	loginReq.RemoteAddr = "127.0.0.1:60000"
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("local login: %d %s", loginRec.Code, loginRec.Body.String())
	}
	var cookie string
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == authCookieName {
			cookie = c.Value
		}
	}
	userAuth := map[string]string{"Cookie": authCookieName + "=" + cookie}
	rec := requestWithHeaders(t, h, http.MethodPost, "/api/runners/bind-codes", map[string]any{}, userAuth)
	var issued struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil || issued.Code == "" {
		t.Fatalf("bind code response: %d %s", rec.Code, rec.Body.String())
	}
	rec = requestWithHeaders(t, h, http.MethodPost, "/api/runners/register", map[string]any{
		"name": "my-mac", "os": "darwin", "arch": "arm64", "maxConcurrency": 1,
	}, map[string]string{"X-Bootstrap-Token": issued.Code})
	var registered struct {
		RunnerID     string `json:"runnerId"`
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil || registered.RunnerID == "" {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	poolRunnerID, _ := registerTestRunner(t, h)
	path := "/api/runners/" + registered.RunnerID + "/run-limit"

	if rec = requestWithHeaders(t, h, http.MethodPut, path, map[string]any{"runLimit": 3}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous set status = %d, want 401", rec.Code)
	}
	if rec = requestWithHeaders(t, h, http.MethodPut, "/api/runners/"+poolRunnerID+"/run-limit", map[string]any{"runLimit": 3}, userAuth); rec.Code != http.StatusForbidden {
		t.Fatalf("set someone else's machine status = %d, want 403", rec.Code)
	}
	for _, body := range []map[string]any{{}, {"runLimit": -1}, {"runLimit": 101}} {
		if rec = requestWithHeaders(t, h, http.MethodPut, path, body, userAuth); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid %v status = %d, want 400", body, rec.Code)
		}
	}
	rec = requestWithHeaders(t, h, http.MethodPut, path, map[string]any{"runLimit": 3}, userAuth)
	var updated domain.Runner
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); rec.Code != http.StatusOK || err != nil || updated.RunLimit != 3 || updated.SessionTokenHash != "" {
		t.Fatalf("set own machine: %d %s", rec.Code, rec.Body.String())
	}
	// A heartbeat from the machine must not reset the owner's limit.
	requestWithHeaders(t, h, http.MethodPost, "/api/runners/heartbeat", map[string]any{}, map[string]string{"Authorization": "Bearer " + registered.SessionToken})
	rec = requestWithHeaders(t, h, http.MethodGet, "/api/runners?mine=1", nil, userAuth)
	var mine []domain.Runner
	if err := json.Unmarshal(rec.Body.Bytes(), &mine); err != nil || len(mine) != 1 || mine[0].RunLimit != 3 {
		t.Fatalf("limit after heartbeat: %s", rec.Body.String())
	}
	rec = requestWithHeaders(t, h, http.MethodPut, path, map[string]any{"runLimit": 0}, userAuth)
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); rec.Code != http.StatusOK || err != nil || updated.RunLimit != 0 {
		t.Fatalf("clear limit: %d %s", rec.Code, rec.Body.String())
	}
}
