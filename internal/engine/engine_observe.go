package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/observability"
)

type pendingObservation struct {
	runID, nodeID string
	event         domain.TelemetryEvent
}

const (
	observationFlushInterval = 50 * time.Millisecond
	observationFlushKickSize = 32
)

func (e *Engine) enqueueObservation(runID, nodeID string, observation domain.TelemetryEvent) {
	if e == nil {
		return
	}
	e.obsMu.Lock()
	e.obsPending = append(e.obsPending, pendingObservation{runID: runID, nodeID: nodeID, event: observation})
	n := len(e.obsPending)
	e.obsMu.Unlock()
	if n >= observationFlushKickSize {
		e.signalObservationFlush()
	}
}

func (e *Engine) signalObservationFlush() {
	if e == nil || e.obsKick == nil {
		return
	}
	select {
	case e.obsKick <- struct{}{}:
	default:
	}
}

func (e *Engine) observationFlushLoop(ctx context.Context) {
	if e == nil || e.obsKick == nil {
		return
	}
	ticker := time.NewTicker(observationFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.flushAllObservations()
			return
		case <-e.obsKick:
			e.flushAllObservations()
		case <-ticker.C:
			e.flushAllObservations()
		}
	}
}

func (e *Engine) flushAllObservations() {
	if e == nil {
		return
	}
	for _, runID := range e.pendingObservationRunIDs() {
		e.updateRun(runID, nil)
	}
}

func (e *Engine) pendingObservationRunIDs() []string {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()
	seen := make(map[string]struct{}, 4)
	ids := make([]string, 0, 4)
	for _, item := range e.obsPending {
		if _, ok := seen[item.runID]; ok {
			continue
		}
		seen[item.runID] = struct{}{}
		ids = append(ids, item.runID)
	}
	return ids
}

func (e *Engine) takePendingObservations(runID string) []pendingObservation {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()
	if len(e.obsPending) == 0 {
		return nil
	}
	taken := make([]pendingObservation, 0, len(e.obsPending))
	kept := e.obsPending[:0]
	for _, item := range e.obsPending {
		if item.runID == runID {
			taken = append(taken, item)
			continue
		}
		kept = append(kept, item)
	}
	e.obsPending = kept
	return taken
}

func (e *Engine) applyObservation(r *domain.Run, nodeID string, observation domain.TelemetryEvent) {
	if isTerminalRunStatus(r.Status) && observation.Name != "sandbox.closed" {
		return
	}
	// Worker/OTLP retries must be idempotent. Preserve a producer supplied ID
	// and ignore it when the same Run has already accepted that observation.
	// Internal observations have no ID and continue to receive one here.
	if id := strings.TrimSpace(observation.ID); id != "" {
		if e.repo.TelemetryEventExists(r.ID, id) {
			return
		}
		for _, existing := range r.Telemetry {
			if existing.ID == id {
				return
			}
		}
	}
	now := time.Now().UTC()
	if strings.TrimSpace(observation.ID) == "" {
		observation.ID = fmt.Sprintf("obs-%d", now.UnixNano())
	}
	observation.RunID = r.ID
	observation.NodeID = nodeID
	r.TelemetrySequence++
	observation.Sequence = r.TelemetrySequence
	if observation.Timestamp.IsZero() || (observation.Source.Adapter != observability.AdapterHooks && observation.Source.Adapter != observability.AdapterOTEL) {
		observation.Timestamp = now
	}
	observation.SpecVersion = domain.ObservabilitySpecVersion
	if observation.Level == "" {
		observation.Level = "info"
	}
	clarifyProcessLog(&observation)
	nr := r.Nodes[nodeID]
	observation.Context = e.observationContext(*r, nodeID, nr, observation)
	if observation.Source.Layer == "" {
		layer := "orchestrator"
		if observation.Category == "sandbox" || observation.Category == "process" {
			layer = "sandbox"
		}
		if observation.Category == "verification" {
			layer = "verification"
		}
		observation.Source = domain.TelemetrySource{Layer: layer, Adapter: "agentflow-control-plane", Provider: observation.Provider}
	}
	if shouldRecordLog(observation) {
		r.LogSequence++
		stream := "worker"
		if observation.Name == "process.stdout" {
			stream = "stdout"
		}
		if observation.Name == "process.stderr" {
			stream = "stderr"
		}
		if observation.Source.Layer != "executor" && observation.Category != "process" {
			stream = "platform"
		}
		r.Logs = append(r.Logs, domain.LogRecord{SpecVersion: domain.ObservabilitySpecVersion, ID: fmt.Sprintf("log-%d", now.UnixNano()), Sequence: r.LogSequence, Timestamp: now, Level: observation.Level, Stream: stream, Body: boundedLogBody(observation.Message), Source: observation.Source, Context: observation.Context, Attributes: logAttributes(observation)})
		if len(r.Logs) > 8000 {
			r.Logs = append([]domain.LogRecord(nil), r.Logs[len(r.Logs)-8000:]...)
		}
	}
	if nr != nil {
		if observation.Provider != "" {
			nr.Provider = observation.Provider
		}
		if observation.Model != "" {
			nr.Model = observation.Model
		}
		if observation.SessionID != "" {
			nr.SessionID = observation.SessionID
			nr.ResumeSessionID = ""
		}
		metricsSideChannel := observation.Source.Adapter == observability.AdapterHooks || observation.Source.Adapter == observability.AdapterOTEL
		if !metricsSideChannel {
			switch observation.Category {
			case "reasoning":
				nr.Activity = "模型正在推理"
			case "output":
				nr.Activity = "模型正在生成输出"
			case "tool":
				if observation.Name == "tool.call" && (observation.Phase != "finish" || observation.Level == "error") {
					nr.Activity = "调用工具：" + observation.Message
				}
			case "sandbox":
				if observation.Name != "sandbox.closed" {
					nr.Activity = observation.Message
				}
			case "process":
				if observation.Name == "process.started" {
					label := observation.Provider
					if label == "" {
						label = "执行器"
					}
					if observation.Model != "" {
						label += " · " + observation.Model
					}
					nr.Activity = label + " 已启动"
				} else {
					nr.Activity = observation.Message
				}
			default:
				nr.Activity = observation.Message
				if nr.Activity == "" {
					nr.Activity = observation.Name
				}
			}
			if observation.Category == "tool" && observation.Name == "tool.call" && observation.Phase == "finish" {
				nr.ToolCalls++
				r.ToolCalls++
			}
			if observation.Usage != nil {
				applyUsage(r, nr, observation.SessionID, *observation.Usage)
			}
		}
	}
	coalesced := coalesceProgressTelemetry(&r.Telemetry, observation)
	if !coalesced {
		r.Telemetry = append(r.Telemetry, observation)
	}
	if len(r.Telemetry) > 4000 {
		r.Telemetry = append([]domain.TelemetryEvent(nil), r.Telemetry[len(r.Telemetry)-4000:]...)
	}
}
