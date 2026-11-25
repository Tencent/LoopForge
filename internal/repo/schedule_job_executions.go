package repo

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// MaxScheduleJobExecutionsPerJob bounds the stored history of each job; the
// oldest executions are pruned when a new one is recorded.
const MaxScheduleJobExecutionsPerJob = 200

// ScheduleJobExecutionQuery filters the execution history. A non-nil empty
// JobIDs matches nothing (no visible jobs).
type ScheduleJobExecutionQuery struct {
	JobIDs    []string
	ProjectID string
	Status    string
	TraceID   string
	Limit     int
}

// PutScheduleJobExecution inserts or replaces one execution. The first time an
// execution has an outcome it is also counted in the hourly statistics.
func (r *CatalogRepo) PutScheduleJobExecution(e domain.ScheduleJobExecution) error {
	if strings.TrimSpace(e.ID) == "" {
		e.ID = domain.ScheduleJobExecutionID(e.TraceID)
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		// One execution is written in order by the scheduler that runs it, so
		// its previous status cannot change under us.
		var prior []string
		if err := tx.Model(&scheduleJobExecutionRow{}).Where("id = ?", e.ID).Pluck("status", &prior).Error; err != nil {
			return err
		}
		existing := len(prior) > 0
		if err := upsert(tx, "schedule_job_executions", map[string]any{
			"id":         e.ID,
			"job_id":     e.JobID,
			"project_id": e.ProjectID,
			"trace_id":   e.TraceID,
			"status":     e.Status,
			"payload":    mustJSON(e),
			"started_at": timePtr(e.StartedAt),
			"updated_at": timePtr(time.Now().UTC()),
		}); err != nil {
			return err
		}
		if e.Finished() && !(existing && (domain.ScheduleJobExecution{Status: prior[0]}).Finished()) {
			if err := countScheduleJobExecution(tx, e); err != nil {
				return err
			}
		}
		if existing {
			return nil
		}
		if err := tx.Where("job_id = ? AND bucket_hour < ?", e.JobID, time.Now().UTC().Add(-scheduleExecutionStatsRetention)).
			Delete(&scheduleJobExecutionStatRow{}).Error; err != nil {
			return err
		}
		// Pruning on every insert keeps a job at most one row over the cap, so
		// listing its IDs is cheap. Skip in Go: MySQL rejects OFFSET without
		// LIMIT, and gorm renders Offset alone as a bare OFFSET there.
		var ids []string
		if err := tx.Model(&scheduleJobExecutionRow{}).Where("job_id = ?", e.JobID).
			Order("started_at DESC").Order("id DESC").Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) <= MaxScheduleJobExecutionsPerJob {
			return nil
		}
		return tx.Where("id IN ?", ids[MaxScheduleJobExecutionsPerJob:]).Delete(&scheduleJobExecutionRow{}).Error
	})
}

func (r *CatalogRepo) ScheduleJobExecution(id string) (domain.ScheduleJobExecution, bool) {
	return payloadOne[domain.ScheduleJobExecution](r.DB, "schedule_job_executions", id)
}

// scheduleExecutionStatsRetention bounds the hourly statistics; the dashboard
// compares the last 7 days with the 7 before.
const scheduleExecutionStatsRetention = 30 * 24 * time.Hour

// countScheduleJobExecution adds a finished execution to the statistics of
// the hour it started in.
func countScheduleJobExecution(tx *gorm.DB, e domain.ScheduleJobExecution) error {
	started := e.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	hour := started.UTC().Truncate(time.Hour)
	if _, err := insertIgnore(tx, "schedule_job_execution_stats", map[string]any{
		"job_id": e.JobID, "bucket_hour": hour, "project_id": e.ProjectID,
		"finished": 0, "success": 0, "failed": 0, "timed": 0, "duration_ms": 0,
		"metered": 0, "tokens_total": 0, "tokens_input": 0, "tokens_output": 0,
		"tokens_cache_read": 0, "tokens_cache_write": 0, "priced": 0, "cost_usd": 0,
	}); err != nil {
		return err
	}
	updates := map[string]any{"finished": gorm.Expr("finished + 1")}
	switch e.Status {
	case "success":
		updates["success"] = gorm.Expr("success + 1")
	case "failed":
		updates["failed"] = gorm.Expr("failed + 1")
	}
	if e.DurationMs > 0 {
		updates["timed"] = gorm.Expr("timed + 1")
		updates["duration_ms"] = gorm.Expr("duration_ms + ?", e.DurationMs)
	}
	if u := e.Usage; u != nil {
		updates["metered"] = gorm.Expr("metered + 1")
		updates["tokens_total"] = gorm.Expr("tokens_total + ?", e.ExecutionTotalTokens())
		updates["tokens_input"] = gorm.Expr("tokens_input + ?", u.InputTokens)
		updates["tokens_output"] = gorm.Expr("tokens_output + ?", u.OutputTokens)
		updates["tokens_cache_read"] = gorm.Expr("tokens_cache_read + ?", u.CacheReadTokens)
		updates["tokens_cache_write"] = gorm.Expr("tokens_cache_write + ?", u.CacheWriteTokens)
		if e.CostCNY != nil {
			updates["priced"] = gorm.Expr("priced + 1")
			updates["cost_usd"] = gorm.Expr("cost_usd + ?", u.CostUSD)
		}
	}
	return tx.Model(&scheduleJobExecutionStatRow{}).Where("job_id = ? AND bucket_hour = ?", e.JobID, hour).Updates(updates).Error
}

// ScheduleJobExecutionOverview summarizes the executions of jobIDs started in
// the last 7×24 hours (current, the hour of now included) and the 7×24 hours
// before (previous). Finished executions come from the hourly statistics, so
// the numbers do not depend on how many executions a job keeps; running ones
// are counted from the history.
func (r *CatalogRepo) ScheduleJobExecutionOverview(jobIDs []string, now time.Time) (current, previous domain.ScheduleExecutionWindow) {
	if len(jobIDs) == 0 {
		return
	}
	currentFrom := now.UTC().Truncate(time.Hour).Add(-167 * time.Hour)
	previousFrom := currentFrom.Add(-168 * time.Hour)
	pick := func(t time.Time) *domain.ScheduleExecutionWindow {
		if t.Before(previousFrom) {
			return nil
		}
		if t.Before(currentFrom) {
			return &previous
		}
		return &current
	}
	durations := map[*domain.ScheduleExecutionWindow][2]int64{}
	costs := map[*domain.ScheduleExecutionWindow]float64{}
	var stats []scheduleJobExecutionStatRow
	if err := r.DB.Where("job_id IN ? AND bucket_hour >= ?", jobIDs, previousFrom).Find(&stats).Error; err == nil {
		for _, s := range stats {
			w := pick(s.BucketHour)
			if w == nil {
				continue
			}
			w.Finished += s.Finished
			w.Success += s.Success
			w.Failed += s.Failed
			d := durations[w]
			durations[w] = [2]int64{d[0] + s.DurationMs, d[1] + int64(s.Timed)}
			w.Metered += s.Metered
			w.Tokens.Total += s.TokensTotal
			w.Tokens.Input += s.TokensInput
			w.Tokens.Output += s.TokensOutput
			w.Tokens.CacheRead += s.TokensCacheRead
			w.Tokens.CacheWrite += s.TokensCacheWrite
			w.Priced += s.Priced
			costs[w] += s.CostUSD
		}
	}
	var running []*time.Time
	if err := r.DB.Model(&scheduleJobExecutionRow{}).Where("job_id IN ? AND status = ? AND started_at >= ?", jobIDs, "running", previousFrom).
		Pluck("started_at", &running).Error; err == nil {
		for _, t := range running {
			if t == nil {
				continue
			}
			if w := pick(*t); w != nil {
				w.Running++
			}
		}
	}
	for _, w := range []*domain.ScheduleExecutionWindow{&current, &previous} {
		w.Total = w.Finished + w.Running
		if d := durations[w]; d[1] > 0 {
			w.AvgMs = d[0] / d[1]
		}
		if w.Metered > 0 {
			w.AvgTokens = w.Tokens.Total / int64(w.Metered)
		}
		w.CostCNY = math.Round(costs[w]*domain.AnalyticsUSDToCNY*100) / 100
	}
	return
}

// ScheduleJobExecutionPageQuery selects one page of the execution dashboard.
// Search is matched case-insensitively against the fields of
// ScheduleJobExecution.MatchesSearch.
type ScheduleJobExecutionPageQuery struct {
	JobIDs []string
	Status string
	Since  time.Time
	Search string
	Offset int
	Limit  int
}

// ScheduleJobExecutionPage returns one page of executions, newest first, and
// how many executions match in total.
func (r *CatalogRepo) ScheduleJobExecutionPage(q ScheduleJobExecutionPageQuery) ([]domain.ScheduleJobExecution, int) {
	out := []domain.ScheduleJobExecution{}
	if len(q.JobIDs) == 0 {
		return out, 0
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset := max(q.Offset, 0)
	db := r.DB.Model(&scheduleJobExecutionRow{}).Where("job_id IN ?", q.JobIDs)
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if !q.Since.IsZero() {
		db = db.Where("started_at >= ?", q.Since.UTC())
	}
	decode := func(rows []scheduleJobExecutionRow) []domain.ScheduleJobExecution {
		items := make([]domain.ScheduleJobExecution, 0, len(rows))
		for _, row := range rows {
			var e domain.ScheduleJobExecution
			if json.Unmarshal(row.Payload, &e) == nil {
				items = append(items, e)
			}
		}
		return items
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	if search == "" {
		var total int64
		if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return out, 0
		}
		var rows []scheduleJobExecutionRow
		if err := db.Order("started_at DESC").Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
			return out, int(total)
		}
		return decode(rows), int(total)
	}
	// The payload text narrows the candidates in SQL; the fields decide. A
	// job keeps at most MaxScheduleJobExecutionsPerJob rows, so the candidates
	// stay bounded.
	cast := "LOWER(CAST(payload AS TEXT))"
	if r.DB.Dialector.Name() == "mysql" {
		cast = "LOWER(CAST(payload AS CHAR))"
	}
	var rows []scheduleJobExecutionRow
	if err := db.Where("INSTR("+cast+", ?) > 0", search).Order("started_at DESC").Order("id DESC").Find(&rows).Error; err != nil {
		return out, 0
	}
	for _, e := range decode(rows) {
		if e.MatchesSearch(search) {
			out = append(out, e)
		}
	}
	total := len(out)
	if offset >= total {
		return []domain.ScheduleJobExecution{}, total
	}
	return out[offset:min(offset+limit, total)], total
}

// CountScheduleJobExecutions counts the executions kept for jobIDs.
func (r *CatalogRepo) CountScheduleJobExecutions(jobIDs []string) int {
	if len(jobIDs) == 0 {
		return 0
	}
	var total int64
	if err := r.DB.Model(&scheduleJobExecutionRow{}).Where("job_id IN ?", jobIDs).Count(&total).Error; err != nil {
		return 0
	}
	return int(total)
}

// ScheduleJobExecutions lists executions newest first.
func (r *CatalogRepo) ScheduleJobExecutions(q ScheduleJobExecutionQuery) []domain.ScheduleJobExecution {
	if q.JobIDs != nil && len(q.JobIDs) == 0 {
		return []domain.ScheduleJobExecution{}
	}
	db := r.DB.Model(&scheduleJobExecutionRow{})
	if q.JobIDs != nil {
		db = db.Where("job_id IN ?", q.JobIDs)
	}
	if q.ProjectID != "" {
		db = db.Where("project_id = ?", q.ProjectID)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.TraceID != "" {
		db = db.Where("trace_id = ?", q.TraceID)
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []scheduleJobExecutionRow
	if err := db.Order("started_at DESC").Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return []domain.ScheduleJobExecution{}
	}
	out := make([]domain.ScheduleJobExecution, 0, len(rows))
	for _, row := range rows {
		var e domain.ScheduleJobExecution
		if json.Unmarshal(row.Payload, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// FailRunningScheduleJobExecutions marks executions left running by a
// stopped process as failed. Only executions started before cutoff change.
func (r *CatalogRepo) FailRunningScheduleJobExecutions(cutoff time.Time, reason string) error {
	var rows []scheduleJobExecutionRow
	if err := r.DB.Where("status = ? AND started_at < ?", "running", cutoff.UTC()).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var e domain.ScheduleJobExecution
		if err := json.Unmarshal(row.Payload, &e); err != nil {
			continue
		}
		finished := cutoff.UTC()
		e.Status = "failed"
		if e.Error == "" {
			e.Error = reason
		}
		e.FinishedAt = &finished
		if !e.StartedAt.IsZero() {
			e.DurationMs = finished.Sub(e.StartedAt).Milliseconds()
		}
		if err := r.PutScheduleJobExecution(e); err != nil {
			return err
		}
	}
	return nil
}
