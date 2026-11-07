package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func TestRunStreamDeliversLiveSnapshots(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "live", Name: "Live", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "command", Command: "printf live", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, ok := s.Run(run.ID)
		if ok && current.LogSequence > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not produce a log before the SSE assertion deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{deps: &deps{repo: s, engine: e}}
	srv.stream = &streamHandlers{deps: srv.deps}
	g := gin.New()
	g.GET("/api/runs/:id/stream", srv.stream.streamRun)
	req := httptest.NewRequest("GET", "/api/runs/"+run.ID+"/stream", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		g.ServeHTTP(recorder, req)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: run") || !strings.Contains(body, run.ID) || !strings.Contains(body, "telemetryDelta") || !strings.Contains(body, "logDelta") {
		t.Fatalf("stream missing live snapshot: %s", body)
	}
	if !strings.Contains(body, `"logDelta":[{`) {
		t.Fatalf("stream did not deliver incremental logs: %s", body)
	}
}

func TestDeleteRunEndpoint(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "wf", Name: "WF", SandboxProfileID: "local"}); err != nil {
		t.Fatal(err)
	}
	h := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	if err := st.PutRun(domain.Run{ID: "done-run", WorkflowID: "wf", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(domain.Run{ID: "live-run", WorkflowID: "wf", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	requestJSON[map[string]string](t, h, http.MethodDelete, "/api/runs/live-run", nil, http.StatusBadRequest)
	if _, ok := st.Run("live-run"); !ok {
		t.Fatal("live run was deleted")
	}
	requestJSON[map[string]string](t, h, http.MethodDelete, "/api/runs/done-run", nil, http.StatusOK)
	if _, ok := st.Run("done-run"); ok {
		t.Fatal("settled run was not deleted")
	}
}
