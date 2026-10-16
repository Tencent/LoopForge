package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	RunEvalRunning   = "running"
	RunEvalSucceeded = "succeeded"
	RunEvalFailed    = "failed"

	RunEvalRecPending = "pending"
	RunEvalRecApplied = "applied"

	RunEvalPolicyVersion = 1
)

type RunEvalDimension struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Question string `json:"question"`
	Weight   int    `json:"weight"`
}

type RunEvalPolicy struct {
	Enabled    bool               `json:"enabled"`
	Version    int                `json:"version,omitempty"`
	Threshold  int                `json:"threshold"`
	Dimensions []RunEvalDimension `json:"dimensions,omitempty"`
}

func (p RunEvalPolicy) IsZero() bool {
	return !p.Enabled && p.Threshold == 0 && p.Version == 0 && len(p.Dimensions) == 0
}

func DefaultRunEvalPolicy() RunEvalPolicy {
	return RunEvalPolicy{Enabled: true, Version: RunEvalPolicyVersion, Threshold: 70, Dimensions: []RunEvalDimension{
		{ID: "control", Name: "控制流健康", Question: "节点是否多次循环、返工或 A→B→A 打回？attempt/rework 是否超过节点上限？", Weight: 25},
		{ID: "latency", Name: "耗时与等待", Question: "单节点是否占了大部分执行时间？墙钟是否远大于实际执行（空转/排队）？", Weight: 20},
		{ID: "tools", Name: "工具浪费", Question: "是否有同工具连续失败、重复 Read/Grep 无进展、或无产物的空转调用？", Weight: 20},
		{ID: "tokens", Name: "Token 效率", Question: "Token 是否集中在单一节点？缓存命中是否过低且输入很大？", Weight: 15},
		{ID: "recovery", Name: "错误恢复", Question: "相同错误是否反复出现？门禁是否多次失败才过？失败后有没有有效变化？", Weight: 10},
		{ID: "delivery", Name: "交付完整", Question: "终态是否成功？预期产物和 verification/delivery 信号是否齐全？", Weight: 10},
	}}
}

func EffectiveRunEvalPolicy(p RunEvalPolicy) RunEvalPolicy {
	if p.IsZero() {
		return DefaultRunEvalPolicy()
	}
	if len(p.Dimensions) == 0 {
		p.Dimensions = DefaultRunEvalPolicy().Dimensions
	}
	if p.Threshold <= 0 {
		p.Threshold = DefaultRunEvalPolicy().Threshold
	}
	if p.Version == 0 {
		p.Version = RunEvalPolicyVersion
	}
	p.Enabled = true
	return p
}

type RunEvalNodeFact struct {
	NodeID       string     `json:"nodeId"`
	Name         string     `json:"name,omitempty"`
	Type         string     `json:"type,omitempty"`
	Status       string     `json:"status"`
	Attempt      int        `json:"attempt"`
	ReworkCount  int        `json:"reworkCount"`
	MaxAttempts  int        `json:"maxAttempts,omitempty"`
	MaxReworks   int        `json:"maxReworks,omitempty"`
	ToolCalls    int        `json:"toolCalls"`
	DurationMs   int64      `json:"durationMs"`
	Usage        TokenUsage `json:"usage,omitempty"`
	Error        string     `json:"error,omitempty"`
	Lifecycle    int        `json:"lifecycleStarts"`
	Artifacts    int        `json:"artifacts"`
	Verification int        `json:"verificationEvents"`
	AgentID      string     `json:"agentId,omitempty"`
}

type RunEvalToolRepeat struct {
	NodeID string `json:"nodeId,omitempty"`
	Tool   string `json:"tool"`
	Count  int    `json:"count"`
	Failed int    `json:"failed"`
}

type RunEvalErrorFact struct {
	NodeID string `json:"nodeId,omitempty"`
	Text   string `json:"text"`
	Count  int    `json:"count"`
}

type RunEvalDigestEvent struct {
	Sequence int64  `json:"seq"`
	Time     string `json:"ts,omitempty"`
	NodeID   string `json:"nodeId,omitempty"`
	Category string `json:"category"`
	Name     string `json:"name"`
	Phase    string `json:"phase,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Message  string `json:"message,omitempty"`
	Error    string `json:"error,omitempty"`
}

type RunEvalFacts struct {
	RunID           string               `json:"runId"`
	WorkflowID      string               `json:"workflowId"`
	WorkflowName    string               `json:"workflowName,omitempty"`
	Status          string               `json:"status"`
	Input           string               `json:"input,omitempty"`
	ProjectID       string               `json:"projectId,omitempty"`
	WallDurationMs  int64                `json:"wallDurationMs"`
	ExecDurationMs  int64                `json:"execDurationMs"`
	Usage           TokenUsage           `json:"usage"`
	ToolCalls       int                  `json:"toolCalls"`
	ArtifactCount   int                  `json:"artifactCount"`
	EventCount      int                  `json:"eventCount"`
	TelemetryCount  int                  `json:"telemetryCount"`
	Nodes           []RunEvalNodeFact    `json:"nodes"`
	ToolRepeats     []RunEvalToolRepeat  `json:"toolRepeats,omitempty"`
	Errors          []RunEvalErrorFact   `json:"errors,omitempty"`
	PingPong        []string             `json:"pingPong,omitempty"`
	EventTypes      map[string]int       `json:"eventTypes,omitempty"`
	Digest          []RunEvalDigestEvent `json:"digest"`
	EditableCatalog RunEvalCatalog       `json:"editableCatalog,omitempty"`
}

type RunEvalCatalog struct {
	WorkflowID string                `json:"workflowId,omitempty"`
	Generic    bool                  `json:"generic,omitempty"`
	Nodes      []RunEvalCatalogNode  `json:"nodes,omitempty"`
	Agents     []RunEvalCatalogAgent `json:"agents,omitempty"`
}

type RunEvalCatalogNode struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	AgentID       string `json:"agentId,omitempty"`
	MaxAttempts   int    `json:"maxAttempts"`
	MaxReworks    int    `json:"maxReworks"`
	OnFailure     string `json:"onFailure,omitempty"`
	RetryStrategy string `json:"retryStrategy,omitempty"`
}

type RunEvalCatalogAgent struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	ProjectID    string             `json:"projectId,omitempty"`
	Shared       bool               `json:"shared,omitempty"`
	Instructions string             `json:"instructions,omitempty"`
	Objective    string             `json:"objective,omitempty"`
	Verification []VerificationGate `json:"verification,omitempty"`
}

type RunEvalFinding struct {
	Severity  string `json:"severity"`
	Dimension string `json:"dimension"`
	NodeID    string `json:"nodeId,omitempty"`
	RuleID    string `json:"ruleId,omitempty"`
	Message   string `json:"message"`
	Evidence  string `json:"evidence,omitempty"`
}

type RunEvalRecommendation struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	Rationale       string            `json:"rationale,omitempty"`
	Target          string            `json:"target,omitempty"`
	TargetID        string            `json:"targetId,omitempty"`
	Action          string            `json:"action,omitempty"`
	NodeID          string            `json:"nodeId,omitempty"`
	Set             map[string]any    `json:"set,omitempty"`
	Gate            *VerificationGate `json:"gate,omitempty"`
	Text            string            `json:"text,omitempty"`
	Status          string            `json:"status,omitempty"`
	AppliedAt       *time.Time        `json:"appliedAt,omitempty"`
	AppliedTargetID string            `json:"appliedTargetId,omitempty"`
	ApplyError      string            `json:"applyError,omitempty"`
}

func (r RunEvalRecommendation) Applyable() bool {
	if r.Status == RunEvalRecApplied {
		return false
	}
	switch NormalizeRunEvalAction(r.Target, r.Action) {
	case "workflow.update_node", "agent.upsert_gate", "agent.remove_gate",
		"agent.append_instructions", "agent.append_task_prompt":
		return strings.TrimSpace(r.TargetID) != "" || strings.TrimSpace(r.NodeID) != ""
	default:
		return false
	}
}

func NormalizeRunEvalAction(target, action string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return ""
	}
	if strings.Contains(action, ".") {
		return action
	}
	if target == "" {
		return action
	}
	return target + "." + action
}

type RunEvalResult struct {
	Score           int                     `json:"score"`
	Passed          bool                    `json:"passed"`
	Dimensions      map[string]int          `json:"dimensions,omitempty"`
	Findings        []RunEvalFinding        `json:"findings,omitempty"`
	Summary         string                  `json:"summary,omitempty"`
	Recommendations []RunEvalRecommendation `json:"recommendations,omitempty"`
}

type RunEvalRecord struct {
	ID         string        `json:"id"`
	RunID      string        `json:"runId"`
	Status     string        `json:"status"`
	Score      int           `json:"score"`
	Passed     bool          `json:"passed"`
	Model      string        `json:"model,omitempty"`
	Policy     RunEvalPolicy `json:"policy"`
	Facts      RunEvalFacts  `json:"facts,omitempty"`
	Result     RunEvalResult `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	CreatedBy  int64         `json:"createdBy,omitempty"`
	CreatedAt  time.Time     `json:"createdAt"`
	FinishedAt time.Time     `json:"finishedAt,omitempty"`
}

func ScoreRunEvalDimensions(policy RunEvalPolicy, ratings map[string]int) (int, map[string]int, error) {
	policy = EffectiveRunEvalPolicy(policy)
	if len(policy.Dimensions) == 0 {
		return 0, nil, fmt.Errorf("run eval policy has no dimensions")
	}
	clean := map[string]int{}
	totalWeight := 0
	weighted := 0
	for _, dim := range policy.Dimensions {
		score, ok := ratings[dim.ID]
		if !ok {
			return 0, nil, fmt.Errorf("missing dimension score %q", dim.ID)
		}
		if score < 0 || score > 100 {
			return 0, nil, fmt.Errorf("dimension %q score %d out of range", dim.ID, score)
		}
		weight := dim.Weight
		if weight <= 0 {
			weight = 1
		}
		clean[dim.ID] = score
		totalWeight += weight
		weighted += score * weight
	}
	if totalWeight == 0 {
		return 0, clean, fmt.Errorf("run eval policy weights sum to 0")
	}
	return (weighted + totalWeight/2) / totalWeight, clean, nil
}

func TerminalRunStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}
