package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestRunDetailAndPublicLiveIncludeAutoAssignFailure(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")
	now := time.Now().UTC()
	for _, scenario := range []struct {
		name              string
		autoSelectSandbox bool
		executionMode     string
	}{
		{name: "fixed-sandbox"},
		{name: "automatic-sandbox", autoSelectSandbox: true},
		{name: "already-bound", autoSelectSandbox: true, executionMode: domain.ExecutionModeCloud},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			run := domain.Run{
				ID: scenario.name, WorkflowID: "wf", Status: "failed", CreatedAt: now,
				ExecutionContext: domain.TelemetryContext{
					ProjectID: "platform", ExecutionPool: domain.ExecutionPoolProjectAuto, ExecutionMode: scenario.executionMode,
				},
				WorkflowSnapshot: domain.WorkflowDefinition{
					ID: "wf", AutoSelectSandbox: scenario.autoSelectSandbox,
					Nodes: []domain.WorkflowNode{{ID: "work", Type: "script"}},
				},
				Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "pending"}},
			}
			var want []domain.RunEvent
			if scenario.autoSelectSandbox {
				selected := domain.RunEvent{ID: "sandbox", RunID: run.ID, Type: domain.RunEventSandboxSelected, Message: "已选择沙箱", Timestamp: now}
				run.Events = append(run.Events, selected)
				want = append(want, selected)
			}
			run.Events = append(run.Events, domain.RunEvent{
				ID: "first-failure", RunID: run.ID, Type: domain.RunEventFailed, Message: "上次自动分配失败", Timestamp: now,
			})
			// Published snapshots may contain more events than the detail history cap.
			for i := range 80 {
				run.Events = append(run.Events, domain.RunEvent{ID: "queued-" + strconv.Itoa(i), RunID: run.ID, Type: "run.queued", Timestamp: now})
			}
			failed := domain.RunEvent{
				ID: "latest-failure", RunID: run.ID, Type: domain.RunEventFailed, Timestamp: now,
				Message: "兼容的项目执行机器已离线超过 2 分钟，请启动执行机器后重试",
			}
			run.Events = append(run.Events, failed)
			if scenario.executionMode == "" {
				want = append(want, failed)
			}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}

			detail := requestEmptyJSON[domain.Run](t, h, http.MethodGet, "/api/runs/"+run.ID, http.StatusOK)
			if !slices.Equal(detail.Events, want) {
				t.Fatalf("GET events = %+v, want %+v", detail.Events, want)
			}
			stream, ok := s.RunStreamView(run.ID)
			if !ok {
				t.Fatal("run stream view is unavailable")
			}
			if live := publicLiveRun(stream); !slices.Equal(live.Events, want) {
				t.Fatalf("initial SSE events = %+v, want %+v", live.Events, want)
			}
			full, ok := s.RunState(run.ID)
			if !ok {
				t.Fatal("full run state is unavailable")
			}
			if live := publicLiveRun(full); !slices.Equal(live.Events, want) {
				t.Fatalf("published SSE events = %+v, want %+v", live.Events, want)
			}
		})
	}
}
