package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ScheduleJobExecution is one execution of a schedule job. ScheduleJob only
// keeps the latest outcome; executions keep the history shown on the
// execution dashboard. TraceID is generated when the job is claimed and is
// written to every scheduler log line of that execution, so a failed
// execution can be located in server logs by its trace.
type ScheduleJobExecution struct {
	ID               string `json:"id"`
	JobID            string `json:"jobId"`
	JobName          string `json:"jobName"`
	JobKind          string `json:"jobKind"` // assistant, trigger
	ProjectID        string `json:"projectId"`
	TraceID          string `json:"traceId"`
	TriggerOrigin    string `json:"triggerOrigin,omitempty"` // timer, manual_run
	Status           string `json:"status"`                  // running, success, failed, waiting
	Error            string `json:"error,omitempty"`
	Pulled           int    `json:"pulled,omitempty"`
	Dispatched       int    `json:"dispatched,omitempty"`
	AssistantMessage string `json:"assistantMessage,omitempty"`
	// SessionID is the Studio conversation the AI prompt ran in.
	SessionID  string                    `json:"sessionId,omitempty"`
	Runs       []ScheduleJobExecutionRun `json:"runs,omitempty"`
	StartedAt  time.Time                 `json:"startedAt"`
	FinishedAt *time.Time                `json:"finishedAt,omitempty"`
	DurationMs int64                     `json:"durationMs,omitempty"`
}

// ScheduleJobExecutionRun links an execution to the SOP run it started.
type ScheduleJobExecutionRun struct {
	IssueID    string `json:"issueId"`
	IssueTitle string `json:"issueTitle,omitempty"`
	RunID      string `json:"runId,omitempty"`
}

// ScheduleJobExecutionID derives the execution primary key from its trace.
func ScheduleJobExecutionID(traceID string) string {
	return "sjx-" + traceID
}

// ScheduleSessionFixed is ScheduleJob.SessionMode for an AI job that keeps
// running in one pinned conversation.
const ScheduleSessionFixed = "fixed"

// UsesFixedSession reports whether an AI job continues its pinned conversation.
func (j ScheduleJob) UsesFixedSession() bool {
	return j.SkipDispatch && j.SessionMode == ScheduleSessionFixed
}

// ScheduleJobKind labels a job for the execution dashboard.
func ScheduleJobKind(job ScheduleJob) string {
	if job.SkipDispatch {
		return "assistant"
	}
	return "trigger"
}

// NewTraceID returns a W3C trace-context compatible 32-hex trace ID.
func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000")))[:32]
	}
	return hex.EncodeToString(b[:])
}
