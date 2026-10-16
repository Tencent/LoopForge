package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/observability"
	"fmt"
	"strings"
	"time"
)

func (e *Engine) AppendRunEvent(runID, typ, msg string) {
	e.updateRun(runID, func(r *domain.Run) {
		e.event(r, "", typ, msg)
	})
}

func (e *Engine) event(r *domain.Run, nodeID, t, msg string) {
	r.Events = append(r.Events, domain.RunEvent{ID: fmt.Sprintf("evt-%d", time.Now().UnixNano()), RunID: r.ID, NodeID: nodeID, Type: t, Message: msg, Timestamp: time.Now().UTC()})
}

func (e *Engine) observe(runID, nodeID string, observation domain.TelemetryEvent) {
	if current, ok := e.repo.RunState(runID); ok && isTerminalRunStatus(current.Status) && observation.Name != "sandbox.closed" {
		return
	}
	e.enqueueObservation(runID, nodeID, observation)
}

func (e *Engine) IngestOTLPTraces(events []domain.TelemetryEvent) int {
	accepted := 0
	for _, event := range events {
		runID := strings.TrimSpace(event.RunID)
		if runID == "" {
			runID = strings.TrimSpace(event.Context.RunID)
		}
		if runID == "" {
			continue
		}
		if _, ok := e.repo.RunState(runID); !ok {
			continue
		}
		nodeID := strings.TrimSpace(event.NodeID)
		if nodeID == "" {
			nodeID = strings.TrimSpace(event.Context.NodeID)
		}
		e.observe(runID, nodeID, event)
		accepted++
	}
	e.flushAllObservations()
	return accepted
}

// IngestWorkerEnvelope is the executor-neutral ingress used by local adapters
// and remote workers. Native provider payloads must be normalized before here.
func (e *Engine) IngestWorkerEnvelope(runID string, envelope domain.WorkerEnvelope) error {
	if envelope.SpecVersion != domain.ObservabilitySpecVersion {
		return fmt.Errorf("unsupported observability spec %q", envelope.SpecVersion)
	}
	run, ok := e.repo.RunState(runID)
	if !ok {
		return fmt.Errorf("run not found")
	}
	switch envelope.Type {
	case "observation":
		if envelope.Observation == nil {
			return fmt.Errorf("observation is required")
		}
		if envelope.Observation.Source.Layer == "" || envelope.Observation.Source.Adapter == "" {
			return fmt.Errorf("observation source.layer and source.adapter are required")
		}
		nodeID := envelope.Observation.Context.NodeID
		if nodeID == "" {
			nodeID = envelope.Observation.NodeID
		}
		if nodeID != "" {
			if _, exists := run.Nodes[nodeID]; !exists {
				return fmt.Errorf("node not found in run")
			}
		}
		e.observe(runID, nodeID, *envelope.Observation)
		e.flushAllObservations()
		return nil
	case "log":
		if envelope.Log == nil {
			return fmt.Errorf("log is required")
		}
		log := *envelope.Log
		if log.Source.Layer == "" || log.Source.Adapter == "" {
			return fmt.Errorf("log source.layer and source.adapter are required")
		}
		if log.Body == "" {
			return fmt.Errorf("log body is required")
		}
		if len(log.Body) > 64*1024 {
			return fmt.Errorf("log body exceeds 65536 bytes")
		}
		if log.Context.NodeID != "" {
			if _, exists := run.Nodes[log.Context.NodeID]; !exists {
				return fmt.Errorf("node not found in run")
			}
		}
		e.updateRun(runID, func(r *domain.Run) {
			now := time.Now().UTC()
			r.LogSequence++
			log.SpecVersion = domain.ObservabilitySpecVersion
			log.ID = fmt.Sprintf("log-%d", now.UnixNano())
			log.Sequence = r.LogSequence
			if log.Timestamp.IsZero() {
				log.Timestamp = now
			}
			log.Context.RunID = runID
			log.Context.TraceID = runID
			if log.Level == "" {
				log.Level = "info"
			}
			if log.Stream == "" {
				log.Stream = "worker"
			}
			r.Logs = append(r.Logs, log)
			if len(r.Logs) > 8000 {
				r.Logs = append([]domain.LogRecord(nil), r.Logs[len(r.Logs)-8000:]...)
			}
		})
		return nil
	default:
		return fmt.Errorf("type must be observation or log")
	}
}

func logAttributes(event domain.TelemetryEvent) map[string]any {
	attrs := map[string]any{"eventName": event.Name, "category": event.Category, "phase": event.Phase}
	if event.Attributes == nil {
		return attrs
	}
	for _, key := range []string{"input", "status", "exitCode", "repositoryId", "repositoryName", "repositoryDir"} {
		if v, ok := event.Attributes[key]; ok && v != nil {
			attrs[key] = v
		}
	}
	return attrs
}

func shouldRecordLog(event domain.TelemetryEvent) bool {
	if event.Source.Adapter == observability.AdapterHooks || event.Source.Adapter == observability.AdapterOTEL {
		return false
	}
	if event.Category == "reasoning" || event.Category == "usage" || (event.Category == "output" && strings.HasSuffix(event.Name, ".delta")) {
		return false
	}
	if event.Category == "model" && event.Name == "model.request" {
		return false
	}
	if isNoisyWorkerLog(event) {
		return false
	}
	return event.Message != ""
}

// isNoisyWorkerLog drops tool-lifecycle heartbeats. Worker logs are for humans:
// keep "Bash git status" / "Read foo.go", not "Bash 完成" / "工具完成".
func isNoisyWorkerLog(event domain.TelemetryEvent) bool {
	if event.Level == "error" {
		return false
	}
	if event.Category == "tool" && event.Phase == "finish" {
		return true
	}
	return toolCompletionMessage(event.Message)
}

func toolCompletionMessage(msg string) bool {
	s := strings.TrimSpace(msg)
	if s == "" {
		return false
	}
	switch s {
	case "工具完成", "tool result", "工具返回结果", "工具结果":
		return true
	}
	if strings.HasPrefix(s, "工具完成 ") {
		return true
	}
	name, rest, ok := strings.Cut(s, " ")
	if !ok || !looksLikeToolName(name) {
		return false
	}
	return rest == "完成" || strings.HasPrefix(rest, "完成:") || strings.HasPrefix(rest, "完成：") || rest == "结果" || strings.HasPrefix(rest, "结果")
}

func looksLikeToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == '/' {
			continue
		}
		return false
	}
	return true
}

func boundedLogBody(value string) string {
	const limit = 64 * 1024
	if len(value) <= limit {
		return value
	}
	return value[:limit] + " … [log truncated]"
}

func (e *Engine) observationContext(run domain.Run, nodeID string, nr *domain.NodeRun, event domain.TelemetryEvent) domain.TelemetryContext {
	ctx := run.ExecutionContext
	ctx.RunID, ctx.TraceID, ctx.WorkflowID, ctx.WorkflowVersion, ctx.NodeID, ctx.SessionID, ctx.ParentSpanID = run.ID, run.ID, run.WorkflowID, run.WorkflowSnapshot.Version, nodeID, event.SessionID, event.ParentID
	if nr != nil {
		ctx.Attempt = nr.Attempt
		ctx.SandboxID = nr.SandboxID
		ctx.SpanID = fmt.Sprintf("%s:%s:%d", run.ID, nodeID, nr.Attempt)
	}
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID == nodeID {
			ctx.AgentID = node.AgentID
			ctx.ExecutorID = node.ExecutorID
			ctx.MetricStage = node.MetricStage
			if agent, ok := e.repo.Agent(node.AgentID); ok {
				ctx.AgentVersion = agent.Version
				if agent.ExecutorID != "" {
					ctx.ExecutorID = agent.ExecutorID
				}
			}
			break
		}
	}
	for _, task := range e.repo.Tasks() {
		if task.RunID == run.ID {
			ctx.TaskID = task.ID
			ctx.IssueID = task.IssueID
			if issue, ok := e.repo.Issue(task.IssueID); ok {
				ctx.ProjectID = issue.ProjectID
				ctx.TAPDID = issue.RequirementTAPDID()
			}
			break
		}
	}
	return ctx
}

const maxTelemetryProgressRunes = 24_000

// coalesceProgressTelemetry keeps parallel streaming nodes compact. Looking at
// only the global tail fails when two model streams interleave token-by-token.
func coalesceProgressTelemetry(events *[]domain.TelemetryEvent, observation domain.TelemetryEvent) bool {
	if observation.Phase != "progress" || observation.Usage != nil || (observation.Category != "reasoning" && observation.Category != "output" && observation.Name != "tool.input") {
		return false
	}
	values := *events
	for i := len(values) - 1; i >= 0; i-- {
		candidate := values[i]
		if candidate.NodeID != observation.NodeID {
			continue
		}
		if candidate.Phase != "progress" || candidate.Category != observation.Category || candidate.Name != observation.Name {
			return false
		}
		candidate.Message = compactProgressText(candidate.Message + observation.Message)
		candidate.Sequence = observation.Sequence
		candidate.Timestamp = observation.Timestamp
		candidate.Level = observation.Level
		copy(values[i:], values[i+1:])
		values[len(values)-1] = candidate
		*events = values
		return true
	}
	return false
}

func compactProgressText(value string) string {
	runes := []rune(value)
	if len(runes) <= maxTelemetryProgressRunes {
		return value
	}
	half := maxTelemetryProgressRunes / 2
	return string(runes[:half]) + "\n…[stream truncated]…\n" + string(runes[len(runes)-half:])
}

func appendControlTelemetry(r *domain.Run, nodeID, name, message string) {
	now := time.Now().UTC()
	r.TelemetrySequence++
	r.Telemetry = append(r.Telemetry, domain.TelemetryEvent{ID: fmt.Sprintf("obs-%d", now.UnixNano()), RunID: r.ID, NodeID: nodeID, Sequence: r.TelemetrySequence, Timestamp: now, Category: "verification", Name: name, Phase: "finish", Level: "info", Message: message})
}
