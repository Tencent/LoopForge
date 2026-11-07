package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWorkspacePreparationRemoteBranches(t *testing.T) {
	e, st := cvEEngine(t)
	cvERunner(t, e, "prep-runner", domain.RunnerStatusOnline, "terminal")
	run := cvERunnerRun("prep-run", "prep-runner")
	run.Status = "waiting"
	run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "setup", Type: "script", Script: "echo setup", RunOnWorkspaceRebuild: true}}
	run.Nodes = map[string]*domain.NodeRun{"setup": {NodeID: "setup", Status: "succeeded"}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, _ domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"status":"ready","steps":[]}`)}, nil
	}
	state, err := e.WorkspacePreparationStatus(context.Background(), run.ID)
	if err != nil || state.Status != "ready" {
		t.Fatalf("status = %+v %v", state, err)
	}
	state, err = e.EnsureWorkspaceReady(context.Background(), run.ID)
	if err != nil || state.Status != "ready" {
		t.Fatalf("ensure = %+v %v", state, err)
	}
	state, err = e.PrepareNodeWorkspace(context.Background(), run, nil)
	if err != nil || state.Status != "ready" {
		t.Fatalf("node prepare = %+v %v", state, err)
	}
	request, err := e.TrustedWorkspaceRequest(run.ID, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationPreparationStatus})
	if err != nil || request.Sandbox == nil || request.Workflow == nil || request.RunID != run.ID {
		t.Fatalf("trusted = %+v %v", request, err)
	}
	response, err := e.ForwardWorkspaceRequest(context.Background(), run, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationPreparationStatus})
	if err != nil || len(response.Payload) == 0 {
		t.Fatalf("forward = %+v %v", response, err)
	}
	if err := e.recordNodeWorkspacePreparation(context.Background(), run, nil, run.WorkflowSnapshot.Nodes[0], "output"); err != nil {
		t.Fatal(err)
	}
	if err := e.recordNodeWorkspacePreparation(context.Background(), run, nil, domain.WorkflowNode{ID: "other"}, "output"); err != nil {
		t.Fatal(err)
	}

	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, _ domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Error: "remote failure"}, nil
	}
	if state, err := e.WorkspacePreparationStatus(context.Background(), run.ID); err == nil || state.Status != "failed" {
		t.Fatalf("remote error = %+v %v", state, err)
	}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, _ domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{}, nil
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), run.ID); err == nil {
		t.Fatal("empty remote state accepted")
	}

	legacy := run
	legacy.ExecutionContext.RunnerWorkspaceLayout = ""
	if _, err := e.preparedWorkspaceRequest(legacy, domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("legacy node workspace accepted preparation")
	}
	old := cvERunner(t, e, "old-prep", domain.RunnerStatusOnline, "terminal")
	old.RunnerVersion = "0.1.0"
	if err := st.PutRunner(old); err != nil {
		t.Fatal(err)
	}
	legacy.ExecutionContext.ExecutionRunnerID = old.ID
	legacy.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	if _, err := e.preparedWorkspaceRequest(legacy, domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("old runner accepted preparation")
	}
}

func TestCloudPoolSelectionStatusAndBindingErrors(t *testing.T) {
	e, st := cvEEngine(t)
	if got := ParseCloudRunnerProjects(" p1, ,* "); len(got) != 2 || got[0] != "p1" {
		t.Fatalf("projects = %v", got)
	}
	if cloudPoolServes([]string{"p1"}, "") || !cloudPoolServes([]string{"*"}, "p2") {
		t.Fatal("pool serves")
	}
	if poolRunnerEligible(domain.Runner{Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkBranchSync, Labels: []string{"pool"}}, "pool") {
		t.Fatal("old runner eligible")
	}
	eligible := domain.Runner{ID: "pool", Name: "Pool", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{" pool "}}
	if !poolRunnerEligible(eligible, "pool") {
		t.Fatal("eligible runner rejected")
	}
	if err := st.PutRunner(eligible); err != nil {
		t.Fatal(err)
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: " pool ", Projects: []string{"p1"}, Required: true})
	if configured, online := e.CloudPoolStatus(); !configured || online != 1 {
		t.Fatalf("pool status = %v %d", configured, online)
	}
	if !e.cloudPoolRequired("p1") || e.cloudPoolRequired("p2") {
		t.Fatal("pool required")
	}
	if picked, ok := e.cloudPoolRunner("p1"); !ok || picked.ID != eligible.ID {
		t.Fatalf("picked = %+v %v", picked, ok)
	}
	run := domain.Run{ID: "pool-run", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud, ProjectID: "p1"}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	bound, err := e.bindPoolRunner(context.Background(), run, "node")
	if err != nil || bound.ExecutionContext.ExecutionRunnerID != eligible.ID || !isPoolRun(bound) {
		t.Fatalf("bound = %+v %v", bound.ExecutionContext, err)
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{})
	if configured, online := e.CloudPoolStatus(); configured || online != 0 {
		t.Fatalf("disabled status = %v %d", configured, online)
	}
	noPool := domain.Run{ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner}}
	if got, err := e.bindPoolRunner(context.Background(), noPool, "n"); err != nil || got.ExecutionContext.ExecutionMode != domain.ExecutionModeRunner {
		t.Fatalf("non-pool binding = %+v %v", got, err)
	}
}

func TestRunRecoveryAndSchedulerBookkeeping(t *testing.T) {
	e, st := cvEEngine(t)
	e.executeRuns = false
	if got := e.ResumeInterruptedRuns(); got != nil {
		t.Fatalf("disabled recovery = %v", got)
	}
	e.executeRuns = true
	wf := domain.WorkflowDefinition{ID: "recovery-wf", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "failed", Type: "agent"}}}
	run := domain.Run{ID: "recover-failed", Status: "running", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"failed": {NodeID: "failed", Status: "failed"}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.ResumeInterruptedRuns()
	got, _ := st.RunState(run.ID)
	if got.Status != "failed" {
		t.Fatalf("recovered terminal = %+v", got)
	}
	if out := e.RecoverInterruptedRuns(); out == nil {
		// A non-nil result is not required; this call covers the compatibility entry point.
	}
	if e.ActiveRunCount() != 0 {
		t.Fatal("unexpected active runs")
	}
	_, cancel := context.WithCancel(context.Background())
	if !e.claimRunning("bookkeeping", cancel) || e.claimRunning("bookkeeping", cancel) {
		t.Fatal("claim running")
	}
	if e.ActiveRunCount() != 1 || !e.isRunSchedulerActive("bookkeeping") {
		t.Fatal("active scheduler missing")
	}
	unlock := e.lockRunExec("bookkeeping")
	unlock()
	e.clearRunning("bookkeeping")
	if e.ActiveRunCount() != 0 {
		t.Fatal("scheduler not cleared")
	}
	if err := e.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunControlHeartbeatNotifyAndHTTP(t *testing.T) {
	e, st := cvEEngine(t)
	e.ConfigureControl(ControlConfig{Role: " worker ", URL: "http://worker/", Token: "secret", FallbackURL: "http://fallback/", APIURL: "http://api/"})
	e.heartbeatInstance()
	if urls := st.LiveWorkerControlURLs(); len(urls) != 1 || urls[0] != "http://worker" {
		t.Fatalf("worker URLs = %v", urls)
	}
	if !e.controlTokenOK("secret") || e.controlTokenOK("bad") {
		t.Fatal("control token")
	}
	called := ""
	e.SetRunControlNotify(func(runID string) { called = runID })
	e.notifyRunControl("run")
	if called != "run" {
		t.Fatal(called)
	}
	e.notifyRunControl("")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get(controlTokenHeader) != "secret" {
			t.Error("missing token")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	postRunInterrupt(http.DefaultClient, server.URL, "run/id", "secret")
	HTTPRunControlNotify(server.URL, "secret")("run")
	postRunInterrupt(nil, server.URL, "run", "secret")
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}

	handler := func(token, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/runs/"+id+"/interrupt", nil)
		req.SetPathValue("id", id)
		req.Header.Set(controlTokenHeader, token)
		rec := httptest.NewRecorder()
		e.HandleRunInterrupt(rec, req)
		return rec
	}
	if rec := handler("bad", "run"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized = %d", rec.Code)
	}
	if rec := handler("secret", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty ID = %d", rec.Code)
	}
	if rec := handler("secret", "run"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "interrupted") {
		t.Fatalf("interrupt = %d %s", rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.StartInstanceHeartbeat(ctx)
	time.Sleep(time.Millisecond)
}

func TestConversationWorkspaceTokenLeaseAndIdle(t *testing.T) {
	e, st := cvEEngine(t)
	ctx := WithConversationWorkspaceToken(context.Background(), "token", "run")
	if ConversationWorkspaceToken(ctx, "run") != "token" || ConversationWorkspaceToken(ctx, "other") != "" || ConversationWorkspaceToken(context.Background()) != "" {
		t.Fatal("workspace token scope")
	}
	run := domain.Run{ID: "conversation", Status: "failed", Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := e.BeginConversationWorkspaceUse(run.ID)
	if err != nil || token == "" {
		t.Fatalf("begin use = %q %v", token, err)
	}
	if err := e.RenewConversationWorkspaceUse(run.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.ConversationWorkspaceStatus(run.ID); !ok {
		t.Fatal("workspace status missing")
	}
	if !e.conversationWorkspaceIdle(run) {
		t.Fatal("failed run not idle")
	}
	run.Status = "running"
	if e.conversationWorkspaceIdle(run) {
		t.Fatal("running run idle")
	}
	e.WorkspaceActive = func(domain.Run) bool { return true }
	run.Status = "failed"
	if e.conversationWorkspaceIdle(run) {
		t.Fatal("active workspace idle")
	}
	e.WorkspaceActive = nil
	if err := e.EndConversationWorkspaceUse(context.Background(), run.ID, token); err == nil {
		t.Fatal("workspace save unexpectedly succeeded without a manager")
	}
}
