// Package evolution holds the pure logic of platform asset evolution:
// attributing run signals to role prompts and rules, building the improver
// prompt, statically checking a candidate, and judging replay outcomes. It has
// no storage or HTTP dependencies so every rule here is unit-testable.
package evolution

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// Signal kinds attributed to assets.
const (
	SignalNodeFailed   = "node_failed"
	SignalRework       = "rework"
	SignalEvalLow      = "eval_low"
	SignalUserFeedback = "user_feedback"
)

// DiagnoseInput is everything diagnosis needs, already loaded by the caller.
type DiagnoseInput struct {
	ProjectID string
	Now       time.Time
	// Runs are full run states (nodes populated) of the window, newest first.
	// Evolution replays must already be excluded.
	Runs []domain.Run
	// Evals holds the latest finished Run Eval per run ID.
	Evals map[string]domain.RunEvalRecord
	// Feedback holds explicit user feedback signals.
	Feedback []domain.EvoSignal
	// Agent resolves an evolvable stored role; generic templates return false.
	Agent func(wf domain.WorkflowDefinition, id string) (domain.AgentDefinition, bool)
	// Rule resolves an evolvable stored rule.
	Rule func(id string) (domain.RuleDefinition, bool)
	// Level returns the asset's autonomy level.
	Level func(kind, id string) int
}

type assetBucket struct {
	ref       domain.EvoAssetRef
	name      string
	signals   []domain.EvoSignal
	runs      map[string]int // run ID -> worst signal weight
	workflows map[string]bool
	failures  int
	reworks   int
}

func signalWeight(kind string) int {
	switch kind {
	case SignalNodeFailed:
		return 3
	case SignalEvalLow, SignalUserFeedback:
		return 2
	default:
		return 1
	}
}

// Diagnose groups failure / rework / low-eval / feedback signals by the role
// or rule they point at and returns one finding per affected asset.
func Diagnose(in DiagnoseInput) []domain.EvoFinding {
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	buckets := map[string]*assetBucket{}
	order := []string{}
	add := func(ref domain.EvoAssetRef, name, workflowID string, sig domain.EvoSignal) {
		if ref.ID == "" {
			return
		}
		if in.Level != nil && in.Level(ref.Kind, ref.ID) <= domain.EvoLevelOff {
			return
		}
		key := ref.Key()
		b := buckets[key]
		if b == nil {
			b = &assetBucket{ref: ref, name: name, runs: map[string]int{}, workflows: map[string]bool{}}
			buckets[key] = b
			order = append(order, key)
		}
		sig.ProjectID = in.ProjectID
		b.signals = append(b.signals, sig)
		if w := signalWeight(sig.Kind); w > b.runs[sig.RunID] {
			b.runs[sig.RunID] = w
		}
		if workflowID != "" {
			b.workflows[workflowID] = true
		}
		switch sig.Kind {
		case SignalNodeFailed:
			b.failures++
		case SignalRework:
			b.reworks++
		}
	}
	runByID := map[string]domain.Run{}
	for _, run := range in.Runs {
		if run.ExecutionContext.IsEvolutionReplay() {
			continue
		}
		runByID[run.ID] = run
		wf := run.WorkflowSnapshot
		at := run.UpdatedAt
		for _, node := range wf.Nodes {
			if !agentNode(node) {
				continue
			}
			nr := run.Nodes[node.ID]
			if nr == nil {
				continue
			}
			agent, ok := resolveAgent(in, wf, node.AgentID)
			if !ok {
				continue
			}
			ref := domain.EvoAssetRef{Kind: domain.EvoAssetAgent, ID: agent.ID}
			if nr.Status == "failed" {
				add(ref, agent.Name, run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: node.ID, Kind: SignalNodeFailed, Detail: domain.TrimEvoText(firstText(nr.Error, nr.Activity), 400), CreatedAt: at})
			}
			if nr.ReworkCount > 0 {
				add(ref, agent.Name, run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: node.ID, Kind: SignalRework, Detail: domain.TrimEvoText(firstText(nr.Feedback, fmt.Sprintf("被打回 %d 次", nr.ReworkCount)), 400), CreatedAt: at})
			}
		}
		if eval, ok := in.Evals[run.ID]; ok && eval.Status == domain.RunEvalSucceeded && !eval.Passed {
			for _, finding := range eval.Result.Findings {
				if strings.TrimSpace(finding.Severity) == "info" {
					continue
				}
				detail := domain.TrimEvoText(finding.Message, 400)
				if finding.RuleID != "" && in.Rule != nil {
					if rule, found := in.Rule(finding.RuleID); found {
						add(domain.EvoAssetRef{Kind: domain.EvoAssetRule, ID: rule.ID}, rule.Name, run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: finding.NodeID, Kind: SignalEvalLow, Detail: detail, CreatedAt: eval.FinishedAt})
						continue
					}
				}
				if node, found := nodeByID(wf, finding.NodeID); found && agentNode(node) {
					if agent, ok := resolveAgent(in, wf, node.AgentID); ok {
						add(domain.EvoAssetRef{Kind: domain.EvoAssetAgent, ID: agent.ID}, agent.Name, run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: node.ID, Kind: SignalEvalLow, Detail: detail, CreatedAt: eval.FinishedAt})
					}
				}
			}
		}
	}
	for _, fb := range in.Feedback {
		run, ok := runByID[fb.RunID]
		if !ok {
			continue
		}
		wf := run.WorkflowSnapshot
		node, found := nodeByID(wf, fb.NodeID)
		if !found || !agentNode(node) {
			node, found = lastAgentNode(run)
		}
		if !found {
			continue
		}
		if agent, ok := resolveAgent(in, wf, node.AgentID); ok {
			sig := fb
			sig.Kind = SignalUserFeedback
			sig.NodeID = node.ID
			sig.Detail = domain.TrimEvoText(fb.Detail, 400)
			add(domain.EvoAssetRef{Kind: domain.EvoAssetAgent, ID: agent.ID}, agent.Name, run.WorkflowID, sig)
		}
	}
	out := make([]domain.EvoFinding, 0, len(buckets))
	for _, key := range order {
		b := buckets[key]
		if b.failures == 0 && len(b.signals) < 2 {
			continue
		}
		out = append(out, b.finding(in))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return severityRank(out[i].Severity) > severityRank(out[j].Severity) ||
			(severityRank(out[i].Severity) == severityRank(out[j].Severity) && len(out[i].Signals) > len(out[j].Signals))
	})
	return out
}

func (b *assetBucket) finding(in DiagnoseInput) domain.EvoFinding {
	runIDs := make([]string, 0, len(b.runs))
	for id := range b.runs {
		runIDs = append(runIDs, id)
	}
	latest := map[string]time.Time{}
	for _, sig := range b.signals {
		if sig.CreatedAt.After(latest[sig.RunID]) {
			latest[sig.RunID] = sig.CreatedAt
		}
	}
	sort.Slice(runIDs, func(i, j int) bool {
		if b.runs[runIDs[i]] != b.runs[runIDs[j]] {
			return b.runs[runIDs[i]] > b.runs[runIDs[j]]
		}
		return latest[runIDs[i]].After(latest[runIDs[j]])
	})
	failedRuns := 0
	for _, w := range b.runs {
		if w >= 3 {
			failedRuns++
		}
	}
	severity := "low"
	switch {
	case failedRuns >= 3 || len(b.signals) >= 6:
		severity = "high"
	case failedRuns >= 1 || len(runIDs) >= 2:
		severity = "medium"
	}
	kindLabel := "角色"
	if b.ref.Kind == domain.EvoAssetRule {
		kindLabel = "规则"
	}
	parts := []string{}
	if b.failures > 0 {
		parts = append(parts, fmt.Sprintf("%d 次节点失败", b.failures))
	}
	if b.reworks > 0 {
		parts = append(parts, fmt.Sprintf("%d 次被打回", b.reworks))
	}
	other := len(b.signals) - b.failures - b.reworks
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d 条评测/反馈问题", other))
	}
	title := fmt.Sprintf("%s「%s」在 %d 次运行中出现 %s", kindLabel, firstText(b.name, b.ref.ID), len(runIDs), strings.Join(parts, "、"))
	workflows := make([]string, 0, len(b.workflows))
	for id := range b.workflows {
		workflows = append(workflows, id)
	}
	sort.Strings(workflows)
	signals := append([]domain.EvoSignal(nil), b.signals...)
	sort.SliceStable(signals, func(i, j int) bool { return signalWeight(signals[i].Kind) > signalWeight(signals[j].Kind) })
	if len(signals) > 30 {
		signals = signals[:30]
	}
	return domain.EvoFinding{
		ProjectID:    in.ProjectID,
		AssetKind:    b.ref.Kind,
		AssetID:      b.ref.ID,
		AssetName:    b.name,
		WorkflowIDs:  workflows,
		Title:        title,
		Summary:      summarizeDetails(signals, 3),
		Severity:     severity,
		Signals:      signals,
		SampleRunIDs: runIDs,
		Status:       domain.EvoFindingOpen,
	}
}

func summarizeDetails(signals []domain.EvoSignal, n int) string {
	seen := map[string]bool{}
	lines := []string{}
	for _, sig := range signals {
		detail := strings.TrimSpace(sig.Detail)
		if detail == "" || seen[detail] {
			continue
		}
		seen[detail] = true
		lines = append(lines, "· "+domain.TrimEvoText(detail, 160))
		if len(lines) >= n {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func severityRank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	}
	return 1
}

func agentNode(node domain.WorkflowNode) bool {
	switch node.Type {
	case "script", "command", "approval", "sop":
		return false
	}
	return strings.TrimSpace(node.AgentID) != "" && !domain.IsPreviewBuildAgent(node.AgentID)
}

func resolveAgent(in DiagnoseInput, wf domain.WorkflowDefinition, id string) (domain.AgentDefinition, bool) {
	if in.Agent == nil {
		return domain.AgentDefinition{}, false
	}
	return in.Agent(wf, id)
}

func nodeByID(wf domain.WorkflowDefinition, id string) (domain.WorkflowNode, bool) {
	if id == "" {
		return domain.WorkflowNode{}, false
	}
	for _, node := range wf.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return domain.WorkflowNode{}, false
}

// lastAgentNode is the agent node that finished last: the one whose output
// a reviewer most likely judged.
func lastAgentNode(run domain.Run) (domain.WorkflowNode, bool) {
	var best domain.WorkflowNode
	var bestAt time.Time
	found := false
	for _, node := range run.WorkflowSnapshot.Nodes {
		if !agentNode(node) {
			continue
		}
		nr := run.Nodes[node.ID]
		if nr == nil || nr.Status == "pending" {
			continue
		}
		if !found || nr.FinishedAt.After(bestAt) {
			best, bestAt, found = node, nr.FinishedAt, true
		}
	}
	return best, found
}

func firstText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
