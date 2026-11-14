package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func workerIngestGuardRun(t *testing.T, id string) (*Engine, domain.Run, func() domain.Run) {
	t.Helper()
	e, st := cvEEngine(t)
	run := domain.Run{ID: id, Status: "running", WorkflowID: "wf", Nodes: map[string]*domain.NodeRun{
		"node":  {NodeID: "node", Status: "running"},
		"other": {NodeID: "other", Status: "running"},
	}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	load := func() domain.Run {
		t.Helper()
		updated, ok := st.Run(id)
		if !ok {
			t.Fatalf("run %s not found", id)
		}
		return updated
	}
	return e, run, load
}

func telemetryByName(run domain.Run, name string) (domain.TelemetryEvent, bool) {
	for _, event := range run.Telemetry {
		if event.Name == name {
			return event, true
		}
	}
	return domain.TelemetryEvent{}, false
}

// The envelope path must attribute an accepted observation to the resolved
// node (Context.NodeID first, then NodeID, otherwise run scoped) and flush it
// before returning, so readers see it without waiting for the flush loop.
func TestIngestWorkerEnvelopeObservationAttributesNodeAndFlushesImmediately(t *testing.T) {
	e, run, load := workerIngestGuardRun(t, "obs-attribution")
	source := domain.TelemetrySource{Layer: "executor", Adapter: "test"}
	envelopes := []domain.TelemetryEvent{
		{Name: "obs.context-wins", Message: "a", NodeID: "other", Context: domain.TelemetryContext{NodeID: "node"}, Source: source},
		{Name: "obs.top-level", Message: "b", NodeID: "other", Source: source},
		{Name: "obs.run-scoped", Message: "c", Source: source},
	}
	for _, observation := range envelopes {
		observation := observation
		if err := e.IngestWorkerEnvelope(run.ID, domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "observation", Observation: &observation}); err != nil {
			t.Fatalf("%s rejected: %v", observation.Name, err)
		}
		// No explicit flush here: the ingest path itself must have flushed.
		if _, ok := telemetryByName(load(), observation.Name); !ok {
			t.Fatalf("%s not visible right after ingest", observation.Name)
		}
	}
	updated := load()
	want := map[string]string{"obs.context-wins": "node", "obs.top-level": "other", "obs.run-scoped": ""}
	for name, node := range want {
		event, _ := telemetryByName(updated, name)
		if event.NodeID != node || event.RunID != run.ID {
			t.Fatalf("%s attributed to run=%q node=%q, want node %q", name, event.RunID, event.NodeID, node)
		}
	}
}

func TestIngestWorkerEnvelopeObservationRejectsUnknownNodeWithoutRecording(t *testing.T) {
	e, run, load := workerIngestGuardRun(t, "obs-unknown-node")
	observation := domain.TelemetryEvent{Name: "obs.unknown", Context: domain.TelemetryContext{NodeID: "missing"}, Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}}
	err := e.IngestWorkerEnvelope(run.ID, domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "observation", Observation: &observation})
	if err == nil || err.Error() != "node not found in run" {
		t.Fatalf("err=%v", err)
	}
	e.flushAllObservations()
	if _, ok := telemetryByName(load(), "obs.unknown"); ok {
		t.Fatal("rejected observation was recorded")
	}
}

func TestIngestWorkerLogRequiresBothSourceFields(t *testing.T) {
	e, run, load := workerIngestGuardRun(t, "log-source")
	for i, source := range []domain.TelemetrySource{{Layer: "executor"}, {Adapter: "test"}} {
		err := e.ingestWorkerLog(run.ID, run, &domain.LogRecord{Body: "x", Source: source})
		if err == nil || err.Error() != "log source.layer and source.adapter are required" {
			t.Fatalf("case %d err=%v", i, err)
		}
	}
	for i, source := range []domain.TelemetrySource{{Layer: "executor"}, {Adapter: "test"}} {
		err := e.ingestWorkerObservation(run.ID, run, &domain.TelemetryEvent{Name: "x", Source: source})
		if err == nil || err.Error() != "observation source.layer and source.adapter are required" {
			t.Fatalf("observation case %d err=%v", i, err)
		}
	}
	if logs := load().Logs; len(logs) != 0 {
		t.Fatalf("rejected logs recorded: %+v", logs)
	}
}

func TestIngestWorkerLogAcceptsBodyAtLimit(t *testing.T) {
	e, run, load := workerIngestGuardRun(t, "log-limit")
	body := strings.Repeat("x", 64*1024)
	if err := e.ingestWorkerLog(run.ID, run, &domain.LogRecord{Body: body, Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"}}); err != nil {
		t.Fatalf("body of exactly 65536 bytes rejected: %v", err)
	}
	logs := load().Logs
	if len(logs) != 1 || logs[0].Body != body {
		t.Fatalf("logs=%d", len(logs))
	}
}
