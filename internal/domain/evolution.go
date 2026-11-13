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

// Autonomy levels. P1 supports L0–L2; L3+ (auto canary) is reserved.
const (
	EvoLevelOff     = 0 // no diagnosis for this asset
	EvoLevelReport  = 1 // report findings only
	EvoLevelPropose = 2 // propose + replay, human confirms release
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
)

// Asset version sources.
const (
	EvoVersionBaseline  = "baseline"
	EvoVersionManual    = "manual"
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
	Severity     string      `json:"severity"` // high, medium, low
	Signals      []EvoSignal `json:"signals,omitempty"`
	SampleRunIDs []string    `json:"sampleRunIds,omitempty"`
	Status       string      `json:"status"`
	ProposalID   string      `json:"proposalId,omitempty"`
	CreatedAt    time.Time   `json:"createdAt"`
	UpdatedAt    time.Time   `json:"updatedAt"`
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
	StartedAt    time.Time        `json:"startedAt,omitempty"`
	FinishedAt   time.Time        `json:"finishedAt,omitempty"`
	WaitingSince time.Time        `json:"waitingSince,omitempty"`
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
	Blocked   int    `json:"blocked"`
	Passed    bool   `json:"passed"`
	Verdict   string `json:"verdict,omitempty"`
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
	BaseContent      string           `json:"baseContent"`
	BaseHash         string           `json:"baseHash"`
	CandidateContent string           `json:"candidateContent,omitempty"`
	Status           string           `json:"status"`
	Model            string           `json:"model,omitempty"`
	Replays          []EvoReplay      `json:"replays,omitempty"`
	Summary          EvoReplaySummary `json:"summary"`
	Error            string           `json:"error,omitempty"`
	CreatedBy        int64            `json:"createdBy,omitempty"`
	DecidedBy        int64            `json:"decidedBy,omitempty"`
	DecisionNote     string           `json:"decisionNote,omitempty"`
	ReleasedVersion  int              `json:"releasedVersion,omitempty"`
	CreatedAt        time.Time        `json:"createdAt"`
	UpdatedAt        time.Time        `json:"updatedAt"`
	DecidedAt        time.Time        `json:"decidedAt,omitempty"`
}

func (p EvoProposal) Asset() EvoAssetRef { return EvoAssetRef{Kind: p.AssetKind, ID: p.AssetID} }

// Open reports whether the proposal still waits for validation or a decision.
func (p EvoProposal) Open() bool {
	switch p.Status {
	case EvoProposalDrafting, EvoProposalValidating, EvoProposalPending:
		return true
	}
	return false
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
	AssetKind     string  `json:"assetKind"`
	AssetID       string  `json:"assetId"`
	AssetName     string  `json:"assetName"`
	Runs          int     `json:"runs"`
	Succeeded     int     `json:"succeeded"`
	Failed        int     `json:"failed"`
	Reworked      int     `json:"reworked"`
	SuccessRate   float64 `json:"successRate"`
	Level         int     `json:"level"`
	LatestVersion int     `json:"latestVersion,omitempty"`
	OpenFindings  int     `json:"openFindings,omitempty"`
}

// EvoOverview is the 进化中心 dashboard payload.
type EvoOverview struct {
	ProjectID   string           `json:"projectId"`
	Findings    []EvoFinding     `json:"findings"`
	Validating  []EvoProposal    `json:"validating"`
	Pending     []EvoProposal    `json:"pending"`
	Recent      []EvoProposal    `json:"recent"`
	Health      []EvoAssetHealth `json:"health"`
	Policies    []EvoPolicy      `json:"policies"`
	WindowDays  int              `json:"windowDays"`
	GeneratedAt time.Time        `json:"generatedAt"`
}

// NormalizeEvoLevel clamps unknown levels into the supported P1 range.
func NormalizeEvoLevel(level int) int {
	if level < EvoLevelOff {
		return EvoLevelOff
	}
	if level > EvoLevelPropose {
		return EvoLevelPropose
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
