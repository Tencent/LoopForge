package httpapi

import (
	"strconv"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
)

// Feedback remains a reader action. It may materialize its own problem, but
// never generates a proposal, archives other assets, changes policy or runs a model.
type evoFeedbackResponse struct {
	domain.EvoSignal
	FindingID string `json:"findingId,omitempty"`
	ClusterID string `json:"clusterId,omitempty"`
	Message   string `json:"message"`
}

func (h *evolutionHandlers) feedbackFinding(run domain.Run, sig domain.EvoSignal) evoFeedbackResponse {
	out := evoFeedbackResponse{EvoSignal: sig}
	if run.IsWorkspace() {
		out.Message = "反馈已保存。工作区对话暂不支持资产进化，请在绑定已保存角色的 SOP 运行中验证。"
		return out
	}
	if !domain.TerminalRunStatus(run.Status) {
		out.Message = "反馈已保存。运行结束后可在RSI工作台检查最近运行。"
		return out
	}
	node, ok := evolution.FeedbackNode(run, sig.NodeID)
	if !ok {
		out.Message = "反馈已保存。该运行没有已执行的角色节点，暂时无法生成角色改进问题。"
		return out
	}
	agent, ok := h.evolvableAgent(run.WorkflowSnapshot, node.AgentID)
	if !ok {
		out.Message = "反馈已保存。该节点未绑定可进化的已保存角色，请在 SOP 中绑定项目角色后新建运行。"
		return out
	}
	if h.repo.EvoLevel(domain.EvoAssetAgent, agent.ID) == domain.EvoLevelOff {
		out.Message = "反馈已保存。该角色已关闭进化，可在进化设置中开启后检查最近运行。"
		return out
	}
	for _, pin := range h.repo.RunAssetPins(run.ID) {
		if pin.AssetKind == domain.EvoAssetAgent && pin.AssetID == agent.ID && pin.Hash != domain.ContentHash(domain.EvoAgentContent(agent)) {
			out.Message = "反馈已保存。该运行使用旧版角色正文，请用当前版本新建运行后验证。"
			return out
		}
	}
	at := run.CreatedAt
	if at.IsZero() {
		at = run.UpdatedAt
	}
	if at.Before(time.Now().UTC().AddDate(0, 0, -evoDefaultWindowDays)) {
		out.Message = "反馈已保存。该运行超出近 " + strconv.Itoa(evoDefaultWindowDays) + " 天的检查范围，请用当前角色新建运行后验证。"
		return out
	}
	h.findingsM.Lock()
	defer h.findingsM.Unlock()
	runs := h.loadWindowRuns(sig.ProjectID, evoDefaultWindowDays, evoMaxDiagnoseRuns)
	containsRun := false
	for _, item := range runs {
		containsRun = containsRun || item.ID == run.ID
	}
	if !containsRun {
		// Explicit feedback still gets examined when its recent run fell past
		// the bounded automatic scan; the rest of the window remains bounded.
		if len(runs) == evoMaxDiagnoseRuns {
			runs = runs[:len(runs)-1]
		}
		runs = append(runs, run)
	}
	in := h.diagnoseInput(sig.ProjectID, evoDefaultWindowDays, runs, h.repo.EvoLevel)
	for _, f := range evolution.Diagnose(in) {
		if f.AssetKind != domain.EvoAssetAgent || f.AssetID != agent.ID {
			continue
		}
		saved, err := h.saveDiagnosedFinding(f, h.repo.EvoFindings(sig.ProjectID, domain.EvoFindingOpen, domain.EvoFindingProposed), h.repo.EvoQueue(sig.ProjectID))
		if err != nil {
			out.Message = "反馈已保存，整理问题暂未完成。请稍后在RSI工作台检查最近运行。"
			return out
		}
		out.FindingID = saved.ID
		out.Message = "反馈已加入RSI工作台。"
		for _, cluster := range saved.Clusters {
			if cluster.Kind != evolution.SignalUserFeedback || cluster.NodeID != node.ID {
				continue
			}
			for _, occurrence := range cluster.Occurrences {
				if occurrence.RunID == run.ID && occurrence.Detail == domain.TrimEvoText(sig.Detail, 400) {
					out.ClusterID = cluster.ID
				}
			}
		}
		return out
	}
	out.Message = "反馈已保存，暂未能关联到当前角色问题。请在RSI工作台检查最近运行。"
	return out
}

func (h *evolutionHandlers) saveDiagnosedFinding(f domain.EvoFinding, active []domain.EvoFinding, queued []domain.EvoQueueItem) (domain.EvoFinding, error) {
	matched := false
	for _, existing := range active {
		if existing.AssetKind != f.AssetKind || existing.AssetID != f.AssetID {
			continue
		}
		if existing.Status == domain.EvoFindingProposed || existing.ProposalID != "" || evoFindingQueued(queued, existing.ID) {
			if !sameEvoFindingIntent(f, existing) {
				continue
			}
			f = existing
		} else {
			f = mergeEvoFinding(f, existing)
		}
		matched = true
		break
	}
	if !matched {
		f.ID = "evf-" + randomID()
	}
	if p, open := h.repo.OpenEvoProposalForAsset(f.AssetKind, f.AssetID); open && p.ProjectID == f.ProjectID && p.FindingID == f.ID {
		f.ProposalID = p.ID
	}
	return f, h.repo.PutEvoFinding(f)
}
