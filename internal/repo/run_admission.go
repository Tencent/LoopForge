package repo

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProjectRunLimit caps concurrently admitted top-level SOP runs per project on
// platform (cloud) execution. Personal machines are counted per machine
// against their optional user-configured Runner.RunLimit instead.
const ProjectRunLimit = 10

// Slot accounting is partitioned into buckets: "project:<id>" for platform
// execution (the empty id is the shared bucket of unassigned runs) and
// "runner:<id>" for a personal machine. Each bucket has its own lock row, so
// different projects and machines never wait on each other.
//
// Lock rule: a transaction that takes a bucket lock takes it FIRST, before any
// run row lock, and holds at most one bucket. Only transactions that claim a
// slot (insert a running run, restart a succeeded/unstarted run, promote the
// queue) or change a machine limit take it. Writes that keep or release a
// slot never do: a release that races an admission decision can only make it
// over-count and queue a little longer, never exceed the limit, and the next
// promotion pass corrects it.
type runAdmissionLockRow struct {
	ID string `gorm:"column:id;primaryKey;size:128"`
}

func (runAdmissionLockRow) TableName() string { return "run_admission_locks" }

// lockAdmissionGate locks one bucket. The row is runtime lock bookkeeping
// created on first use, not seed data.
func lockAdmissionGate(d *deps, tx *gorm.DB, key string) error {
	row := runAdmissionLockRow{ID: key}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	return d.locking(tx.Where("id = ?", row.ID)).Take(&row).Error
}

// admissionTxOptions makes plain reads inside a bucket-locked transaction see
// every commit up to the lock (MySQL defaults to REPEATABLE READ snapshots).
// sqlite serializes writers on its single test connection already.
func (d *deps) admissionTxOptions() []*sql.TxOptions {
	if d.isMySQL() {
		return []*sql.TxOptions{{Isolation: sql.LevelReadCommitted}}
	}
	return nil
}

// admissionLockRequired aborts a write attempt that turned out to claim a
// slot without holding its bucket lock; writeRun retries with the lock.
type admissionLockRequired struct{ key string }

func (e *admissionLockRequired) Error() string { return "admission lock required for " + e.key }

func admissionBucketKey(project, runnerID string, exempt bool) string {
	if exempt {
		return "runner:" + strings.TrimSpace(runnerID)
	}
	return "project:" + strings.TrimSpace(project)
}

// runAdmissionBucketKey is the persisted bucket of a run whose project and
// exemption are already frozen on it.
func runAdmissionBucketKey(run domain.Run) string {
	return admissionBucketKey(run.ExecutionContext.ProjectID, run.ExecutionContext.ExecutionRunnerID, run.ConcurrencyExempt)
}

type runnerAdmissionInfo struct {
	personal bool
	limit    int
}

// admissionScope caches per-transaction lookups so legacy rows without
// persisted quota metadata do not issue one runner/issue query each.
type admissionScope struct {
	tx      *gorm.DB
	runners map[string]runnerAdmissionInfo
	issues  map[string]string
}

func newAdmissionScope(tx *gorm.DB) *admissionScope {
	return &admissionScope{tx: tx, runners: map[string]runnerAdmissionInfo{}, issues: map[string]string{}}
}

func (s *admissionScope) runner(id string) (runnerAdmissionInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return runnerAdmissionInfo{}, nil
	}
	if info, ok := s.runners[id]; ok {
		return info, nil
	}
	var row runnerRow
	info := runnerAdmissionInfo{}
	if err := s.tx.Select("id", "owner_user_id", "run_limit").Where("id = ?", id).Take(&row).Error; err == nil {
		info.personal = row.OwnerUserID != nil && *row.OwnerUserID > 0
		if row.RunLimit != nil && *row.RunLimit > 0 {
			info.limit = *row.RunLimit
		}
	} else if err != gorm.ErrRecordNotFound {
		return runnerAdmissionInfo{}, err
	}
	s.runners[id] = info
	return info, nil
}

func (s *admissionScope) project(run domain.Run) (string, error) {
	if id := strings.TrimSpace(run.ExecutionContext.ProjectID); id != "" {
		return id, nil
	}
	issueID := strings.TrimSpace(run.ExecutionContext.IssueID)
	if issueID == "" {
		return "", nil
	}
	if id, ok := s.issues[issueID]; ok {
		return id, nil
	}
	var row issueRow
	project := ""
	if err := s.tx.Select("id", "project_id").Where("id = ?", issueID).Take(&row).Error; err == nil {
		project = row.ProjectID
	} else if err != gorm.ErrRecordNotFound {
		return "", err
	}
	s.issues[issueID] = project
	return project, nil
}

func (s *admissionScope) exempt(run domain.Run) (bool, error) {
	info, err := s.runner(run.ExecutionContext.ExecutionRunnerID)
	return info.personal, err
}

// limit returns the bucket's slot count; 0 means unlimited.
func (s *admissionScope) limit(key string) (int, error) {
	if id, ok := strings.CutPrefix(key, "runner:"); ok {
		info, err := s.runner(id)
		return info.limit, err
	}
	return ProjectRunLimit, nil
}

// rowBucketKey reads the persisted bucket, falling back to execution_context,
// the linked issue, and current runner ownership for legacy rows that the
// 010 backfill has not populated (written by the previous binary).
func (s *admissionScope) rowBucketKey(row runRow) (string, error) {
	if row.QuotaBucket != nil && *row.QuotaBucket != "" {
		return *row.QuotaBucket, nil
	}
	run := runFromRow(row)
	project := ""
	if row.QuotaProjectID != nil {
		project = *row.QuotaProjectID
	} else {
		var err error
		if project, err = s.project(run); err != nil {
			return "", err
		}
	}
	exempt := row.QuotaExempt != nil && *row.QuotaExempt
	if row.QuotaExempt == nil {
		var err error
		if exempt, err = s.exempt(run); err != nil {
			return "", err
		}
	}
	return admissionBucketKey(project, run.ExecutionContext.ExecutionRunnerID, exempt), nil
}

// occupied counts admitted top-level runs in one bucket through
// idx_runs_quota_bucket. Rows with a NULL bucket are legacy rows not yet
// backfilled; they are resolved in Go so they still count.
func (s *admissionScope) occupied(key, exclude string) (int, error) {
	var rows []runRow
	q := s.tx.Select("id", "status", "quota_bucket", "quota_project_id", "quota_parent_run_id", "quota_exempt", "admission_state", "execution_context").
		Where("(quota_bucket = ? OR quota_bucket IS NULL) AND status NOT IN ? AND (admission_state IS NULL OR admission_state <> ?)",
			key, []string{"succeeded", "queued"}, "unstarted")
	if exclude != "" {
		q = q.Where("id <> ?", exclude)
	}
	if err := q.Find(&rows).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		parent := ""
		if row.QuotaParentRunID != nil {
			parent = *row.QuotaParentRunID
		} else {
			parent = runFromRow(row).ExecutionContext.ParentRunID
		}
		if strings.TrimSpace(parent) != "" {
			continue
		}
		bucket, err := s.rowBucketKey(row)
		if err != nil {
			return 0, err
		}
		if bucket == key {
			n++
		}
	}
	return n, nil
}

// hasQueued reports whether a run already waits in the bucket; a new request
// must not overtake it. Queued rows always carry a persisted bucket.
func (s *admissionScope) hasQueued(key, exclude string) (bool, error) {
	var ids []string
	err := s.tx.Model(&runRow{}).Where("quota_bucket = ? AND status = ? AND id <> ?", key, "queued", exclude).Limit(1).Pluck("id", &ids).Error
	return len(ids) > 0, err
}

func queuedMessage(key string) string {
	if strings.HasPrefix(key, "runner:") {
		return "等待执行机器可用运行名额"
	}
	return "等待项目可用运行名额"
}

// prepareRunAdmission freezes the run's quota identity and decides whether a
// running write claims a slot. lockedKey is the bucket lock held by the
// caller's transaction ("" for none).
func (r *RunsRepo) prepareRunAdmission(tx *gorm.DB, run *domain.Run, current runRow, exists bool, lockedKey string) error {
	s := newAdmissionScope(tx)
	if exists {
		cur := runFromRow(current)
		run.AdmissionState = current.AdmissionState
		if current.QueueEntered != nil {
			run.QueueEnteredAt = *current.QueueEntered
		}
		run.ExecutionContext.ParentRunID = cur.ExecutionContext.ParentRunID
		if current.QuotaProjectID != nil {
			run.ExecutionContext.ProjectID = *current.QuotaProjectID
		} else {
			project, err := s.project(cur)
			if err != nil {
				return err
			}
			run.ExecutionContext.ProjectID = project
		}
		if current.QuotaExempt != nil {
			run.ConcurrencyExempt = *current.QuotaExempt
		} else {
			exempt, err := s.exempt(cur)
			if err != nil {
				return err
			}
			run.ConcurrencyExempt = exempt
		}
		// A running write based on an earlier snapshot must not bypass a queue.
		if current.Status == "queued" && run.Status == "running" {
			return fmt.Errorf("run is queued")
		}
	} else {
		project, err := s.project(*run)
		if err != nil {
			return err
		}
		run.ExecutionContext.ProjectID = project
		exempt, err := s.exempt(*run)
		if err != nil {
			return err
		}
		run.ConcurrencyExempt = exempt
	}
	if run.Status == "succeeded" {
		run.AdmissionState = "released"
		return nil
	}
	if run.Status != "running" {
		if exists && current.Status == "queued" {
			if run.Status != "cancelled" && run.Status != "queued" {
				return fmt.Errorf("run is queued")
			}
			if run.Status == "cancelled" {
				run.AdmissionState = "unstarted"
			}
		}
		return nil
	}
	if exists && current.Status != "succeeded" && current.AdmissionState != "unstarted" {
		run.AdmissionState = "held"
		return r.checkParentAdmission(tx, *run)
	}
	run.AdmissionState = "held"
	if strings.TrimSpace(run.ExecutionContext.ParentRunID) != "" {
		return r.checkParentAdmission(tx, *run)
	}
	key := runAdmissionBucketKey(*run)
	limit, err := s.limit(key)
	if err != nil || limit <= 0 {
		return err
	}
	if key != lockedKey {
		return &admissionLockRequired{key: key}
	}
	occupied, err := s.occupied(key, run.ID)
	if err != nil {
		return err
	}
	queued, err := s.hasQueued(key, run.ID)
	if err != nil {
		return err
	}
	if occupied >= limit || queued {
		run.Status, run.AdmissionState = "queued", "queued"
		run.QueueEnteredAt = time.Now().UTC()
		run.OwnerInstanceID, run.LeaseUntil = "", time.Time{}
		run.CancelRequested, run.PauseRequested = false, false
		for i := range run.Events {
			if run.Events[i].Type == "run.started" && !exists {
				run.Events[i].Type = "run.queued"
				run.Events[i].Message = queuedMessage(key)
			}
		}
	}
	return nil
}

func (r *RunsRepo) checkParentAdmission(tx *gorm.DB, run domain.Run) error {
	parentID := strings.TrimSpace(run.ExecutionContext.ParentRunID)
	seen := map[string]bool{run.ID: true}
	for parentID != "" {
		if seen[parentID] {
			return fmt.Errorf("cyclic parent run")
		}
		seen[parentID] = true
		var parent runRow
		if err := tx.Where("id = ?", parentID).Take(&parent).Error; err != nil {
			return fmt.Errorf("parent run unavailable: %w", err)
		}
		if parent.Status == "succeeded" || parent.Status == "queued" || parent.AdmissionState == "unstarted" {
			return fmt.Errorf("请从顶层 SOP 重新运行，父流程尚未获得运行名额")
		}
		parentID = strings.TrimSpace(runFromRow(parent).ExecutionContext.ParentRunID)
	}
	return nil
}

// PromoteQueuedRuns admits queued runs FIFO within each bucket, one short
// transaction per bucket. A bucket whose limit was raised or cleared releases
// its queue on the next pass. A failing bucket does not block the others.
func (r *RunsRepo) PromoteQueuedRuns() ([]string, error) {
	var raw []*string
	if err := r.DB.Model(&runRow{}).Where("status = ?", "queued").Distinct("quota_bucket").Pluck("quota_bucket", &raw).Error; err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(raw))
	for _, key := range raw {
		if key != nil && *key != "" {
			keys = append(keys, *key)
		}
	}
	sort.Strings(keys)
	var promoted []string
	var firstErr error
	for _, key := range keys {
		ids, err := r.promoteBucket(key)
		promoted = append(promoted, ids...)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("promote %s: %w", key, err)
		}
	}
	return promoted, firstErr
}

func (r *RunsRepo) promoteBucket(key string) ([]string, error) {
	var promoted []string
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		promoted = nil
		if err := lockAdmissionGate(r.deps, tx, key); err != nil {
			return err
		}
		s := newAdmissionScope(tx)
		limit, err := s.limit(key)
		if err != nil {
			return err
		}
		occupied := 0
		if limit > 0 {
			if occupied, err = s.occupied(key, ""); err != nil {
				return err
			}
			if occupied >= limit {
				return nil
			}
		}
		var queued []runRow
		if err := tx.Select("id", "queue_entered_at", "created_at").Where("quota_bucket = ? AND status = ?", key, "queued").
			Order("queue_entered_at, created_at, id").Find(&queued).Error; err != nil {
			return err
		}
		for _, row := range queued {
			if limit > 0 && occupied >= limit {
				break
			}
			now := time.Now().UTC()
			// A concurrent cancel holds the run row; re-check the status here.
			res := tx.Model(&runRow{}).Where("id = ? AND status = ?", row.ID, "queued").Updates(map[string]any{
				"status": "running", "admission_state": "held", "revision": gorm.Expr("revision + 1"), "updated_at": now,
				"cancel_requested": false, "pause_requested": false, "owner_instance_id": "", "lease_until": nil,
			})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			var seq int64
			if err := tx.Model(&runEventRow{}).Select("COALESCE(MAX(seq), -1) + 1").Where("run_id = ?", row.ID).Scan(&seq).Error; err != nil {
				return err
			}
			ev := domain.RunEvent{ID: fmt.Sprintf("adm-%d-%d", now.UnixNano(), seq), RunID: row.ID, Type: "run.started", Message: "获得运行名额，开始执行", Timestamp: now}
			if err := tx.Create(&runEventRow{RunID: row.ID, Seq: seq, EventID: ev.ID, Type: ev.Type, Message: ev.Message, Payload: jsonBytes(ev), Ts: &now}).Error; err != nil {
				return err
			}
			occupied++
			promoted = append(promoted, row.ID)
		}
		return nil
	}, r.admissionTxOptions()...)
	if err != nil {
		return nil, err
	}
	return promoted, nil
}

// CancelQueuedRun settles a queued run as cancelled-before-start. It takes
// only the run row: promotion re-checks the status on that same row.
func (r *RunsRepo) CancelQueuedRun(id string) (bool, error) {
	cancelled := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, id)
		if err != nil {
			return err
		}
		if row.Status != "queued" {
			return nil
		}
		cancelled = true
		return tx.Model(&runRow{}).Where("id = ?", id).Updates(map[string]any{"status": "cancelled", "admission_state": "unstarted", "revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()}).Error
	})
	return cancelled, err
}
