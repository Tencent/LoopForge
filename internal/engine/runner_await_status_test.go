package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestRunnerTaskStatusEvent(t *testing.T) {
	source := domain.TelemetrySource{Layer: "executor", Adapter: "runner-v1", Provider: "codex"}
	cases := []struct {
		status string
		want   domain.TelemetryEvent
		ok     bool
	}{
		{status: domain.RunnerTaskLeased, ok: true, want: domain.TelemetryEvent{
			Category: "sandbox", Name: "runner.claimed", Phase: "progress",
			Message: "执行机器已领取任务，正在准备工作区和沙箱", Provider: "codex", Source: source,
		}},
		{status: domain.RunnerTaskRunning, ok: true, want: domain.TelemetryEvent{
			Category: "process", Name: "runner.started", Phase: "start",
			Message: "执行机器已启动任务", Provider: "codex", Source: source,
		}},
		{status: domain.RunnerTaskQueued},
		{status: domain.RunnerTaskSuccess},
		{status: domain.RunnerTaskFailed},
		{status: domain.RunnerTaskCanceled},
		{status: ""},
	}
	for _, tc := range cases {
		got, ok := runnerTaskStatusEvent(domain.RunnerTask{Status: tc.status, Provider: "codex"})
		if ok != tc.ok {
			t.Fatalf("status %q: ok = %v, want %v", tc.status, ok, tc.ok)
		}
		if got.Category != tc.want.Category || got.Name != tc.want.Name || got.Phase != tc.want.Phase ||
			got.Message != tc.want.Message || got.Provider != tc.want.Provider || got.Source != tc.want.Source {
			t.Fatalf("status %q: event = %+v, want %+v", tc.status, got, tc.want)
		}
	}
}

func TestRunnerTaskFinished(t *testing.T) {
	for status, want := range map[string]bool{
		domain.RunnerTaskQueued:   false,
		domain.RunnerTaskLeased:   false,
		domain.RunnerTaskRunning:  false,
		domain.RunnerTaskSuccess:  true,
		domain.RunnerTaskFailed:   true,
		domain.RunnerTaskCanceled: true,
		"":                        false,
	} {
		if got := runnerTaskFinished(status); got != want {
			t.Fatalf("runnerTaskFinished(%q) = %v, want %v", status, got, want)
		}
	}
}
