package domain

import (
	"fmt"
	"strings"
	"time"
)

// SyncConfig holds platform-level external sync settings. TAPD workspace/iteration
// belong on each Project; legacy sync_config TAPD fields are migrated on startup.
type SyncConfig struct {
	PlaybookSeedRev     int       `json:"playbookSeedRev,omitempty"`
	GongfengProjectID   string    `json:"gongfengProjectId"`
	GongfengMCPURL      string    `json:"gongfengMcpUrl"`
	TAPDWorkspaceID     string    `json:"tapdWorkspaceId"`
	TAPDIterationID     string    `json:"tapdIterationId"`
	TAPDMCPURL          string    `json:"tapdMcpUrl"`
	AutoSyncEnabled     bool      `json:"autoSyncEnabled"`
	AutoSyncIntervalSec int       `json:"autoSyncIntervalSec"`
	LastSyncAt          time.Time `json:"lastSyncAt"`
	LastSyncStatus      string    `json:"lastSyncStatus"`
	LastSyncError       string    `json:"lastSyncError,omitempty"`
}

const DefaultIntegrateBranch = "agentflow/integration"

// NormalizeIntegrateBranch trims the configured integrate branch. Empty means
// the repository does not use an integration baseline: scheduled SOPs skip
// default→integrate align and post-run merge into a shared branch.
func NormalizeIntegrateBranch(name string) string {
	return strings.TrimSpace(name)
}

// ConflictBlock is one conflicted hunk extracted from a failed
// default→integrate merge attempt. Ours carries the integrate-branch side,
// Theirs the default-branch side; both are clipped for UI/prompt use.
type ConflictBlock struct {
	File   string `json:"file"`
	Ours   string `json:"ours,omitempty"`
	Theirs string `json:"theirs,omitempty"`
}

// ScheduleJobBlocker 结构化记录定时任务的仓库级阻塞（基线冲突等）。
// 随 ScheduleJob.Blockers 一起以 payload JSON 落库，供定时任务列表警示卡与
// 「去处理基线冲突」按钮直达对齐 Run。
type ScheduleJobBlocker struct {
	RepositoryID      string          `json:"repositoryId"`
	RepositoryName    string          `json:"repositoryName,omitempty"`
	IntegrateBranch   string          `json:"integrateBranch,omitempty"`
	DefaultBranch     string          `json:"defaultBranch,omitempty"`
	ConflictFiles     []string        `json:"conflictFiles,omitempty"`
	ConflictBlocks    []ConflictBlock `json:"conflictBlocks,omitempty"`
	Reason            string          `json:"reason"`
	AlignRunID        string          `json:"alignRunId,omitempty"`
	ResponsibleUserID int64           `json:"responsibleUserId,omitempty"`
	Since             time.Time       `json:"since,omitempty"`
}

type ScheduleJob struct {
	Notifications        []NotificationBinding `json:"notifications,omitempty"`
	NotificationSnapshot []NotificationBinding `json:"notificationSnapshot,omitempty"`
	NotificationResult   string                `json:"notificationResult,omitempty"`
	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Enabled              bool                  `json:"enabled"`
	ProjectID            string                `json:"projectId"`
	// Source limits which issues a trigger job starts.
	// Empty means 工蜂 and TAPD (and platform-local issues). "gongfeng" or "tapd" starts only that source.
	Source           string `json:"source,omitempty"`
	TAPDWorkspaceID  string `json:"tapdWorkspaceId,omitempty"`
	TAPDIterationID  string `json:"tapdIterationId,omitempty"`
	WorkflowID       string `json:"workflowId,omitempty"`
	UseIssueWorkflow bool   `json:"useIssueWorkflow,omitempty"`
	SkipDispatch     bool   `json:"skipDispatch,omitempty"`
	// IssueStatuses limits trigger jobs to these local issue statuses.
	// Empty means backlog + todo. AI jobs leave this unused.
	IssueStatuses        []string             `json:"issueStatuses,omitempty"`
	IntervalSec          int                  `json:"intervalSec"`
	Cron                 string               `json:"cron,omitempty"`
	AssistantPrompt      string               `json:"assistantPrompt,omitempty"`
	AssistantModel       string               `json:"assistantModel,omitempty"`
	OwnerUserID          int64                `json:"ownerUserId,omitempty"`
	LastRunAt            time.Time            `json:"lastRunAt,omitempty"`
	LastStatus           string               `json:"lastStatus,omitempty"` // running, success, failed, waiting
	LastError            string               `json:"lastError,omitempty"`
	LastPulled           int                  `json:"lastPulled,omitempty"`
	LastDispatched       int                  `json:"lastDispatched,omitempty"`
	LastAssistantMessage string               `json:"lastAssistantMessage,omitempty"`
	Blockers             []ScheduleJobBlocker `json:"blockers,omitempty"`
	CreatedAt            time.Time            `json:"createdAt"`
	UpdatedAt            time.Time            `json:"updatedAt"`
}

// ResolveWorkflow picks the SOP for a scheduled dispatch: the issue's bound
// workflow when UseIssueWorkflow is set and present, otherwise the job default.
func (j ScheduleJob) ResolveWorkflow(issue Issue) string {
	if j.UseIssueWorkflow {
		if id := strings.TrimSpace(issue.WorkflowID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(j.WorkflowID)
}

// DispatchesSOP is true for trigger jobs that scan issues and start SOP runs.
// AI scheduled jobs set SkipDispatch and never dispatch.
func (j ScheduleJob) DispatchesSOP() bool {
	return !j.SkipDispatch && strings.TrimSpace(j.WorkflowID) != ""
}

// ScheduleIssueStatusOptions are the statuses a trigger job may scan.
var ScheduleIssueStatusOptions = []string{"backlog", "todo", "in_progress", "in_review", "blocked"}

// DefaultScheduleIssueStatuses is what new trigger jobs persist when the
// Studio form or API omits a list.
var DefaultScheduleIssueStatuses = []string{"backlog", "todo"}

// NormalizeScheduleIssueStatuses keeps known dispatchable statuses in option order.
func NormalizeScheduleIssueStatuses(values []string) []string {
	want := map[string]bool{}
	for _, raw := range values {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s != "" {
			want[s] = true
		}
	}
	out := make([]string, 0, len(ScheduleIssueStatusOptions))
	for _, s := range ScheduleIssueStatusOptions {
		if want[s] {
			out = append(out, s)
		}
	}
	return out
}

// TriggerIssueStatuses is the status set a trigger job will scan.
// An empty list is treated as every open status except done/cancelled, matching
// the historical implicit filter. New jobs persist an explicit list.
func (j ScheduleJob) TriggerIssueStatuses() []string {
	if j.SkipDispatch {
		return nil
	}
	if out := NormalizeScheduleIssueStatuses(j.IssueStatuses); len(out) > 0 {
		return out
	}
	return append([]string{}, ScheduleIssueStatusOptions...)
}

// NormalizeScheduleSource accepts the trigger-job source filter.
// Empty, "all", and "both" persist as empty and match every source.
func NormalizeScheduleSource(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all", "both":
		return "", nil
	case "github", "gongfeng", "tapd":
		return strings.ToLower(strings.TrimSpace(raw)), nil
	default:
		return "", fmt.Errorf("来源只能是工蜂、TAPD，或两边都同步")
	}
}

// MatchesIssueSource reports whether a trigger job should start this issue.
// An empty source matches 工蜂, TAPD, and platform-local issues.
func (j ScheduleJob) MatchesIssueSource(source string) bool {
	want := strings.ToLower(strings.TrimSpace(j.Source))
	if want == "" || want == "all" || want == "both" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(source)) == want
}

// MatchesIssueStatus reports whether a trigger job should consider this issue status.
func (j ScheduleJob) MatchesIssueStatus(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "backlog"
	}
	for _, want := range j.TriggerIssueStatuses() {
		if want == status {
			return true
		}
	}
	return false
}

// SyncLog records a single sync execution.
type SyncLog struct {
	ID        string `json:"id"`
	Direction string `json:"direction"` // pull, push
	Source    string `json:"source"`    // gongfeng, tapd
	Action    string `json:"action"`    // sync_issues, sync_stories, create_issue, create_story
	Status    string `json:"status"`    // success, failed
	Count     int    `json:"count"`
	// ProjectID scopes the log to the project whose repositories/pools were
	// synced (empty on legacy entries and cross-project aggregates).
	ProjectID  string    `json:"projectId,omitempty"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}
