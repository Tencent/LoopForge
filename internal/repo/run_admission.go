package repo

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ProjectRunLimit            = domain.DefaultRunnerRunLimit
	PlatformAdmissionBucketKey = "platform"
	projectAutoBucketPrefix    = "project-auto:"
	runnerBucketPrefix         = "runner:"
	legacyProjectBucketPrefix  = "project:"
)

// Slot accounting uses one platform bucket across all projects and one
// bucket per personal machine. Elastic queues do not hold slots or locks.
//
// Lock rule: a transaction that takes a bucket lock takes it FIRST, before any
// run row lock, and holds at most one bucket. Only transactions that claim a
// slot (insert a running run, restart a released/unstarted run, promote the
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

// admissionTxOptions makes plain reads after a bucket or workspace lock see
// every commit up to that lock, and avoids gap locks on missing run IDs.
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

func runnerAdmissionBucketKey(id string) string {
	return runnerBucketPrefix + strings.TrimSpace(id)
}

func admissionBucketKey(runnerID string, exempt bool) string {
	if exempt {
		return runnerAdmissionBucketKey(runnerID)
	}
	return PlatformAdmissionBucketKey
}

func unboundProjectAutoRun(run domain.Run) bool {
	ec := run.ExecutionContext
	return ec.ExecutionPool == domain.ExecutionPoolProjectAuto && ec.ExecutionMode == "" && strings.TrimSpace(ec.ExecutionRunnerID) == ""
}

func canonicalAdmissionBucketKey(key string) string {
	if strings.HasPrefix(key, legacyProjectBucketPrefix) {
		return PlatformAdmissionBucketKey
	}
	return key
}

func runAdmissionBucketKey(run domain.Run) string {
	if unboundProjectAutoRun(run) {
		return projectAutoBucketPrefix + strings.TrimSpace(run.ExecutionContext.ProjectID)
	}
	return admissionBucketKey(run.ExecutionContext.ExecutionRunnerID, run.ConcurrencyExempt)
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
	if id, ok := strings.CutPrefix(key, runnerBucketPrefix); ok {
		info, err := s.runner(id)
		return info.limit, err
	}
	return platformRunnerRunLimit(s.tx)
}

// rowBucketKey reads the persisted bucket, falling back to execution_context,
// the linked issue, and current runner ownership for legacy rows that the
// 010 backfill has not populated (written by the previous binary).
func (s *admissionScope) rowBucketKey(row runRow) (string, error) {
	if row.QuotaBucket != nil && *row.QuotaBucket != "" {
		return canonicalAdmissionBucketKey(*row.QuotaBucket), nil
	}
	run := runFromRow(row)
	run.ConcurrencyExempt = row.QuotaExempt != nil && *row.QuotaExempt
	if row.QuotaExempt == nil {
		var err error
		if run.ConcurrencyExempt, err = s.exempt(run); err != nil {
			return "", err
		}
	}
	return runAdmissionBucketKey(run), nil
}

// admissionBuckets matches the fixed queues for the given machines. Older
// platform runs used a project-prefixed bucket and now share the platform gate.
func admissionBuckets(keys []string) (string, []any) {
	for _, key := range keys {
		if key == PlatformAdmissionBucketKey {
			return "(quota_bucket IN ? OR quota_bucket LIKE ?)", []any{keys, legacyProjectBucketPrefix + "%"}
		}
	}
	return "quota_bucket IN ?", []any{keys}
}

// loadSnapshots reads all relevant slot holders and queues in one query so a
// concurrent write filling a legacy NULL bucket cannot make a slot disappear.
func (s *admissionScope) loadSnapshots(keys []string, exclude string) (map[string]domain.RunnerLoad, error) {
	loads := make(map[string]domain.RunnerLoad, len(keys))
	for _, key := range keys {
		loads[key] = domain.RunnerLoad{}
	}
	if len(keys) == 0 {
		return loads, nil
	}
	var rows []runRow
	buckets, args := admissionBuckets(keys)
	q := s.tx.Select("status", "quota_bucket", "quota_parent_run_id", "quota_exempt",
		"CASE WHEN quota_bucket IS NULL OR quota_bucket = '' OR quota_parent_run_id IS NULL THEN execution_context END AS execution_context").
		Where("("+buckets+" OR quota_bucket IS NULL OR quota_bucket = '')", args...).
		Where("status <> ? AND (status = ? OR admission_state IS NULL OR admission_state NOT IN ?)", "succeeded", "queued", []string{"unstarted", "released"}).
		Where("(kind IS NULL OR kind <> ?) AND (workflow_id IS NULL OR workflow_id <> ?)", domain.RunKindWorkspace, domain.FreeCollaborationWorkflowID).
		Where("(quota_parent_run_id IS NULL OR quota_parent_run_id = '')")
	if exclude != "" {
		q = q.Where("id <> ?", exclude)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.QuotaParentRunID == nil && strings.TrimSpace(runFromRow(row).ExecutionContext.ParentRunID) != "" {
			continue
		}
		bucket, err := s.rowBucketKey(row)
		if err != nil {
			return nil, err
		}
		load, ok := loads[bucket]
		if !ok {
			continue
		}
		if row.Status == "queued" {
			load.QueuedRuns++
		} else {
			load.OccupiedRuns++
			if row.Status == "running" {
				load.RunningRuns++
			}
		}
		loads[bucket] = load
	}
	return loads, nil
}

func (s *admissionScope) occupied(key, exclude string) (int, error) {
	key = canonicalAdmissionBucketKey(key)
	loads, err := s.loadSnapshots([]string{key}, exclude)
	return loads[key].OccupiedRuns, err
}

// LoadSnapshots returns the global platform load and the requested machine
// loads keyed by their admission buckets.
func (r *RunsRepo) LoadSnapshots(runnerIDs []string) (map[string]domain.RunnerLoad, error) {
	keys := []string{PlatformAdmissionBucketKey}
	for _, id := range runnerIDs {
		keys = append(keys, runnerAdmissionBucketKey(id))
	}
	return newAdmissionScope(r.DB).loadSnapshots(keys, "")
}

func (r *RunsRepo) RunnerLoadSnapshots(ids []string) (map[string]domain.RunnerLoad, error) {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, runnerAdmissionBucketKey(id))
	}
	loads, err := newAdmissionScope(r.DB).loadSnapshots(keys, "")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.RunnerLoad, len(ids))
	for _, id := range ids {
		byID[id] = loads[runnerAdmissionBucketKey(id)]
	}
	return byID, nil
}

// hasQueued reports whether a run already waits in the bucket; a new request
// must not overtake it. Queued rows always carry a persisted bucket.
func (s *admissionScope) hasQueued(key, exclude string) (bool, error) {
	buckets, args := admissionBuckets([]string{canonicalAdmissionBucketKey(key)})
	var ids []string
	err := s.tx.Model(&runRow{}).Where(buckets, args...).Where("status = ? AND id <> ?", "queued", exclude).Limit(1).Pluck("id", &ids).Error
	return len(ids) > 0, err
}

func (s *admissionScope) wouldQueue(key, exclude string, limit int) (bool, error) {
	occupied, err := s.occupied(key, exclude)
	if err != nil || occupied >= limit {
		return occupied >= limit, err
	}
	return s.hasQueued(key, exclude)
}

// RunWouldQueueOnAdmission is a preflight for workspace restoration. Only the
// eventual write under the bucket lock decides admission.
func (r *RunsRepo) RunWouldQueueOnAdmission(run domain.Run) (bool, error) {
	if !isTopLevelSOP(run) || run.AdmissionState != "released" && run.AdmissionState != "unstarted" && run.Status != "succeeded" {
		return false, nil
	}
	s := newAdmissionScope(r.DB)
	var row runRow
	if err := r.DB.Select("quota_bucket", "quota_project_id", "quota_exempt", "execution_context").Where("id = ?", run.ID).Take(&row).Error; err != nil {
		return false, err
	}
	key, err := s.rowBucketKey(row)
	if err != nil {
		return false, err
	}
	if unboundProjectAutoRun(run) {
		return true, nil
	}
	limit, err := s.limit(key)
	if err != nil || limit <= 0 {
		return false, err
	}
	return s.wouldQueue(key, run.ID, limit)
}

func queuedMessage(key string) string {
	if strings.HasPrefix(key, runnerBucketPrefix) {
		return "等待执行机器可用运行名额"
	}
	if strings.HasPrefix(key, projectAutoBucketPrefix) {
		return "等待项目内可用执行机器名额"
	}
	return "等待平台执行机器可用运行名额"
}

// prepareRunAdmission freezes the run's quota identity and decides whether a
// running write claims a slot. lockedKey is the bucket lock held by the
// caller's transaction ("" for none).
func (r *RunsRepo) prepareRunAdmission(tx *gorm.DB, run *domain.Run, current runRow, exists bool, lockedKey string) error {
	s := newAdmissionScope(tx)
	if exists {
		cur := runFromRow(current)
		if cur.ExecutionContext.ExecutionPool == domain.ExecutionPoolProjectAuto {
			// Promotion is the only write that may bind a project-auto run.
			run.ExecutionContext.ExecutionPool = cur.ExecutionContext.ExecutionPool
			run.ExecutionContext.ExecutionRunnerID = cur.ExecutionContext.ExecutionRunnerID
			run.ExecutionContext.ExecutionMode = cur.ExecutionContext.ExecutionMode
			run.ExecutionContext.RunnerWorkspaceLayout = cur.ExecutionContext.RunnerWorkspaceLayout
		}
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
	if run.IsWorkspace() || run.WorkflowID == domain.FreeCollaborationWorkflowID {
		return nil
	}
	if unboundProjectAutoRun(*run) {
		markRunQueued(run, time.Now().UTC())
		return nil
	}
	if exists && current.Status != "succeeded" && current.AdmissionState != "unstarted" && current.AdmissionState != "released" {
		run.AdmissionState = "held"
		return r.checkParentAdmission(tx, run)
	}
	run.AdmissionState = "held"
	if strings.TrimSpace(run.ExecutionContext.ParentRunID) != "" {
		return r.checkParentAdmission(tx, run)
	}
	key := runAdmissionBucketKey(*run)
	if key != lockedKey {
		return &admissionLockRequired{key: key}
	}
	limit, err := s.limit(key)
	if err != nil || limit <= 0 {
		return err
	}
	queued, err := s.wouldQueue(key, run.ID, limit)
	if err != nil {
		return err
	}
	if queued {
		markRunQueued(run, time.Now().UTC())
		for i := range run.Events {
			if run.Events[i].Type == "run.started" && !exists {
				run.Events[i].Type = "run.queued"
				run.Events[i].Message = queuedMessage(key)
			}
		}
	}
	return nil
}

func markRunQueued(run *domain.Run, now time.Time) {
	run.Status, run.AdmissionState = "queued", "queued"
	run.QueueEnteredAt = now
	run.OwnerInstanceID, run.LeaseUntil = "", time.Time{}
	run.CancelRequested, run.PauseRequested = false, false
}

// ReleaseCleanedRunAdmission frees a slot after workspace cleanup succeeded.
// Releases take only the run row lock, never the admission bucket lock.
func (r *RunsRepo) ReleaseCleanedRunAdmission(runID, eventMessage string) (bool, error) {
	released := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if row.Status == "running" || row.Status == "queued" || row.WorkspaceCleaned == nil || row.AdmissionState == "unstarted" || row.AdmissionState == "released" {
			return nil
		}
		released = true
		return releaseRunAdmission(tx, runID, eventMessage)
	})
	return released && err == nil, err
}

// ReleaseIdleBorrowedSOPAdmission frees a slot while leaving the conversation
// checkout under its owner's lifecycle.
func (r *RunsRepo) ReleaseIdleBorrowedSOPAdmission(runID string, revision int, now time.Time) (bool, error) {
	released := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		run := runFromRow(row)
		if row.Revision != revision || !isTopLevelSOP(run) || !run.UsesConversationWorkspace() ||
			(row.Status != "failed" && row.Status != "waiting") || row.AdmissionState == "unstarted" || row.AdmissionState == "released" ||
			row.CleanupNotBefore != nil && now.Before(*row.CleanupNotBefore) {
			return nil
		}
		var checkpoint WorkspaceCheckpoint
		if err := tx.Omit("archive").Where("run_id = ?", runID).Take(&checkpoint).Error; err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if !sopWorkspaceIdleAt(run, checkpoint, now) {
			return nil
		}
		active, err := r.hasActiveSOPTree(tx, run)
		if err != nil || active {
			return err
		}
		released = true
		return releaseRunAdmission(tx, runID, "")
	}, r.admissionTxOptions()...)
	return released && err == nil, err
}

func releaseRunAdmission(tx *gorm.DB, runID, eventMessage string) error {
	if err := tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{
		"admission_state": "released", "revision": gorm.Expr("revision + 1"),
	}).Error; err != nil {
		return err
	}
	if eventMessage != "" {
		return appendAdmissionRunEvent(tx, runID, "workspace.reclaimed", eventMessage, time.Now().UTC())
	}
	return nil
}

func appendAdmissionRunEvent(tx *gorm.DB, runID, eventType, message string, now time.Time) error {
	var seq int64
	if err := tx.Model(&runEventRow{}).Select("COALESCE(MAX(seq), -1) + 1").Where("run_id = ?", runID).Scan(&seq).Error; err != nil {
		return err
	}
	ev := domain.RunEvent{ID: fmt.Sprintf("adm-%d-%d", now.UnixNano(), seq), RunID: runID, Type: eventType, Message: message, Timestamp: now}
	return tx.Create(&runEventRow{RunID: runID, Seq: seq, EventID: ev.ID, Type: ev.Type, Message: ev.Message, Payload: jsonBytes(ev), Ts: &now}).Error
}

func (r *RunsRepo) checkParentAdmission(tx *gorm.DB, run *domain.Run) error {
	parentID := strings.TrimSpace(run.ExecutionContext.ParentRunID)
	seen := map[string]bool{run.ID: true}
	for parentID != "" {
		if seen[parentID] {
			return fmt.Errorf("cyclic parent run")
		}
		seen[parentID] = true
		// Idle cleanup and slot release lock the ancestor row to fence child
		// activation against the final activity check.
		parent, err := r.takeRunForUpdate(tx, parentID)
		if err != nil {
			return fmt.Errorf("parent run unavailable: %w", err)
		}
		if parent.Status == "succeeded" || parent.Status == "queued" || parent.WorkspaceCleaned != nil || parent.AdmissionState == "unstarted" || parent.AdmissionState == "released" {
			return fmt.Errorf("请从顶层 SOP 重新运行，父流程尚未获得运行名额")
		}
		if parentID == run.ExecutionContext.ParentRunID && run.ExecutionContext.ExecutionPool == domain.ExecutionPoolProjectAuto {
			run.ConcurrencyExempt = parent.QuotaExempt != nil && *parent.QuotaExempt
		}
		parentID = strings.TrimSpace(runFromRow(parent).ExecutionContext.ParentRunID)
	}
	return nil
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
