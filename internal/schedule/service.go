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

// ErrAlreadyRunning is returned when a second job is started while another
// schedule job is still executing on this instance.
var ErrAlreadyRunning = errors.New("已有定时任务正在运行")

var errNotDue = errors.New("schedule job is not due")

type Dispatcher func(domain.ScheduleJob, domain.Issue, string) error

// BaselineGuard inspects eligible issues before dispatch. blocked[repoID]
// carries the structured blocker (repository, conflict files, alignment run)
// that skips every issue bound to that repository.
type BaselineGuard func(job domain.ScheduleJob, issues []domain.Issue) (blocked map[string]domain.ScheduleJobBlocker, err error)

// AssistantPrep runs an AI job's user-written prompt. Trigger jobs leave
// AssistantPrompt empty; TAPD/Gongfeng pull stays on the project sync job.
type AssistantPrep func(job domain.ScheduleJob) (message string, err error)

type Service struct {
	repo           *repo.Store
	sync           *tracker.Service
	dispatch       Dispatcher
	guard          BaselineGuard
	assistant      AssistantPrep
	mu             sync.Mutex
	stopCh         chan struct{}
	runningJob     string
	lastFinishedAt time.Time
}

func New(st *repo.Store, sy *tracker.Service, d Dispatcher) *Service {
	return &Service{repo: st, sync: sy, dispatch: d}
}

func (s *Service) SetBaselineGuard(g BaselineGuard) {
	s.guard = g
}

func (s *Service) SetAssistantPrep(fn AssistantPrep) {
	s.assistant = fn
}
func (s *Service) Start() {
	s.mu.Lock()
	if s.stopCh != nil {
		s.mu.Unlock()
		return
	}
	s.stopCh = make(chan struct{})
	stop := s.stopCh
	s.mu.Unlock()
	s.recoverInterruptedJobs()
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

func (s *Service) recoverInterruptedJobs() {
	for _, job := range s.repo.ScheduleJobs() {
		if job.LastStatus != "running" {
			continue
		}
		job.LastStatus = "failed"
		job.LastError = "上次运行中断（服务重启）"
		s.saveResult(job)
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
		defer s.release()
		if _, execErr := s.execute(job, triggerOrigin); execErr != nil {
			log.Printf("schedule job %s: %v", job.ID, execErr)
		}
	}()
}

func (s *Service) runJob(id, triggerOrigin string) (domain.ScheduleJob, error) {
	job, err := s.claim(id)
	if err != nil {
		return job, err
	}
	defer s.release()
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
	if s.runningJob != "" {
		if current, ok := s.repo.ScheduleJob(s.runningJob); ok {
			return current, ErrAlreadyRunning
		}
		return job, ErrAlreadyRunning
	}
	if job.LastStatus == "running" {
		return job, ErrAlreadyRunning
	}
	if !dueAt.IsZero() {
		candidate := job
		// Cron occurrences during any running job are dropped, not queued.
		// Use completion as the lower bound without changing LastRunAt,
		// which continues to describe the actual execution shown in the UI.
		if strings.TrimSpace(job.Cron) != "" && s.lastFinishedAt.After(candidate.LastRunAt) {
			candidate.LastRunAt = s.lastFinishedAt
		}
		if !jobDue(candidate, dueAt) {
			return job, errNotDue
		}
	}
	s.runningJob = id
	job.LastRunAt = time.Now().UTC()
	job.LastStatus = "running"
	job.LastError = ""
	job.LastDispatched = 0
	job.LastPulled = 0
	job.LastAssistantMessage = ""
	job.NotificationResult = ""
	return s.saveResult(job), nil
}

func (s *Service) release() {
	s.mu.Lock()
	s.runningJob = ""
	s.lastFinishedAt = time.Now().UTC()
	s.mu.Unlock()
}

func (s *Service) execute(job domain.ScheduleJob, triggerOrigin string) (domain.ScheduleJob, error) {
	if s.assistant != nil && strings.TrimSpace(job.AssistantPrompt) != "" {
		msg, prepErr := s.assistant(job)
		job.NotificationResult = domain.NotificationText(msg, 18000)
		_, plain, _ := domain.ParseNotificationResult(msg)
		job.LastAssistantMessage = domain.NotificationText(plain, 1200)
		if prepErr != nil {
			job.LastStatus = "failed"
			job.LastError = prepErr.Error()
			job.LastPulled = 0
			job.Blockers = nil
			job = s.saveResult(job)
			return job, prepErr
		}
	}
	if !job.DispatchesSOP() {
		job.LastPulled = 0
		job.LastDispatched = 0
		job.LastStatus = "success"
		job.LastError = ""
		job.Blockers = nil
		job = s.saveResult(job)
		return job, nil
	}
	matched := s.matchingIssues(job)
	job.LastPulled = len(matched)
	var eligible []domain.Issue
	for _, issue := range matched {
		workflowID := job.ResolveWorkflow(issue)
		if workflowID == "" {
			continue
		}
		if s.repo.IssueHasTask(issue.ID) || issue.DeliveryLocked() || issue.GongfengClosed() || issue.Status == "done" || issue.Status == "closed" || issue.Status == "cancelled" || issue.AutomationState == "ignored" {
			continue
		}
		if project, ok := s.repo.Project(issue.ProjectID); ok && domain.RequirementSOPBlocked(issue, project.Clarification) {
			continue
		}
		eligible = append(eligible, issue)
	}
	blocked := map[string]domain.ScheduleJobBlocker{}
	if s.guard != nil && len(eligible) > 0 {
		var guardErr error
		blocked, guardErr = s.guard(job, eligible)
		if guardErr != nil {
			job.LastStatus = "failed"
			job.LastError = guardErr.Error()
			job.LastDispatched = 0
			job.Blockers = nil
			job = s.saveResult(job)
			return job, guardErr
		}
	}
	dispatched := 0
	for _, issue := range eligible {
		// A multi-repository issue must wait when any of its repositories has
		// an integration blocker.
		waiting := false
		for _, repoID := range issueRepositoryIDs(s.repo, issue) {
			if _, isBlocked := blocked[repoID]; isBlocked {
				waiting = true
				break
			}
		}
		if waiting {
			continue
		}
		if err := s.dispatch(job, issue, triggerOrigin); err != nil {
			if strings.Contains(err.Error(), "已经跑过") || strings.Contains(err.Error(), "澄清未达标") {
				continue
			}
			job.LastStatus = "failed"
			job.LastError = err.Error()
			job.LastDispatched = dispatched
			job.Blockers = nil
			job = s.saveResult(job)
			return job, err
		}
		dispatched++
	}
	job.LastDispatched = dispatched
	if len(blocked) > 0 {
		job.LastStatus = "waiting"
		job.LastError = joinBlockReasons(blocked)
		job.Blockers = mergeBlockers(job.Blockers, blocked, time.Now().UTC())
		job = s.saveResult(job)
		return job, nil
	}
	job.LastStatus = "success"
	job.LastError = ""
	job.Blockers = nil
	job = s.saveResult(job)
	return job, nil
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
	if result.LastStatus == "running" {
		_ = s.repo.PutScheduleJob(result)
	} else {
		_ = s.repo.SaveScheduleJobResult(result)
	}
	stored, ok := s.repo.ScheduleJob(result.ID)
	if ok {
		return stored
	}
	return result
}
