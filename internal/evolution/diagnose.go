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
	SignalSOPDetection = "sop_detection"
)

// DiagnoseInput is everything diagnosis needs, already loaded by the caller.
type DiagnoseInput struct {
	ProjectID string
	Now       time.Time
	// Runs are full run states (nodes populated) of the window, newest first.
	// Evolution replays must already be excluded.
	Runs []domain.Run
	// Evals holds the latest finished Run Eval per run ID.
	Evals         map[string]domain.RunEvalRecord
	SOPDetections map[string]domain.RunEvalRecord
	// Feedback holds explicit user feedback signals.
	Feedback []domain.EvoSignal
	// Agent resolves an evolvable stored role; generic templates return false.
	Agent func(wf domain.WorkflowDefinition, id string) (domain.AgentDefinition, bool)
	// Rule resolves an evolvable stored rule.
	Rule func(id string) (domain.RuleDefinition, bool)
	// Level returns the asset's autonomy level.
	Level func(kind, id string) int
	// WindowDays is the diagnosis window; it scales weekly impact.
	WindowDays int
	// Pins returns the asset bodies a run started with. Runs whose pin does
	// not match an asset's live body ran an older version and are left out
	// of that asset's diagnosis. Nil keeps every run.
	Pins func(runID string) []domain.RunAssetPin
}

type assetBucket struct {
	ref        domain.EvoAssetRef
	name       string
	hash       string
	signals    []domain.EvoSignal
	runs       map[string]int // run ID -> worst signal weight
	workflows  map[string]bool
	failures   int
	reworks    int
	feedbacks  int
	detections int
	// triage tallies
	exposed  map[string]time.Time // runs on the current version -> run time
	problems map[string]float64   // run ID -> problem weight (max over signals)
	kinds    map[string]map[string]bool
	stale    map[string]bool
}

func (b *assetBucket) stats(now time.Time, days int) triageStats {
	s := triageStats{runs: len(b.exposed), stale: len(b.stale), windowDays: days, kinds: map[string]int{}}
	for id, at := range b.exposed {
		w := decayWeight(now, at)
		s.n += w
		s.k += w * b.problems[id]
		s.problem += b.problems[id]
	}
	for kind, runs := range b.kinds {
		s.kinds[kind] = len(runs)
	}
	return s
}

func signalWeight(kind string) int {
	switch kind {
	case SignalNodeFailed:
		return 3
	case SignalEvalLow, SignalUserFeedback, SignalSOPDetection:
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
	pinCache := map[string]map[string]string{}
	pinnedHash := func(runID, key string) (string, bool) {
		if in.Pins == nil {
			return "", false
		}
		m, ok := pinCache[runID]
		if !ok {
			m = map[string]string{}
			for _, pin := range in.Pins(runID) {
				m[pin.AssetKind+":"+pin.AssetID] = pin.Hash
			}
			pinCache[runID] = m
		}
		h, ok := m[key]
		return h, ok
	}
	// expose marks that a run used an asset and returns its bucket, or nil
	// when the asset is disabled or the run used an older body.
	expose := func(ref domain.EvoAssetRef, name, content string, run domain.Run) *assetBucket {
		if ref.ID == "" {
			return nil
		}
		if in.Level != nil && in.Level(ref.Kind, ref.ID) <= domain.EvoLevelOff {
			return nil
		}
		key := ref.Key()
		b := buckets[key]
		if b == nil {
			b = &assetBucket{ref: ref, name: name, hash: domain.ContentHash(content), runs: map[string]int{}, workflows: map[string]bool{},
				exposed: map[string]time.Time{}, problems: map[string]float64{}, kinds: map[string]map[string]bool{}, stale: map[string]bool{}}
			buckets[key] = b
			order = append(order, key)
		}
		if h, ok := pinnedHash(run.ID, key); ok && h != b.hash {
			b.stale[run.ID] = true
			return nil
		}
		if _, ok := b.exposed[run.ID]; !ok {
			at := run.UpdatedAt
			if at.IsZero() {
				at = in.Now
			}
			b.exposed[run.ID] = at
		}
		return b
	}
	add := func(b *assetBucket, workflowID string, sig domain.EvoSignal) {
		if b == nil {
			return
		}
		sig.ProjectID = in.ProjectID
		b.signals = append(b.signals, sig)
		if w := signalWeight(sig.Kind); w > b.runs[sig.RunID] {
			b.runs[sig.RunID] = w
		}
		if w := problemWeight(sig.Kind); w > b.problems[sig.RunID] {
			b.problems[sig.RunID] = w
		}
		if b.kinds[sig.Kind] == nil {
			b.kinds[sig.Kind] = map[string]bool{}
		}
		b.kinds[sig.Kind][sig.RunID] = true
		if workflowID != "" {
			b.workflows[workflowID] = true
		}
		switch sig.Kind {
		case SignalNodeFailed:
			b.failures++
		case SignalRework:
			b.reworks++
		case SignalUserFeedback:
			b.feedbacks++
		case SignalSOPDetection:
			b.detections++
		}
	}
	exposeAgent := func(agent domain.AgentDefinition, run domain.Run) *assetBucket {
		return expose(domain.EvoAssetRef{Kind: domain.EvoAssetAgent, ID: agent.ID}, agent.Name, domain.EvoAgentContent(agent), run)
	}
	exposeRule := func(rule domain.RuleDefinition, run domain.Run) *assetBucket {
		return expose(domain.EvoAssetRef{Kind: domain.EvoAssetRule, ID: rule.ID}, rule.Name, domain.EvoRuleContent(rule), run)
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
			if nr == nil || nr.Status == "pending" {
				continue
			}
			agent, ok := resolveAgent(in, wf, node.AgentID)
			if !ok {
				continue
			}
			if in.Rule != nil {
				for _, ruleID := range agent.Rules {
					if rule, found := in.Rule(ruleID); found {
						exposeRule(rule, run)
					}
				}
			}
			b := exposeAgent(agent, run)
			if nr.Status == "failed" {
				add(b, run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: node.ID, Kind: SignalNodeFailed, Detail: domain.TrimEvoText(firstText(nr.Error, nr.Activity), 400), CreatedAt: at})
			}
			if nr.ReworkCount > 0 {
				add(b, run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: node.ID, Kind: SignalRework, Detail: domain.TrimEvoText(firstText(nr.Feedback, fmt.Sprintf("被打回 %d 次", nr.ReworkCount)), 400), CreatedAt: at})
			}
		}
		if detection, ok := in.SOPDetections[run.ID]; ok {
			for _, finding := range detection.Result.Findings {
				node, found := nodeByID(wf, finding.NodeID)
				if !found || !agentNode(node) {
					continue
				}
				if agent, ok := resolveAgent(in, wf, node.AgentID); ok {
					add(exposeAgent(agent, run), run.WorkflowID, domain.EvoSignal{
						RunID: run.ID, NodeID: node.ID, Kind: SignalSOPDetection,
						Detail: domain.TrimEvoText(finding.Message, 400), CreatedAt: detection.FinishedAt,
					})
				}
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
						add(exposeRule(rule, run), run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: finding.NodeID, Kind: SignalEvalLow, Detail: detail, CreatedAt: eval.FinishedAt})
						continue
					}
				}
				if node, found := nodeByID(wf, finding.NodeID); found && agentNode(node) {
					if agent, ok := resolveAgent(in, wf, node.AgentID); ok {
						add(exposeAgent(agent, run), run.WorkflowID, domain.EvoSignal{RunID: run.ID, NodeID: node.ID, Kind: SignalEvalLow, Detail: detail, CreatedAt: eval.FinishedAt})
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
		node, found := FeedbackNode(run, fb.NodeID)
		if !found {
			continue
		}
		if agent, ok := resolveAgent(in, wf, node.AgentID); ok {
			sig := fb
			sig.Kind = SignalUserFeedback
			sig.NodeID = node.ID
			sig.Detail = domain.TrimEvoText(fb.Detail, 400)
			add(exposeAgent(agent, run), run.WorkflowID, sig)
		}
	}
	var totalN, totalK float64
	for _, key := range order {
		s := buckets[key].stats(in.Now, in.WindowDays)
		totalN += s.n
		totalK += s.k
	}
	baseline := TriageBaseline(totalN, totalK)
	out := make([]domain.EvoFinding, 0, len(buckets))
	for _, key := range order {
		b := buckets[key]
		// Explicit human feedback is actionable evidence even when the run
		// succeeded. Keep the repetition threshold for automatic signals only.
		if b.failures == 0 && b.feedbacks == 0 && b.detections == 0 && len(b.signals) < 2 {
			continue
		}
		out = append(out, b.finding(in, baseline))
	}
	sort.SliceStable(out, func(i, j int) bool {
		vi, vj := verdictRank(out[i].Triage.Verdict), verdictRank(out[j].Triage.Verdict)
		if vi != vj {
			return vi > vj
		}
		if out[i].Triage.Score != out[j].Triage.Score {
			return out[i].Triage.Score > out[j].Triage.Score
		}
		if out[i].Triage.ProbWorse != out[j].Triage.ProbWorse {
			return out[i].Triage.ProbWorse > out[j].Triage.ProbWorse
		}
		return len(out[i].Signals) > len(out[j].Signals)
	})
	return out
}

func (b *assetBucket) finding(in DiagnoseInput, baseline float64) domain.EvoFinding {
	runIDs := make([]string, 0, len(b.runs))
	for id := range b.runs {
		runIDs = append(runIDs, id)
	}
	latest := map[string]time.Time{}
	var first, last time.Time
	for _, sig := range b.signals {
		if sig.CreatedAt.After(latest[sig.RunID]) {
			latest[sig.RunID] = sig.CreatedAt
		}
		if sig.CreatedAt.IsZero() {
			continue
		}
		if first.IsZero() || sig.CreatedAt.Before(first) {
			first = sig.CreatedAt
		}
		if sig.CreatedAt.After(last) {
			last = sig.CreatedAt
		}
	}
	sort.Slice(runIDs, func(i, j int) bool {
		if b.runs[runIDs[i]] != b.runs[runIDs[j]] {
			return b.runs[runIDs[i]] > b.runs[runIDs[j]]
		}
		return latest[runIDs[i]].After(latest[runIDs[j]])
	})
	triage := evalTriage(b.stats(in.Now, in.WindowDays), baseline)
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
	if b.detections > 0 {
		parts = append(parts, fmt.Sprintf("%d 条 SOP 自动检测问题", b.detections))
	}
	other := len(b.signals) - b.failures - b.reworks - b.detections
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d 条评测/反馈问题", other))
	}
	title := fmt.Sprintf("%s「%s」在 %d 次运行中出现 %s", kindLabel, firstText(b.name, b.ref.ID), len(runIDs), strings.Join(parts, "、"))
	workflows := make([]string, 0, len(b.workflows))
	for id := range b.workflows {
		workflows = append(workflows, id)
	}
	sort.Strings(workflows)
	clusters := buildClusters(b.signals, in.Now, in.WindowDays)
	signals := append([]domain.EvoSignal(nil), b.signals...)
	sort.SliceStable(signals, func(i, j int) bool { return signalWeight(signals[i].Kind) > signalWeight(signals[j].Kind) })
	if len(signals) > 30 {
		signals = signals[:30]
	}
	runAt := map[string]time.Time{}
	for id, at := range b.exposed {
		runAt[id] = at
	}
	return domain.EvoFinding{
		ProjectID:    in.ProjectID,
		AssetKind:    b.ref.Kind,
		AssetID:      b.ref.ID,
		AssetName:    b.name,
		WorkflowIDs:  workflows,
		Title:        title,
		Summary:      summarizeDetails(signals, 3),
		Severity:     verdictSeverity(triage.Verdict),
		Signals:      signals,
		SampleRunIDs: runIDs,
		Status:       domain.EvoFindingOpen,
		Triage:       &triage,
		Clusters:     clusters,
		Trend:        buildTrend(in.Now, in.WindowDays, b.problems, runAt),
		FirstSeenAt:  first,
		LastSeenAt:   last,
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

// FeedbackNode attributes feedback to an executed role node, falling back to
// the last completed role when the selected node is a gate or is unavailable.
func FeedbackNode(run domain.Run, id string) (domain.WorkflowNode, bool) {
	if node, found := nodeByID(run.WorkflowSnapshot, id); found && agentNode(node) {
		if nr := run.Nodes[node.ID]; nr != nil && nr.Status != "pending" {
			return node, true
		}
	}
	return lastAgentNode(run)
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
