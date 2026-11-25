package engine

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// ValidateEvolutionReplay fails closed for workflows whose external effects
// cannot yet be isolated. Check the current definition, not just the historic
// snapshot: an operator may have added a script or delivery step since then.
func (e *Engine) ValidateEvolutionReplay(source domain.Run) error {
	if len(normalizeRepositories(source.RepositoryContexts())) > 0 {
		return fmt.Errorf("当前仅支持未绑定代码仓库的历史回放，Git / MR 流程暂不支持")
	}
	wf, ok := e.repo.Workflow(source.WorkflowID)
	if !ok {
		return fmt.Errorf("workflow not found")
	}
	return e.validateEvolutionReplayDefinition(wf, nil)
}

func (e *Engine) validateEvolutionReplayDefinition(wf domain.WorkflowDefinition, repositories []domain.RepositoryContext) error {
	if len(normalizeRepositories(repositories)) > 0 {
		return fmt.Errorf("当前仅支持未绑定代码仓库的历史回放，Git / MR 流程暂不支持")
	}
	for _, node := range wf.Nodes {
		if len(node.CredentialEnv) > 0 {
			return fmt.Errorf("进化回放不允许节点「%s」使用外部凭据", node.ID)
		}
		if node.Type == "approval" {
			continue
		}
		if node.Type != "agent" && node.Type != "" {
			return fmt.Errorf("进化回放暂不支持节点「%s」的 %s 类型（脚本、命令和子 SOP 不在首版范围内）", node.ID, node.Type)
		}
		agent, ok := workflowAgent(e.repo, wf, node.AgentID)
		if !ok {
			return fmt.Errorf("agent %q not found", node.AgentID)
		}
		if len(agent.Hooks) > 0 {
			return fmt.Errorf("进化回放暂不支持角色「%s」的生命周期脚本", agent.ID)
		}
		for _, gate := range agent.Verification {
			if strings.EqualFold(gate.Mode, domain.VerificationModeMR) {
				return fmt.Errorf("进化回放暂不支持角色「%s」的 MR 验证", agent.ID)
			}
		}
	}
	return nil
}

// Evolution replay runs execute a historical input with candidate asset
// bodies injected. The catalog is never touched: overrides live on the replay
// row and apply only while this run (and its child SOPs) resolves assets.

// StartEvolutionReplay replays source's workflow from the nodes the candidate
// shapes. Every node that is neither a start node nor downstream of one, and
// that succeeded in the source run, is seeded as succeeded with the source
// outputs and artifacts; the engine mounts and restores those artifacts as
// usual, so the start nodes see the same inputs the source run produced and
// upstream randomness, human gates and tokens are not replayed. It returns the
// reused node ids (empty for a full replay).
func (e *Engine) StartEvolutionReplay(source domain.Run, repositories []domain.RepositoryContext, exec domain.TelemetryContext, startNodes map[string]bool) (domain.Run, []string, error) {
	if err := e.ValidateEvolutionReplay(source); err != nil {
		return domain.Run{}, nil, err
	}
	// The replay API always creates a replay, even if its caller omitted the
	// marker. Otherwise it could acquire business credentials and notifications.
	exec.Purpose = domain.RunPurposeEvolutionReplay
	wf, ok := e.repo.Workflow(source.WorkflowID)
	if !ok {
		return domain.Run{}, nil, fmt.Errorf("workflow not found")
	}
	reuse := replayReusableNodes(wf, source, startNodes, len(repositories) > 0)
	var seed func(*domain.Run)
	if len(reuse) > 0 {
		seed = func(r *domain.Run) {
			if names := seedReplayFromSource(r, source, reuse); len(names) > 0 {
				e.event(r, "", "evolution.replay.reused", "进化回放沿用原运行 "+source.ID+" 中「"+strings.Join(names, "、")+"」的产出，从受影响的节点开始执行")
			}
		}
	}
	run, err := e.startWorkflowDefinitionSeeded(context.Background(), wf, source.Input, repositories, exec, false, seed)
	if err != nil {
		return domain.Run{}, nil, err
	}
	return run, sortedKeys(reuse), nil
}

// replayReusableNodes picks the nodes a replay may take from the source run.
// It returns nil (full replay) when no start node is in the workflow, or when
// a skipped node changed repository code: a fresh checkout would not have
// those changes and the start node would review or build on a different tree.
func replayReusableNodes(wf domain.WorkflowDefinition, source domain.Run, startNodes map[string]bool, hasRepositories bool) map[string]bool {
	affected := map[string]bool{}
	for _, node := range wf.Nodes {
		if startNodes[node.ID] {
			affected[node.ID] = true
			for id := range descendants(wf, node.ID) {
				affected[id] = true
			}
		}
	}
	if len(affected) == 0 {
		return nil
	}
	reuse := map[string]bool{}
	for _, node := range wf.Nodes {
		if affected[node.ID] {
			continue
		}
		if src := source.Nodes[node.ID]; src != nil && src.Status == "succeeded" {
			reuse[node.ID] = true
		}
	}
	if len(reuse) == 0 {
		return nil
	}
	if hasRepositories {
		for _, a := range source.Artifacts {
			if reuse[a.NodeID] && a.Name == "repository-patch" && strings.TrimSpace(a.Content) != "" {
				return nil
			}
		}
	}
	return reuse
}

// seedReplayFromSource marks reused nodes succeeded with the source outputs
// and copies their artifacts; it returns the reused node names in order.
func seedReplayFromSource(r *domain.Run, source domain.Run, reuse map[string]bool) []string {
	now := time.Now().UTC()
	names := []string{}
	for _, node := range r.WorkflowSnapshot.Nodes {
		src := source.Nodes[node.ID]
		if !reuse[node.ID] || src == nil || r.Nodes[node.ID] == nil {
			continue
		}
		seeded := &domain.NodeRun{
			NodeID: node.ID, Status: "succeeded", Attempt: max(src.Attempt, 1),
			Output: src.Output, Provider: src.Provider, Model: src.Model,
			Activity:  "沿用原运行 " + source.ID + " 的产出",
			StartedAt: now, FinishedAt: now,
		}
		if src.GateResult != nil {
			gate := *src.GateResult
			seeded.GateResult = &gate
		}
		r.Nodes[node.ID] = seeded
		names = append(names, firstNonEmptyString(node.Name, node.ID))
	}
	for _, a := range source.Artifacts {
		if !reuse[a.NodeID] || r.Nodes[a.NodeID] == nil || r.Nodes[a.NodeID].Status != "succeeded" {
			continue
		}
		cloned := newArtifact(r.ID, a.NodeID, max(a.Attempt, 1), a.Name, a.Path, a.MediaType, []byte(a.Content))
		cloned.RepositoryDir = a.RepositoryDir
		r.Artifacts = append(r.Artifacts, cloned)
	}
	if strings.TrimSpace(r.HumanContext) == "" {
		r.HumanContext = source.HumanContext
	}
	return names
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func replayOverrides(st *repo.Store, run domain.Run) (domain.EvoAssetOverride, bool) {
	if st == nil || st.EvolutionRepo == nil || !run.ExecutionContext.IsEvolutionReplay() {
		return domain.EvoAssetOverride{}, false
	}
	return st.EvoReplayOverrides(run.ExecutionContext.EvolutionReplayID)
}

// applyReplayAgent swaps in the candidate prompt for a replay run or a
// business run in a trial's canary arm.
func applyReplayAgent(st *repo.Store, agent domain.AgentDefinition, run domain.Run) domain.AgentDefinition {
	if run.ExecutionContext.IsEvolutionReplay() {
		agent.PlatformMCP, agent.KnowledgeBase = false, false
		agent.MCPServers = nil
	}
	ov, ok := runAssetOverrides(st, run)
	if !ok {
		return agent
	}
	if prompt, found := ov.AgentPrompts[agent.ID]; found {
		agent.Prompt = prompt
	}
	return agent
}

// applyReplayAssets swaps candidate rule bodies into the bundle. Replay runs
// lose every external MCP and its files, including inline authenticated MCPs.
// Trial runs are real business runs and keep their ordinary capabilities.
func applyReplayAssets(st *repo.Store, bundle *domain.AgentAssetBundle, run domain.Run) {
	if bundle == nil {
		return
	}
	if run.ExecutionContext.IsEvolutionReplay() {
		bundle.MCPServers, bundle.MCPFiles = nil, nil
	}
	ov, ok := runAssetOverrides(st, run)
	if !ok {
		return
	}
	for i, rule := range bundle.Rules {
		if content, found := ov.RuleContents[rule.ID]; found {
			bundle.Rules[i].Content = content
		}
	}
}

func (e *Engine) resolveRunScriptMCPAssets(run domain.Run) domain.AgentAssetBundle {
	if run.ExecutionContext.IsEvolutionReplay() {
		return domain.AgentAssetBundle{}
	}
	return e.resolveScriptMCPAssets(run.ExecutionContext.ProjectID, run.ExecutionContext.ResponsibleUserID)
}

// recordRunAssetPins freezes which role prompts and rule bodies a run started
// with, so every run can answer "which asset version did you use". Version is
// the evolution version whose body matches; 0 means an unversioned edit.
func (e *Engine) recordRunAssetPins(run domain.Run) {
	if e == nil || e.repo == nil || e.repo.EvolutionRepo == nil {
		return
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	var pins []domain.RunAssetPin
	add := func(kind, id, content string) {
		key := kind + ":" + id
		if id == "" || seen[key] {
			return
		}
		seen[key] = true
		hash := domain.ContentHash(content)
		pin := domain.RunAssetPin{RunID: run.ID, AssetKind: kind, AssetID: id, Hash: hash, CreatedAt: now}
		if v, ok := e.repo.AssetVersionByHash(kind, id, hash); ok {
			pin.Version = v.Version
		}
		pins = append(pins, pin)
	}
	wf := run.WorkflowSnapshot
	ov, _ := runAssetOverrides(e.repo, run)
	ruleContent := func(rule domain.RuleDefinition) string {
		if candidate, overridden := ov.RuleContents[rule.ID]; overridden {
			return candidate
		}
		return rule.Content
	}
	for _, node := range wf.Nodes {
		if strings.TrimSpace(node.AgentID) == "" || node.Type == "script" || node.Type == "command" || node.Type == "approval" || node.Type == "sop" {
			continue
		}
		if nr := run.Nodes[node.ID]; run.ExecutionContext.IsEvolutionReplay() && nr != nil && nr.Status == "succeeded" {
			continue
		}
		agent, ok := workflowAgent(e.repo, wf, node.AgentID)
		if !ok || domain.IsPreviewBuildAgent(agent.ID) {
			continue
		}
		agent = applyReplayAgent(e.repo, agent, run)
		add(domain.EvoAssetAgent, agent.ID, agent.Prompt)
		for _, ruleID := range agent.Rules {
			if rule, found := e.repo.Rule(ruleID); found {
				add(domain.EvoAssetRule, rule.ID, ruleContent(rule))
			}
		}
	}
	for _, rule := range e.repo.Rules() {
		if rule.Global && ruleTargetsWorkflow(rule, run.WorkflowID) && (rule.ProjectID == run.ExecutionContext.ProjectID || rule.Shared) {
			add(domain.EvoAssetRule, rule.ID, ruleContent(rule))
		}
	}
	if err := e.repo.PutRunAssetPins(pins); err != nil {
		log.Printf("run asset pins %s: %v", run.ID, err)
	}
}
