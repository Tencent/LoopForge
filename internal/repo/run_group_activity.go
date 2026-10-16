package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
)

// RunGroupActivityState loads only facts used by the collaboration projection.
// Polling this view must not hydrate model prose, worker logs or artifact bodies.
func (r *RunsRepo) RunGroupActivityState(id string) (domain.Run, bool) {
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
	var events []runEventRow
	if err := r.DB.Where("run_id = ? AND type IN ?", id, []string{"group.dispatched", "workspace.changed"}).Order("seq").Find(&events).Error; err != nil {
		return domain.Run{}, false
	}
	for _, row := range events {
		var e domain.RunEvent
		if json.Unmarshal(row.Payload, &e) == nil {
			run.Events = append(run.Events, e)
		}
	}
	category := "JSON_EXTRACT(payload, '$.category')"
	name := "JSON_EXTRACT(payload, '$.name')"
	if r.isMySQL() {
		category = "JSON_UNQUOTE(" + category + ")"
		name = "JSON_UNQUOTE(" + name + ")"
	}
	var telemetry []runTelemetryRow
	q := r.DB.Where("run_id = ?", id).Where("("+category+" = ? AND "+name+" IN ?) OR "+category+" = ?", "verification", []string{"process.started", "process.finished"}, "tool")
	if err := q.Order("seq").Find(&telemetry).Error; err != nil {
		return domain.Run{}, false
	}
	for _, row := range telemetry {
		var e domain.TelemetryEvent
		if json.Unmarshal(row.Payload, &e) == nil {
			run.Telemetry = append(run.Telemetry, e)
		}
	}
	return run, true
}
