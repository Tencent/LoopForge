package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runeval"

	"github.com/gin-gonic/gin"
)

const (
	runEvalTimeout     = 10 * time.Minute
	runEvalMaxInflight = 2
)

type runEvalExecFunc func(ctx context.Context, rec domain.RunEvalRecord, run domain.Run, user domain.User, emit func(string, any)) (domain.RunEvalResult, error)

type runEvalJobEvent struct {
	Seq     int
	Type    string
	Payload any
}

type runEvalJob struct {
	runID  string
	evalID string
	mu     sync.Mutex
	seq    int
	events []runEvalJobEvent
	subs   map[chan runEvalJobEvent]struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

type runEvalHub struct {
	mu     sync.Mutex
	jobs   map[string]*runEvalJob
	tokens chan struct{}
	exec   runEvalExecFunc
}

var (
	runEvalExecMu       sync.Mutex
	runEvalExecOverride runEvalExecFunc
)

func setRunEvalExecForTest(fn runEvalExecFunc) {
	runEvalExecMu.Lock()
	runEvalExecOverride = fn
	runEvalExecMu.Unlock()
}

func currentRunEvalExec() runEvalExecFunc {
	runEvalExecMu.Lock()
	defer runEvalExecMu.Unlock()
	if runEvalExecOverride != nil {
		return runEvalExecOverride
	}
	return nil
}

func newRunEvalHub() *runEvalHub {
	return &runEvalHub{jobs: map[string]*runEvalJob{}, tokens: make(chan struct{}, runEvalMaxInflight)}
}

func (h *runsHandlers) evalHub() *runEvalHub {
	if h.evalJobs == nil {
		h.evalJobs = newRunEvalHub()
	}
	return h.evalJobs
}

func (h *runsHandlers) startRunEval(c *gin.Context) {
	id := c.Param("id")
	run, ok := h.repo.Run(id)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	if !domain.TerminalRunStatus(run.Status) {
		c.JSON(http.StatusConflict, map[string]string{"error": "请等 SOP 跑完后再评测"})
		return
	}
	current, loggedIn := h.requestUser(c.Request, true)
	if !loggedIn {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后评测"})
		return
	}
	user, ok := h.repo.User(current.ID)
	if !ok || strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "请先在个人设置中配置自己的 CodeBuddy API Key"})
		return
	}
	if rec, busy := h.repo.ActiveRunEval(id); busy {
		hub := h.evalHub()
		hub.mu.Lock()
		job := hub.jobs[id]
		hub.mu.Unlock()
		if job != nil {
			c.JSON(http.StatusConflict, map[string]string{"error": "这次运行已有评测在进行"})
			return
		}
		// The row was saved as running, then startup failed before a job existed.
		h.failRunEval(rec, "上次评测没有真正启动")
	}
	var req struct {
		Model string `json:"model"`
	}
	if c.Request.ContentLength > 0 && !decode(c, &req) {
		return
	}
	catalog := loadAssistantModels(c.Request.Context(), user.CodeBuddyInternetEnvironment)
	model := assistantAllowedModel(req.Model, catalog)
	project, _ := h.repo.Project(run.ExecutionContext.ProjectID)
	rec := domain.RunEvalRecord{
		ID:        "eval-" + randomID(),
		RunID:     id,
		Status:    domain.RunEvalRunning,
		Model:     model,
		Policy:    domain.EffectiveRunEvalPolicy(project.RunEval),
		CreatedBy: user.ID,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.repo.PutRunEval(rec); err != nil {
		respond(c, err)
		return
	}
	h.launchRunEval(rec, run, user)
	c.JSON(http.StatusAccepted, rec)
}

func (h *runsHandlers) getRunEval(c *gin.Context) {
	id := c.Param("id")
	if !h.repo.RunExists(id) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	if rec, ok := h.repo.LatestRunEval(id); ok {
		c.JSON(http.StatusOK, rec)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"runId": id, "status": ""})
}

func (h *runsHandlers) listRunEvals(c *gin.Context) {
	id := c.Param("id")
	if !h.repo.RunExists(id) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	c.JSON(http.StatusOK, h.repo.ListRunEvals(id))
}

func (h *runsHandlers) streamRunEval(c *gin.Context) {
	id := c.Param("id")
	if !h.repo.RunExists(id) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	hub := h.evalHub()
	hub.mu.Lock()
	job := hub.jobs[id]
	hub.mu.Unlock()
	if job == nil {
		if rec, ok := h.repo.LatestRunEval(id); ok {
			c.Header("Content-Type", "text/event-stream; charset=utf-8")
			c.Status(http.StatusOK)
			writeSSE(c, "done", rec)
			return
		}
		c.JSON(http.StatusNotFound, map[string]string{"error": "eval not found"})
		return
	}
	ch, replay := job.subscribe()
	defer job.unsubscribe(ch)
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	for _, ev := range replay {
		writeSSE(c, ev.Type, ev.Payload)
	}
	notify := c.Request.Context().Done()
	for {
		select {
		case <-notify:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(c, ev.Type, ev.Payload)
			if ev.Type == "done" || ev.Type == "error" {
				return
			}
		}
	}
}

func writeSSE(c *gin.Context, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, raw)
	if f, ok := c.Writer.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *runsHandlers) applyRunEval(c *gin.Context) {
	id := c.Param("id")
	run, ok := h.repo.Run(id)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	if _, loggedIn := h.requestUser(c.Request, true); !loggedIn {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "请先登录后再应用评测建议"})
		return
	}
	var req struct {
		EvalID            string   `json:"evalId"`
		RecommendationIDs []string `json:"recommendationIds"`
	}
	if !decode(c, &req) {
		return
	}
	rec, ok := h.repo.LatestRunEval(id)
	if req.EvalID != "" {
		rec, ok = h.repo.RunEval(req.EvalID)
	}
	if !ok || rec.RunID != id {
		c.JSON(http.StatusNotFound, map[string]string{"error": "eval not found"})
		return
	}
	if rec.Status != domain.RunEvalSucceeded {
		c.JSON(http.StatusConflict, map[string]string{"error": "评测尚未完成"})
		return
	}
	wanted := map[string]bool{}
	for _, item := range req.RecommendationIDs {
		wanted[strings.TrimSpace(item)] = true
	}
	if len(wanted) == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "请选择要应用的建议"})
		return
	}
	projectID := run.ExecutionContext.ProjectID
	changed := false
	for i, item := range rec.Result.Recommendations {
		if !wanted[item.ID] {
			continue
		}
		if item.Status == domain.RunEvalRecApplied {
			continue
		}
		if !item.Applyable() {
			rec.Result.Recommendations[i].ApplyError = "这条建议只能查看，不能一键应用"
			changed = true
			continue
		}
		applied, err := h.applyOneRecommendation(run, projectID, item)
		if err != nil {
			rec.Result.Recommendations[i].ApplyError = err.Error()
			changed = true
			continue
		}
		now := time.Now().UTC()
		applied.Status = domain.RunEvalRecApplied
		applied.AppliedAt = &now
		applied.ApplyError = ""
		rec.Result.Recommendations[i] = applied
		changed = true
	}
	if changed {
		if err := h.repo.PutRunEval(rec); err != nil {
			respond(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, rec)
}

func (h *runsHandlers) applyOneRecommendation(run domain.Run, projectID string, rec domain.RunEvalRecommendation) (domain.RunEvalRecommendation, error) {
	action := domain.NormalizeRunEvalAction(rec.Target, rec.Action)
	switch {
	case strings.HasPrefix(action, "workflow."):
		wf, err := h.ensureProjectWorkflow(run, projectID, firstNonEmpty(rec.TargetID, run.WorkflowID))
		if err != nil {
			return rec, err
		}
		if err := runeval.ApplyRecommendation(rec, &wf, nil); err != nil {
			return rec, err
		}
		if err := h.repo.PutWorkflow(wf); err != nil {
			return rec, err
		}
		rec.AppliedTargetID = wf.ID
		return rec, nil
	case strings.HasPrefix(action, "agent."):
		agentID := firstNonEmpty(rec.TargetID, agentIDFromRun(run, rec.NodeID))
		agent, err := h.ensureProjectAgent(run, projectID, agentID)
		if err != nil {
			return rec, err
		}
		if err := runeval.ApplyRecommendation(rec, nil, &agent); err != nil {
			return rec, err
		}
		if err := h.repo.PutAgent(agent); err != nil {
			return rec, err
		}
		rec.AppliedTargetID = agent.ID
		return rec, nil
	default:
		return rec, fmt.Errorf("unsupported action")
	}
}

func (h *runsHandlers) ensureProjectWorkflow(run domain.Run, projectID, workflowID string) (domain.WorkflowDefinition, error) {
	wf, ok := h.repo.Workflow(workflowID)
	if !ok {
		if run.WorkflowSnapshot.ID != "" {
			wf, ok = run.WorkflowSnapshot, true
		}
	}
	if !ok {
		return domain.WorkflowDefinition{}, fmt.Errorf("SOP %q 不存在", workflowID)
	}
	if strings.TrimSpace(wf.Name) == "" {
		wf.Name = firstNonEmpty(wf.ID, "workflow")
	}
	if !wf.Generic && (wf.ProjectID == "" || wf.ProjectID == projectID) {
		return wf, nil
	}
	cloneID := runeval.CloneID(wf.ID, firstNonEmpty(projectID, "copy"))
	if existing, exists := h.repo.Workflow(cloneID); exists {
		return existing, nil
	}
	wf.ID = cloneID
	wf.Name = strings.TrimSpace(wf.Name) + "（评测副本）"
	wf.ProjectID = projectID
	wf.Generic = false
	wf.Shared = false
	wf.Status = "draft"
	wf.Revision = 0
	wf.CreatedAt, wf.UpdatedAt = time.Time{}, time.Time{}
	if err := h.repo.PutWorkflow(wf); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	stored, _ := h.repo.Workflow(cloneID)
	return stored, nil
}

func (h *runsHandlers) ensureProjectAgent(run domain.Run, projectID, agentID string) (domain.AgentDefinition, error) {
	agent, ok := h.repo.Agent(agentID)
	if !ok {
		return domain.AgentDefinition{}, fmt.Errorf("角色 %q 不存在", agentID)
	}
	if !agent.Shared && (agent.ProjectID == "" || agent.ProjectID == projectID) {
		return agent, nil
	}
	cloneID := runeval.CloneID(agent.ID, firstNonEmpty(projectID, "copy"))
	if existing, exists := h.repo.Agent(cloneID); exists {
		return existing, nil
	}
	agent.ID = cloneID
	agent.Name = strings.TrimSpace(agent.Name) + "（评测副本）"
	agent.ProjectID = projectID
	agent.Shared = false
	agent.Version = 1
	agent.UpdatedAt = time.Time{}
	if err := h.repo.PutAgent(agent); err != nil {
		return domain.AgentDefinition{}, err
	}
	if wf, err := h.ensureProjectWorkflow(run, projectID, run.WorkflowID); err == nil {
		changed := false
		for i, node := range wf.Nodes {
			if node.AgentID == agentID {
				wf.Nodes[i].AgentID = cloneID
				changed = true
			}
		}
		if changed {
			_ = h.repo.PutWorkflow(wf)
		}
	}
	stored, _ := h.repo.Agent(cloneID)
	return stored, nil
}

func agentIDFromRun(run domain.Run, nodeID string) string {
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID == nodeID {
			return node.AgentID
		}
	}
	return ""
}

func (h *runsHandlers) launchRunEval(rec domain.RunEvalRecord, run domain.Run, user domain.User) {
	hub := h.evalHub()
	ctx, cancel := context.WithTimeout(context.Background(), runEvalTimeout)
	job := &runEvalJob{
		runID:  rec.RunID,
		evalID: rec.ID,
		subs:   map[chan runEvalJobEvent]struct{}{},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	hub.mu.Lock()
	if old := hub.jobs[rec.RunID]; old != nil && old.cancel != nil {
		old.cancel()
	}
	hub.jobs[rec.RunID] = job
	hub.mu.Unlock()
	go h.runEvalJob(ctx, job, rec, run, user)
}

func (h *runsHandlers) runEvalJob(ctx context.Context, job *runEvalJob, rec domain.RunEvalRecord, run domain.Run, user domain.User) {
	defer close(job.done)
	defer func() {
		hub := h.evalHub()
		hub.mu.Lock()
		if hub.jobs[job.runID] == job {
			delete(hub.jobs, job.runID)
		}
		hub.mu.Unlock()
	}()
	hub := h.evalHub()
	select {
	case hub.tokens <- struct{}{}:
		defer func() { <-hub.tokens }()
	case <-ctx.Done():
		h.failRunEval(rec, "评测排队超时")
		job.publish("error", map[string]string{"error": "评测排队超时"})
		return
	}
	emit := func(event string, payload any) { job.publish(event, payload) }
	emit("thought", map[string]string{"kind": "thinking", "text": "正在整理本次 SOP 轨迹…"})
	exec := currentRunEvalExec()
	if exec == nil {
		exec = h.executeRunEval
	}
	result, err := exec(ctx, rec, run, user, emit)
	if err != nil {
		h.failRunEval(rec, err.Error())
		job.publish("error", map[string]string{"error": err.Error()})
		return
	}
	if saved, ok := h.repo.RunEval(rec.ID); ok {
		rec = saved
	}
	rec.Status = domain.RunEvalSucceeded
	rec.Score = result.Score
	rec.Passed = result.Passed
	rec.Result = result
	rec.FinishedAt = time.Now().UTC()
	if err := h.repo.PutRunEval(rec); err != nil {
		h.failRunEval(rec, err.Error())
		job.publish("error", map[string]string{"error": err.Error()})
		return
	}
	job.publish("done", rec)
}

func (h *runsHandlers) executeRunEval(ctx context.Context, rec domain.RunEvalRecord, run domain.Run, user domain.User, emit func(string, any)) (domain.RunEvalResult, error) {
	catalog := runeval.BuildCatalog(run, func(id string) (domain.AgentDefinition, bool) {
		return h.repo.Agent(id)
	})
	facts := runeval.BuildFacts(run, catalog)
	rec.Facts = facts
	_ = h.repo.PutRunEval(rec)
	emit("thought", map[string]string{"kind": "thinking", "text": "正在用 " + rec.Model + " 评测…"})
	system, prompt := runeval.BuildPrompt(rec.Policy, facts)
	raw, _, err := runAssistantCLI(ctx, assistantCLIRequest{Prompt: prompt, Model: rec.Model, SystemPrompt: system, UserID: user.ID}, user, func(ev assistantStreamEvent) {
		if emit == nil {
			return
		}
		if text := strings.TrimSpace(ev.Text); text != "" {
			emit("thought", map[string]string{"kind": ev.Kind, "text": text})
		}
	})
	if err != nil {
		return domain.RunEvalResult{}, err
	}
	result, err := runeval.ParseModelResult(raw, rec.Policy)
	if err != nil {
		return domain.RunEvalResult{}, err
	}
	return result, nil
}

func (h *runsHandlers) failRunEval(rec domain.RunEvalRecord, message string) {
	rec.Status = domain.RunEvalFailed
	rec.Error = message
	rec.FinishedAt = time.Now().UTC()
	if err := h.repo.PutRunEval(rec); err != nil {
		log.Printf("run eval persist failure %s: %v", rec.ID, err)
	}
}

func (j *runEvalJob) publish(event string, payload any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	item := runEvalJobEvent{Seq: j.seq, Type: event, Payload: payload}
	j.events = append(j.events, item)
	for ch := range j.subs {
		select {
		case ch <- item:
		default:
		}
	}
}

func (j *runEvalJob) subscribe() (chan runEvalJobEvent, []runEvalJobEvent) {
	ch := make(chan runEvalJobEvent, 16)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.subs[ch] = struct{}{}
	return ch, append([]runEvalJobEvent(nil), j.events...)
}

func (j *runEvalJob) unsubscribe(ch chan runEvalJobEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.subs, ch)
	close(ch)
}
