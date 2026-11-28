package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Asset evolution: the platform observes runs, attributes problems to the
// role prompts and rules they used, proposes a candidate, validates it by
// replaying historical runs with the candidate injected, and releases it only
// after a human confirms. Every released content is an immutable version so
// any change can be rolled back.

// RunPurposeEvolutionReplay marks a run that validates an asset candidate.
// Such runs never deliver issues, send notifications or count in analytics.
const RunPurposeEvolutionReplay = "evolution_replay"

// Evolvable asset kinds. P1 covers role prompts and rule bodies only.
const (
	EvoAssetAgent = "agent"
	EvoAssetRule  = "rule"
)

// Autonomy levels.
const (
	EvoLevelOff     = 0 // no diagnosis for this asset
	EvoLevelReport  = 1 // report findings only
	EvoLevelPropose = 2 // propose + replay; humans start trials, release and roll back
	// EvoLevelAuto: a passed replay starts a trial by itself, a trial that is
	// not worse than the live body is released, and a release that turns out
	// worse is rolled back. Humans can still step in at any point.
	EvoLevelAuto = 3
)

const EvoDefaultLevel = EvoLevelPropose

// Finding status.
const (
	EvoFindingOpen      = "open"
	EvoFindingProposed  = "proposed"
	EvoFindingResolved  = "resolved"
	EvoFindingDismissed = "dismissed"
)

// Proposal status.
const (
	EvoProposalDrafting   = "drafting"
	EvoProposalValidating = "validating"
	EvoProposalPending    = "pending"
	// EvoProposalCanary: a share of new runs uses the candidate while the
	// rest keep the live body; the two arms are compared before release.
	EvoProposalCanary     = "canary"
	EvoProposalReleased   = "released"
	EvoProposalRejected   = "rejected"
	EvoProposalFailed     = "failed"
	EvoProposalRolledBack = "rolled_back"
)

// Replay sample groups. Only diagnosis samples are shown to the improver.
const (
	EvoGroupDiagnosis  = "diagnosis"
	EvoGroupHoldout    = "holdout"
	EvoGroupRegression = "regression"
	EvoGroupPinned     = "pinned"
)

// Replay outcomes compared with the source run.
const (
	EvoOutcomeFixed     = "fixed"
	EvoOutcomeRegressed = "regressed"
	EvoOutcomeSame      = "unchanged"
	EvoOutcomeBlocked   = "blocked"
	// EvoOutcomeChanged: a feedback sample whose improved node now reaches a
	// different, deliberate verdict (e.g. a reviewer REJECTs). Run status
	// cannot tell whether that matches the feedback; a human reads the output.
	EvoOutcomeChanged = "changed"
)

// Asset version sources.
const (
	EvoVersionBaseline  = "baseline"
	EvoVersionManual    = "manual"
	EvoVersionChatbot   = "chatbot"
	EvoVersionEvolution = "evolution"
	EvoVersionRollback  = "rollback"
)

// IsEvolutionReplay reports whether the run only validates an asset candidate.
func (c TelemetryContext) IsEvolutionReplay() bool {
	return c.Purpose == RunPurposeEvolutionReplay
}

// ContentHash is the stable fingerprint of an asset body.
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}

// EvoAssetRef names one evolvable asset.
type EvoAssetRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (r EvoAssetRef) Key() string { return r.Kind + ":" + r.ID }

func ValidEvoAssetKind(kind string) bool {
	return kind == EvoAssetAgent || kind == EvoAssetRule
}

// EvoPolicy is the autonomy level of one asset.
type EvoPolicy struct {
	AssetKind string    `json:"assetKind"`
	AssetID   string    `json:"assetId"`
	ProjectID string    `json:"projectId,omitempty"`
	Level     int       `json:"level"`
	UpdatedBy int64     `json:"updatedBy,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EvoSignal is one piece of evidence that an asset may be wrong.
type EvoSignal struct {
	ID        string    `json:"id,omitempty"`
	ProjectID string    `json:"projectId,omitempty"`
	RunID     string    `json:"runId"`
	NodeID    string    `json:"nodeId,omitempty"`
	Kind      string    `json:"kind"` // run_failed, node_failed, rework, eval_low, user_feedback
	Detail    string    `json:"detail,omitempty"`
	UserID    int64     `json:"userId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// EvoFinding groups signals attributed to one asset.
type EvoFinding struct {
	ID           string      `json:"id"`
	ProjectID    string      `json:"projectId"`
	AssetKind    string      `json:"assetKind"`
	AssetID      string      `json:"assetId"`
	AssetName    string      `json:"assetName,omitempty"`
	WorkflowIDs  []string    `json:"workflowIds,omitempty"`
	Title        string      `json:"title"`
	Summary      string      `json:"summary,omitempty"`
	Severity     string      `json:"severity"` // high, medium, low (mirrors Triage.Verdict)
	Signals      []EvoSignal `json:"signals,omitempty"`
	SampleRunIDs []string    `json:"sampleRunIds,omitempty"`
	Status       string      `json:"status"`
	ProposalID   string      `json:"proposalId,omitempty"`
	// Triage is the Bayesian "worth fixing" judgement of the asset.
	Triage *EvoTriage `json:"triage,omitempty"`
	// Clusters group the signals by kind and normalised detail.
	Clusters []EvoSignalCluster `json:"clusters,omitempty"`
	// Trend is the daily problem-run count over the window, oldest first.
	Trend        []float64 `json:"trend,omitempty"`
	FirstSeenAt  time.Time `json:"firstSeenAt,omitempty"`
	LastSeenAt   time.Time `json:"lastSeenAt,omitempty"`
	SnoozedUntil time.Time `json:"snoozedUntil,omitempty"`
	// CloseNote says why the finding left the queue (dismiss reason or
	// automatic archive).
	CloseNote string    `json:"closeNote,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Triage verdicts.
const (
	EvoVerdictFix   = "fix"   // very probably worse than the project baseline and costly
	EvoVerdictWatch = "watch" // evidence not yet conclusive, or impact small
	EvoVerdictNoise = "noise" // close to the baseline; likely incidental
)

// EvoTriage is the Beta-Binomial posterior of an asset's problem rate on its
// current version, compared with the project baseline (empirical Bayes).
type EvoTriage struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	// Runs are window runs that used the asset's current version; StaleRuns
	// used an older body and are left out.
	Runs      int `json:"runs"`
	StaleRuns int `json:"staleRuns,omitempty"`
	// ProblemRuns counts runs with a problem (rework weighs half).
	ProblemRuns float64 `json:"problemRuns"`
	// Kinds counts problem runs per signal kind.
	Kinds        map[string]int `json:"kinds,omitempty"`
	BaselineRate float64        `json:"baselineRate"`
	Rate         float64        `json:"rate"` // posterior mean
	RateLow      float64        `json:"rateLow"`
	RateHigh     float64        `json:"rateHigh"` // 90% credible interval
	// ProbWorse is P(rate > baseline) under the posterior.
	ProbWorse float64 `json:"probWorse"`
	// WeeklyExcess is the expected extra problem runs per week over the
	// baseline: what fixing the asset would save.
	WeeklyRuns   float64 `json:"weeklyRuns"`
	WeeklyExcess float64 `json:"weeklyExcess"`
	// RunsToDecide estimates how many more runs (at the current rate) it
	// takes for ProbWorse to reach the fix threshold; 0 when it never will.
	RunsToDecide int     `json:"runsToDecide,omitempty"`
	Score        float64 `json:"score"` // ProbWorse * WeeklyExcess, for ranking
}

// EvoSignalCluster is a group of similar signals of one asset.
type EvoSignalCluster struct {
	ID          string                 `json:"id,omitempty"`
	Kind        string                 `json:"kind"`
	NodeID      string                 `json:"nodeId,omitempty"`
	Label       string                 `json:"label"`
	Count       int                    `json:"count"`
	Runs        int                    `json:"runs"`
	RunID       string                 `json:"runId,omitempty"` // latest example
	FirstSeenAt time.Time              `json:"firstSeenAt,omitempty"`
	LastSeenAt  time.Time              `json:"lastSeenAt,omitempty"`
	Trend       []float64              `json:"trend,omitempty"`
	Occurrences []EvoClusterOccurrence `json:"occurrences,omitempty"`
}

// EvoClusterOccurrence preserves one example per run before Signals is capped.
type EvoClusterOccurrence struct {
	RunID     string    `json:"runId"`
	Detail    string    `json:"detail"`
	Count     int       `json:"count"`
	CreatedAt time.Time `json:"createdAt"`
}

// EvoReplay is one historical run replayed with a candidate.
type EvoReplay struct {
	ID           string           `json:"id"`
	ProposalID   string           `json:"proposalId"`
	ProjectID    string           `json:"projectId,omitempty"`
	Group        string           `json:"group"`
	SourceRunID  string           `json:"sourceRunId"`
	SourceStatus string           `json:"sourceStatus,omitempty"`
	SourceScore  int              `json:"sourceScore"`
	RunID        string           `json:"runId,omitempty"`
	Status       string           `json:"status"` // pending, running, waiting, succeeded, failed, cancelled, error
	Score        int              `json:"score"`
	Outcome      string           `json:"outcome,omitempty"`
	Error        string           `json:"error,omitempty"`
	Overrides    EvoAssetOverride `json:"overrides"`
	// ReusedNodes are source-run nodes the replay took over instead of
	// re-executing; empty means the whole SOP was replayed.
	ReusedNodes        []string               `json:"reusedNodes,omitempty"`
	SourceMeasurements *EvoReplayMeasurements `json:"sourceMeasurements,omitempty"`
	ReplayMeasurements *EvoReplayMeasurements `json:"replayMeasurements,omitempty"`
	StartedAt          time.Time              `json:"startedAt,omitempty"`
	FinishedAt         time.Time              `json:"finishedAt,omitempty"`
	WaitingSince       time.Time              `json:"waitingSince,omitempty"`
}

// EvoReplayMeasurements compare only the executed scope of each side.
type EvoReplayMeasurements struct {
	Tokens      *int64 `json:"tokens,omitempty"`
	ToolCalls   *int   `json:"toolCalls,omitempty"`
	ExecutionMs *int64 `json:"executionMs,omitempty"`
}

// EvoAssetOverride replaces asset bodies inside one replay run only.
type EvoAssetOverride struct {
	AgentPrompts map[string]string `json:"agentPrompts,omitempty"`
	RuleContents map[string]string `json:"ruleContents,omitempty"`
}

func (o EvoAssetOverride) Empty() bool {
	return len(o.AgentPrompts) == 0 && len(o.RuleContents) == 0
}

// EvoReplaySummary is the verdict over all replays of one proposal.
type EvoReplaySummary struct {
	Total     int    `json:"total"`
	Finished  int    `json:"finished"`
	Fixed     int    `json:"fixed"`
	Regressed int    `json:"regressed"`
	Unchanged int    `json:"unchanged"`
	Changed   int    `json:"changed"`
	Blocked   int    `json:"blocked"`
	Passed    bool   `json:"passed"`
	Verdict   string `json:"verdict,omitempty"`
}

// EvoChangeReview explains a proposal without replacing its original diff or
// replay evidence. Before/After are excerpts checked against the two bodies.
type EvoChangeReview struct {
	Problem string           `json:"problem"`
	Reason  string           `json:"reason"`
	Changes []EvoChangePoint `json:"changes,omitempty"`
}

type EvoChangePoint struct {
	Title        string   `json:"title"`
	Reason       string   `json:"reason,omitempty"`
	Before       string   `json:"before"`
	After        string   `json:"after"`
	SourceRunIDs []string `json:"sourceRunIds,omitempty"`
}

// EvoProposal is a candidate change to one asset.
type EvoProposal struct {
	ID               string           `json:"id"`
	ProjectID        string           `json:"projectId"`
	FindingID        string           `json:"findingId,omitempty"`
	AssetKind        string           `json:"assetKind"`
	AssetID          string           `json:"assetId"`
	AssetName        string           `json:"assetName,omitempty"`
	Title            string           `json:"title"`
	Rationale        string           `json:"rationale,omitempty"`
	Review           *EvoChangeReview `json:"review,omitempty"`
	BaseContent      string           `json:"baseContent"`
	BaseHash         string           `json:"baseHash"`
	BaseVersion      int              `json:"baseVersion,omitempty"`
	BaseCatalogHash  string           `json:"baseCatalogHash,omitempty"`
	Revision         int64            `json:"revision"`
	CandidateContent string           `json:"candidateContent,omitempty"`
	Status           string           `json:"status"`
	Model            string           `json:"model,omitempty"`
	Replays          []EvoReplay      `json:"replays,omitempty"`
	ExpectedReplays  int              `json:"expectedReplays,omitempty"`
	Summary          EvoReplaySummary `json:"summary"`
	Error            string           `json:"error,omitempty"`
	CreatedBy        int64            `json:"createdBy,omitempty"`
	DecidedBy        int64            `json:"decidedBy,omitempty"`
	DecisionNote     string           `json:"decisionNote,omitempty"`
	ReleasedVersion  int              `json:"releasedVersion,omitempty"`
	Canary           *EvoCanary       `json:"canary,omitempty"`
	Watch            *EvoWatch        `json:"watch,omitempty"`
	CreatedAt        time.Time        `json:"createdAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
	DecidedAt        time.Time        `json:"decidedAt,omitempty"`
}

func (p EvoProposal) Asset() EvoAssetRef { return EvoAssetRef{Kind: p.AssetKind, ID: p.AssetID} }

func (p EvoProposal) OccupiesAsset() bool {
	return p.Open() || (p.Status == EvoProposalReleased && p.Watch.Active())
}

// EvoQueueItem records a later improvement intent, never a generated candidate.
type EvoQueueItem struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"projectId"`
	FindingID  string    `json:"findingId"`
	AssetKind  string    `json:"assetKind"`
	AssetID    string    `json:"assetId"`
	AssetName  string    `json:"assetName,omitempty"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Model      string    `json:"model,omitempty"`
	ProposalID string    `json:"proposalId,omitempty"`
	CreatedBy  int64     `json:"createdBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// EvoOpenProposalStatuses hold the asset: one open proposal per asset.
var EvoOpenProposalStatuses = []string{EvoProposalDrafting, EvoProposalValidating, EvoProposalPending, EvoProposalCanary}

// Open reports whether the proposal still waits for validation, a trial or
// a decision.
func (p EvoProposal) Open() bool {
	for _, status := range EvoOpenProposalStatuses {
		if p.Status == status {
			return true
		}
	}
	return false
}

// TrialActive reports whether new runs are still split onto the candidate.
func (p EvoProposal) TrialActive() bool {
	return p.Status == EvoProposalCanary && p.Canary != nil && (p.Canary.Status == EvoCanaryRunning || p.Canary.Status == EvoCanaryPassed)
}

// EvoArmStats tallies how the nodes an asset shapes ended in a set of runs:
// clean (succeeded without rework), reworked (succeeded after being sent
// back) or failed.
type EvoArmStats struct {
	Runs     int     `json:"runs"`
	Clean    int     `json:"clean"`
	Reworked int     `json:"reworked"`
	Failed   int     `json:"failed"`
	Rate     float64 `json:"rate"` // clean / runs
}

// Trial (canary) status.
const (
	EvoCanaryRunning  = "running"  // collecting runs
	EvoCanaryPassed   = "passed"   // not worse than the live body; awaiting release (L2)
	EvoCanaryFailed   = "failed"   // worse than the live body; stopped
	EvoCanaryAborted  = "aborted"  // stopped by a human or an external edit
	EvoCanaryPromoted = "promoted" // released to every run
)

// EvoCanary is a small-traffic trial of a candidate on new business runs.
// Percent of new runs that use the asset take the candidate (canary arm);
// the rest keep the live body (control arm).
type EvoCanary struct {
	Percent   int         `json:"percent"`
	MinRuns   int         `json:"minRuns"`
	Status    string      `json:"status"`
	Auto      bool        `json:"auto,omitempty"`
	StartedBy int64       `json:"startedBy,omitempty"`
	StartedAt time.Time   `json:"startedAt"`
	CheckedAt time.Time   `json:"checkedAt,omitempty"`
	EndedAt   time.Time   `json:"endedAt,omitempty"`
	Canary    EvoArmStats `json:"canary"`
	Control   EvoArmStats `json:"control"`
	// ProbWorse is the posterior probability that the candidate's clean
	// rate is below the live body's (Beta-Binomial, uniform prior).
	ProbWorse float64 `json:"probWorse"`
	Verdict   string  `json:"verdict,omitempty"`
}

// Post-release watch status.
const (
	EvoWatchWatching   = "watching"
	EvoWatchHealthy    = "healthy"
	EvoWatchDegraded   = "degraded"
	EvoWatchRolledBack = "rolled_back"
	EvoWatchEnded      = "ended"
)

// EvoWatch follows a released version on real runs and compares it with
// the version it replaced.
type EvoWatch struct {
	Version       int         `json:"version"`
	Hash          string      `json:"hash"`
	PrevVersion   int         `json:"prevVersion"`
	PrevHash      string      `json:"prevHash"`
	MinRuns       int         `json:"minRuns"`
	Status        string      `json:"status"`
	StartedAt     time.Time   `json:"startedAt"`
	CheckedAt     time.Time   `json:"checkedAt,omitempty"`
	EndedAt       time.Time   `json:"endedAt,omitempty"`
	BaselineReady bool        `json:"baselineReady,omitempty"`
	Baseline      EvoArmStats `json:"baseline"`
	Current       EvoArmStats `json:"current"`
	ProbWorse     float64     `json:"probWorse"`
	Verdict       string      `json:"verdict,omitempty"`
}

// Active reports whether the watch still follows new runs.
func (w *EvoWatch) Active() bool {
	return w != nil && (w.Status == EvoWatchWatching || w.Status == EvoWatchDegraded)
}

// AssetVersion is one immutable body of an evolvable asset.
type AssetVersion struct {
	AssetKind  string    `json:"assetKind"`
	AssetID    string    `json:"assetId"`
	Version    int       `json:"version"`
	Content    string    `json:"content"`
	Hash       string    `json:"hash"`
	Source     string    `json:"source"`
	ProposalID string    `json:"proposalId,omitempty"`
	Note       string    `json:"note,omitempty"`
	CreatedBy  int64     `json:"createdBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	Current    bool      `json:"current,omitempty"`
}

// RunAssetPin records the asset body a run started with.
type RunAssetPin struct {
	RunID     string    `json:"runId"`
	AssetKind string    `json:"assetKind"`
	AssetID   string    `json:"assetId"`
	Version   int       `json:"version,omitempty"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
}

// EvoSample is a run a human pinned as a must-keep-passing regression case.
type EvoSample struct {
	ProjectID  string    `json:"projectId"`
	RunID      string    `json:"runId"`
	WorkflowID string    `json:"workflowId,omitempty"`
	Note       string    `json:"note,omitempty"`
	CreatedBy  int64     `json:"createdBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// EvoAssetHealth summarises recent runs that used one asset.
type EvoAssetHealth struct {
	AssetKind      string  `json:"assetKind"`
	AssetID        string  `json:"assetId"`
	AssetName      string  `json:"assetName"`
	Runs           int     `json:"runs"`
	Succeeded      int     `json:"succeeded"`
	Failed         int     `json:"failed"`
	Reworked       int     `json:"reworked"`
	SuccessRate    float64 `json:"successRate"`
	Level          int     `json:"level"`
	LatestVersion  int     `json:"latestVersion,omitempty"`
	OpenFindings   int     `json:"openFindings,omitempty"`
	ReadOnlyReason string  `json:"readOnlyReason,omitempty"`
}

type EvoSOPDetectionSetting struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
}

// SOP evolution stages shown on the SOP-centric RSI workbench.
const (
	EvoSOPStageMonitor  = "monitor"
	EvoSOPStageOptimize = "optimize"
	EvoSOPStageGray     = "gray"
)

// SOP optimization preference keys.
const (
	EvoPrefTokens   = "tokens"
	EvoPrefTools    = "tools"
	EvoPrefDuration = "duration"
	EvoPrefClean    = "clean"
)

// EvoSOPPolicy configures per-SOP evolution switch, multi-select preferences,
// and batch trigger size (default 10 runs, adjustable per SOP).
type EvoSOPPolicy struct {
	WorkflowID  string    `json:"workflowId"`
	ProjectID   string    `json:"projectId"`
	Enabled     bool      `json:"enabled"`
	Preferences []string  `json:"preferences"`
	BatchSize   int       `json:"batchSize"`
	Model       string    `json:"model,omitempty"`
	UpdatedBy   int64     `json:"updatedBy,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

func DefaultEvoSOPPolicy(projectID, workflowID string) EvoSOPPolicy {
	return EvoSOPPolicy{
		WorkflowID:  strings.TrimSpace(workflowID),
		ProjectID:   strings.TrimSpace(projectID),
		Enabled:     false,
		Preferences: []string{EvoPrefTokens, EvoPrefTools},
		BatchSize:   10,
	}
}

// EvoSOPMetrics summarizes core execution metrics and computed SOP score.
type EvoSOPMetrics struct {
	Runs        int     `json:"runs"`
	TotalTokens int64   `json:"totalTokens"`
	AvgTokens   int64   `json:"avgTokens"`
	TotalTools  int     `json:"totalTools"`
	AvgTools    float64 `json:"avgTools"`
	AvgDuration float64 `json:"avgDuration"` // seconds
	CleanCount  int     `json:"cleanCount"`
	CleanRate   float64 `json:"cleanRate"`
	Score       *int    `json:"score,omitempty"`
	ScoredRuns  int     `json:"scoredRuns"`
	ScoreSource string  `json:"scoreSource,omitempty"` // "eval" | "heuristic"
}

// EvoSOPVersionPoint is one historical SOP version row for the trend chart/table.
type EvoSOPVersionPoint struct {
	Version         string        `json:"version"`
	Current         bool          `json:"current,omitempty"`
	Source          string        `json:"source,omitempty"` // "" | "evolution" | "chatbot" | "manual" | "rollback"
	ChangeNote      string        `json:"changeNote,omitempty"`
	AssetKind       string        `json:"assetKind,omitempty"`
	AssetID         string        `json:"assetId,omitempty"`
	AssetName       string        `json:"assetName,omitempty"`
	RollbackVersion int           `json:"rollbackVersion,omitempty"`
	Metrics         EvoSOPMetrics `json:"metrics"`
}

// EvoSOPAssetRef summarizes one evolvable role or rule attached to an SOP so
// users can inspect its version timeline and roll back to any historical vN.
type EvoSOPAssetRef struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	CurrentVersion int    `json:"currentVersion,omitempty"`
	VersionCount   int    `json:"versionCount,omitempty"`
	ReadOnly       bool   `json:"readOnly,omitempty"`
}

// EvoSOPGrayRunEvent is one recent run inside a grayscale trial.
type EvoSOPGrayRunEvent struct {
	RunID    string  `json:"runId"`
	Group    string  `json:"group"` // control | candidate
	Tokens   int64   `json:"tokens"`
	Tools    int     `json:"tools"`
	Duration float64 `json:"duration"`
	Clean    bool    `json:"clean"`
	Score    *int    `json:"score,omitempty"`
}

// EvoSOPGrayRound summarizes one grayscale trial round (control vs candidate).
type EvoSOPGrayRound struct {
	ProposalID  string               `json:"proposalId"`
	BaseVersion string               `json:"baseVersion"`
	Target      string               `json:"target"`
	Status      string               `json:"status"` // running, passed, failed, published
	Published   bool                 `json:"published,omitempty"`
	Percent     int                  `json:"percent,omitempty"`
	MinRuns     int                  `json:"minRuns,omitempty"`
	Control     EvoSOPMetrics        `json:"control"`
	Candidate   EvoSOPMetrics        `json:"candidate"`
	RecentRuns  []EvoSOPGrayRunEvent `json:"recentRuns,omitempty"`
}

// EvoSOPChangeFile is one modified asset/file diff shown in the "本次修改" tab.
type EvoSOPChangeFile struct {
	Path      string      `json:"path"`
	AssetKind string      `json:"assetKind,omitempty"`
	AssetID   string      `json:"assetId,omitempty"`
	AssetName string      `json:"assetName,omitempty"`
	Lines     [][2]string `json:"lines"` // [kind("ctx"|"del"|"add"), text]
}

// EvoSOPProblemItem describes one problem discovered in the batch for the SOP.
type EvoSOPProblemItem struct {
	Key    string `json:"key,omitempty"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	Hits   int    `json:"hits"`
	Runs   int    `json:"runs"`
}

// EvoSOPSummary is one row on the SOP-centric RSI home table.
type EvoSOPSummary struct {
	WorkflowID       string        `json:"workflowId"`
	WorkflowName     string        `json:"workflowName"`
	Description      string        `json:"description,omitempty"`
	CurrentVersion   string        `json:"currentVersion"`
	Enabled          bool          `json:"enabled"`
	Preferences      []string      `json:"preferences"`
	BatchSize        int           `json:"batchSize"`
	Model            string        `json:"model,omitempty"`
	Stage            string        `json:"stage"` // monitor | optimize | gray
	PendingRuns      int           `json:"pendingRuns"`
	LastRunNote      string        `json:"lastRunNote,omitempty"`
	LatestMetrics    EvoSOPMetrics `json:"latestMetrics"`
	ActiveProposalID string        `json:"activeProposalId,omitempty"`
	OpenFindings     int           `json:"openFindings,omitempty"`
}

// EvoSOPExternalEdit records an edit to an SOP's role/rule made outside RSI
// (via Chatbot or manual asset editor) so it stays visible in RSI history & diffs.
type EvoSOPExternalEdit struct {
	ID        string           `json:"id"`
	AssetKind string           `json:"assetKind"`
	AssetID   string           `json:"assetId"`
	AssetName string           `json:"assetName"`
	Version   int              `json:"version"`
	Source    string           `json:"source"` // "chatbot" | "manual"
	Note      string           `json:"note,omitempty"`
	CreatedAt time.Time        `json:"createdAt"`
	Diff      EvoSOPChangeFile `json:"diff"`
}

// EvoSOPDetail is the full SOP detail payload for #/evolution/sops/:workflowId.
type EvoSOPDetail struct {
	EvoSOPSummary
	Versions       []EvoSOPVersionPoint `json:"versions"`
	GrayRounds     []EvoSOPGrayRound    `json:"grayRounds"`
	ActiveProposal *EvoProposal         `json:"activeProposal,omitempty"`
	LastProposal   *EvoProposal         `json:"lastProposal,omitempty"`
	BatchMetrics   *EvoSOPMetrics       `json:"batchMetrics,omitempty"`
	Problems       []EvoSOPProblemItem  `json:"problems,omitempty"`
	ChangeFiles    []EvoSOPChangeFile   `json:"changeFiles,omitempty"`
	RecentEdits    []EvoSOPExternalEdit `json:"recentEdits,omitempty"`
	Assets         []EvoSOPAssetRef     `json:"assets,omitempty"`
}

// EvoOverview is the 进化中心 dashboard payload.
type EvoOverview struct {
	SOPDetection EvoSOPDetectionSetting `json:"sopDetection"`
	ProjectID    string                 `json:"projectId"`
	Findings     []EvoFinding           `json:"findings"`
	Validating   []EvoProposal          `json:"validating"`
	Pending      []EvoProposal          `json:"pending"`
	Queue        []EvoQueueItem         `json:"queue"`
	// Trial lists proposals in a canary trial and released ones whose
	// post-release watch is still running or flagged a degradation.
	Trial       []EvoProposal    `json:"trial"`
	Recent      []EvoProposal    `json:"recent"`
	Health      []EvoAssetHealth `json:"health"`
	Policies    []EvoPolicy      `json:"policies"`
	SOPs        []EvoSOPSummary  `json:"sops"`
	WindowDays  int              `json:"windowDays"`
	GeneratedAt time.Time        `json:"generatedAt"`
	AutoEnabled bool             `json:"autoEnabled"`
}

// NormalizeEvoLevel clamps unknown levels into the supported range.
func NormalizeEvoLevel(level int) int {
	if level < EvoLevelOff {
		return EvoLevelOff
	}
	if level > EvoLevelAuto {
		return EvoLevelAuto
	}
	return level
}

// EvoAssetContent returns the evolvable body of an asset.
func EvoAgentContent(agent AgentDefinition) string { return agent.Prompt }
func EvoRuleContent(rule RuleDefinition) string    { return rule.Content }

// TrimEvoText bounds model-facing evidence.
func TrimEvoText(value string, n int) string {
	value = strings.TrimSpace(value)
	r := []rune(value)
	if len(r) <= n {
		return value
	}
	return string(r[:n]) + "…"
}
