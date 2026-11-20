package engine

import (
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
	"agentflow/internal/repo"
)

// Evolution trials (canary): while a proposal is in a trial, a fixed share
// of new business runs that use the asset start with the candidate body
// instead of the live one. The arm is decided once at run start from the
// run ID, stored on the execution context (child SOP runs inherit it) and
// frozen in the run's asset pins, so trial and control runs can be compared
// by pin hash. The catalog asset is never touched during a trial.

// assignEvolutionCanaries returns the trial proposals whose candidate this
// run uses, with display names for the run event.
func (e *Engine) assignEvolutionCanaries(runID string, wf domain.WorkflowDefinition, ctx domain.TelemetryContext) ([]string, []string) {
	if e == nil || e.repo == nil || e.repo.EvolutionRepo == nil || ctx.IsEvolutionReplay() {
		return nil, nil
	}
	if strings.TrimSpace(ctx.ParentRunID) != "" {
		return ctx.EvolutionCanaryIDs, nil
	}
	var ids, names []string
	for _, p := range e.repo.ActiveEvoCanaries() {
		if p.ProjectID == "" || p.ProjectID != ctx.ProjectID {
			continue
		}
		if p.AssetKind == domain.EvoAssetAgent {
			asset, ok := e.repo.Agent(p.AssetID)
			if !ok || asset.Shared || asset.ProjectID != ctx.ProjectID {
				continue
			}
		} else {
			asset, ok := e.repo.Rule(p.AssetID)
			if !ok || asset.Shared || asset.ProjectID != ctx.ProjectID {
				continue
			}
		}
		if !workflowUsesEvoAsset(e.repo, wf, ctx.ProjectID, p.AssetKind, p.AssetID) {
			continue
		}
		if evolution.CanaryBucket(runID, p.ID) < p.Canary.Percent {
			ids = append(ids, p.ID)
			names = append(names, firstNonEmptyString(p.AssetName, p.AssetID))
		}
	}
	return ids, names
}

// workflowUsesEvoAsset mirrors recordRunAssetPins: a role is used when an
// agent node runs the stored role, a rule when such a role carries it or it
// is a global rule targeting the workflow.
func workflowUsesEvoAsset(st *repo.Store, wf domain.WorkflowDefinition, projectID, kind, id string) bool {
	for _, node := range wf.Nodes {
		if strings.TrimSpace(node.AgentID) == "" || node.Type == "script" || node.Type == "command" || node.Type == "approval" || node.Type == "sop" {
			continue
		}
		stored, ok := st.Agent(node.AgentID)
		if !ok || st.AgentShadowsTemplateOutside(stored, wf.ProjectID) {
			continue
		}
		if kind == domain.EvoAssetAgent && stored.ID == id {
			return true
		}
		if kind == domain.EvoAssetRule {
			for _, ruleID := range stored.Rules {
				if ruleID == id {
					return true
				}
			}
		}
	}
	if kind == domain.EvoAssetRule {
		if rule, ok := st.Rule(id); ok && rule.Global && ruleTargetsWorkflow(rule, wf.ID) && (rule.ProjectID == projectID || rule.Shared) {
			return true
		}
	}
	return false
}

// canaryOverrides merges the candidates of every trial this run takes part in.
func canaryOverrides(st *repo.Store, run domain.Run) (domain.EvoAssetOverride, bool) {
	if st == nil || st.EvolutionRepo == nil || len(run.ExecutionContext.EvolutionCanaryIDs) == 0 || run.ExecutionContext.IsEvolutionReplay() {
		return domain.EvoAssetOverride{}, false
	}
	out := domain.EvoAssetOverride{}
	for _, id := range run.ExecutionContext.EvolutionCanaryIDs {
		ov, ok := st.EvoCanaryOverride(id)
		if !ok {
			continue
		}
		for k, v := range ov.AgentPrompts {
			if out.AgentPrompts == nil {
				out.AgentPrompts = map[string]string{}
			}
			out.AgentPrompts[k] = v
		}
		for k, v := range ov.RuleContents {
			if out.RuleContents == nil {
				out.RuleContents = map[string]string{}
			}
			out.RuleContents[k] = v
		}
	}
	return out, !out.Empty()
}

// runAssetOverrides is the candidate set a run executes with: the replay's
// candidate for replay runs, the trial candidates for business runs.
func runAssetOverrides(st *repo.Store, run domain.Run) (domain.EvoAssetOverride, bool) {
	if ov, ok := replayOverrides(st, run); ok {
		return ov, true
	}
	return canaryOverrides(st, run)
}
