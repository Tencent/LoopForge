package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRunEvalCloneAndApplyBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runsHandlers{deps: &deps{repo: f.s}, evalJobs: newRunEvalHub()}
	if err := f.s.PutProject(domain.Project{ID: "cvb-eval-p", Key: "EVP", Name: "Eval", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "cvb-eval-run", WorkflowID: "cvb-eval-wf", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "cvb-eval-p"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "cvb-eval-wf", Name: "Snapshot", Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "cvb-eval-agent"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {Status: "succeeded"}},
	}
	if _, err := h.ensureProjectWorkflow(run, "cvb-eval-p", "missing"); err != nil {
		t.Fatalf("snapshot fallback: %v", err)
	}
	if _, err := h.ensureProjectWorkflow(domain.Run{}, "cvb-eval-p", "missing"); err == nil {
		t.Fatal("missing workflow accepted")
	}
	if _, err := h.ensureProjectAgent(run, "cvb-eval-p", "missing"); err == nil {
		t.Fatal("missing agent accepted")
	}
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "cvb-shared-wf", Name: "Shared", ProjectID: "elsewhere", Shared: true, SandboxProfileID: "local", Nodes: run.WorkflowSnapshot.Nodes}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-eval-agent", Name: "Shared agent", ProjectID: "elsewhere", Shared: true, Version: 2}); err != nil {
		t.Fatal(err)
	}
	run.WorkflowID = "cvb-shared-wf"
	run.WorkflowSnapshot.ID = "cvb-shared-wf"
	wf, err := h.ensureProjectWorkflow(run, "cvb-eval-p", run.WorkflowID)
	if err != nil || wf.ProjectID != "cvb-eval-p" {
		t.Fatalf("workflow clone: %+v %v", wf, err)
	}
	agent, err := h.ensureProjectAgent(run, "cvb-eval-p", "cvb-eval-agent")
	if err != nil || agent.ProjectID != "cvb-eval-p" || agent.Shared {
		t.Fatalf("agent clone: %+v %v", agent, err)
	}
	if again, err := h.ensureProjectAgent(run, "cvb-eval-p", "cvb-eval-agent"); err != nil || again.ID != agent.ID {
		t.Fatalf("agent clone reuse: %+v %v", again, err)
	}
	for _, rec := range []domain.RunEvalRecommendation{
		{Target: "workflow", Action: "update_node", NodeID: "n", Set: map[string]any{"maxAttempts": 3}},
		{Target: "agent", Action: "append_instructions", TargetID: "cvb-eval-agent", Text: "be careful"},
		{Target: "unknown", Action: "unknown"},
	} {
		_, _ = h.applyOneRecommendation(run, "cvb-eval-p", rec)
	}

	failure := domain.RunEvalRecord{ID: "cvb-fail-eval", RunID: run.ID, Status: domain.RunEvalRunning}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRunEval(failure); err != nil {
		t.Fatal(err)
	}
	h.failRunEval(failure, "boom")
	stored, _ := f.s.RunEval(failure.ID)
	if stored.Status != domain.RunEvalFailed {
		t.Fatalf("failed eval = %+v", stored)
	}
}

func TestRunEvalJobAndLiveStreamBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runsHandlers{deps: &deps{repo: f.s}, evalJobs: newRunEvalHub()}
	run := domain.Run{ID: "cvb-live-eval-run", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	job := &runEvalJob{runID: run.ID, evalID: "e", subs: map[chan runEvalJobEvent]struct{}{}, done: make(chan struct{})}
	job.publish("thought", map[string]string{"text": "before"})
	h.evalJobs.jobs[run.ID] = job
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/stream/"+run.ID, nil)
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	go func() {
		time.Sleep(20 * time.Millisecond)
		job.publish("done", map[string]bool{"ok": true})
	}()
	h.streamRunEval(c)
	if recorder.Code != 200 {
		t.Fatalf("stream = %d: %s", recorder.Code, recorder.Body.String())
	}

	// Queue timeout and evaluator failure paths without any external model.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := domain.RunEvalRecord{ID: "cvb-cancel-eval", RunID: run.ID, Status: domain.RunEvalRunning}
	_ = f.s.PutRunEval(rec)
	queued := &runEvalJob{runID: run.ID, evalID: rec.ID, subs: map[chan runEvalJobEvent]struct{}{}, done: make(chan struct{})}
	h.runEvalJob(ctx, queued, rec, run, f.user)
	if saved, _ := f.s.RunEval(rec.ID); saved.Status != domain.RunEvalFailed {
		t.Fatalf("cancelled queued eval = %+v", saved)
	}
}

func TestRunEvalExecutorSuccessAndFailureBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runsHandlers{deps: &deps{repo: f.s}, evalJobs: newRunEvalHub()}
	run := domain.Run{ID: "cvb-eval-exec-run", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	for _, tc := range []struct {
		id      string
		result  domain.RunEvalResult
		execErr error
		want    string
	}{
		{"cvb-eval-exec-ok", domain.RunEvalResult{Score: 91, Passed: true}, nil, domain.RunEvalSucceeded},
		{"cvb-eval-exec-fail", domain.RunEvalResult{}, errors.New("mock evaluator failed"), domain.RunEvalFailed},
	} {
		rec := domain.RunEvalRecord{ID: tc.id, RunID: run.ID, Status: domain.RunEvalRunning}
		if err := f.s.PutRunEval(rec); err != nil {
			t.Fatal(err)
		}
		setRunEvalExecForTest(func(_ context.Context, _ domain.RunEvalRecord, _ domain.Run, _ domain.User, emit func(string, any)) (domain.RunEvalResult, error) {
			emit("thought", map[string]string{"text": "mock"})
			return tc.result, tc.execErr
		})
		job := &runEvalJob{runID: run.ID, evalID: rec.ID, subs: map[chan runEvalJobEvent]struct{}{}, done: make(chan struct{})}
		h.evalJobs.jobs[run.ID] = job
		h.runEvalJob(context.Background(), job, rec, run, f.user)
		saved, ok := f.s.RunEval(rec.ID)
		if !ok || saved.Status != tc.want {
			t.Errorf("%s status=%q want %q", tc.id, saved.Status, tc.want)
		}
		if _, exists := h.evalJobs.jobs[run.ID]; exists {
			t.Errorf("%s job not removed", tc.id)
		}
	}
}

func TestRunEvalHTTPValidationAndApplyBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	active := domain.Run{ID: "cvb-eval-active", Status: "running", Nodes: map[string]*domain.NodeRun{}}
	done := domain.Run{ID: "cvb-eval-http", Status: "succeeded", WorkflowID: "missing",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(active); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutRun(done); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, "/api/runs/missing/eval", nil, 404},
		{http.MethodPost, "/api/runs/cvb-eval-active/eval", nil, 409},
		{http.MethodPost, "/api/runs/cvb-eval-http/eval", nil, 400},
		{http.MethodGet, "/api/runs/missing/eval", nil, 404},
		{http.MethodGet, "/api/runs/cvb-eval-http/eval", nil, 200},
		{http.MethodGet, "/api/runs/missing/evals", nil, 404},
		{http.MethodGet, "/api/runs/cvb-eval-http/evals", nil, 200},
		{http.MethodGet, "/api/runs/missing/eval/stream", nil, 404},
		{http.MethodGet, "/api/runs/cvb-eval-http/eval/stream", nil, 404},
		{http.MethodPost, "/api/runs/missing/eval/apply", map[string]any{}, 404},
		{http.MethodPost, "/api/runs/cvb-eval-http/eval/apply", map[string]any{"evalId": "missing", "recommendationIds": []string{"x"}}, 404},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	eval := domain.RunEvalRecord{ID: "cvb-http-eval", RunID: done.ID, Status: domain.RunEvalSucceeded,
		Result: domain.RunEvalResult{Recommendations: []domain.RunEvalRecommendation{
			{ID: "view", Target: "unknown", Action: "unknown"},
			{ID: "broken", Target: "workflow", Action: "update_node", NodeID: "none"},
			{ID: "already", Target: "unknown", Action: "unknown", Status: domain.RunEvalRecApplied},
		}}}
	if err := f.s.PutRunEval(eval); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body any
		want int
	}{
		{map[string]any{}, 400},
		{map[string]any{"recommendationIds": []string{"view", "broken", "already"}}, 200},
	} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+done.ID+"/eval/apply", tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("apply = %d want %d: %s", rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/"+done.ID+"/eval/stream", nil, true); rec.Code != 200 {
		t.Errorf("stored stream = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunEvalLocalEntityReuseBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runsHandlers{deps: &deps{repo: f.s}}
	const projectID = "cvb-eval-local-p"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "EVL", Name: "Eval local", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "cvb-eval-local-wf", Name: "Local workflow", ProjectID: projectID, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "cvb-eval-local-agent"}}}
	agent := domain.AgentDefinition{ID: "cvb-eval-local-agent", Name: "Local", ProjectID: projectID}
	if err := f.s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{WorkflowID: wf.ID, WorkflowSnapshot: wf}
	gotWF, err := h.ensureProjectWorkflow(run, projectID, wf.ID)
	if err != nil || gotWF.ID != wf.ID || gotWF.Name == "" {
		t.Fatalf("local workflow: %+v %v", gotWF, err)
	}
	gotAgent, err := h.ensureProjectAgent(run, projectID, agent.ID)
	if err != nil || gotAgent.ID != agent.ID {
		t.Fatalf("local agent: %+v %v", gotAgent, err)
	}
}

func TestRunEvalHTTPStartSuccessAndBusy(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "cvb-test-key"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "cvb-r2-eval", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	setRunEvalExecForTest(func(_ context.Context, _ domain.RunEvalRecord, _ domain.Run, _ domain.User, emit func(string, any)) (domain.RunEvalResult, error) {
		emit("thought", map[string]string{"text": "mock evaluation"})
		return domain.RunEvalResult{Score: 88, Passed: true}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval", map[string]any{"model": "gpt-5.6-sol"}, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start eval = %d: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		eval, ok := f.s.LatestRunEval(run.ID)
		if ok && eval.Status == domain.RunEvalSucceeded {
			if eval.Score != 88 || !eval.Passed {
				t.Fatalf("eval result: %+v", eval)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("eval did not complete: %+v", eval)
		}
		time.Sleep(time.Millisecond)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/"+run.ID+"/eval", nil, true); rec.Code != http.StatusOK {
		t.Errorf("get eval = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/"+run.ID+"/evals", nil, true); rec.Code != http.StatusOK {
		t.Errorf("list evals = %d", rec.Code)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runs/"+run.ID+"/eval/stream", nil, true); rec.Code != http.StatusOK {
		t.Errorf("stored eval stream = %d", rec.Code)
	}
}

func TestRunEvalActiveRowRecoveryAndJobReplacement(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runsHandlers{deps: &deps{repo: f.s}, evalJobs: newRunEvalHub()}
	run := domain.Run{ID: "cvb-r2-eval-recover", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	stale := domain.RunEvalRecord{ID: "cvb-r2-stale", RunID: run.ID, Status: domain.RunEvalRunning}
	if err := f.s.PutRunEval(stale); err != nil {
		t.Fatal(err)
	}
	h.failRunEval(stale, "stale")
	if saved, ok := f.s.RunEval(stale.ID); !ok || saved.Status != domain.RunEvalFailed {
		t.Fatalf("failed stale eval: %+v", saved)
	}
	oldCancelled := make(chan struct{})
	h.evalJobs.jobs[run.ID] = &runEvalJob{runID: run.ID, cancel: func() { close(oldCancelled) }, done: make(chan struct{}), subs: map[chan runEvalJobEvent]struct{}{}}
	release := make(chan struct{})
	setRunEvalExecForTest(func(_ context.Context, _ domain.RunEvalRecord, _ domain.Run, _ domain.User, _ func(string, any)) (domain.RunEvalResult, error) {
		<-release
		return domain.RunEvalResult{Score: 1}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	rec := domain.RunEvalRecord{ID: "cvb-r2-replacement", RunID: run.ID, Status: domain.RunEvalRunning}
	if err := f.s.PutRunEval(rec); err != nil {
		t.Fatal(err)
	}
	h.launchRunEval(rec, run, f.user)
	job := h.evalJobs.jobs[run.ID]
	select {
	case <-oldCancelled:
	case <-time.After(time.Second):
		t.Fatal("old eval was not cancelled")
	}
	close(release)
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement eval did not finish")
	}
}

func TestRunEvalLiveStreamAndPersistenceErrors(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "cvb-r2-live-eval", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: &deps{repo: f.s}, evalJobs: newRunEvalHub()}
	job := &runEvalJob{runID: run.ID, evalID: "eval", subs: map[chan runEvalJobEvent]struct{}{}, done: make(chan struct{})}
	job.publish("thought", map[string]string{"text": "replay"})
	h.evalJobs.jobs[run.ID] = job
	router := gin.New()
	router.GET("/api/runs/:id/eval/stream", h.streamRunEval)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/eval/stream", nil))
		done <- rec
	}()
	deadline := time.Now().Add(time.Second)
	for {
		job.mu.Lock()
		subscribed := len(job.subs) > 0
		job.mu.Unlock()
		if subscribed || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	job.publish("done", map[string]bool{"ok": true})
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("live eval stream = %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("live eval stream did not finish")
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cancelRec := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/eval/stream", nil).WithContext(cancelledContext)
	router.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancelled eval stream = %d", cancelRec.Code)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	writeSSE(c, "bad", make(chan int))
	if rec.Body.Len() != 0 {
		t.Fatal("unmarshalable SSE payload written")
	}

	f2 := cvBNewHTTPFixture(t)
	f2.s.Close()
	h2 := &runsHandlers{deps: &deps{repo: f2.s}, evalJobs: newRunEvalHub()}
	setRunEvalExecForTest(func(context.Context, domain.RunEvalRecord, domain.Run, domain.User, func(string, any)) (domain.RunEvalResult, error) {
		return domain.RunEvalResult{Score: 1}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	closedJob := &runEvalJob{runID: "closed", evalID: "closed-eval", subs: map[chan runEvalJobEvent]struct{}{}, done: make(chan struct{})}
	h2.evalJobs.jobs["closed"] = closedJob
	h2.runEvalJob(context.Background(), closedJob,
		domain.RunEvalRecord{ID: "closed-eval", RunID: "closed", Status: domain.RunEvalRunning},
		domain.Run{ID: "closed"}, f2.user)
	sawError := false
	for _, event := range closedJob.events {
		if event.Type == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal(errors.New("persistence failure was not published"))
	}
}

func TestRunEvalRecoveryAndApplyConflicts(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "cvb-r2-eval-recovery-http", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	active := domain.RunEvalRecord{ID: "cvb-r2-orphan-eval", RunID: run.ID, Status: domain.RunEvalRunning}
	if err := f.s.PutRunEval(active); err != nil {
		t.Fatal(err)
	}
	setRunEvalExecForTest(func(context.Context, domain.RunEvalRecord, domain.Run, domain.User, func(string, any)) (domain.RunEvalResult, error) {
		return domain.RunEvalResult{Score: 5}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval", nil, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("orphan eval recovery = %d: %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		if latest, ok := f.s.LatestRunEval(run.ID); ok && latest.Status == domain.RunEvalSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered evaluation did not finish")
		}
		time.Sleep(time.Millisecond)
	}

	running := domain.RunEvalRecord{ID: "cvb-r2-running-apply", RunID: run.ID, Status: domain.RunEvalRunning}
	if err := f.s.PutRunEval(running); err != nil {
		t.Fatal(err)
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval/apply",
		map[string]any{"evalId": running.ID, "recommendationIds": []string{"x"}}, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("running eval apply = %d: %s", rec.Code, rec.Body.String())
	}

	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	rec = cvBDirectHandlerRequest(t, f, http.MethodPost, "/apply", map[string]any{},
		h.applyRunEval, map[string]string{"id": run.ID}, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous eval apply = %d", rec.Code)
	}
	if malformed := cvBRawRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval", "{", true); malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed eval start = %d", malformed.Code)
	}
	if malformed := cvBRawRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval/apply", "{", true); malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed eval apply = %d", malformed.Code)
	}
	setRunEvalExecForTest(nil)
	if currentRunEvalExec() != nil {
		t.Fatal("nil eval override was not cleared")
	}
	if (&runsHandlers{}).evalHub() == nil {
		t.Fatal("lazy eval hub missing")
	}
}
