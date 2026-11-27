package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"

	"github.com/gin-gonic/gin"
)

const evoNoAssertionsAutoBlockMsg = "该 SOP 未配置结果验收规则，按安全策略仅限人工发布"

// generateSOPAssertions compiles a single natural-language sentence into one or
// more deterministic Layer 2 assertions and immediately backtests each candidate
// against recent clean runs of the SOP.
func (h *evolutionHandlers) generateSOPAssertions(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	var req struct {
		ProjectID string `json:"projectId"`
		Prompt    string `json:"prompt"`
	}
	if !decode(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = strings.TrimSpace(c.Query("projectId"))
	}
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if workflowID == "" || prompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入一句话结果验收描述"})
		return
	}
	runs := h.loadWindowRuns(req.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
	wf, ok := h.resolveSOPWorkflow(req.ProjectID, workflowID, runs)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在或不属于当前项目"})
		return
	}
	sopRuns := filterRunsByWorkflow(runs, wf.ID)

	var candidates []domain.EvoSOPAssertion
	source := "rule"

	// Try LLM compilation when the requesting user has usable model credentials.
	if current, loggedIn := h.requestUser(c.Request, true); loggedIn {
		if user, found := h.repo.User(current.ID); found {
			pol := h.repo.EvoSOPPolicy(req.ProjectID, wf.ID)
			model := strings.TrimSpace(pol.Model)
			if model == "" {
				if proj, ok := h.repo.Project(req.ProjectID); ok && proj.SOPDetection != nil {
					model = strings.TrimSpace(proj.SOPDetection.Model)
				}
			}
			if resolvedUser, resolvedModel, credOK := h.applySOPModelCredentials(c.Request.Context(), user, model, wf, true); credOK {
				sys, userPrompt := evolution.BuildAssertionGenPrompt(wf, prompt, sopRuns)
				ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
				raw, err := currentEvoImprove()(ctx, domain.EvoProposal{ProjectID: req.ProjectID, WorkflowID: wf.ID, Model: resolvedModel}, sys, userPrompt, resolvedUser)
				cancel()
				if err == nil {
					if parsed := evolution.ParseAssertionGenResponse(raw, prompt); len(parsed) > 0 {
						candidates = parsed
						source = "llm"
					}
				}
			}
		}
	}

	if len(candidates) == 0 {
		candidates = evolution.ParseAssertionFromSentence(prompt, sopRuns)
		source = "rule"
	}
	if len(candidates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未能从该描述提取可执行的验收规则，请补充文件名、字段名或期望关键字"})
		return
	}

	results := make([]domain.EvoAssertionBacktest, 0, len(candidates))
	for _, a := range candidates {
		results = append(results, evolution.BacktestAssertion(a, sopRuns))
	}
	c.JSON(http.StatusOK, gin.H{
		"source":     source,
		"candidates": results,
	})
}

// suggestSOPAssertions extracts conservative high-confidence rules from the
// intersection of recent clean historical runs and backtests each one.
func (h *evolutionHandlers) suggestSOPAssertions(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	var req struct {
		ProjectID string `json:"projectId"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = strings.TrimSpace(c.Query("projectId"))
	}
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	runs := h.loadWindowRuns(req.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
	wf, ok := h.resolveSOPWorkflow(req.ProjectID, workflowID, runs)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在或不属于当前项目"})
		return
	}
	sopRuns := filterRunsByWorkflow(runs, wf.ID)
	suggested := evolution.SuggestAssertionsFromRuns(sopRuns)
	results := make([]domain.EvoAssertionBacktest, 0, len(suggested))
	for _, a := range suggested {
		results = append(results, evolution.BacktestAssertion(a, sopRuns))
	}
	c.JSON(http.StatusOK, gin.H{"candidates": results})
}

// putSOPAssertions persists the SOP's declarative Layer 2 outcome assertions.
func (h *evolutionHandlers) putSOPAssertions(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	var req struct {
		ProjectID  string                   `json:"projectId"`
		Assertions []domain.EvoSOPAssertion `json:"assertions"`
	}
	if !decode(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = strings.TrimSpace(c.Query("projectId"))
	}
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 workflowId"})
		return
	}
	wf, ok := h.resolveSOPWorkflow(req.ProjectID, workflowID, nil)
	if !ok {
		runs := h.loadWindowRuns(req.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
		wf, ok = h.resolveSOPWorkflow(req.ProjectID, workflowID, runs)
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在或不属于当前项目"})
		return
	}
	if len(req.Assertions) > 30 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单个 SOP 最多配置 30 条结果验收规则"})
		return
	}
	validated := make([]domain.EvoSOPAssertion, 0, len(req.Assertions))
	for _, raw := range req.Assertions {
		norm, err := evolution.ValidateSOPAssertion(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		validated = append(validated, norm)
	}
	pol := h.repo.EvoSOPPolicy(req.ProjectID, wf.ID)
	pol.ProjectID = req.ProjectID
	pol.WorkflowID = wf.ID
	pol.Preferences = evolution.NormalizeSOPPreferences(pol.Preferences)
	pol.BatchSize = evolution.NormalizeSOPBatchSize(pol.BatchSize)
	pol.Assertions = validated
	pol.UpdatedBy = h.currentUserID(c)
	pol.UpdatedAt = time.Now().UTC()
	if err := h.repo.PutEvoSOPPolicy(pol); err != nil {
		respond(c, err)
		return
	}
	activeCount := len(evolution.ActiveSOPAssertions(validated))
	c.JSON(http.StatusOK, gin.H{
		"assertions":         validated,
		"assertionCount":     activeCount,
		"autoReleaseAllowed": activeCount > 0,
	})
}

// resetSOPBaseline explicitly re-freezes the SOP's Layer 1 score baseline from
// the current clean runs.
func (h *evolutionHandlers) resetSOPBaseline(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	var req struct {
		ProjectID string `json:"projectId"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = strings.TrimSpace(c.Query("projectId"))
	}
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	runs := h.loadWindowRuns(req.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
	wf, ok := h.resolveSOPWorkflow(req.ProjectID, workflowID, runs)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在或不属于当前项目"})
		return
	}
	pol := h.repo.EvoSOPPolicy(req.ProjectID, wf.ID)
	pol.ProjectID = req.ProjectID
	pol.WorkflowID = wf.ID
	pol.Preferences = evolution.NormalizeSOPPreferences(pol.Preferences)
	pol.BatchSize = evolution.NormalizeSOPBatchSize(pol.BatchSize)
	sopRuns := filterRunsByWorkflow(runs, wf.ID)
	// Take the latest clean runs when explicitly resetting baseline.
	latestClean := make([]domain.Run, 0, pol.BatchSize)
	for _, r := range sopRuns {
		if evolution.RunScore(r) == 2 {
			latestClean = append(latestClean, r)
			if len(latestClean) >= pol.BatchSize {
				break
			}
		}
	}
	pol.ScoreBaseline = evolution.ComputeSOPScoreBaseline(latestClean, pol.BatchSize)
	pol.UpdatedBy = h.currentUserID(c)
	pol.UpdatedAt = time.Now().UTC()
	if err := h.repo.PutEvoSOPPolicy(pol); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"scoreBaseline": pol.ScoreBaseline})
}

// proposalSOPPolicy returns the governing SOP policy when a proposal originates
// from an SOP workflow.
func (h *evolutionHandlers) proposalSOPPolicy(p domain.EvoProposal) (domain.EvoSOPPolicy, bool) {
	wfID := strings.TrimSpace(p.WorkflowID)
	if wfID == "" && strings.TrimSpace(p.FindingID) != "" {
		if f, ok := h.repo.EvoFinding(p.FindingID); ok && len(f.WorkflowIDs) > 0 {
			wfID = strings.TrimSpace(f.WorkflowIDs[0])
		}
	}
	if wfID == "" || strings.TrimSpace(p.ProjectID) == "" {
		return domain.EvoSOPPolicy{}, false
	}
	return h.repo.EvoSOPPolicy(p.ProjectID, wfID), true
}

func (h *evolutionHandlers) proposalSOPAssertions(p domain.EvoProposal) []domain.EvoSOPAssertion {
	pol, ok := h.proposalSOPPolicy(p)
	if !ok {
		return nil
	}
	return evolution.ActiveSOPAssertions(pol.Assertions)
}

func (h *evolutionHandlers) proposalSOPBaseline(p domain.EvoProposal) *domain.EvoSOPScoreBaseline {
	pol, ok := h.proposalSOPPolicy(p)
	if !ok {
		return nil
	}
	if pol.ScoreBaseline != nil && pol.ScoreBaseline.BaselineReady {
		return pol.ScoreBaseline
	}
	runs := h.loadWindowRuns(p.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
	return evolution.EffectiveSOPScoreBaseline(pol, filterRunsByWorkflow(runs, pol.WorkflowID))
}

// proposalAllowsAutoRelease enforces Layer 2 safety: an SOP-governed proposal
// with zero configured outcome assertions is restricted to manual release only
// and cannot be auto-started into canary or auto-released under L3.
func (h *evolutionHandlers) proposalAllowsAutoRelease(p domain.EvoProposal) (bool, string) {
	if strings.TrimSpace(p.WorkflowID) == "" {
		return true, ""
	}
	pol := h.repo.EvoSOPPolicy(p.ProjectID, p.WorkflowID)
	if len(evolution.ActiveSOPAssertions(pol.Assertions)) == 0 {
		return false, evoNoAssertionsAutoBlockMsg
	}
	return true, ""
}

// attachSemanticJudge runs Layer 3 swapped A/B AI pairwise comparison in the
// background as a pure reference signal when Layer 2 passed and Layer 1 process
// score delta is close. It never overrides Layer 1 or blocks release.
func (h *evolutionHandlers) attachSemanticJudge(proposalID string, replays []domain.EvoReplay, user domain.User) {
	p, ok := h.repo.EvoProposal(proposalID)
	if !ok || p.SemanticJudge != nil {
		return
	}
	// Pick first completed non-blocked replay with outputs on both sides.
	var oldOut, newOut string
	for _, r := range replays {
		if r.Outcome == "" || r.Outcome == domain.EvoOutcomeBlocked || r.RunID == "" || r.SourceRunID == "" {
			continue
		}
		src, okS := h.repo.RunState(r.SourceRunID)
		rep, okR := h.repo.RunState(r.RunID)
		if !okS || !okR {
			continue
		}
		o := summarizeRunOutputForJudge(src)
		n := summarizeRunOutputForJudge(rep)
		if o != "" && n != "" {
			oldOut, newOut = o, n
			break
		}
	}
	if oldOut == "" || newOut == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	fn := currentEvoImprove()
	sys1, prompt1 := evolution.BuildSemanticJudgePrompt(oldOut, newOut, false, nil)
	raw1, err1 := fn(ctx, p, sys1, prompt1, user)
	if err1 != nil {
		log.Printf("evolution semantic judge r1 %s: %v", proposalID, err1)
		return
	}
	w1, items1, ok1 := evolution.ParseSemanticJudgeResponse(raw1, false)
	if !ok1 {
		return
	}
	sys2, prompt2 := evolution.BuildSemanticJudgePrompt(oldOut, newOut, true, nil)
	raw2, err2 := fn(ctx, p, sys2, prompt2, user)
	if err2 != nil {
		log.Printf("evolution semantic judge r2 %s: %v", proposalID, err2)
		return
	}
	w2, items2, ok2 := evolution.ParseSemanticJudgeResponse(raw2, true)
	if !ok2 {
		return
	}
	cmp := evolution.CombineSemanticJudgeRounds(w1, w2, items1, items2, p.Model)
	latest, ok := h.repo.EvoProposal(proposalID)
	if !ok {
		return
	}
	latest.SemanticJudge = &cmp
	if err := h.repo.PutEvoProposal(latest); err != nil {
		log.Printf("evolution semantic judge persist %s: %v", proposalID, err)
	}
}

func summarizeRunOutputForJudge(run domain.Run) string {
	var sb strings.Builder
	for _, art := range run.Artifacts {
		if strings.TrimSpace(art.Content) == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("[%s]\n%s\n", firstNonEmpty(art.Path, art.Name), domain.TrimEvoText(art.Content, 1200)))
		if sb.Len() >= 2000 {
			break
		}
	}
	if sb.Len() == 0 {
		for _, nr := range run.Nodes {
			if nr != nil && strings.TrimSpace(nr.Output) != "" {
				sb.WriteString(domain.TrimEvoText(nr.Output, 1200))
				sb.WriteString("\n")
				if sb.Len() >= 2000 {
					break
				}
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

func formatFailedAssertionReasons(rep domain.EvoRunAssertionReport) string {
	var parts []string
	for _, res := range rep.Results {
		if !res.Passed {
			parts = append(parts, firstNonEmpty(res.Detail, res.Name))
			if len(parts) >= 2 {
				break
			}
		}
	}
	if len(parts) == 0 {
		return "未通过配置的结果断言"
	}
	return strings.Join(parts, "；")
}
