package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
	"agentflow/internal/repo"
	"agentflow/internal/runeval"

	"github.com/gin-gonic/gin"
)

const (
	sopDetectionInterval = 10 * time.Second
	sopDetectionTimeout  = 3 * time.Minute
	sopDetectionLease    = sopDetectionTimeout + time.Minute
)

func (h *evolutionHandlers) putSOPDetection(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择项目"})
		return
	}
	if !h.requireProjectReader(c, projectID) || !h.requireProjectWriter(c, projectID) {
		return
	}
	project, ok := h.repo.Project(projectID)
	if !ok || project.ArchivedAt != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return
	}
	var setting domain.EvoSOPDetectionSetting
	if !decode(c, &setting) {
		return
	}
	config := domain.SOPDetectionConfig{}
	if setting.Enabled {
		current, loggedIn := h.requestUser(c.Request, true)
		if !loggedIn {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请登录并选择检测模型"})
			return
		}
		user, ok := h.repo.User(current.ID)
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "配置账号不可用"})
			return
		}
		if assistantModelUnspecified(setting.Model) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请选择检测模型"})
			return
		}
		model, err := resolveFeatureModel(user, setting.Model)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		config = domain.SOPDetectionConfig{Enabled: true, Model: model, EnabledAt: time.Now().UTC(), ConfiguredBy: user.ID}
	} else if project.SOPDetection != nil {
		config.Model = project.SOPDetection.Model
	}
	if err := h.repo.PutSOPDetection(projectID, config); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, domain.EvoSOPDetectionSetting{Enabled: config.Enabled, Model: config.Model})
}

type sopTraceEvaluator func(context.Context, string, domain.SOPDetectionConfig, domain.Run) (domain.RunEvalResult, error)

func (h *evolutionHandlers) monitorSOPDetection(ctx context.Context, recovery <-chan struct{}) {
	if recovery != nil {
		select {
		case <-ctx.Done():
			return
		case <-recovery:
		}
	}
	ticker := time.NewTicker(sopDetectionInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		h.sweepSOPDetections(ctx, h.evaluateSOPTrace)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *evolutionHandlers) sweepSOPDetections(ctx context.Context, evaluate sopTraceEvaluator) {
	projects, err := h.repo.SOPDetectionProjects()
	if err != nil {
		log.Printf("SOP detection projects: %v", err)
		return
	}
	scannedProjects := map[string]bool{}
	for _, project := range projects {
		if ctx.Err() != nil {
			return
		}
		if project.Config.EnabledAt.IsZero() {
			continue
		}
		scannedProjects[project.ID] = true
		var disabledWFIDs []string
		for wfID, pol := range h.repo.EvoSOPPolicies(project.ID) {
			if !pol.Enabled && strings.TrimSpace(wfID) != "" {
				disabledWFIDs = append(disabledWFIDs, wfID)
			}
		}
		ids, err := h.repo.PendingSOPDetectionsExcludingWorkflows(project.ID, disabledWFIDs, project.Config.EnabledAt, time.Now().UTC().Add(-sopDetectionLease), 2)
		if err != nil {
			log.Printf("SOP detection queue %s: %v", project.ID, err)
			continue
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			if err := h.detectSOPRun(ctx, project, id, evaluate); err != nil {
				log.Printf("SOP detection run %s: %v", id, err)
			}
		}
	}

	// Also auto-detect runs for individually enabled SOP policies without requiring
	// the legacy project-level SOP detection toggle.
	staleBefore := time.Now().UTC().Add(-sopDetectionLease)
	since := time.Now().UTC().AddDate(0, 0, -evoDefaultWindowDays)
	policiesByProject := map[string]map[string]domain.EvoSOPPolicy{}
	for _, policy := range h.repo.AllEnabledEvoSOPPolicies() {
		if scannedProjects[policy.ProjectID] {
			continue
		}
		if policiesByProject[policy.ProjectID] == nil {
			policiesByProject[policy.ProjectID] = map[string]domain.EvoSOPPolicy{}
		}
		policiesByProject[policy.ProjectID][policy.WorkflowID] = policy
	}
	for projectID, wfPolicies := range policiesByProject {
		if ctx.Err() != nil {
			return
		}
		wfIDs := make([]string, 0, len(wfPolicies))
		for wfID := range wfPolicies {
			wfIDs = append(wfIDs, wfID)
		}
		if len(wfIDs) == 0 {
			continue
		}
		ids, err := h.repo.PendingSOPDetectionsForWorkflows(projectID, wfIDs, since, staleBefore, 4)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			run, ok := h.repo.RunState(id)
			if !ok || strings.TrimSpace(run.WorkflowID) == "" {
				continue
			}
			policy, enabled := wfPolicies[run.WorkflowID]
			if !enabled || !policy.Enabled {
				continue
			}
			wf, _ := h.resolveSOPWorkflow(projectID, run.WorkflowID, []domain.Run{run})
			user, resolvedModel, ok := h.resolveSOPActorUser(policy, wf)
			if !ok {
				continue
			}
			model := strings.TrimSpace(policy.Model)
			if model == "" {
				model = strings.TrimSpace(resolvedModel)
			}
			if model == "" {
				if proj, ok := h.repo.Project(projectID); ok && proj.SOPDetection != nil {
					model = strings.TrimSpace(proj.SOPDetection.Model)
				}
			}
			if model == "" {
				continue
			}
			cfg := domain.SOPDetectionConfig{Enabled: true, Model: model, EnabledAt: since, ConfiguredBy: user.ID}
			if err := h.detectSOPRunWithConfig(ctx, projectID, cfg, run, evaluate); err != nil {
				log.Printf("SOP policy detection run %s: %v", id, err)
			}
		}
	}
}

func (h *evolutionHandlers) detectSOPRun(ctx context.Context, project repo.SOPDetectionProject, runID string, evaluate sopTraceEvaluator) error {
	latest, ok := h.repo.Project(project.ID)
	if !ok || latest.ArchivedAt != nil || latest.SOPDetection == nil || *latest.SOPDetection != project.Config {
		return nil
	}
	run, ok := h.repo.RunState(runID)
	if !ok || run.IsWorkspace() || run.HistoricalGroup() || run.ExecutionContext.IsEvolutionReplay() ||
		(run.Status != "succeeded" && run.Status != "failed") || h.runProject(run) != project.ID {
		return nil
	}
	cfg := project.Config
	if strings.TrimSpace(run.WorkflowID) != "" {
		if policy, stored := h.repo.EvoSOPPolicyStored(project.ID, run.WorkflowID); stored {
			if !policy.Enabled {
				return nil
			}
			if strings.TrimSpace(policy.Model) != "" {
				cfg.Model = strings.TrimSpace(policy.Model)
			}
		}
	}
	return h.detectSOPRunWithConfig(ctx, project.ID, cfg, run, evaluate)
}

func (h *evolutionHandlers) detectSOPRunWithConfig(ctx context.Context, projectID string, cfg domain.SOPDetectionConfig, run domain.Run, evaluate sopTraceEvaluator) error {
	if run.IsWorkspace() || run.HistoricalGroup() || run.ExecutionContext.IsEvolutionReplay() ||
		(run.Status != "succeeded" && run.Status != "failed") || h.runProject(run) != projectID {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := domain.RunEvalRecord{
		ID: repo.SOPDetectionEvalID(run.ID), RunID: run.ID, SOPDetection: true,
		Status: domain.RunEvalRunning, Model: cfg.Model, CreatedBy: cfg.ConfiguredBy, CreatedAt: now,
	}
	claimed, err := h.repo.ClaimSOPDetection(rec, now.Add(-sopDetectionLease))
	if err != nil || !claimed {
		return err
	}
	scanCtx, cancel := context.WithTimeout(ctx, sopDetectionTimeout)
	defer cancel()
	rec.Result, err = evaluate(scanCtx, projectID, cfg, run)
	if ctx.Err() != nil {
		// The claim expires so a new instance can resume after shutdown.
		return nil
	}
	rec.FinishedAt = time.Now().UTC()
	rec.Status = domain.RunEvalSucceeded
	rec.Score, rec.Passed = rec.Result.Score, rec.Result.Passed
	if err == nil && !rec.Passed {
		// Publish before settling the claim so interrupted work is retried.
		// A failed publication leaves an expiring claim and can be reconciled.
		if err := h.publishSOPDetection(projectID, run, rec); err != nil {
			return err
		}
	}
	if err != nil {
		rec.Status = domain.RunEvalFailed
		rec.Error = domain.TrimEvoText(err.Error(), 400)
	}
	return h.repo.FinishSOPDetection(rec)
}

func (h *evolutionHandlers) evaluateSOPTrace(ctx context.Context, projectID string, cfg domain.SOPDetectionConfig, run domain.Run) (domain.RunEvalResult, error) {
	user, exists := h.repo.User(cfg.ConfiguredBy)
	if !exists || !h.speakerProjectWriter(projectID, user, cfg.ConfiguredBy) {
		return domain.RunEvalResult{}, fmt.Errorf("检测配置账号已失去项目权限")
	}
	model, err := resolveFeatureModel(user, cfg.Model)
	if err != nil {
		return domain.RunEvalResult{}, err
	}
	tools, err := h.repo.SOPDetectionTools(run.ID, 2000)
	if err != nil {
		return domain.RunEvalResult{}, err
	}
	evidence := runeval.SOPTraceCandidates(run, tools)
	system, prompt := runeval.SOPTracePrompt(run.Status, evidence)
	raw, _, err := runAssistantCLI(ctx, assistantCLIRequest{
		Prompt: prompt, SystemPrompt: system, Model: model, UserID: user.ID, ReviewOnly: true,
	}, user, func(assistantStreamEvent) {})
	if err != nil {
		return domain.RunEvalResult{}, fmt.Errorf("检测模型调用失败，请检查模型、执行器及账号配置")
	}
	return runeval.ParseSOPTraceResult(raw, evidence)
}

func (h *evolutionHandlers) publishSOPDetection(projectID string, run domain.Run, rec domain.RunEvalRecord) error {
	h.findingsM.Lock()
	defer h.findingsM.Unlock()
	days := evoDefaultWindowDays
	runs := h.loadWindowRuns(projectID, days, evoMaxDiagnoseRuns)
	included := false
	for _, item := range runs {
		included = included || item.ID == run.ID
	}
	if !included {
		runs = append(runs, run)
	}
	input := h.diagnoseInput(projectID, days, runs, h.repo.EvoLevel)
	input.SOPDetections[run.ID] = rec
	findings := h.repo.EvoFindings(projectID, domain.EvoFindingOpen, domain.EvoFindingProposed)
	queue := h.repo.EvoQueue(projectID)
	for _, finding := range evolution.Diagnose(input) {
		detected := slices.ContainsFunc(finding.Clusters, func(cluster domain.EvoSignalCluster) bool {
			return cluster.Kind == evolution.SignalSOPDetection && slices.ContainsFunc(cluster.Occurrences, func(item domain.EvoClusterOccurrence) bool {
				return item.RunID == run.ID
			})
		})
		if detected {
			if _, err := h.saveDiagnosedFinding(finding, findings, queue); err != nil {
				return err
			}
		}
	}
	return nil
}
