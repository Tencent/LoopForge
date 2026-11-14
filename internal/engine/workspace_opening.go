package engine

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/workspaceprep"
)

// RecordWorkspaceOpening stores progress even when an idle workspace has no
// nodes. Runner progress is routed here by its authenticated request ID.
func (e *Engine) RecordWorkspaceOpening(runID string, progress domain.WorkspaceOpening) {
	run, ok := e.repo.RunState(runID)
	if !ok || !run.IsWorkspace() {
		return
	}
	token := e.resolveRunCredentials(run).GitToken
	progress.Message = workspaceprep.Sanitize(gitwork.Redact(progress.Message, token))
	progress.Output = workspaceprep.Sanitize(gitwork.Redact(progress.Output, token))
	e.updateRun(runID, func(r *domain.Run) {
		now := time.Now().UTC()
		progress.StartedAt = now
		if previous := r.LatestWorkspaceOpening(); previous != nil {
			if progress.Phase == "" && progress.State == "failed" {
				progress.Phase = previous.Phase
			}
			if progress.Phase == previous.Phase && previous.State == "running" && (progress.State == "failed" || progress.Message == previous.Message) {
				progress.StartedAt = previous.StartedAt
			}
		}
		progress.Warning = e.WorkspaceDockerfileWarning(*r)
		r.Events = append(r.Events, domain.RunEvent{ID: fmt.Sprintf("evt-%d", now.UnixNano()), RunID: runID, Type: "workspace.opening." + progress.State, Message: progress.Message, Timestamp: now, WorkspaceOpening: &progress})
		r.LogSequence++
		level := "info"
		if progress.State == "failed" {
			level = "error"
		}
		r.Logs = append(r.Logs, domain.LogRecord{SpecVersion: domain.ObservabilitySpecVersion, ID: fmt.Sprintf("log-%d", now.UnixNano()), Sequence: r.LogSequence, Timestamp: now, Level: level, Stream: "platform", Body: strings.TrimSpace(progress.Message + "\n" + progress.Output), Source: domain.TelemetrySource{Layer: "platform", Adapter: "workspace"}, Context: domain.TelemetryContext{RunID: runID}, Attributes: map[string]any{"phase": progress.Phase, "state": progress.State}})
	})
}

func (e *Engine) WorkspaceDockerfileWarning(run domain.Run) string {
	snapshot := run.WorkflowSnapshot.SandboxSnapshot
	if snapshot == nil || snapshot.Dockerfile == nil {
		return ""
	}
	frozen := snapshot.Dockerfile
	current, ok := e.repo.Dockerfile(frozen.ID)
	if !ok || current.Content == frozen.Content && maps.Equal(current.Files, frozen.Files) {
		return ""
	}
	return "项目 Dockerfile 已更新；当前工作区仍使用创建时的版本。要应用新配置，请先保存改动，再新开工作区。"
}
