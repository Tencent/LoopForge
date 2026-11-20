package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
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
	// Model and Usage cover this execution's generation, including retries
	// and usage reported before failure, but not prior turns in a fixed session.
	// Usage.CostUSD/CostStatus are the analytics estimate; CostCNY is set only
	// when that estimate is known. Unreported executions and SOP triggers have
	// no usage.
	Model   string      `json:"model,omitempty"`
	Usage   *TokenUsage `json:"usage,omitempty"`
	CostCNY *float64    `json:"costCny,omitempty"`
}

// RecordUsage stores the prompt's model and usage on the execution, priced
// like conversation turns in analytics. Empty usage is not recorded.
func (e *ScheduleJobExecution) RecordUsage(model string, usage *TokenUsage) {
	if usage == nil || usageTotalTokens(*usage) == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 && usage.ReasoningTokens == 0 && usage.CostUSD == 0 {
		return
	}
	priced := PriceScheduleExecutionUsage(model, *usage)
	e.Model, e.Usage, e.CostCNY = strings.TrimSpace(model), &priced, nil
	if priced.CostStatus == "estimated" {
		cny := priced.CostUSD * AnalyticsUSDToCNY
		e.CostCNY = &cny
	}
}

// PriceScheduleExecutionUsage resolves CLI routing before looking up a tariff.
// Codex input includes caches; Claude Code input excludes them, even when a
// custom endpoint names an OpenAI model. Unprefixed GPT usage stays inclusive.
func PriceScheduleExecutionUsage(model string, usage TokenUsage) TokenUsage {
	model = strings.ToLower(strings.TrimSpace(model))
	engine := ""
	if prefix, rest, ok := strings.Cut(model, ":"); ok && (prefix == "codex" || prefix == "claude-code" || prefix == "codebuddy" || IsAgentCLIKind(prefix)) {
		engine, model = prefix, strings.TrimSpace(rest)
	}
	inclusive := engine == "codex" || engine != "claude-code" && strings.HasPrefix(pricingModel(model), "gpt-")
	return AnalyticsPricedUsage(model, usage, inclusive)
}

// usageTotalTokens is the billed total, falling back to input + output.
func usageTotalTokens(u TokenUsage) int64 {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}

// ExecutionTotalTokens is the billed total of an execution's usage.
func (e ScheduleJobExecution) ExecutionTotalTokens() int64 {
	if e.Usage == nil {
		return 0
	}
	return usageTotalTokens(*e.Usage)
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

// Finished reports whether the execution has an outcome.
func (e ScheduleJobExecution) Finished() bool {
	return e.Status != "" && e.Status != "running"
}

// MatchesSearch reports whether a lower-case query occurs in what the
// execution dashboard lets users search: names, IDs, the error, the AI reply
// and the runs it started.
func (e ScheduleJobExecution) MatchesSearch(query string) bool {
	if query == "" {
		return true
	}
	fields := []string{e.JobName, e.JobID, e.TraceID, e.Error, e.AssistantMessage}
	for _, r := range e.Runs {
		fields = append(fields, r.IssueTitle, r.IssueID, r.RunID)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), query) {
			return true
		}
	}
	return false
}

// ScheduleExecutionWindow summarizes the executions started in one window.
// Total is Finished plus Running; AvgMs averages finished executions with a
// duration.
type ScheduleExecutionWindow struct {
	Total    int   `json:"total"`
	Finished int   `json:"finished"`
	Success  int   `json:"success"`
	Failed   int   `json:"failed"`
	Running  int   `json:"running"`
	AvgMs    int64 `json:"avgMs"`
	// Metered counts the finished executions that recorded token usage;
	// AvgTokens averages Tokens.Total over them, so runs that failed before
	// reaching a model do not pull the average down. Priced counts those
	// whose cost could be estimated; CostCNY sums their estimates.
	Metered   int                     `json:"metered"`
	Tokens    ScheduleExecutionTokens `json:"tokens"`
	AvgTokens int64                   `json:"avgTokens"`
	Priced    int                     `json:"priced"`
	CostCNY   float64                 `json:"costCny"`
}

// ScheduleExecutionTokens sums token usage. Total is the billed total; the
// four parts follow the providers' own split and need not add up to it.
type ScheduleExecutionTokens struct {
	Total      int64 `json:"total"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// ScheduleSessionFixed is ScheduleJob.SessionMode for an AI job that keeps
// running in one pinned conversation.
const ScheduleSessionFixed = "fixed"

// UsesFixedSession reports whether an AI job continues its pinned conversation.
func (j ScheduleJob) UsesFixedSession() bool {
	return j.SkipDispatch && j.SessionMode == ScheduleSessionFixed
}

const (
	// ScheduleSessionKeepDefault is the SessionKeep of a job that has none.
	ScheduleSessionKeepDefault = 50
	// ScheduleSessionKeepUnlimited keeps every conversation of a job.
	ScheduleSessionKeepUnlimited = -1
)

// ScheduleSessionKeepChoices are the limits an AI job may pick, besides
// ScheduleSessionKeepUnlimited.
var ScheduleSessionKeepChoices = []int{10, 50, 100}

// NormalizeScheduleSessionKeep validates a requested SessionKeep; zero picks
// the default.
func NormalizeScheduleSessionKeep(n int) (int, error) {
	if n == 0 {
		return ScheduleSessionKeepDefault, nil
	}
	if n == ScheduleSessionKeepUnlimited {
		return n, nil
	}
	for _, choice := range ScheduleSessionKeepChoices {
		if n == choice {
			return n, nil
		}
	}
	return 0, fmt.Errorf("保留会话数只能是 10、50、100 或不限")
}

// SessionKeepLimit is how many newest conversations the job keeps; 0 means no
// limit by count. A fixed-mode job has a single conversation and no limit.
func (j ScheduleJob) SessionKeepLimit() int {
	if !j.SkipDispatch || j.UsesFixedSession() || j.SessionKeep == ScheduleSessionKeepUnlimited {
		return 0
	}
	if j.SessionKeep <= 0 {
		return ScheduleSessionKeepDefault
	}
	return j.SessionKeep
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
