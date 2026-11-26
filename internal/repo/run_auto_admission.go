package repo

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

type AutoAssignPlan struct {
	ExecutionContext domain.TelemetryContext
	ExecutorDefaults map[string]domain.AssistantDefaults
	EventMessage     string
}

// AutoAssignEvaluator checks execution compatibility without holding any
// admission locks. A nil runner denotes the platform bucket.
type AutoAssignEvaluator struct {
	EvaluateAutoRun       func(run domain.Run, targetBucket string, runner *domain.Runner) (AutoAssignPlan, bool)
	CheckAutoRunReachable func(run domain.Run, now time.Time) (failReason string)
	OnAutoRunFailed       func(runID string)
}

func validProjectAutoRun(run domain.Run) bool {
	ec := run.ExecutionContext
	return isTopLevelSOP(run) && ec.ProjectID != "" && ec.WorkspaceRunID == "" &&
		!ec.ExecutionPinned && ec.ExecutionPool == domain.ExecutionPoolProjectAuto
}

// TryAdmitRunOnCandidate reserves a slot and inserts a new, bound run atomically.
// A lost slot or a revoked grant leaves the caller free to try the next machine.
func (r *RunsRepo) TryAdmitRunOnCandidate(run domain.Run, key string) (bool, error) {
	runnerID, shared := strings.CutPrefix(key, runnerBucketPrefix)
	run.ConcurrencyExempt = shared
	if !validProjectAutoRun(run) || unboundProjectAutoRun(run) || key != runAdmissionBucketKey(run) {
		return false, fmt.Errorf("invalid project auto-assignment target %q", key)
	}
	admitted := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockAdmissionGate(r.deps, tx, key); err != nil {
			return err
		}
		var runner *domain.Runner
		message := "已自动分配至" + domain.PlatformRunnerName
		if shared {
			var err error
			if runner, err = r.lockOnlineRunner(tx, runnerID); err != nil || runner == nil {
				return err
			}
			message = "已自动分配至执行机器 " + cmp.Or(runner.Name, runner.ID)
		}
		allowed, err := r.runners.canAssignProjectRun(tx, run.ExecutionContext, runner)
		if err != nil || !allowed {
			return err
		}
		s := newAdmissionScope(tx)
		limit, err := s.limit(key)
		if err != nil {
			return err
		}
		if limit > 0 {
			queued, err := s.wouldQueue(key, "", limit)
			if err != nil || queued {
				return err
			}
		}
		run.Status, run.AdmissionState = "running", "held"
		run.UpdatedAt = time.Now().UTC()
		run.Revision++
		if err := r.persistRun(tx, run, runRow{}); err != nil {
			return err
		}
		if err := appendAdmissionRunEvent(tx, run.ID, "execution.bound", message, run.UpdatedAt); err != nil {
			return err
		}
		admitted = true
		return nil
	}, r.admissionTxOptions()...)
	return admitted && err == nil, err
}

// EnqueueProjectAutoRun writes the frozen SOP to its elastic project queue.
// Queued runs have no execution lease or reservation on any machine.
func (r *RunsRepo) EnqueueProjectAutoRun(run domain.Run) error {
	if !validProjectAutoRun(run) || !unboundProjectAutoRun(run) {
		return fmt.Errorf("invalid unbound project auto-assignment run")
	}
	now := time.Now().UTC()
	markRunQueued(&run, now)
	run.ConcurrencyExempt = false
	run.Generation = 0
	run.UpdatedAt = now
	run.Revision++
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := r.persistRun(tx, run, runRow{}); err != nil {
			return err
		}
		return appendAdmissionRunEvent(tx, run.ID, "run.queued", queuedMessage(runAdmissionBucketKey(run)), now)
	}, r.admissionTxOptions()...)
}

func (r *RunsRepo) lockOnlineRunner(tx *gorm.DB, id string) (*domain.Runner, error) {
	row, err := r.runners.lockRunner(tx, id)
	if errors.Is(err, ErrRunnerNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runner := runnerFromRow(row)
	if runner.DefaultSelectionBlocker() != "" {
		return nil, nil
	}
	return &runner, nil
}

func (r *RunsRepo) PromoteQueuedRuns() ([]string, error) {
	return r.PromoteQueuedRunsWithAuto(nil)
}

// PromoteQueuedRunsWithAuto merges each machine's fixed queue with the
// compatible elastic runs. No transaction locks more than one machine.
func (r *RunsRepo) PromoteQueuedRunsWithAuto(eval *AutoAssignEvaluator) ([]string, error) {
	var raw []*string
	if err := r.DB.Model(&runRow{}).Where("status = ?", "queued").Distinct("quota_bucket").Pluck("quota_bucket", &raw).Error; err != nil {
		return nil, err
	}
	targets := make(map[string][]string)
	var elastic []string
	for _, key := range raw {
		if key == nil || *key == "" {
			continue
		}
		if strings.HasPrefix(*key, projectAutoBucketPrefix) {
			elastic = append(elastic, *key)
			continue
		}
		targets[canonicalAdmissionBucketKey(*key)] = nil
	}
	available := map[string]domain.Runner{}
	if len(elastic) != 0 && eval != nil && eval.EvaluateAutoRun != nil {
		targets[PlatformAdmissionBucketKey] = elastic
		for _, runner := range r.runners.Runners() {
			if runner.OwnerUserID <= 0 || runner.Status != domain.RunnerStatusOnline {
				continue
			}
			key := runnerAdmissionBucketKey(runner.ID)
			for _, projectID := range runner.SharedProjectIDs {
				queue := projectAutoBucketPrefix + projectID
				if slices.Contains(elastic, queue) {
					targets[key] = append(targets[key], queue)
					available[key] = runner
				}
			}
		}
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var promoted []string
	var errs []error
	for _, key := range keys {
		var runner *domain.Runner
		if registered, ok := available[key]; ok {
			runner = &registered
		}
		plans, err := r.planAutoRuns(targets[key], key, runner, eval)
		if err != nil {
			errs = append(errs, fmt.Errorf("evaluate %s queue: %w", key, err))
			continue
		}
		ids, err := r.promoteBucket(key, plans)
		promoted = append(promoted, ids...)
		if err != nil {
			errs = append(errs, fmt.Errorf("promote %s: %w", key, err))
		}
	}
	if eval != nil && eval.CheckAutoRunReachable != nil {
		errs = append(errs, r.failUnreachableAutoRuns(elastic, eval))
	}
	return promoted, errors.Join(errs...)
}

const autoQueuePageSize = 20

// Walk a FIFO page at a time. An incompatible prefix may be waiting for a
// different machine, so keep looking until this target has enough matches.
func (r *RunsRepo) visitQueuedAutoRuns(keys []string, visit func(runRow) bool) error {
	if len(keys) == 0 {
		return nil
	}
	var last runRow
	for {
		var rows []runRow
		q := r.DB.Where("quota_bucket IN ? AND status = ?", keys, "queued")
		if last.ID != "" {
			q = q.Where("(queue_entered_at, created_at, id) > (?, ?, ?)", last.QueueEntered, last.Created, last.ID)
		}
		if err := q.Order("queue_entered_at, created_at, id").Limit(autoQueuePageSize).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if !visit(row) {
				return nil
			}
			last = row
		}
		if len(rows) < autoQueuePageSize {
			return nil
		}
	}
}

type queuedAutoPlan struct {
	run  domain.Run
	plan AutoAssignPlan
}

type queuedAutoPlans struct {
	byID map[string]queuedAutoPlan
	next *runRow
}

func (r *RunsRepo) planAutoRuns(keys []string, target string, runner *domain.Runner, eval *AutoAssignEvaluator) (queuedAutoPlans, error) {
	plans := queuedAutoPlans{byID: make(map[string]queuedAutoPlan)}
	err := r.visitQueuedAutoRuns(keys, func(row runRow) bool {
		run := runFromRow(row)
		if plan, compatible := eval.EvaluateAutoRun(run, target, runner); compatible {
			if len(plans.byID) == autoQueuePageSize {
				// A newer fixed run must wait for the first compatible run
				// beyond the evaluated batch, even if this machine is unlimited.
				plans.next = &row
				return false
			}
			plans.byID[run.ID] = queuedAutoPlan{run: run, plan: plan}
		}
		return true
	})
	return plans, err
}

func (r *RunsRepo) promoteBucket(key string, plans queuedAutoPlans) ([]string, error) {
	var promoted []string
	err := r.DB.Transaction(func(tx *gorm.DB) error {
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
			if occupied, err = s.occupied(key, ""); err != nil || occupied >= limit {
				return err
			}
		}
		var runner *domain.Runner
		if id, shared := strings.CutPrefix(key, runnerBucketPrefix); shared && len(plans.byID) > 0 {
			runner, err = r.lockOnlineRunner(tx, id)
			if err != nil {
				return err
			}
			if runner == nil {
				plans = queuedAutoPlans{}
			}
		}
		var planIDs []string
		for id := range plans.byID {
			planIDs = append(planIDs, id)
		}
		buckets, args := admissionBuckets([]string{key})
		if len(planIDs) > 0 {
			buckets = "(" + buckets + " OR (quota_bucket LIKE ? AND id IN ?))"
			args = append(args, projectAutoBucketPrefix+"%", planIDs)
		}
		var queued []runRow
		q := tx.Select("id", "quota_bucket", "revision").Where(buckets, args...).Where("status = ?", "queued")
		if next := plans.next; next != nil {
			q = q.Where("(queue_entered_at IS NULL OR queue_entered_at < ? OR (queue_entered_at = ? AND created_at IS NULL) OR (queue_entered_at, created_at, id) < (?, ?, ?))",
				next.QueueEntered, next.QueueEntered, next.QueueEntered, next.Created, next.ID)
		}
		if err := q.Order("queue_entered_at, created_at, id").Find(&queued).Error; err != nil {
			return err
		}
		for _, row := range queued {
			if limit > 0 && occupied >= limit {
				break
			}
			claimed, err := r.promoteQueuedRun(tx, row, key, runner, plans.byID[row.ID])
			if err != nil {
				return err
			}
			if claimed {
				occupied++
				promoted = append(promoted, row.ID)
			}
		}
		return nil
	}, r.admissionTxOptions()...)
	if err != nil {
		return nil, err
	}
	return promoted, nil
}

func (r *RunsRepo) promoteQueuedRun(tx *gorm.DB, row runRow, key string, runner *domain.Runner, candidate queuedAutoPlan) (bool, error) {
	now := time.Now().UTC()
	where := tx.Model(&runRow{}).Where("id = ? AND status = ? AND quota_bucket = ?", row.ID, "queued", *row.QuotaBucket)
	values := map[string]any{
		"status": "running", "admission_state": "held", "quota_bucket": key, "revision": gorm.Expr("revision + 1"),
		"updated_at": now, "cancel_requested": false, "pause_requested": false, "owner_instance_id": "", "lease_until": nil,
	}
	automatic := strings.HasPrefix(*row.QuotaBucket, projectAutoBucketPrefix)
	if automatic {
		if row.Revision != candidate.run.Revision || *row.QuotaBucket != runAdmissionBucketKey(candidate.run) {
			return false, nil
		}
		bound := candidate.run
		bound.ExecutionContext = candidate.plan.ExecutionContext
		bound.ConcurrencyExempt = key != PlatformAdmissionBucketKey
		if !validProjectAutoRun(bound) || unboundProjectAutoRun(bound) || runAdmissionBucketKey(bound) != key ||
			bound.ExecutionContext.ProjectID != candidate.run.ExecutionContext.ProjectID ||
			bound.ExecutionContext.ResponsibleUserID != candidate.run.ExecutionContext.ResponsibleUserID {
			return false, fmt.Errorf("invalid project auto-assignment plan for run %s", row.ID)
		}
		allowed, err := r.runners.canAssignProjectRun(tx, bound.ExecutionContext, runner)
		if err != nil || !allowed {
			return false, err
		}
		wf := candidate.run.WorkflowSnapshot
		wf.ExecutorDefaults = candidate.plan.ExecutorDefaults
		values["quota_exempt"] = key != PlatformAdmissionBucketKey
		values["execution_context"] = jsonBytes(candidate.plan.ExecutionContext)
		values["snapshot"] = jsonBytes(wf)
		where = where.Where("revision = ?", row.Revision)
	}
	result := where.Updates(values)
	if result.Error != nil || result.RowsAffected == 0 {
		return false, result.Error
	}
	if automatic {
		if err := appendAdmissionRunEvent(tx, row.ID, "execution.bound", candidate.plan.EventMessage, now); err != nil {
			return false, err
		}
	}
	return true, appendAdmissionRunEvent(tx, row.ID, "run.started", "获得运行名额，开始执行", now)
}

func (r *RunsRepo) failUnreachableAutoRuns(keys []string, eval *AutoAssignEvaluator) error {
	var errs []error
	now := time.Now().UTC()
	err := r.visitQueuedAutoRuns(keys, func(row runRow) bool {
		reason := eval.CheckAutoRunReachable(runFromRow(row), now)
		if reason == "" {
			return true
		}
		failed := false
		err := r.DB.Transaction(func(tx *gorm.DB) error {
			failed = false
			result := tx.Model(&runRow{}).Where("id = ? AND status = ? AND quota_bucket = ? AND revision = ?",
				row.ID, "queued", *row.QuotaBucket, row.Revision).Updates(map[string]any{
				"status": "failed", "admission_state": "unstarted", "revision": gorm.Expr("revision + 1"), "updated_at": now,
			})
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			failed = true
			return appendAdmissionRunEvent(tx, row.ID, domain.RunEventFailed, reason, now)
		}, r.admissionTxOptions()...)
		if err != nil {
			errs = append(errs, err)
		} else if failed && eval.OnAutoRunFailed != nil {
			eval.OnAutoRunFailed(row.ID)
		}
		return true
	})
	return errors.Join(append(errs, err)...)
}
