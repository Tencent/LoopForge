package engine

import (
	"log"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Evolution replay runs execute a historical input with candidate asset
// bodies injected. The catalog is never touched: overrides live on the replay
// row and apply only while this run (and its child SOPs) resolves assets.

func replayOverrides(st *repo.Store, run domain.Run) (domain.EvoAssetOverride, bool) {
	if st == nil || st.EvolutionRepo == nil || !run.ExecutionContext.IsEvolutionReplay() {
		return domain.EvoAssetOverride{}, false
	}
	return st.EvoReplayOverrides(run.ExecutionContext.EvolutionReplayID)
}

// applyReplayAgent swaps in the candidate prompt for a replay run.
func applyReplayAgent(st *repo.Store, agent domain.AgentDefinition, run domain.Run) domain.AgentDefinition {
	ov, ok := replayOverrides(st, run)
	if !ok {
		return agent
	}
	if prompt, found := ov.AgentPrompts[agent.ID]; found {
		agent.Prompt = prompt
	}
	return agent
}

// applyReplayAssets swaps candidate rule bodies into the bundle and removes
// the platform MCP so a replay cannot write issues, runs or projects.
func applyReplayAssets(st *repo.Store, bundle *domain.AgentAssetBundle, run domain.Run) {
	if bundle == nil || !run.ExecutionContext.IsEvolutionReplay() {
		return
	}
	servers := bundle.MCPServers[:0]
	for _, server := range bundle.MCPServers {
		if server.ID == domain.PlatformMCPServerID {
			continue
		}
		servers = append(servers, server)
	}
	bundle.MCPServers = servers
	ov, ok := replayOverrides(st, run)
	if !ok {
		return
	}
	for i, rule := range bundle.Rules {
		if content, found := ov.RuleContents[rule.ID]; found {
			bundle.Rules[i].Content = content
		}
	}
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
	ov, _ := replayOverrides(e.repo, run)
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
