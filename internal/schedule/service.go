package schedule

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/cronexpr"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

// ErrAlreadyRunning is returned when a job is started while that same job is
// still executing on this instance. Different jobs run concurrently.
var ErrAlreadyRunning = errors.New("该定时任务正在运行")

var errNotDue = errors.New("schedule job is not due")

type Dispatcher func(domain.ScheduleJob, domain.Issue, string) error

// RunDispatcher is a Dispatcher that also reports the started run ID, so the
// execution record can link to the run.
type RunDispatcher func(domain.ScheduleJob, domain.Issue, string) (runID string, err error)

const interruptedReason = "上次运行中断（服务重启）"

// BaselineGuard inspects eligible issues before dispatch. blocked[repoID]
// carries the structured blocker (repository, conflict files, alignment run)
// that skips every issue bound to that repository.
type BaselineGuard func(job domain.ScheduleJob, issues []domain.Issue) (blocked map[string]domain.ScheduleJobBlocker, err error)

// AssistantPrep resolves a pinned shared version or runs an independent AI prompt.
// TAPD/Gongfeng pull stays on the project sync job.
type AssistantPrep func(job domain.ScheduleJob) (message string, err error)

// AssistantSessionPrep is an AssistantPrep that also reports the Studio
// conversation the prompt ran in (empty when none was opened) and what the
// prompt consumed. It calls opened as soon as the conversation exists,
// before the prompt finishes.
type AssistantSessionPrep func(job domain.ScheduleJob, opened func(sessionID string)) (AssistantRunResult, error)

// AssistantRunResult is the outcome of one AI prompt run. Usage sums the
// replies the run added (nil when it reached no model); Model is the model
// that produced them.
type AssistantRunResult struct {
	Message   string
	SessionID string
	Model     string
	Usage     *domain.TokenUsage
}

type Service struct {
	repo           *repo.Store
	sync           *tracker.Service
	dispatch       Dispatcher
	runDispatch    RunDispatcher
	guard          BaselineGuard
	assistant      AssistantPrep
	assistantRun   AssistantSessionPrep
	mu             sync.Mutex
	stopCh         chan struct{}
	stopped        bool
	startedAt      time.Time
	running        map[string]bool
	lastFinishedAt map[string]time.Time
	// dispatchMu serializes issue scanning and SOP dispatch across jobs so two
	// trigger jobs cannot both claim the same issue; AI prompts run outside it.
	dispatchMu sync.Mutex
}

func New(st *repo.Store, sy *tracker.Service, d Dispatcher) *Service {
	return &Service{repo: st, sync: sy, dispatch: d, running: map[string]bool{}, lastFinishedAt: map[string]time.Time{}}
}

func (s *Service) SetBaselineGuard(g BaselineGuard) {
	s.guard = g
}

func (s *Service) SetAssistantPrep(fn AssistantPrep) {
	s.assistant = fn
}

// SetAssistantSessionPrep replaces the AssistantPrep with one that reports
// the conversation, so executions link to it and fixed jobs stay pinned.
func (s *Service) SetAssistantSessionPrep(fn AssistantSessionPrep) {
	s.assistantRun = fn
}

// SetRunDispatcher replaces the Dispatcher with one that reports run IDs.
func (s *Service) SetRunDispatcher(d RunDispatcher) {
	s.runDispatch = d
}
func (s *Service) Start() {
	s.StartAfter(nil)
}

// StartAfter starts the ticker now but defers marking interrupted jobs as
// failed until recoveryGate closes (nil: immediately). A successor process
// uses it while its predecessor may still be executing a job.
func (s *Service) StartAfter(recoveryGate <-chan struct{}) {
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return
	}
	s.stopCh = make(chan struct{})
	s.startedAt = time.Now().UTC()
	stop := s.stopCh
	s.mu.Unlock()
	if recoveryGate == nil {
		s.recoverInterruptedJobs()
	} else {
		go func() {
			select {
			case <-recoveryGate:
				s.recoverInterruptedJobs()
			case <-stop:
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.runDueJobs()
			}
		}
	}()
}

// Stop ends the ticker; a job already executing runs to completion.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopCh == nil || s.stopped {
		return
	}
	s.stopped = true
	close(s.stopCh)
}

func (s *Service) recoverInterruptedJobs() {
	for _, job := range s.repo.ScheduleJobs() {
		if job.LastStatus != "running" {
			continue
		}
		job.LastStatus = "failed"
		job.LastError = interruptedReason
		log.Printf("schedule job %s trace=%s: %s", job.ID, job.LastTraceID, interruptedReason)
		s.saveResult(job)
	}
	// Executions started before this process cannot still be running here;
	// executions this process already started are left alone.
	s.mu.Lock()
	cutoff := s.startedAt
	s.mu.Unlock()
	if cutoff.IsZero() {
		cutoff = time.Now().UTC()
	}
	if err := s.repo.FailRunningScheduleJobExecutions(cutoff, interruptedReason); err != nil {
		log.Printf("schedule executions recover: %v", err)
	}
}
func (s *Service) runDueJobs() {
	s.runDueJobsAt(time.Now().UTC())
}

func (s *Service) runDueJobsAt(now time.Time) {
	for _, job := range s.repo.ScheduleJobs() {
		claimed, err := s.claimJob(job.ID, now)
		if errors.Is(err, ErrAlreadyRunning) || errors.Is(err, errNotDue) {
			continue
		}
		if err != nil {
			log.Printf("schedule job %s: %v", job.ID, err)
			continue
		}
		s.executeAsync(claimed, "timer")
	}
}

func jobDue(job domain.ScheduleJob, now time.Time) bool {
	if !job.Enabled {
		return false
	}
	if job.LastStatus == "running" {
		return false
	}
	if cron := strings.TrimSpace(job.Cron); cron != "" {
		if job.LastRunAt.IsZero() {
			return true
		}
		next, err := cronexpr.Next(cron, job.LastRunAt)
		return err == nil && !next.After(now)
	}
	interval := time.Duration(job.IntervalSec) * time.Second
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if job.LastRunAt.IsZero() {
		return true
	}
	return now.Sub(job.LastRunAt) >= interval
}
func (s *Service) RunJob(id string) (domain.ScheduleJob, error) {
	return s.runJob(id, "timer")
}
func (s *Service) RunNow(id string) (domain.ScheduleJob, error) {
	return s.runJob(id, "manual_run")
}

// StartNow claims the job, persists lastStatus=running, and continues in the
// background. Closing the HTTP client does not cancel the run. Poll
// ScheduleJobs for lastStatus leaving "running".
func (s *Service) StartNow(id string) (domain.ScheduleJob, error) {
	job, err := s.claim(id)
	if err != nil {
		return job, err
	}
	s.executeAsync(job, "manual_run")
	return job, nil
}

func (s *Service) executeAsync(job domain.ScheduleJob, triggerOrigin string) {
	go func() {
		defer s.release(job.ID)
		if _, execErr := s.execute(job, triggerOrigin); execErr != nil {
			log.Printf("schedule job %s trace=%s: %v", job.ID, job.LastTraceID, execErr)
		}
	}()
}

func (s *Service) runJob(id, triggerOrigin string) (domain.ScheduleJob, error) {
	job, err := s.claim(id)
	if err != nil {
		return job, err
	}
	defer s.release(job.ID)
	return s.execute(job, triggerOrigin)
}

func (s *Service) claim(id string) (domain.ScheduleJob, error) {
	return s.claimJob(id, time.Time{})
}

func (s *Service) claimJob(id string, dueAt time.Time) (domain.ScheduleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.repo.ScheduleJob(id)
	if !ok {
		return domain.ScheduleJob{}, fmt.Errorf("schedule job not found")
	}
	if s.running[id] || job.LastStatus == "running" {
		return job, ErrAlreadyRunning
	}
	if !dueAt.IsZero() {
		candidate := job
		// Cron occurrences while this job runs are dropped, not queued.
		// Use completion as the lower bound without changing LastRunAt,
		// which continues to describe the actual execution shown in the UI.
		if finished := s.lastFinishedAt[id]; strings.TrimSpace(job.Cron) != "" && finished.After(candidate.LastRunAt) {
			candidate.LastRunAt = finished
		}
		if !jobDue(candidate, dueAt) {
			return job, errNotDue
		}
	}
	s.running[id] = true
	job.LastRunAt = time.Now().UTC()
	job.LastStatus = "running"
	job.LastError = ""
	job.LastDispatched = 0
	job.LastPulled = 0
	job.LastAssistantMessage = ""
	job.NotificationResult = ""
	job.LastSessionID = ""
	job.LastTraceID = domain.NewTraceID()
	return s.saveResult(job), nil
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.lastFinishedAt[id] = time.Now().UTC()
	s.mu.Unlock()
}

// execute runs a claimed job and records it as one ScheduleJobExecution.
func (s *Service) execute(job domain.ScheduleJob, triggerOrigin string) (domain.ScheduleJob, error) {
	exec := s.beginExecution(job, triggerOrigin)
	return s.executeJob(job, triggerOrigin, exec)
}

func (s *Service) executeJob(job domain.ScheduleJob, triggerOrigin string, exec *domain.ScheduleJobExecution) (domain.ScheduleJob, error) {
	// finish records the outcome computed here: saveResult may return a newer
	// stored state when the job was edited or re-run meanwhile.
	finish := func(result domain.ScheduleJob, err error) (domain.ScheduleJob, error) {
		s.finishExecution(exec, result)
		return s.saveResult(result), err
	}
	if (s.assistant != nil || s.assistantRun != nil) && (strings.TrimSpace(job.AssistantPrompt) != "" || job.SharedTaskID != "") {
		run, prepErr := s.runAssistant(job, exec)
		msg, sessionID := run.Message, run.SessionID
		if sessionID == "" && exec != nil {
			sessionID = exec.SessionID
		}
		if exec != nil {
			exec.RecordUsage(run.Model, run.Usage)
		}
		job.LastSessionID = sessionID
		if sessionID != "" && job.UsesFixedSession() {
			job.FixedSessionID = sessionID
		}
		job.NotificationResult = domain.NotificationText(msg, 18000)
		_, plain, _ := domain.ParseNotificationResult(msg)
		job.LastAssistantMessage = domain.NotificationText(plain, 1200)
		if prepErr != nil {
			job.LastStatus = "failed"
			job.LastError = prepErr.Error()
			job.LastPulled = 0
			job.Blockers = nil
			return finish(job, prepErr)
		}
	}
	if !job.DispatchesSOP() {
		job.LastPulled = 0
		job.LastDispatched = 0
		job.LastStatus = "success"
		job.LastError = ""
		job.Blockers = nil
		return finish(job, nil)
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	matched := s.matchingIssues(job)
	job.LastPulled = len(matched)
	eligible := s.eligibleIssues(job, matched)
	blocked := map[string]domain.ScheduleJobBlocker{}
	if s.guard != nil && len(eligible) > 0 {
		var guardErr error
		blocked, guardErr = s.guard(job, eligible)
		if guardErr != nil {
			job.LastStatus = "failed"
			job.LastError = guardErr.Error()
			job.LastDispatched = 0
			job.Blockers = nil
			return finish(job, guardErr)
		}
	}
	dispatched := 0
	limit := job.DispatchLimit()
	for _, issue := range eligible {
		if dispatched >= limit {
			break
		}
		if issueBlocked(s.repo, issue, blocked) {
			continue
		}
		runID, err := s.dispatchIssue(job, issue, triggerOrigin)
		if err != nil {
			if strings.Contains(err.Error(), "已经跑过") || strings.Contains(err.Error(), "澄清未达标") {
				continue
			}
			job.LastStatus = "failed"
			job.LastError = err.Error()
			job.LastDispatched = dispatched
			job.Blockers = nil
			return finish(job, err)
		}
		if exec != nil {
			exec.Runs = append(exec.Runs, domain.ScheduleJobExecutionRun{IssueID: issue.ID, IssueTitle: issue.Title, RunID: runID})
		}
		dispatched++
	}
	job.LastDispatched = dispatched
	if len(blocked) > 0 {
		job.LastStatus = "waiting"
		job.LastError = joinBlockReasons(blocked)
		job.Blockers = mergeBlockers(job.Blockers, blocked, time.Now().UTC())
		return finish(job, nil)
	}
	job.LastStatus = "success"
	job.LastError = ""
	job.Blockers = nil
	return finish(job, nil)
}

func (s *Service) runAssistant(job domain.ScheduleJob, exec *domain.ScheduleJobExecution) (AssistantRunResult, error) {
	if s.assistantRun != nil {
		return s.assistantRun(job, func(sessionID string) { s.recordOpenedSession(job, exec, sessionID) })
	}
	msg, err := s.assistant(job)
	return AssistantRunResult{Message: msg}, err
}

// recordOpenedSession stores the conversation while the prompt still runs,
// so a run cut short (a restart, a crash) keeps a link to what it did.
func (s *Service) recordOpenedSession(job domain.ScheduleJob, exec *domain.ScheduleJobExecution, sessionID string) {
	if sessionID == "" {
		return
	}
	if exec != nil && exec.SessionID != sessionID {
		exec.SessionID = sessionID
		if err := s.repo.PutScheduleJobExecution(*exec); err != nil {
			log.Printf("schedule job %s trace=%s: record session: %v", job.ID, exec.TraceID, err)
		}
	}
	if err := s.repo.SaveScheduleJobRunSession(job.ID, job.LastRunAt, sessionID); err != nil {
		log.Printf("schedule job %s trace=%s: record session: %v", job.ID, job.LastTraceID, err)
	}
}

func (s *Service) dispatchIssue(job domain.ScheduleJob, issue domain.Issue, triggerOrigin string) (string, error) {
	if s.runDispatch != nil {
		return s.runDispatch(job, issue, triggerOrigin)
	}
	if s.dispatch == nil {
		return "", fmt.Errorf("schedule dispatcher is not configured")
	}
	return "", s.dispatch(job, issue, triggerOrigin)
}

// beginExecution records the claimed job as a running execution. The trace
// comes from the claim; a job executed without one gets a fresh trace.
func (s *Service) beginExecution(job domain.ScheduleJob, triggerOrigin string) *domain.ScheduleJobExecution {
	traceID := job.LastTraceID
	if traceID == "" {
		traceID = domain.NewTraceID()
	}
	started := job.LastRunAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	exec := &domain.ScheduleJobExecution{
		ID:            domain.ScheduleJobExecutionID(traceID),
		JobID:         job.ID,
		JobName:       job.Name,
		JobKind:       domain.ScheduleJobKind(job),
		ProjectID:     job.ProjectID,
		TraceID:       traceID,
		TriggerOrigin: triggerOrigin,
		Status:        "running",
		StartedAt:     started,
	}
	if err := s.repo.PutScheduleJobExecution(*exec); err != nil {
		log.Printf("schedule job %s trace=%s: record execution: %v", job.ID, traceID, err)
	}
	return exec
}

func (s *Service) finishExecution(exec *domain.ScheduleJobExecution, result domain.ScheduleJob) {
	if exec == nil {
		return
	}
	finished := time.Now().UTC()
	exec.Status = result.LastStatus
	exec.Error = result.LastError
	exec.Pulled = result.LastPulled
	exec.Dispatched = result.LastDispatched
	exec.AssistantMessage = result.LastAssistantMessage
	exec.SessionID = result.LastSessionID
	exec.FinishedAt = &finished
	if !exec.StartedAt.IsZero() {
		exec.DurationMs = finished.Sub(exec.StartedAt).Milliseconds()
	}
	if err := s.repo.PutScheduleJobExecution(*exec); err != nil {
		log.Printf("schedule job %s trace=%s: record execution: %v", exec.JobID, exec.TraceID, err)
	}
}

// eligibleIssues keeps the matched issues the job may dispatch: a workflow
// resolves for them, they have no task yet, and they are neither locked,
// closed, finished nor ignored by automation.
func (s *Service) eligibleIssues(job domain.ScheduleJob, matched []domain.Issue) []domain.Issue {
	var eligible []domain.Issue
	for _, issue := range matched {
		if job.ResolveWorkflow(issue) == "" {
			continue
		}
		if s.repo.IssueHasTask(issue.ID) || issue.DeliveryLocked() || issue.GongfengClosed() || issue.Status == "done" || issue.Status == "closed" || issue.Status == "cancelled" || issue.AutomationState == "ignored" {
			continue
		}
		eligible = append(eligible, issue)
	}
	return eligible
}

// issueBlocked reports whether the issue must wait: a multi-repository issue
// waits when any of its repositories has an integration blocker.
func issueBlocked(st *repo.Store, issue domain.Issue, blocked map[string]domain.ScheduleJobBlocker) bool {
	for _, repoID := range issueRepositoryIDs(st, issue) {
		if _, ok := blocked[repoID]; ok {
			return true
		}
	}
	return false
}

// issueRepositoryIDs lists every repository a scheduled issue touches:
// the bound repositories first, then the project default as fallback.
func issueRepositoryIDs(st *repo.Store, issue domain.Issue) []string {
	if ids := issue.EffectiveRepositoryIDs(); len(ids) > 0 {
		return ids
	}
	if p, ok := st.Project(issue.ProjectID); ok {
		if id := strings.TrimSpace(p.DefaultRepositoryID); id != "" {
			return []string{id}
		}
	}
	return nil
}

func joinBlockReasons(blocked map[string]domain.ScheduleJobBlocker) string {
	seen := map[string]bool{}
	var parts []string
	for _, blocker := range blocked {
		reason := strings.TrimSpace(blocker.Reason)
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		parts = append(parts, reason)
	}
	return strings.Join(parts, "；")
}

// mergeBlockers merges freshly reported blockers into the job's blocker list.
// Repositories that were already blocked keep their original Since timestamp
// so the UI can show how long automatic dispatch has been paused.
func mergeBlockers(prev []domain.ScheduleJobBlocker, blocked map[string]domain.ScheduleJobBlocker, now time.Time) []domain.ScheduleJobBlocker {
	since := map[string]time.Time{}
	for _, b := range prev {
		if b.RepositoryID != "" && !b.Since.IsZero() {
			since[b.RepositoryID] = b.Since
		}
	}
	out := make([]domain.ScheduleJobBlocker, 0, len(blocked))
	for repoID, b := range blocked {
		if b.RepositoryID == "" {
			b.RepositoryID = repoID
		}
		if t, ok := since[b.RepositoryID]; ok {
			b.Since = t
		} else {
			b.Since = now
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepositoryID < out[j].RepositoryID })
	return out
}
func (s *Service) matchingIssues(job domain.ScheduleJob) []domain.Issue {
	var out []domain.Issue
	for _, issue := range s.repo.Issues() {
		if job.ProjectID != "" && issue.ProjectID != job.ProjectID {
			continue
		}
		if !job.MatchesIssueStatus(issue.Status) {
			continue
		}
		if !job.MatchesIssueSource(issue.Source) {
			continue
		}
		out = append(out, issue)
	}
	return out
}
func (s *Service) saveResult(result domain.ScheduleJob) domain.ScheduleJob {
	var err error
	if result.LastStatus == "running" {
		err = s.repo.PutScheduleJob(result)
	} else {
		err = s.repo.SaveScheduleJobResult(result)
	}
	if err != nil {
		log.Printf("schedule job %s trace=%s: save %s result: %v", result.ID, result.LastTraceID, result.LastStatus, err)
	}
	stored, ok := s.repo.ScheduleJob(result.ID)
	if ok {
		return stored
	}
	return result
}
