package repo

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/observability"

	"gorm.io/gorm"
)

// ---------- Run header mapping ----------

func runFromRow(row runRow) domain.Run {
	run := domain.Run{
		ID: row.ID, WorkflowID: row.WorkflowID, Status: row.Status, Revision: row.Revision,
		Input: row.Input, ToolCalls: row.ToolCalls,
		TelemetrySequence: row.TelemetrySequence, LogSequence: row.LogSequence,
		OwnerInstanceID: row.OwnerInstanceID, Generation: row.Generation,
		WorkspaceHost: row.WorkspaceHost, WorkspaceRoot: row.WorkspaceRoot, WorkspaceEditorID: row.WorkspaceEditorID,
		HumanContext: row.HumanContext, CancelRequested: row.CancelRequested, PauseRequested: row.PauseRequested,
		SandboxID: row.SandboxID, AdmissionState: row.AdmissionState,
		ConcurrencyExempt: row.QuotaExempt != nil && *row.QuotaExempt,
	}
	if row.QueueEntered != nil {
		run.QueueEnteredAt = *row.QueueEntered
	}
	_ = json.Unmarshal(row.Repository, &run.Repository)
	if len(row.Repositories) > 0 {
		_ = json.Unmarshal(row.Repositories, &run.Repositories)
	}
	_ = json.Unmarshal(row.Snapshot, &run.WorkflowSnapshot)
	_ = json.Unmarshal(row.ExecutionContext, &run.ExecutionContext)
	_ = json.Unmarshal(row.UsageJSON, &run.Usage)
	if row.Created != nil {
		run.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		run.UpdatedAt = *row.Updated
	}
	if row.WorkspaceEditUntil != nil {
		run.WorkspaceEditUntil = *row.WorkspaceEditUntil
	}
	if row.LeaseUntil != nil {
		run.LeaseUntil = *row.LeaseUntil
	}
	if row.WorkspaceCleaned != nil {
		run.WorkspaceCleanedAt = *row.WorkspaceCleaned
	}
	if row.CleanupNotBefore != nil {
		run.WorkspaceCleanupNotBefore = *row.CleanupNotBefore
	}
	return run
}

func runRowValues(run domain.Run) map[string]any {
	return map[string]any{
		"id": run.ID, "workflow_id": run.WorkflowID, "status": run.Status, "revision": run.Revision,
		"input": run.Input, "human_context": run.HumanContext,
		"quota_project_id": strings.TrimSpace(run.ExecutionContext.ProjectID), "quota_parent_run_id": strings.TrimSpace(run.ExecutionContext.ParentRunID),
		"quota_exempt": run.ConcurrencyExempt, "admission_state": run.AdmissionState, "queue_entered_at": timePtr(run.QueueEnteredAt),
		"quota_bucket":     runAdmissionBucketKey(run),
		"cancel_requested": run.CancelRequested, "pause_requested": run.PauseRequested, "sandbox_id": run.SandboxID,
		"repository": jsonBytes(run.Repository),
		// master 的 repositoriesJSON：空集合写 SQL NULL 而不是 '[]'。
		"repositories": repositoriesJSONValue(run.Repositories), "snapshot": jsonBytes(run.WorkflowSnapshot),
		"execution_context": jsonBytes(run.ExecutionContext), "usage_json": jsonBytes(run.Usage),
		"tool_calls": run.ToolCalls, "telemetry_sequence": run.TelemetrySequence, "log_sequence": run.LogSequence,
		"owner_instance_id": run.OwnerInstanceID, "lease_until": timePtr(run.LeaseUntil),
		"execution_generation": run.Generation, "workspace_host": run.WorkspaceHost, "workspace_root": run.WorkspaceRoot,
		"workspace_cleaned_at":         timePtr(run.WorkspaceCleanedAt),
		"workspace_cleanup_not_before": timePtr(run.WorkspaceCleanupNotBefore),
		"workspace_editor_id":          run.WorkspaceEditorID, "workspace_edit_until": timePtr(run.WorkspaceEditUntil),
		"created_at": timePtr(run.CreatedAt), "updated_at": timePtr(run.UpdatedAt),
	}
}

func takeRun(db *gorm.DB, id string) (runRow, bool) {
	var row runRow
	if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
		return runRow{}, false
	}
	return row, true
}

// ---------- Reads ----------

func (r *RunsRepo) Runs() []domain.Run {
	var rows []runRow
	if err := r.DB.Find(&rows).Error; err != nil {
		return []domain.Run{}
	}
	out := make([]domain.Run, 0, len(rows))
	for _, row := range rows {
		run := runFromRow(row)
		if err := r.loadRunNodes(&run); err != nil {
			continue
		}
		if err := r.loadArtifactMeta(&run); err != nil {
			continue
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// RunSummaries returns list-safe Run data. The database removes large node
// output and workflow snapshots before transfer instead of loading them just to
// discard them in the HTTP layer.
func (r *RunsRepo) RunSummaries() ([]domain.Run, error) {
	return r.RunSummariesFor(domain.AnalyticsFilter{}, "")
}

// RunSummariesFor filters headers before loading child rows. Project fallback
// uses the same Go normalization as legacy runs, without hydrating issue payloads.
func (r *RunsRepo) RunSummariesFor(filter domain.AnalyticsFilter, issueID string) ([]domain.Run, error) {
	if filter.ProjectIDs != nil && len(filter.ProjectIDs) == 0 {
		return []domain.Run{}, nil
	}
	q := r.DB
	if filter.ProjectIDs != nil {
		expr := "json_extract(execution_context, '$.projectId')"
		if r.isMySQL() {
			expr = "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.projectId'))"
		}
		q = q.Where(expr+" IN ? OR "+expr+" IS NULL OR "+expr+" = ''", filter.ProjectIDs)
	}
	if filter.WorkflowID != "" {
		q = q.Where("workflow_id = ?", filter.WorkflowID)
	}
	if projectID := strings.TrimSpace(filter.ProjectID); projectID != "" {
		expr := "json_extract(execution_context, '$.projectId')"
		if r.isMySQL() {
			expr = "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.projectId'))"
		}
		// Keep legacy rows with an empty project stamp; FilterRunsByProject
		// still resolves those through the linked issue.
		q = q.Where(expr+" = ? OR "+expr+" IS NULL OR "+expr+" = ''", projectID)
	}
	if !filter.From.IsZero() {
		q = q.Where("created_at >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		q = q.Where("created_at <= ? OR created_at IS NULL", filter.To)
	}
	if issueID != "" {
		expr := "json_extract(execution_context, '$.issueId')"
		if r.isMySQL() {
			expr = "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.issueId'))"
		}
		q = q.Where(expr+" = ?", issueID)
	}
	var rows []runRow
	// 与 master 的 runSummaryHeaderColumns 一致：列表携带控制位与
	// human_context、多仓 repositories，且 input 在 SQL 层截断到 500 字符
	//（master 用 LEFT(input, 500)，使 handler 的 clip 不再追加省略号）。
	// List rows only need the frozen SOP identity. The graph, scripts, and
	// commands stay in the snapshot column and are not transferred.
	snapshotExpr := "json_object('id', json_extract(snapshot, '$.id'), 'name', json_extract(snapshot, '$.name')) AS snapshot"
	if r.isMySQL() {
		snapshotExpr = "JSON_OBJECT('id', JSON_UNQUOTE(JSON_EXTRACT(snapshot, '$.id')), 'name', JSON_UNQUOTE(JSON_EXTRACT(snapshot, '$.name'))) AS snapshot"
	}
	if err := q.Select("id", "workflow_id", "status", "admission_state", "queue_entered_at", "quota_exempt", "cancel_requested", "pause_requested", "human_context", "revision", "SUBSTR(input, 1, 500) AS input", "repository", "repositories", snapshotExpr, "execution_context", "usage_json", "tool_calls", "telemetry_sequence", "log_sequence", "owner_instance_id", "lease_until", "execution_generation", "workspace_host", "workspace_root", "workspace_cleaned_at", "created_at", "updated_at").
		Order("created_at DESC, id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	candidates := make([]domain.Run, 0, len(rows))
	for _, row := range rows {
		run := runFromRow(row)
		if issueID != "" && run.ExecutionContext.IssueID != issueID {
			continue
		}
		if filter.WorkflowID != "" && run.WorkflowID != filter.WorkflowID {
			continue
		}
		if !analyticsRunInWindow(run, filter) {
			continue
		}
		candidates = append(candidates, run)
	}
	candidates = r.FilterRunsByProject(candidates, filter.ProjectID)
	if filter.ProjectIDs != nil {
		candidates = r.filterRunsByProjects(candidates, filter.ProjectIDs)
	}
	if strings.TrimSpace(filter.UserID) != "" {
		candidates = r.filterRunsByResponsibleUser(candidates, filter.UserID)
	}
	ids := make([]string, 0, len(candidates))
	for _, run := range candidates {
		ids = append(ids, run.ID)
	}
	if len(ids) == 0 {
		if strings.TrimSpace(filter.ProjectID) != "" {
			return []domain.Run{}, nil
		}
		return nil, nil
	}
	nodes, err := r.runNodeSummaries(ids)
	if err != nil {
		return nil, err
	}
	artifactCounts, err := r.artifactCounts(ids)
	if err != nil {
		return nil, err
	}
	timingEvents, err := r.runTimingEvents(ids)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]domain.Run, 0, len(candidates))
	for _, run := range candidates {
		// Keep only the summary fields of each node (drop heavy output).
		for nodeID, node := range nodes[run.ID] {
			if run.Nodes == nil {
				run.Nodes = map[string]*domain.NodeRun{}
			}
			run.Nodes[nodeID] = node
		}
		if run.Nodes == nil {
			run.Nodes = map[string]*domain.NodeRun{}
		}
		run.ArtifactCount = artifactCounts[run.ID]
		timing := domain.ComputeRunTiming(run, timingEvents[run.ID], now)
		run.Timing = &timing
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) == 0 {
		// 与 master 的 database/sql 实现保持同一 wire 行为：空结果序列化为
		// null 而非 []（nil slice 语义）。
		return nil, nil
	}
	return out, nil
}

// RunStatuses returns only the fields needed to reconcile task state. It keeps
// list endpoints away from Run events, telemetry, logs, artifacts and nodes.
func (r *RunsRepo) RunStatuses() map[string]string {
	var rows []runRow
	if err := r.DB.Select("id", "status").Find(&rows).Error; err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Status
	}
	return out
}

// RunsWithActiveNodes reports runs that still have running or pending_input nodes.
// Used by task reconciliation without hydrating full run views.
func (r *RunsRepo) RunsWithActiveNodes() map[string]bool {
	var ids []string
	if err := r.DB.Table("run_nodes").Distinct().
		Where("status IN ?", []string{"running", "pending_input"}).
		Pluck("run_id", &ids).Error; err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func (r *RunsRepo) Run(id string) (domain.Run, bool) {
	row, ok := takeRun(r.DB, id)
	if !ok {
		return domain.Run{}, false
	}
	run := runFromRow(row)
	if err := r.loadRunNodes(&run); err != nil {
		return domain.Run{}, false
	}
	if err := r.loadRunHeavy(&run); err != nil {
		return domain.Run{}, false
	}
	return run, true
}

// RunState returns the durable aggregate needed by the execution engine.
// Worker logs are append-only and are never read by engine decisions, so
// loading the entire log history on every state transition only makes long
// runs progressively slower.
func (r *RunsRepo) RunState(id string) (domain.Run, bool) {
	row, ok := takeRun(r.DB, id)
	if !ok {
		return domain.Run{}, false
	}
	run := runFromRow(row)
	if err := r.loadRunNodes(&run); err != nil {
		return domain.Run{}, false
	}
	// Engine decisions only need nodes, events and artifacts. Loading thousands
	// of historical telemetry rows on every lease/heartbeat-adjacent state read
	// made long SOP runs miss their 30s lease: finishAt then refused to persist
	// succeeded, and ResumeInterruptedRuns parked a completed graph forever.
	// Live telemetry still lands through the observation queue (sequence comes
	// from the header); the paginated API keeps the history.
	var eventRows []runEventRow
	if err := r.DB.Where("run_id = ?", run.ID).Order("seq").Find(&eventRows).Error; err != nil {
		return domain.Run{}, false
	}
	for _, row := range eventRows {
		var ev domain.RunEvent
		if json.Unmarshal(row.Payload, &ev) == nil {
			run.Events = append(run.Events, ev)
		}
	}
	var artifactRows []runArtifactRow
	if err := r.DB.Where("run_id = ?", run.ID).Find(&artifactRows).Error; err != nil {
		return domain.Run{}, false
	}
	for _, row := range artifactRows {
		art := artifactFromRow(row, true)
		run.Artifacts = append(run.Artifacts, art)
	}
	return run, true
}

// RunView returns header/nodes/artifacts for the UI without hydrating logs.
func (r *RunsRepo) RunView(id string) (domain.Run, bool) {
	row, ok := takeRun(r.DB, id)
	if !ok {
		return domain.Run{}, false
	}
	run := runFromRow(row)
	if err := r.loadRunNodesFrom(r.runViewNodesQuery(), &run); err != nil {
		return domain.Run{}, false
	}
	if err := r.loadArtifactMeta(&run); err != nil {
		return domain.Run{}, false
	}
	events, err := r.runTimingEvents([]string{run.ID})
	if err != nil {
		return domain.Run{}, false
	}
	timing := domain.ComputeRunTiming(run, events[run.ID], time.Now().UTC())
	run.Timing = &timing
	if run.WorkflowSnapshot.CollaborationMode == "group" {
		var rows []runEventRow
		if err := r.DB.Where("run_id = ?", id).Order("seq DESC").Limit(80).Find(&rows).Error; err != nil {
			return domain.Run{}, false
		}
		for i := len(rows) - 1; i >= 0; i-- {
			var event domain.RunEvent
			if json.Unmarshal(rows[i].Payload, &event) == nil {
				run.Events = append(run.Events, event)
			}
		}
	}
	return run, true
}

// RunStreamView matches the SSE contract, which delivers only the latest 80
// log/telemetry records and artifact metadata, never the historical bodies.
func (r *RunsRepo) RunStreamView(id string) (domain.Run, bool) {
	run, ok := r.RunView(id)
	if !ok {
		return domain.Run{}, false
	}
	var logs []runLogRow
	if err := r.DB.Where("run_id = ?", id).Order("seq DESC").Limit(80).Find(&logs).Error; err != nil {
		return domain.Run{}, false
	}
	for i := len(logs) - 1; i >= 0; i-- {
		var record domain.LogRecord
		if json.Unmarshal(logs[i].Payload, &record) == nil {
			run.Logs = append(run.Logs, record)
		}
	}
	var events []runTelemetryRow
	if err := r.DB.Where("run_id = ?", id).Order("seq DESC").Limit(80).Find(&events).Error; err != nil {
		return domain.Run{}, false
	}
	for i := len(events) - 1; i >= 0; i-- {
		var event domain.TelemetryEvent
		if json.Unmarshal(events[i].Payload, &event) == nil {
			run.Telemetry = append(run.Telemetry, event)
		}
	}
	return run, true
}

// RunRevisions returns the current revision of every existing run in ids in
// one query; missing ids are absent from the map. nil means the query failed.
func (r *RunsRepo) RunRevisions(ids []string) map[string]int {
	out := make(map[string]int, len(ids))
	if len(ids) == 0 {
		return out
	}
	var rows []runRow
	if err := r.DB.Select("id", "revision").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil
	}
	for _, row := range rows {
		out[row.ID] = row.Revision
	}
	return out
}

func (r *RunsRepo) RunRevision(id string) (int, bool) {
	var row runRow
	err := r.DB.Select("revision").Where("id = ?", id).Take(&row).Error
	return row.Revision, err == nil
}

// LiveSandboxRunIDs returns every run whose workspace has not been cleaned.
// Failed and active runs remain live; successful and cancelled workspaces are
// removed explicitly before being excluded here.
// SupersededIssueRunIDs lists settled runs of an issue that a newer dispatch
// has replaced. A failed run keeps its workspace and container only so it can
// be retried; starting the issue over withdraws that reason, and nothing else
// would ever release them.
func (r *RunsRepo) SupersededIssueRunIDs(issueID, keepRunID string) []string {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil
	}
	expr := "json_extract(execution_context, '$.issueId')"
	if r.isMySQL() {
		expr = "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.issueId'))"
	}
	var ids []string
	q := r.DB.Model(&runRow{}).
		Where(expr+" = ?", issueID).
		Where("status IN ?", []string{"failed", "cancelled"}).
		Where("workspace_cleaned_at IS NULL")
	if keep := strings.TrimSpace(keepRunID); keep != "" {
		q = q.Where("id <> ?", keep)
	}
	if err := q.Pluck("id", &ids).Error; err != nil {
		return nil
	}
	return ids
}

func (r *RunsRepo) LiveSandboxRunIDs() []string {
	var ids []string
	if err := r.DB.Model(&runRow{}).Select("id").Where("workspace_cleaned_at IS NULL").Pluck("id", &ids).Error; err != nil {
		return nil
	}
	return ids
}

// LiveSandboxDockerfiles returns the Dockerfiles frozen into runs that still
// own a workspace. Their images must outlive catalog edits: once an idle
// container is discarded, the retry recreates it from the same image.
func (r *RunsRepo) LiveSandboxDockerfiles() []domain.DockerfileDefinition {
	expr := "json_extract(snapshot, '$.sandboxSnapshot.dockerfile')"
	if r.isMySQL() {
		expr = "JSON_EXTRACT(snapshot, '$.sandboxSnapshot.dockerfile')"
	}
	var raws []string
	if err := r.DB.Model(&runRow{}).Where("workspace_cleaned_at IS NULL").
		Where(expr+" IS NOT NULL").Pluck(expr, &raws).Error; err != nil {
		return nil
	}
	out := make([]domain.DockerfileDefinition, 0, len(raws))
	for _, raw := range raws {
		var dockerfile domain.DockerfileDefinition
		if json.Unmarshal([]byte(raw), &dockerfile) == nil && strings.TrimSpace(dockerfile.Content) != "" {
			out = append(out, dockerfile)
		}
	}
	return out
}

// IdleSandboxRunIDs returns runs that keep their workspace for a retry or a
// human decision but have not changed since the given cutoffs.
func (r *RunsRepo) IdleSandboxRunIDs(failedBefore, waitingBefore time.Time) []string {
	var ids []string
	if err := r.DB.Model(&runRow{}).
		Where("workspace_cleaned_at IS NULL").
		Where("(status = ? AND updated_at < ?) OR (status = ? AND updated_at < ?)", "failed", failedBefore, "waiting", waitingBefore).
		Pluck("id", &ids).Error; err != nil {
		return nil
	}
	return ids
}

// TerminalWorkspaceCleanupRuns returns every successful and cancelled run for
// immediate cleanup or cleanup reconciliation.
func (r *RunsRepo) TerminalWorkspaceCleanupRuns() []domain.Run {
	var rows []runRow
	err := r.DB.Where("status IN ?", []string{"succeeded", "cancelled"}).Find(&rows).Error
	if err != nil {
		return nil
	}
	result := make([]domain.Run, 0, len(rows))
	for _, row := range rows {
		result = append(result, runFromRow(row))
	}
	return result
}

// RunsAwaitingBuiltPreview returns runs whose static-site build already
// succeeded and whose workspace is still on disk, but which have no preview
// row yet. The run may still be waiting on a later gate.
func (r *RunsRepo) RunsAwaitingBuiltPreview() []string {
	var ids []string
	err := r.DB.Model(&runRow{}).
		Select("DISTINCT runs.id").
		Joins("JOIN run_nodes ON run_nodes.run_id = runs.id AND run_nodes.node_id = ? AND run_nodes.status = ?", domain.PreviewBuildNodeID, "succeeded").
		Joins("LEFT JOIN run_previews ON run_previews.run_id = runs.id").
		Where("runs.workspace_cleaned_at IS NULL AND runs.status <> ? AND run_previews.id IS NULL", "cancelled").
		Pluck("runs.id", &ids).Error
	if err != nil {
		return nil
	}
	return ids
}

// ClaimRunWorkspaceCleanup atomically makes an expired terminal workspace
// unavailable before cleanup starts. This prevents a concurrent retry from
// reusing a directory while a local deletion or durable Runner cleanup is in
// flight.
func (r *RunsRepo) ClaimRunWorkspaceCleanup(runID string, now time.Time) (bool, error) {
	result := r.DB.Model(&runRow{}).
		Where("id = ? AND status IN ? AND workspace_cleaned_at IS NULL AND (workspace_cleanup_not_before IS NULL OR workspace_cleanup_not_before <= ?)",
			runID, []string{"succeeded", "cancelled"}, now).
		Updates(map[string]any{"workspace_cleaned_at": now, "sandbox_id": ""})
	return result.RowsAffected == 1, result.Error
}

// ResetRunWorkspaceCleanup makes a claimed workspace eligible for a later
// sweep when cleanup could not be safely scheduled or completed.
func (r *RunsRepo) ResetRunWorkspaceCleanup(runID string) error {
	return r.DB.Model(&runRow{}).Where("id = ?", runID).Update("workspace_cleaned_at", nil).Error
}

func (r *RunsRepo) loadRunNodes(run *domain.Run) error {
	return r.loadRunNodesFrom(r.DB, run)
}

// Keep one extra character beyond each HTTP byte limit so the existing
// clipRunText still decides whether to append an ellipsis, including UTF-8.
// Engine state reads retain the complete payload through loadRunNodes.
func (r *RunsRepo) runViewNodesQuery() *gorm.DB {
	parts := []string{"payload"}
	for _, field := range []struct {
		name  string
		limit int
	}{{"output", 2501}, {"error", 1201}, {"feedback", 801}, {"activity", 301}} {
		path := "'$." + field.name + "'"
		extract := "JSON_EXTRACT(payload, " + path + ")"
		text := extract
		if r.isMySQL() {
			text = "JSON_UNQUOTE(" + extract + ")"
		}
		parts = append(parts, path, fmt.Sprintf("CASE WHEN JSON_TYPE(%s) = 'STRING' THEN SUBSTR(%s, 1, %d) ELSE %s END", extract, text, field.limit, extract))
		if !r.isMySQL() {
			parts[len(parts)-1] = fmt.Sprintf("CASE WHEN JSON_TYPE(payload, %s) = 'text' THEN SUBSTR(%s, 1, %d) ELSE %s END", path, text, field.limit, extract)
		}
	}
	return r.DB.Select("run_id, node_id, JSON_SET(" + strings.Join(parts, ", ") + ") AS payload")
}

func (r *RunsRepo) loadRunNodesFrom(q *gorm.DB, run *domain.Run) error {
	var rows []runNodeRow
	if err := q.Where("run_id = ?", run.ID).Find(&rows).Error; err != nil {
		return err
	}
	run.Nodes = map[string]*domain.NodeRun{}
	for _, row := range rows {
		var node domain.NodeRun
		if json.Unmarshal(row.Payload, &node) != nil {
			continue
		}
		run.Nodes[row.NodeID] = &node
	}
	return nil
}

// runNodeSummaries projects a whitelist in SQL: worker output, feedback and
// session state never cross the database connection on a summary path.
func (r *RunsRepo) runNodeSummaries(ids []string) (map[string]map[string]*domain.NodeRun, error) {
	byRun := map[string]map[string]*domain.NodeRun{}
	projection := `JSON_OBJECT(
		'reworkCount', JSON_EXTRACT(payload, '$.reworkCount'),
		'provider', JSON_EXTRACT(payload, '$.provider'),
		'model', JSON_EXTRACT(payload, '$.model'),
		'childRunId', JSON_EXTRACT(payload, '$.childRunId'),
		'usage', JSON_EXTRACT(payload, '$.usage'),
		'toolCalls', JSON_EXTRACT(payload, '$.toolCalls'),
		'startedAt', JSON_EXTRACT(payload, '$.startedAt'),
		'finishedAt', JSON_EXTRACT(payload, '$.finishedAt'),
		'error', SUBSTR(%s, 1, 160)) AS payload`
	errorExpr := "json_extract(payload, '$.error')"
	if r.isMySQL() {
		errorExpr = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.error'))"
	}
	for start := 0; start < len(ids); start += 500 {
		batch := ids[start:min(start+500, len(ids))]
		var rows []runNodeRow
		if err := r.DB.Select("run_id, node_id, status, attempt, "+fmt.Sprintf(projection, errorExpr)).Where("run_id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			var node domain.NodeRun
			if json.Unmarshal(row.Payload, &node) != nil {
				continue
			}
			summary := summarizeNode(node, row.NodeID, row.Status, row.Attempt)
			if byRun[row.RunID] == nil {
				byRun[row.RunID] = map[string]*domain.NodeRun{}
			}
			byRun[row.RunID][row.NodeID] = &summary
		}
	}
	return byRun, nil
}

// summarizeNode keeps only the fields a list view needs, matching the previous
// MySQL JSON_OBJECT projection (error text truncated, no worker output).
func summarizeNode(node domain.NodeRun, nodeID, status string, attempt int) domain.NodeRun {
	summary := domain.NodeRun{
		NodeID: nodeID, Status: status, Attempt: attempt,
		ReworkCount: node.ReworkCount, Provider: node.Provider, Model: node.Model,
		ChildRunID: node.ChildRunID,
		Usage:      node.Usage, ToolCalls: node.ToolCalls,
		StartedAt: node.StartedAt, FinishedAt: node.FinishedAt,
	}
	if len(node.Error) > 160 {
		summary.Error = node.Error[:160]
	} else {
		summary.Error = node.Error
	}
	return summary
}

func (r *RunsRepo) loadRunHeavy(run *domain.Run) error {
	run.Events, run.Telemetry, run.Logs, run.Artifacts = nil, nil, nil, nil
	var eventRows []runEventRow
	if err := r.DB.Where("run_id = ?", run.ID).Order("seq").Find(&eventRows).Error; err != nil {
		return err
	}
	for _, row := range eventRows {
		var ev domain.RunEvent
		if json.Unmarshal(row.Payload, &ev) == nil {
			run.Events = append(run.Events, ev)
		}
	}
	var telemetryRows []runTelemetryRow
	if err := r.DB.Where("run_id = ?", run.ID).Order("seq").Find(&telemetryRows).Error; err != nil {
		return err
	}
	for _, row := range telemetryRows {
		var ev domain.TelemetryEvent
		if json.Unmarshal(row.Payload, &ev) == nil {
			run.Telemetry = append(run.Telemetry, ev)
		}
	}
	var logRows []runLogRow
	if err := r.DB.Where("run_id = ?", run.ID).Order("seq").Find(&logRows).Error; err != nil {
		return err
	}
	for _, row := range logRows {
		var rec domain.LogRecord
		if json.Unmarshal(row.Payload, &rec) == nil {
			run.Logs = append(run.Logs, rec)
		}
	}
	var artifactRows []runArtifactRow
	if err := r.DB.Where("run_id = ?", run.ID).Find(&artifactRows).Error; err != nil {
		return err
	}
	for _, row := range artifactRows {
		run.Artifacts = append(run.Artifacts, artifactFromRow(row, true))
	}
	return nil
}

func (r *RunsRepo) loadArtifactMeta(run *domain.Run) error {
	var rows []runArtifactRow
	if err := r.DB.Select("id", "run_id", "node_id", "attempt", "name", "path", "media_type", "size_bytes", "sha256", "created_at").
		Where("run_id = ?", run.ID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		run.Artifacts = append(run.Artifacts, artifactFromRow(row, false))
	}
	run.ArtifactCount = len(run.Artifacts)
	return nil
}

func (r *RunsRepo) artifactCounts(ids []string) (map[string]int, error) {
	type countRow struct {
		RunID string
		Count int
	}
	counts := make(map[string]int)
	for start := 0; start < len(ids); start += 500 {
		var rows []countRow
		if err := r.DB.Table("run_artifacts").Select("run_id, COUNT(*) AS count").Where("run_id IN ?", ids[start:min(start+500, len(ids))]).Group("run_id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			counts[row.RunID] = row.Count
		}
	}
	return counts, nil
}

// runTimingEvents reads only the columns ComputeRunTiming needs. The payload
// column is skipped; run.resumed heartbeats are excluded by the type filter.
func (r *RunsRepo) runTimingEvents(ids []string) (map[string][]domain.RunEvent, error) {
	out := make(map[string][]domain.RunEvent)
	for start := 0; start < len(ids); start += 500 {
		var rows []runEventRow
		if err := r.DB.Select("run_id", "seq", "node_id", "type", "ts").
			Where("run_id IN ? AND type IN ?", ids[start:min(start+500, len(ids))], domain.RunTimingEventTypes).
			Order("run_id, seq").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Ts == nil {
				continue
			}
			out[row.RunID] = append(out[row.RunID], domain.RunEvent{RunID: row.RunID, NodeID: row.NodeID, Type: row.Type, Timestamp: *row.Ts})
		}
	}
	return out, nil
}

// RunExists performs a primary-key lookup without loading nodes or snapshots.
func (r *RunsRepo) RunExists(id string) bool {
	var row runRow
	return r.DB.Select("id").Where("id = ?", id).Take(&row).Error == nil
}

// TelemetryEventExists provides the durable half of event-id idempotency.
// RunState intentionally does not hydrate telemetry, so checking only the
// in-memory aggregate would accept the same worker event after a flush.
func (r *RunsRepo) TelemetryEventExists(runID, eventID string) bool {
	runID, eventID = strings.TrimSpace(runID), strings.TrimSpace(eventID)
	if runID == "" || eventID == "" {
		return false
	}
	expr := "json_extract(payload, '$.id')"
	if r.isMySQL() {
		expr = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.id'))"
	}
	var count int64
	err := r.DB.Table("run_telemetry").Where("run_id = ? AND "+expr+" = ?", runID, eventID).Limit(1).Count(&count).Error
	return err == nil && count > 0
}

func artifactFromRow(row runArtifactRow, withContent bool) domain.Artifact {
	art := domain.Artifact{
		ID: row.ID, RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt,
		Name: row.Name, Path: row.Path, MediaType: row.MediaType, Size: row.SizeBytes,
		SHA256: row.SHA256, RepositoryDir: row.RepositoryDir,
	}
	if withContent {
		art.Content = row.Content
	}
	if row.Created != nil {
		art.CreatedAt = *row.Created
	}
	return art
}

func (r *RunsRepo) Artifact(runID, artifactID string) (domain.Artifact, bool) {
	var row runArtifactRow
	if err := r.DB.Where("run_id = ? AND id = ?", runID, artifactID).Take(&row).Error; err != nil {
		return domain.Artifact{}, false
	}
	return artifactFromRow(row, true), true
}

// FilterRunsByProject keeps runs stamped with projectID, or legacy runs whose
// linked issue belongs to that project. An empty projectID returns the list
// unchanged.
func (r *RunsRepo) FilterRunsByProject(runs []domain.Run, projectID string) []domain.Run {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return runs
	}
	return r.filterRunsByProjects(runs, []string{projectID})
}

func (r *RunsRepo) filterRunsByProjects(runs []domain.Run, projectIDs []string) []domain.Run {
	allowed := make(map[string]bool, len(projectIDs))
	for _, id := range projectIDs {
		allowed[id] = true
	}

	issueIDs := map[string]struct{}{}
	for _, run := range runs {
		if strings.TrimSpace(run.ExecutionContext.ProjectID) == "" && run.ExecutionContext.IssueID != "" {
			issueIDs[run.ExecutionContext.IssueID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(issueIDs))
	for id := range issueIDs {
		ids = append(ids, id)
	}
	issueProject := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += 500 {
		var issues []issueRow
		_ = r.DB.Select("id", "project_id").Where("id IN ?", ids[start:min(start+500, len(ids))]).Find(&issues).Error
		for _, issue := range issues {
			issueProject[issue.ID] = issue.ProjectID
		}
	}
	out := make([]domain.Run, 0, len(runs))
	for _, run := range runs {
		if allowed[domain.RunProjectID(run, issueProject[run.ExecutionContext.IssueID])] {
			out = append(out, run)
		}
	}
	return out
}

// filterRunsByResponsibleUser keeps runs whose executor or linked issue assignee matches.
func (r *RunsRepo) filterRunsByResponsibleUser(runs []domain.Run, userID string) []domain.Run {
	want := strings.TrimSpace(userID)
	if want == "" {
		return runs
	}
	issueIDs := make([]string, 0)
	seen := map[string]struct{}{}
	for _, run := range runs {
		id := strings.TrimSpace(run.ExecutionContext.IssueID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		issueIDs = append(issueIDs, id)
	}
	assignees := map[string]int64{}
	if r.DB != nil && len(issueIDs) > 0 {
		for start := 0; start < len(issueIDs); start += 500 {
			var issues []issueRow
			_ = r.DB.Select("id", "assignee_user_id").Where("id IN ?", issueIDs[start:min(start+500, len(issueIDs))]).Find(&issues).Error
			for _, issue := range issues {
				if issue.AssigneeUserID != nil {
					assignees[issue.ID] = *issue.AssigneeUserID
				}
			}
		}
	}
	out := make([]domain.Run, 0, len(runs))
	for _, run := range runs {
		issue := domain.Issue{AssigneeUserID: assignees[strings.TrimSpace(run.ExecutionContext.IssueID)]}
		if analyticsRunMatchesUser(run, want, issue) {
			out = append(out, run)
		}
	}
	return out
}

// ---------- Writes ----------

// PutRun writes the complete current Run to the database before returning.
// There is no process-local production copy: the next read queries the
// database again.
func (r *RunsRepo) PutRun(v domain.Run) error {
	v.UpdatedAt = time.Now().UTC()
	v.Revision++
	return r.writeRun(v)
}

// writeRun persists the run header plus whatever telemetry/logs/events arrived
// since the last write. A run is the one entity written on a hot path, so the
// header and its append-only children go in a single transaction.
//
// Most writes keep or release a slot and take no admission lock. A write that
// turns out to claim one (new running run, restart of a succeeded/unstarted
// run) is rolled back and retried with its bucket locked first, so a bucket
// lock is never awaited while a run row is held.
func (r *RunsRepo) writeRun(run domain.Run, expectedRevision ...int) error {
	lockKey := ""
	for attempt := 0; ; attempt++ {
		err := r.writeRunOnce(run, lockKey, expectedRevision...)
		var need *admissionLockRequired
		if errors.As(err, &need) && attempt < 3 {
			lockKey = need.key
			continue
		}
		return err
	}
}

func (r *RunsRepo) writeRunOnce(run domain.Run, lockKey string, expectedRevision ...int) error {
	var opts []*sql.TxOptions
	if lockKey != "" {
		opts = r.admissionTxOptions()
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if lockKey != "" {
			if err := lockAdmissionGate(r.deps, tx, lockKey); err != nil {
				return err
			}
		}
		current, err := r.takeRunForUpdate(tx, run.ID)
		if err == nil {
			if len(expectedRevision) > 0 && current.Revision != expectedRevision[0] {
				return ErrRunDecisionStale
			}
			if run.Generation != current.Generation {
				return ErrRunLeaseLost
			}
			leaseExpired := current.OwnerInstanceID != "" && (current.LeaseUntil == nil || !current.LeaseUntil.After(time.Now().UTC()))
			if leaseExpired && !domain.TerminalRunStatus(run.Status) {
				return ErrRunLeaseLost
			}
			if run.WorkspaceCleanedAt.IsZero() && current.WorkspaceCleaned != nil {
				run.WorkspaceCleanedAt = *current.WorkspaceCleaned
			}
			if run.WorkspaceCleanupNotBefore.IsZero() && current.CleanupNotBefore != nil {
				run.WorkspaceCleanupNotBefore = *current.CleanupNotBefore
			}
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		if len(expectedRevision) > 0 {
			if err != nil {
				return ErrRunDecisionStale
			}
			if run.Status == "running" {
				if err := tx.Model(&runRow{}).Where("id = ?", run.ID).Updates(map[string]any{"cancel_requested": false, "pause_requested": false}).Error; err != nil {
					return err
				}
			}
		}
		if err := r.prepareRunAdmission(tx, &run, current, err == nil, lockKey); err != nil {
			return err
		}
		if run.Status == "queued" {
			if err := tx.Model(&runRow{}).Where("id = ?", run.ID).Updates(map[string]any{"owner_instance_id": "", "lease_until": nil, "cancel_requested": false, "pause_requested": false}).Error; err != nil {
				return err
			}
		}
		var cur struct {
			logs      int64
			telemetry int64
			events    int64
		}
		if err := tx.Raw(`SELECT
			COALESCE((SELECT MAX(seq) FROM run_logs WHERE run_id=?), 0) AS logs,
			COALESCE((SELECT MAX(seq) FROM run_telemetry WHERE run_id=?), 0) AS telemetry,
			COALESCE((SELECT MAX(seq) FROM run_events WHERE run_id=?), -1) + 1 AS events`,
			run.ID, run.ID, run.ID).Scan(&cur).Error; err != nil {
			return err
		}
		// On conflict only the run header is refreshed. Lease fields
		// (owner_instance_id, lease_until, execution_generation) and workspace
		// bindings are owned by the lease operations and must survive PutRun.
		// The control flags (cancel_requested/pause_requested) are likewise
		// excluded: they are written exclusively via RequestRunControl/
		// ClearRunControl, so a worker PutRun can never clobber an API-side
		// cancel/pause request (master's writeRun has the same omission).
		if err := upsertColumns(tx, "runs", runRowValues(run), []string{
			"workflow_id", "status", "revision", "input", "human_context", "sandbox_id", "repository", "repositories", "snapshot",
			"execution_context", "usage_json", "tool_calls", "telemetry_sequence",
			"log_sequence", "workspace_cleaned_at", "updated_at", "admission_state", "queue_entered_at", "quota_project_id", "quota_parent_run_id", "quota_exempt", "quota_bucket",
		}); err != nil {
			return fmt.Errorf("runs upsert %s: %w", run.ID, err)
		}
		// Nodes: upsert every node of the run.
		for id, node := range run.Nodes {
			if node == nil {
				continue
			}
			if err := upsert(tx, "run_nodes", map[string]any{
				"run_id": run.ID, "node_id": id, "status": node.Status, "attempt": node.Attempt, "payload": mustJSON(node),
			}); err != nil {
				return err
			}
		}
		// Events: append-only by index.
		if int64(len(run.Events)) > cur.events {
			for i := cur.events; i < int64(len(run.Events)); i++ {
				ev := run.Events[i]
				if err := r.notifications.enqueueRunNotifications(tx, run, ev, i); err != nil {
					return err
				}
				if err := upsert(tx, "run_events", map[string]any{
					"run_id": run.ID, "seq": i, "event_id": ev.ID, "node_id": ev.NodeID,
					"type": ev.Type, "message": ev.Message, "payload": mustJSON(ev), "ts": timePtr(ev.Timestamp),
				}); err != nil {
					return err
				}
			}
		}
		for _, ev := range run.Telemetry {
			if ev.Sequence == 0 || ev.Sequence <= cur.telemetry {
				continue
			}
			if err := upsert(tx, "run_telemetry", map[string]any{
				"run_id": run.ID, "seq": ev.Sequence, "payload": mustJSON(ev),
			}); err != nil {
				return err
			}
		}
		for _, rec := range run.Logs {
			if rec.Sequence == 0 || rec.Sequence <= cur.logs {
				continue
			}
			if err := upsert(tx, "run_logs", map[string]any{
				"run_id": run.ID, "seq": rec.Sequence, "payload": mustJSON(rec),
			}); err != nil {
				return err
			}
		}
		for _, art := range run.Artifacts {
			id := art.ID
			if id == "" {
				id = fmt.Sprintf("%s-%s-%d-%s", run.ID, art.NodeID, art.Attempt, art.Name)
			}
			if err := upsert(tx, "run_artifacts", map[string]any{
				"id": id, "run_id": run.ID, "node_id": art.NodeID, "attempt": art.Attempt,
				"name": art.Name, "path": art.Path, "media_type": art.MediaType,
				"size_bytes": art.Size, "sha256": art.SHA256, "repository_dir": art.RepositoryDir,
				"content": art.Content, "created_at": timePtr(art.CreatedAt),
			}); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
}

// DeleteRun removes a settled run together with all of its children (nodes,
// events, telemetry, logs, artifacts). Live runs (running/waiting) must be
// cancelled first so engine scheduler state stays consistent. Deleting only
// releases a slot, so no admission lock is needed; the row lock and status
// re-check keep a queued run from being promoted mid-delete.
func (r *RunsRepo) DeleteRun(id string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, id)
		if err != nil {
			return err
		}
		if row.Status == "running" || row.Status == "waiting" || row.Status == "queued" {
			return fmt.Errorf("run %s is %s: cancel it before deleting", id, row.Status)
		}
		for _, table := range []string{"run_nodes", "run_events", "run_telemetry", "run_logs", "run_artifacts", "run_evals", "run_workspace_checkpoints", "assistant_group_followups", "sop_handoff_jobs"} {
			if err := tx.Table(table).Where("run_id = ?", id).Delete(nil).Error; err != nil {
				return fmt.Errorf("runs delete %s: %w", id, err)
			}
		}
		return tx.Where("id = ?", id).Delete(&runRow{}).Error
	})
}

// ---------- Run leases ----------

func (r *RunsRepo) takeRunForUpdate(tx *gorm.DB, id string) (runRow, error) {
	var row runRow
	err := r.locking(tx.Where("id = ?", id)).Take(&row).Error
	return row, err
}

func (r *RunsRepo) TryAcquireRunLease(runID, instanceID string, generation int, ttl time.Duration) error {
	if strings.TrimSpace(instanceID) == "" || ttl <= 0 {
		return ErrRunLeaseLost
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		current, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return fmt.Errorf("run not found")
			}
			return err
		}
		if current.Status != "running" && current.Status != "waiting" && current.Status != "failed" && current.Status != "cancelled" && current.Status != "succeeded" {
			return fmt.Errorf("run cannot be leased")
		}
		now := time.Now().UTC()
		if current.OwnerInstanceID != "" && current.OwnerInstanceID != instanceID && current.LeaseUntil != nil && current.LeaseUntil.After(now) {
			return ErrRunLeaseHeld
		}
		if generation != current.Generation && !(current.Generation == 0 && generation == 1) {
			return ErrRunLeaseLost
		}
		newGeneration := current.Generation
		if current.OwnerInstanceID == instanceID {
			if generation != current.Generation {
				return ErrRunLeaseLost
			}
			if current.Status != "running" {
				newGeneration++
			}
		} else if current.Generation == 0 {
			newGeneration = 1
		} else {
			newGeneration++
		}
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{
			"owner_instance_id": instanceID, "lease_until": now.Add(ttl), "execution_generation": newGeneration,
		}).Error
	})
}

func (r *RunsRepo) RenewRunLease(runID, instanceID string, generation int, ttl time.Duration) error {
	res := r.DB.Model(&runRow{}).
		Where("id = ? AND owner_instance_id = ? AND execution_generation = ?", runID, instanceID, generation).
		Update("lease_until", time.Now().UTC().Add(ttl))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrRunLeaseLost
	}
	return nil
}

func (r *RunsRepo) ReleaseRunLease(runID, instanceID string, generation int) error {
	res := r.DB.Model(&runRow{}).
		Where("id = ? AND owner_instance_id = ? AND execution_generation = ?", runID, instanceID, generation).
		Updates(map[string]any{"owner_instance_id": "", "lease_until": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrRunLeaseLost
	}
	return nil
}

func (r *RunsRepo) BindRunWorkspace(runID, instanceID string, generation int, host, root string) (bool, error) {
	var bound bool
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		current, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrRunLeaseLost
			}
			return err
		}
		if current.OwnerInstanceID != instanceID || current.Generation != generation {
			return ErrRunLeaseLost
		}
		if strings.TrimSpace(current.WorkspaceRoot) != "" {
			if current.WorkspaceHost == host && current.WorkspaceRoot == root {
				bound = false
				return nil
			}
			return ErrRunWorkspaceBound
		}
		bound = true
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{
			"workspace_host": host, "workspace_root": root,
		}).Error
	})
	return bound, err
}

// RebindRunWorkspace moves the workspace bind of a run to (host, root). Only
// the current lease holder may do it; callers use it when the previously
// bound host is gone and the checkout has to be rebuilt elsewhere.
func (r *RunsRepo) RebindRunWorkspace(runID, instanceID string, generation int, host, root string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		current, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return ErrRunLeaseLost
			}
			return err
		}
		if current.OwnerInstanceID != instanceID || current.Generation != generation {
			return ErrRunLeaseLost
		}
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{
			"workspace_host": host, "workspace_root": root,
		}).Error
	})
}

// ---------- Workflow metrics ----------

func (r *RunsRepo) WorkflowMetrics() []domain.WorkflowMetrics {
	return r.WorkflowMetricsFor("")
}

func (r *RunsRepo) WorkflowMetricsFor(projectID string) []domain.WorkflowMetrics {
	metrics := map[string]*domain.WorkflowMetrics{}
	nodeDurations := map[string]map[string]int64{}
	workflowDefs := map[string]domain.WorkflowDefinition{}
	for _, workflow := range r.catalog.Workflows() {
		workflowDefs[workflow.ID] = workflow
		metrics[workflow.ID] = &domain.WorkflowMetrics{WorkflowID: workflow.ID}
		nodeDurations[workflow.ID] = map[string]int64{}
	}
	runs, _ := r.RunSummariesFor(domain.AnalyticsFilter{ProjectID: projectID}, "")
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	durations := map[string]int64{}
	terminal := map[string]int{}
	for _, run := range runs {
		m := metrics[run.WorkflowID]
		if m == nil {
			m = &domain.WorkflowMetrics{WorkflowID: run.WorkflowID}
			metrics[run.WorkflowID] = m
			nodeDurations[run.WorkflowID] = map[string]int64{}
		}
		m.TotalRuns++
		m.TotalTokens += run.Usage.TotalTokens
		m.TotalToolCalls += run.ToolCalls
		m.TotalArtifacts += runArtifactCount(run)
		switch run.Status {
		case "running":
			// 超过 4 分钟没有任何进度的 running 视为中断残留，不计入「进行中」。
			if run.UpdatedAt.IsZero() || time.Since(run.UpdatedAt) > 4*time.Minute {
				break
			}
			m.ActiveRuns++
		case "waiting":
			m.WaitingRuns++
		case "succeeded":
			m.SucceededRuns++
			terminal[run.WorkflowID]++
		case "failed":
			m.FailedRuns++
			terminal[run.WorkflowID]++
		case "cancelled":
			m.CancelledRuns++
		}
		reworks := 0
		for _, node := range run.Nodes {
			reworks += node.ReworkCount
		}
		if reworks > 0 {
			m.ReworkedRuns++
		}
		duration := runDuration(run, time.Now().UTC())
		if duration > 0 && (run.Status == "succeeded" || run.Status == "failed") {
			durations[run.WorkflowID] += duration
		}
		if len(m.RecentRuns) < 5 {
			m.RecentRuns = append(m.RecentRuns, domain.RunSummary{ID: run.ID, Status: run.Status, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, DurationMs: duration, ReworkCount: reworks, TotalTokens: run.Usage.TotalTokens, ToolCalls: run.ToolCalls, Artifacts: runArtifactCount(run)})
		}
		if m.LastRunAt == nil {
			createdAt := run.CreatedAt
			m.LastRunAt = &createdAt
			m.LastRunStatus = run.Status
		}
		index := map[string]int{}
		for i := range m.NodeMetrics {
			index[m.NodeMetrics[i].NodeID] = i + 1
		}
		for nodeID, node := range run.Nodes {
			pos := index[nodeID] - 1
			if pos < 0 {
				name := nodeID
				for _, definition := range workflowDefs[run.WorkflowID].Nodes {
					if definition.ID == nodeID {
						name = definition.Name
					}
				}
				m.NodeMetrics = append(m.NodeMetrics, domain.WorkflowNodeMetric{NodeID: nodeID, Name: name})
				pos = len(m.NodeMetrics) - 1
			}
			nm := &m.NodeMetrics[pos]
			if node.Attempt > 0 {
				nm.Executions += node.Attempt
			}
			switch node.Status {
			case "succeeded":
				nm.Succeeded++
			case "failed":
				nm.Failed++
			case "waiting_approval":
				nm.Waiting++
			}
			nm.Reworks += node.ReworkCount
			if !node.StartedAt.IsZero() && !node.FinishedAt.IsZero() {
				nodeDurations[run.WorkflowID][nodeID] += node.FinishedAt.Sub(node.StartedAt).Milliseconds()
			}
		}
	}
	out := make([]domain.WorkflowMetrics, 0, len(metrics))
	for id, m := range metrics {
		if m.TotalRuns > 0 {
			m.ClosureRate = float64(m.SucceededRuns) * 100 / float64(m.TotalRuns)
			m.ReworkRate = float64(m.ReworkedRuns) * 100 / float64(m.TotalRuns)
		}
		if terminal[id] > 0 {
			m.SuccessRate = float64(m.SucceededRuns) * 100 / float64(terminal[id])
			m.AverageDuration = durations[id] / int64(terminal[id])
		}
		for i := range m.NodeMetrics {
			nm := &m.NodeMetrics[i]
			completed := nm.Succeeded + nm.Failed
			if completed > 0 {
				nm.SuccessRate = float64(nm.Succeeded) * 100 / float64(completed)
				nm.AverageDuration = nodeDurations[id][nm.NodeID] / int64(completed)
			}
		}
		item := *m
		if item.TotalRuns == 0 {
			item.LastRunAt = nil
			item.LastRunStatus = ""
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		var left, right time.Time
		if out[i].LastRunAt != nil {
			left = *out[i].LastRunAt
		}
		if out[j].LastRunAt != nil {
			right = *out[j].LastRunAt
		}
		return left.After(right)
	})
	return out
}

// parallel waits for a set of concurrent loads and returns the first error.
func parallel(fns ...func() error) error {
	var load sync.WaitGroup
	errs := make([]error, len(fns))
	for i, fn := range fns {
		load.Add(1)
		go func(i int, fn func() error) {
			defer load.Done()
			errs[i] = fn()
		}(i, fn)
	}
	load.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func runArtifactCount(run domain.Run) int {
	if run.ArtifactCount > 0 {
		return run.ArtifactCount
	}
	return len(run.Artifacts)
}

// ---------- duration helpers ----------

func runEndedAt(run domain.Run, now time.Time) time.Time {
	switch run.Status {
	case "succeeded", "failed", "cancelled":
		end := time.Time{}
		for _, node := range run.Nodes {
			if node == nil || node.FinishedAt.IsZero() {
				continue
			}
			if end.IsZero() || node.FinishedAt.After(end) {
				end = node.FinishedAt
			}
		}
		if end.IsZero() {
			end = run.UpdatedAt
		}
		if end.IsZero() {
			return now
		}
		return end
	default:
		return now
	}
}

func runDuration(run domain.Run, now time.Time) int64 {
	end := runEndedAt(run, now)
	if run.CreatedAt.IsZero() || end.Before(run.CreatedAt) {
		return 0
	}
	return end.Sub(run.CreatedAt).Milliseconds()
}

// runExecutionDuration 统计 SOP 节点真实执行耗时之和：Σ(节点 FinishedAt − StartedAt)。
// 与 runDuration（Run 创建 → 最后结束的墙钟差）不同，它不含审批、排队、恢复等待，
// 因此执行耗时 ≤ 端到端墙钟。起止时间缺失或倒挂的节点跳过。
// 纯聚合层只读计算，不触碰执行链路；既有的 runDuration/runEndedAt 语义保持不变。
// 有事件流算出的 Timing 时以它为准：节点行只保留最近一次尝试，会漏掉重试前的执行。
func runExecutionDuration(run domain.Run) int64 {
	if run.Timing != nil {
		return run.Timing.ExecutionMs
	}
	var total int64
	for _, node := range run.Nodes {
		if node == nil || node.StartedAt.IsZero() || node.FinishedAt.IsZero() {
			continue
		}
		elapsed := node.FinishedAt.Sub(node.StartedAt).Milliseconds()
		if elapsed <= 0 {
			continue
		}
		total += elapsed
	}
	return total
}

// runActiveDuration returns the union of node execution intervals. Unlike
// runExecutionDuration it does not double-count parallel nodes, so it can be
// subtracted from wall-clock duration when estimating time spent waiting.
func runActiveDuration(run domain.Run, now time.Time) int64 {
	wallStart, wallEnd := run.CreatedAt, runEndedAt(run, now)
	if wallStart.IsZero() || !wallEnd.After(wallStart) {
		return 0
	}
	type interval struct{ start, end time.Time }
	intervals := make([]interval, 0, len(run.Nodes))
	for _, node := range run.Nodes {
		if node == nil || node.StartedAt.IsZero() || node.FinishedAt.IsZero() || !node.FinishedAt.After(node.StartedAt) {
			continue
		}
		start, end := node.StartedAt, node.FinishedAt
		if start.Before(wallStart) {
			start = wallStart
		}
		if end.After(wallEnd) {
			end = wallEnd
		}
		if end.After(start) {
			intervals = append(intervals, interval{start: start, end: end})
		}
	}
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	current := intervals[0]
	var total time.Duration
	for _, next := range intervals[1:] {
		if !next.start.After(current.end) {
			if next.end.After(current.end) {
				current.end = next.end
			}
			continue
		}
		total += current.end.Sub(current.start)
		current = next
	}
	total += current.end.Sub(current.start)
	return total.Milliseconds()
}

// runWaitDuration 为等待时长：端到端墙钟 − 节点活跃区间并集，下限 0。
// 节点活跃区间使用并集，避免并行节点被重复扣减。
func runWaitDuration(run domain.Run, now time.Time) int64 {
	if run.Timing != nil {
		return run.Timing.WaitingMs
	}
	wait := runDuration(run, now) - runActiveDuration(run, now)
	if wait < 0 {
		return 0
	}
	return wait
}

// queryAnalyticsWorkflowSnapshots loads frozen workflow definitions only for
// analytics candidates. RunSummaries stays lightweight for ordinary list/API
// consumers and therefore deliberately does not include the snapshot column.
func queryAnalyticsWorkflowSnapshots(db *gorm.DB, runIDs []string) (map[string]domain.WorkflowDefinition, error) {
	out := make(map[string]domain.WorkflowDefinition, len(runIDs))
	const batchSize = 500
	for start := 0; start < len(runIDs); start += batchSize {
		end := start + batchSize
		if end > len(runIDs) {
			end = len(runIDs)
		}
		batch := runIDs[start:end]
		var rows []runRow
		if err := db.Select("id", "snapshot").Where("id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			var snapshot domain.WorkflowDefinition
			if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
				return nil, err
			}
			out[row.ID] = snapshot
		}
	}
	return out, nil
}

// repositoriesJSONValue 与 master 的 repositoriesJSON 一致：空集合写 SQL
// NULL 而不是 '[]'，保证按 NULL 过滤的查询语义相同。
func repositoriesJSONValue(repositories []domain.RepositoryContext) any {
	if len(repositories) == 0 {
		return nil
	}
	return jsonBytes(repositories)
}

// ---------- Run log/telemetry pages ----------

const defaultLogPageSize = 100
const maxLogPageSize = 200

type LogPage struct {
	Logs       []domain.LogRecord `json:"logs"`
	Sequence   int64              `json:"sequence"`
	NextAfter  int64              `json:"nextAfter"`
	NextBefore int64              `json:"nextBefore"`
	HasMore    bool               `json:"hasMore"`
}

type TelemetryPage struct {
	Events     []domain.TelemetryEvent `json:"events"`
	Sequence   int64                   `json:"sequence"`
	NextAfter  int64                   `json:"nextAfter"`
	NextBefore int64                   `json:"nextBefore"`
	HasMore    bool                    `json:"hasMore"`
}

// ---------- Log pages ----------

func (r *RunsRepo) QueryRunLogs(runID, nodeID string, after, before int64, afterSet, beforeSet bool, limit int) (LogPage, error) {
	limit = clampPageLimit(limit)
	var row runRow
	if err := r.DB.Select("log_sequence").Where("id = ?", runID).Take(&row).Error; err != nil {
		return LogPage{}, fmt.Errorf("run not found")
	}
	if r.isMySQL() {
		// The generated `level` column plus a JSON path filter keep this a
		// single indexed query in production.
		nodeWhere, nodeArgs := r.nodeIDFilter("$.context.nodeId", nodeID)
		raws, hasMore, err := r.queryJSONSeqPage("run_logs", runID, nodeWhere, nodeArgs, sqlFilter{}, after, before, afterSet, beforeSet, limit)
		if err != nil {
			return LogPage{}, err
		}
		out := make([]domain.LogRecord, 0, len(raws))
		for _, raw := range raws {
			var rec domain.LogRecord
			if json.Unmarshal(raw, &rec) == nil {
				out = append(out, rec)
			}
		}
		return finishLogPage(out, row.LogSequence, hasMore), nil
	}
	logs, err := r.loadRunLogsAll(runID)
	if err != nil {
		return LogPage{}, err
	}
	return buildLogPage(logs, row.LogSequence, nodeID, after, before, afterSet, beforeSet, limit), nil
}

const maxLogDump = 20000

func (r *RunsRepo) DumpRunLogs(runID, nodeID string) ([]domain.LogRecord, error) {
	var all []domain.LogRecord
	after := int64(0)
	for {
		page, err := r.QueryRunLogs(runID, nodeID, after, 0, true, false, maxLogPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Logs...)
		if !page.HasMore || len(page.Logs) == 0 || len(all) >= maxLogDump {
			break
		}
		if page.NextAfter <= after {
			break
		}
		after = page.NextAfter
	}
	if len(all) > maxLogDump {
		all = all[:maxLogDump]
	}
	return all, nil
}

// ---------- Telemetry pages ----------

func (r *RunsRepo) QueryRunTelemetry(runID, nodeID, adapter string, after, before int64, afterSet, beforeSet bool, limit int) (TelemetryPage, error) {
	limit = clampPageLimit(limit)
	var row runRow
	if err := r.DB.Select("telemetry_sequence").Where("id = ?", runID).Take(&row).Error; err != nil {
		return TelemetryPage{}, fmt.Errorf("run not found")
	}
	if r.isMySQL() {
		nodeWhere, nodeArgs := r.nodeIDFilter("$.nodeId", nodeID)
		adapterFilter := adapterSQLFilter(adapter)
		raws, hasMore, err := r.queryJSONSeqPage("run_telemetry", runID, nodeWhere, nodeArgs, adapterFilter, after, before, afterSet, beforeSet, limit)
		if err != nil {
			return TelemetryPage{}, err
		}
		out := make([]domain.TelemetryEvent, 0, len(raws))
		for _, raw := range raws {
			var rec domain.TelemetryEvent
			if json.Unmarshal(raw, &rec) == nil {
				out = append(out, rec)
			}
		}
		return finishTelemetryPage(out, row.TelemetrySequence, hasMore), nil
	}
	events, err := r.loadRunTelemetryAll(runID)
	if err != nil {
		return TelemetryPage{}, err
	}
	return buildTelemetryPage(events, row.TelemetrySequence, nodeID, adapter, after, before, afterSet, beforeSet, limit), nil
}

// nodeIDFilter renders the JSON path filter for the current dialect.
func (r *RunsRepo) nodeIDFilter(path, nodeID string) (string, []any) {
	if nodeID == "" {
		return "", nil
	}
	return r.sqlJSONText(path) + " = ?", []any{path, nodeID}
}

type sqlFilter struct {
	clause string
	args   []any
}

// adapterSQLFilter filters on the MySQL generated `adapter` column.
func adapterSQLFilter(adapter string) sqlFilter {
	switch strings.TrimSpace(adapter) {
	case "", "all":
		return sqlFilter{}
	case "hooks":
		return sqlFilter{clause: " AND adapter IN (?, ?)", args: []any{observability.AdapterHooks, observability.AdapterLegacyHooks}}
	case "otel":
		return sqlFilter{clause: " AND adapter=?", args: []any{observability.AdapterOTEL}}
	case "live":
		return sqlFilter{clause: " AND adapter NOT IN (?, ?, ?)", args: []any{observability.AdapterHooks, observability.AdapterLegacyHooks, observability.AdapterOTEL}}
	default:
		return sqlFilter{clause: " AND adapter=?", args: []any{strings.TrimSpace(adapter)}}
	}
}

func (r *RunsRepo) queryJSONSeqPage(table, runID, nodeWhere string, nodeArgs []any, extra sqlFilter, after, before int64, afterSet, beforeSet bool, limit int) ([][]byte, bool, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT payload FROM %s WHERE run_id=?", table)
	args := []any{runID}
	if nodeWhere != "" {
		b.WriteString(" AND ")
		b.WriteString(nodeWhere)
		args = append(args, nodeArgs...)
	}
	if extra.clause != "" {
		b.WriteString(extra.clause)
		args = append(args, extra.args...)
	}
	order := "ASC"
	if afterSet {
		b.WriteString(" AND seq>?")
		args = append(args, after)
	} else {
		if beforeSet && before > 0 {
			b.WriteString(" AND seq<?")
			args = append(args, before)
		}
		order = "DESC"
	}
	fmt.Fprintf(&b, " ORDER BY seq %s LIMIT ?", order)
	args = append(args, limit+1)
	var raws [][]byte
	if err := r.DB.Raw(b.String(), args...).Scan(&raws).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(raws) > limit
	if hasMore {
		raws = raws[:limit]
	}
	if order == "DESC" {
		for i, j := 0, len(raws)-1; i < j; i, j = i+1, j-1 {
			raws[i], raws[j] = raws[j], raws[i]
		}
	}
	return raws, hasMore, nil
}

func (r *RunsRepo) loadRunLogsAll(runID string) ([]domain.LogRecord, error) {
	var rows []runLogRow
	if err := r.DB.Where("run_id = ?", runID).Order("seq").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.LogRecord, 0, len(rows))
	for _, row := range rows {
		var rec domain.LogRecord
		if json.Unmarshal(row.Payload, &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (r *RunsRepo) loadRunTelemetryAll(runID string) ([]domain.TelemetryEvent, error) {
	var rows []runTelemetryRow
	if err := r.DB.Where("run_id = ?", runID).Order("seq").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.TelemetryEvent, 0, len(rows))
	for _, row := range rows {
		var rec domain.TelemetryEvent
		if json.Unmarshal(row.Payload, &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, nil
}

// ---------- Shared paging helpers ----------

func clampPageLimit(limit int) int {
	if limit <= 0 {
		return defaultLogPageSize
	}
	if limit > maxLogPageSize {
		return maxLogPageSize
	}
	return limit
}

func buildLogPage(logs []domain.LogRecord, seq int64, nodeID string, after, before int64, afterSet, beforeSet bool, limit int) LogPage {
	filtered := make([]domain.LogRecord, 0, len(logs))
	for _, rec := range logs {
		if nodeID != "" && rec.Context.NodeID != nodeID {
			continue
		}
		filtered = append(filtered, rec)
	}
	sortSliceStable(filtered, func(a, b domain.LogRecord) bool { return a.Sequence < b.Sequence })
	page, hasMore := pageByCursor(len(filtered), after, before, afterSet, beforeSet, limit, func(i int) int64 { return filtered[i].Sequence })
	out := make([]domain.LogRecord, 0, len(page))
	for _, i := range page {
		out = append(out, filtered[i])
	}
	return finishLogPage(out, seq, hasMore)
}

func buildTelemetryPage(events []domain.TelemetryEvent, seq int64, nodeID, adapter string, after, before int64, afterSet, beforeSet bool, limit int) TelemetryPage {
	filtered := make([]domain.TelemetryEvent, 0, len(events))
	for _, rec := range events {
		if nodeID != "" && rec.NodeID != nodeID {
			continue
		}
		if !observability.MatchAdapter(rec, adapter) {
			continue
		}
		filtered = append(filtered, rec)
	}
	sortSliceStable(filtered, func(a, b domain.TelemetryEvent) bool { return a.Sequence < b.Sequence })
	page, hasMore := pageByCursor(len(filtered), after, before, afterSet, beforeSet, limit, func(i int) int64 { return filtered[i].Sequence })
	out := make([]domain.TelemetryEvent, 0, len(page))
	for _, i := range page {
		out = append(out, filtered[i])
	}
	return finishTelemetryPage(out, seq, hasMore)
}

func pageByCursor(n int, after, before int64, afterSet, beforeSet bool, limit int, seqAt func(int) int64) ([]int, bool) {
	if n == 0 {
		return nil, false
	}
	if afterSet {
		idx := make([]int, 0, limit+1)
		for i := 0; i < n; i++ {
			if seqAt(i) <= after {
				continue
			}
			idx = append(idx, i)
			if len(idx) == limit+1 {
				break
			}
		}
		hasMore := len(idx) > limit
		if hasMore {
			idx = idx[:limit]
		}
		return idx, hasMore
	}
	idx := make([]int, 0, limit+1)
	for i := n - 1; i >= 0; i-- {
		if beforeSet && before > 0 && seqAt(i) >= before {
			continue
		}
		idx = append(idx, i)
		if len(idx) == limit+1 {
			break
		}
	}
	hasMore := len(idx) > limit
	if hasMore {
		idx = idx[:limit]
	}
	for i, j := 0, len(idx)-1; i < j; i, j = i+1, j-1 {
		idx[i], idx[j] = idx[j], idx[i]
	}
	return idx, hasMore
}

func finishLogPage(logs []domain.LogRecord, seq int64, hasMore bool) LogPage {
	page := LogPage{Logs: logs, Sequence: seq, HasMore: hasMore}
	if len(logs) > 0 {
		page.NextAfter = logs[len(logs)-1].Sequence
		page.NextBefore = logs[0].Sequence
	}
	return page
}

func finishTelemetryPage(events []domain.TelemetryEvent, seq int64, hasMore bool) TelemetryPage {
	page := TelemetryPage{Events: events, Sequence: seq, HasMore: hasMore}
	if len(events) > 0 {
		page.NextAfter = events[len(events)-1].Sequence
		page.NextBefore = events[0].Sequence
	}
	return page
}

// RequestRunCancel durably flags a live run (running or waiting) for
// cancellation so any worker observing the heartbeat will settle it.
func (r *RunsRepo) RequestRunCancel(id string) (domain.Run, error) {
	return r.requestRunControl(id, true, false)
}

// RequestRunPause flags a running or waiting run for an explicit pause.
func (r *RunsRepo) RequestRunPause(id string) (domain.Run, error) {
	return r.requestRunControl(id, false, true)
}

// RunControlRequested reads only the durable pause/cancel bits. The worker
// uses this as a lease-heartbeat fallback and to ignore stray interrupts.
func (r *RunsRepo) RunControlRequested(id string) bool {
	var flags struct {
		CancelRequested bool `gorm:"column:cancel_requested"`
		PauseRequested  bool `gorm:"column:pause_requested"`
	}
	if err := r.DB.Model(&runRow{}).Select("cancel_requested", "pause_requested").Where("id = ?", id).Take(&flags).Error; err != nil {
		return false
	}
	return flags.CancelRequested || flags.PauseRequested
}

// ClearRunControl clears pending cancel/pause flags once the request has been
// observed by the run's owner instance.
func (r *RunsRepo) ClearRunControl(id string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		_, err := r.takeRunForUpdate(tx, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("run not found")
		}
		if err != nil {
			return err
		}
		return tx.Model(&runRow{}).Where("id = ?", id).Updates(map[string]any{
			"cancel_requested": false, "pause_requested": false,
			"workspace_editor_id": 0, "workspace_edit_until": nil,
		}).Error
	})
}

func (r *RunsRepo) requestRunControl(id string, cancelRequested, pauseRequested bool) (domain.Run, error) {
	now := time.Now().UTC()
	allowed := "status IN ('running', 'waiting')"
	res := r.DB.Model(&runRow{}).Where("id = ? AND "+allowed, id).Updates(map[string]any{
		"cancel_requested": cancelRequested,
		"pause_requested":  pauseRequested,
		"revision":         gorm.Expr("revision + 1"),
		"updated_at":       now,
	})
	if res.Error != nil {
		return domain.Run{}, fmt.Errorf("request run control: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		if _, ok := takeRun(r.DB, id); !ok {
			return domain.Run{}, fmt.Errorf("run not found")
		}
		return domain.Run{}, fmt.Errorf("run is not active")
	}
	row, ok := takeRun(r.DB, id)
	if !ok {
		return domain.Run{}, fmt.Errorf("run not found")
	}
	return runFromRow(row), nil
}

func sortSliceStable[T any](values []T, less func(a, b T) bool) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && less(values[j], values[j-1]); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// PutRunAtRevision fences a chat-card decision against concurrent API/worker
// writes. Control flags are cleared in the same guarded transaction.
func (r *RunsRepo) PutRunAtRevision(v domain.Run, revision int) error {
	v.UpdatedAt = time.Now().UTC()
	v.Revision++
	return r.writeRun(v, revision)
}

// SwitchRunCollaborationMode atomically replaces the scheduled graph, archives
// removed node rows in the run snapshot, and sets the operator control flags.
func (r *RunsRepo) SwitchRunCollaborationMode(run domain.Run, revision int) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		if err := scoped.PutRunAtRevision(run, revision); err != nil {
			return err
		}
		ids := []string{}
		for id := range run.Nodes {
			ids = append(ids, id)
		}
		q := tx.Where("run_id = ?", run.ID)
		if len(ids) > 0 {
			q = q.Where("node_id NOT IN ?", ids)
		}
		if err := q.Delete(&runNodeRow{}).Error; err != nil {
			return err
		}
		return tx.Model(&runRow{}).Where("id = ?", run.ID).Updates(map[string]any{"pause_requested": run.PauseRequested, "cancel_requested": run.CancelRequested}).Error
	})
}

// RestoreRunSOP keeps the SOP graph, task status and issue delivery lock in one
// transaction, so a stale run decision cannot partially reopen its requirement.
func (r *RunsRepo) RestoreRunSOP(run domain.Run, revision int, complete bool, source ...SOPHandoffSource) error {
	r.issues.mu.Lock()
	defer r.issues.mu.Unlock()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		base := *r.deps
		base.DB = tx
		scoped := *r
		scoped.deps = &base
		if len(source) > 0 {
			assistant := *r.assistant
			assistant.deps = &base
			if err := assistant.checkSOPHandoffSource(source[0]); err != nil {
				return err
			}
		}
		if err := scoped.SwitchRunCollaborationMode(run, revision); err != nil {
			return err
		}
		return r.issues.restoreSOPIssue(tx, run, complete)
	})
}
