package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRequireRunNode(t *testing.T) {
	run := domain.Run{Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node"}}}
	if err := requireRunNode(run, ""); err != nil {
		t.Fatalf("empty node rejected: %v", err)
	}
	if err := requireRunNode(run, "node"); err != nil {
		t.Fatalf("known node rejected: %v", err)
	}
	if err := requireRunNode(run, "missing"); err == nil || err.Error() != "node not found in run" {
		t.Fatalf("unknown node err=%v", err)
	}
}

func TestIngestWorkerLogValidationMessages(t *testing.T) {
	e, _ := cvEEngine(t)
	run := domain.Run{ID: "log-validate", Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node"}}}
	source := domain.TelemetrySource{Layer: "executor", Adapter: "test"}
	cases := []struct {
		record *domain.LogRecord
		want   string
	}{
		{nil, "log is required"},
		{&domain.LogRecord{Body: "x"}, "log source.layer and source.adapter are required"},
		{&domain.LogRecord{Source: source}, "log body is required"},
		{&domain.LogRecord{Source: source, Body: strings.Repeat("x", 64*1024+1)}, "log body exceeds 65536 bytes"},
		{&domain.LogRecord{Source: source, Body: "x", Context: domain.TelemetryContext{NodeID: "missing"}}, "node not found in run"},
	}
	for i, tc := range cases {
		if err := e.ingestWorkerLog(run.ID, run, tc.record); err == nil || err.Error() != tc.want {
			t.Fatalf("case %d err=%v want %q", i, err, tc.want)
		}
	}
}

func TestIngestWorkerObservationValidationMessages(t *testing.T) {
	e, _ := cvEEngine(t)
	run := domain.Run{ID: "obs-validate", Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node"}}}
	source := domain.TelemetrySource{Layer: "executor", Adapter: "test"}
	cases := []struct {
		observation *domain.TelemetryEvent
		want        string
	}{
		{nil, "observation is required"},
		{&domain.TelemetryEvent{Source: domain.TelemetrySource{Layer: "executor"}}, "observation source.layer and source.adapter are required"},
		// Context.NodeID wins over the top-level NodeID when both are set.
		{&domain.TelemetryEvent{Source: source, NodeID: "node", Context: domain.TelemetryContext{NodeID: "missing"}}, "node not found in run"},
		{&domain.TelemetryEvent{Source: source, NodeID: "missing"}, "node not found in run"},
	}
	for i, tc := range cases {
		if err := e.ingestWorkerObservation(run.ID, run, tc.observation); err == nil || err.Error() != tc.want {
			t.Fatalf("case %d err=%v want %q", i, err, tc.want)
		}
	}
}

func TestIngestWorkerLogFillsDefaultsAndKeepsCallerRecord(t *testing.T) {
	e, st := cvEEngine(t)
	run := domain.Run{ID: "log-defaults", Status: "running", WorkflowID: "wf", Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running"}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	record := &domain.LogRecord{Body: "hello", Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}, Context: domain.TelemetryContext{NodeID: "node"}}
	if err := e.ingestWorkerLog(run.ID, run, record); err != nil {
		t.Fatal(err)
	}
	explicit := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := e.ingestWorkerLog(run.ID, run, &domain.LogRecord{Body: "second", Level: "warn", Stream: "stderr", Timestamp: explicit, Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}}); err != nil {
		t.Fatal(err)
	}
	if record.ID != "" || record.Level != "" || record.Context.RunID != "" {
		t.Fatalf("caller record mutated: %+v", record)
	}
	updated, _ := st.Run(run.ID)
	if len(updated.Logs) != 2 {
		t.Fatalf("logs=%d", len(updated.Logs))
	}
	first, second := updated.Logs[0], updated.Logs[1]
	if first.SpecVersion != domain.ObservabilitySpecVersion || !strings.HasPrefix(first.ID, "log-") || first.Timestamp.IsZero() {
		t.Fatalf("first identity not filled: %+v", first)
	}
	if first.Level != "info" || first.Stream != "worker" || first.Context.RunID != run.ID || first.Context.TraceID != run.ID || first.Context.NodeID != "node" {
		t.Fatalf("first defaults wrong: %+v", first)
	}
	if second.Sequence != first.Sequence+1 {
		t.Fatalf("sequence first=%d second=%d", first.Sequence, second.Sequence)
	}
	if second.Level != "warn" || second.Stream != "stderr" || !second.Timestamp.Equal(explicit) {
		t.Fatalf("second explicit fields overwritten: %+v", second)
	}
}
