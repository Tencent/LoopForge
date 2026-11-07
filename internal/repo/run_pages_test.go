package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestQueryRunLogsPagesNewestThenOlder(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{ID: "run-logs", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}, LogSequence: 250}
	for i := 1; i <= 250; i++ {
		node := "fix"
		if i <= 120 {
			node = "comments"
		}
		run.Logs = append(run.Logs, domain.LogRecord{ID: "l", Sequence: int64(i), Body: node, Context: domain.TelemetryContext{NodeID: node}})
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	view, ok := s.RunView(run.ID)
	if !ok || len(view.Logs) != 0 {
		t.Fatalf("RunView should omit logs: ok=%v n=%d", ok, len(view.Logs))
	}
	latest, err := s.QueryRunLogs(run.ID, "comments", 0, 0, false, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !latest.HasMore || len(latest.Logs) != 100 || latest.Logs[0].Sequence != 21 || latest.Logs[99].Sequence != 120 {
		t.Fatalf("latest comments page = seq %d-%d hasMore=%v n=%d", latest.Logs[0].Sequence, latest.Logs[len(latest.Logs)-1].Sequence, latest.HasMore, len(latest.Logs))
	}
	older, err := s.QueryRunLogs(run.ID, "comments", 0, latest.NextBefore, false, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	if older.HasMore || len(older.Logs) != 20 || older.Logs[0].Sequence != 1 || older.Logs[19].Sequence != 20 {
		t.Fatalf("older comments page = n=%d hasMore=%v first=%d last=%d", len(older.Logs), older.HasMore, older.Logs[0].Sequence, older.Logs[len(older.Logs)-1].Sequence)
	}
	fromStart, err := s.QueryRunLogs(run.ID, "fix", 0, 0, true, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !fromStart.HasMore || fromStart.Logs[0].Sequence != 121 || len(fromStart.Logs) != 100 {
		t.Fatalf("after=0 fix page = n=%d first=%d hasMore=%v", len(fromStart.Logs), fromStart.Logs[0].Sequence, fromStart.HasMore)
	}
}

func TestQueryRunTelemetryFiltersAdapter(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{ID: "run-tel", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}, TelemetrySequence: 3, Telemetry: []domain.TelemetryEvent{
		{Sequence: 1, NodeID: "fix", Message: "live", Source: domain.TelemetrySource{Adapter: "anthropic-stream-v1"}},
		{Sequence: 2, NodeID: "fix", Message: "hook", Source: domain.TelemetrySource{Adapter: "codebuddy-hooks-v1"}},
		{Sequence: 3, NodeID: "fix", Message: "otel", Source: domain.TelemetrySource{Adapter: "codebuddy-otel-v1"}},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	live, err := s.QueryRunTelemetry(run.ID, "fix", "live", 0, 0, false, false, 100)
	if err != nil || len(live.Events) != 1 || live.Events[0].Message != "live" {
		t.Fatalf("live = %#v err=%v", live.Events, err)
	}
	hooks, err := s.QueryRunTelemetry(run.ID, "fix", "hooks", 0, 0, false, false, 100)
	if err != nil || len(hooks.Events) != 1 || hooks.Events[0].Message != "hook" {
		t.Fatalf("hooks = %#v err=%v", hooks.Events, err)
	}
	otel, err := s.QueryRunTelemetry(run.ID, "fix", "otel", 0, 0, false, false, 100)
	if err != nil || len(otel.Events) != 1 || otel.Events[0].Message != "otel" {
		t.Fatalf("otel = %#v err=%v", otel.Events, err)
	}
}

func TestAdapterSQLKeepsLegacyAndCrossCLIHookEventsVisible(t *testing.T) {
	hooks := adapterSQLFilter("hooks")
	if hooks.clause != " AND adapter IN (?, ?)" || len(hooks.args) != 2 {
		t.Fatalf("hooks SQL filter = %+v", hooks)
	}
	live := adapterSQLFilter("live")
	if live.clause != " AND adapter NOT IN (?, ?, ?)" || len(live.args) != 3 {
		t.Fatalf("live SQL filter = %+v", live)
	}
}

func TestArtifactReturnsSingleFileWithoutHydratingRun(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{ID: "run-art", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}, Artifacts: []domain.Artifact{
		{ID: "a1", Name: "fix-report", Path: "fix-report.md", Content: "hello artifact", Size: 14},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Artifact("run-art", "a1")
	if !ok || got.Content != "hello artifact" {
		t.Fatalf("artifact = ok=%v %#v", ok, got)
	}
	view, ok := s.RunView("run-art")
	if !ok || (len(view.Artifacts) > 0 && view.Artifacts[0].Content != "") {
		t.Fatalf("RunView leaked artifact content: %#v", view.Artifacts)
	}
}

func TestDumpRunLogsWalksPagesInOrder(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{ID: "run-dump-all", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}, LogSequence: 250}
	for i := 1; i <= 250; i++ {
		run.Logs = append(run.Logs, domain.LogRecord{Sequence: int64(i), Body: "line", Context: domain.TelemetryContext{NodeID: "fix"}})
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got, err := s.DumpRunLogs("run-dump-all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 || got[0].Sequence != 1 || got[249].Sequence != 250 {
		t.Fatalf("dump n=%d first=%d last=%d", len(got), got[0].Sequence, got[len(got)-1].Sequence)
	}
}

func TestRunSummariesKeepSnapshotNameOnly(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{
		ID: "run-snap-name", WorkflowID: "sop-sandbox", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: "sop-sandbox", Name: "Issue修复·沙箱",
			Nodes: []domain.WorkflowNode{{ID: "comments", Name: "评论", Script: "echo secret"}},
		},
		Nodes: map[string]*domain.NodeRun{"comments": {NodeID: "comments", Status: "succeeded", Output: "big"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.RunSummaries()
	if err != nil {
		t.Fatal(err)
	}
	var summary *domain.Run
	for i := range summaries {
		if summaries[i].ID == run.ID {
			summary = &summaries[i]
			break
		}
	}
	if summary == nil {
		t.Fatal("summary missing")
	}
	if summary.WorkflowSnapshot.ID != "sop-sandbox" || summary.WorkflowSnapshot.Name != "Issue修复·沙箱" {
		t.Fatalf("summary snapshot identity: %+v", summary.WorkflowSnapshot)
	}
	if len(summary.WorkflowSnapshot.Nodes) != 0 || summary.Nodes["comments"].Output != "" {
		t.Fatalf("summary transferred the graph or node output: %+v", summary.WorkflowSnapshot)
	}
}
