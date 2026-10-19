package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

const sopHandoffLease = 30 * time.Second

var errSOPHandoffLeaseLost = errors.New("交接任务已由其他实例接手")

func handoffJobActive(j domain.SOPHandoffJob) bool {
	return j.Status == domain.SOPHandoffJobPending || j.Status == domain.SOPHandoffJobRunning
}

func (h *runsHandlers) beginSOPHandoffJob(c *gin.Context, run domain.Run, nodeID string, actor int64) {
	if old, ok, err := h.repo.SOPHandoffJob(run.ID); err != nil {
		respond(c, err)
		return
	} else if ok && handoffJobActive(old) {
		c.JSON(202, gin.H{"job": old})
		return
	}
	if h.assistant == nil {
		c.JSON(503, gin.H{"error": "Main Bot 交接服务不可用"})
		return
	}
	if err := h.engine.CheckGroupSOPHandoffReady(run.ID, nodeID); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	name := nodeID
	wf := run.WorkflowSnapshot
	if wf.SOPCheckpoint != nil {
		wf = wf.SOPCheckpoint.Workflow
	}
	for _, n := range wf.Nodes {
		if n.ID == nodeID && n.Name != "" {
			name = n.Name
		}
	}
	job, _, err := h.repo.BeginSOPHandoffJob(domain.SOPHandoffJob{ID: "sop-" + newAssistantSessionID(), RunID: run.ID, NodeID: nodeID, NodeName: name, ActorID: actor, CreatedAt: time.Now().UTC()})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(202, gin.H{"job": job})
}

func (h *runsHandlers) sopHandoffJob(c *gin.Context) {
	r, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	if _, ok = h.groupUser(c, r.ExecutionContext.ProjectID); !ok {
		return
	}
	job, exists, err := h.repo.SOPHandoffJob(r.ID)
	if err != nil {
		respond(c, err)
		return
	}
	if !exists {
		c.JSON(200, gin.H{"job": nil})
		return
	}
	if job.Status == domain.SOPHandoffJobFailed && job.Phase == domain.SOPHandoffPhaseStarting {
		if changed, err := h.repo.CompleteObservedSOPHandoffJob(r.ID, job.ID, time.Now().UTC()); err != nil {
			respond(c, err)
			return
		} else if changed {
			job, _, _ = h.repo.SOPHandoffJob(r.ID)
		}
	}
	c.JSON(200, gin.H{"job": job})
}
func (h *runsHandlers) sopHandoffJobs(c *gin.Context) {
	project := strings.TrimSpace(c.Query("projectId"))
	if project == "" {
		c.JSON(200, gin.H{"jobs": []domain.SOPHandoffJob{}})
		return
	}
	if _, ok := h.groupUser(c, project); !ok {
		return
	}
	jobs, err := h.repo.SOPHandoffJobsForProject(project)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"jobs": jobs})
}
func (h *runsHandlers) retrySOPHandoffJob(c *gin.Context) {
	r, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	uid, allowed := h.groupUser(c, r.ExecutionContext.ProjectID)
	if !allowed {
		return
	}
	job, exists, err := h.repo.SOPHandoffJob(r.ID)
	if err != nil {
		respond(c, err)
		return
	}
	if !exists {
		c.JSON(404, gin.H{"error": "交接任务不存在"})
		return
	}
	if job.Status == domain.SOPHandoffJobFailed && job.Phase == domain.SOPHandoffPhaseStarting && r.WorkflowSnapshot.CollaborationMode == "group" {
		h.beginSOPHandoffJob(c, r, job.NodeID, uid)
		return
	}
	if job.Status == domain.SOPHandoffJobFailed {
		if job.Phase == domain.SOPHandoffPhaseStarting {
			if changed, err := h.repo.CompleteObservedSOPHandoffJob(r.ID, job.ID, time.Now().UTC()); err != nil {
				respond(c, err)
				return
			} else if changed {
				job, _, _ = h.repo.SOPHandoffJob(r.ID)
				c.JSON(200, gin.H{"job": job})
				return
			}
		} else if err := h.engine.CheckGroupSOPHandoffReady(r.ID, job.NodeID); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		retried, changed, err := h.repo.RetrySOPHandoffJob(r.ID, job.ID, time.Now().UTC(), uid)
		if err != nil {
			respond(c, err)
			return
		}
		if changed {
			job = retried
		} else {
			job, _, _ = h.repo.SOPHandoffJob(r.ID)
		}
	}
	c.JSON(202, gin.H{"job": job})
}

// Work is claimed from SQL and belongs to the API lifecycle, never the HTTP
// request. A new API instance resumes expired work after a crash.
func (h *runsHandlers) startSOPHandoffJobs(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	slots := make(chan struct{}, 2)
	var workers sync.WaitGroup
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			jobs, err := h.repo.RunnableSOPHandoffJobs(time.Now().UTC(), 20)
			if err != nil && ctx.Err() == nil {
				log.Printf("handoff sweep: %v", err)
			}
			for _, job := range jobs {
				if ctx.Err() != nil {
					break
				}
				select {
				case slots <- struct{}{}:
				default:
					continue
				}
				claimed, ok, err := h.repo.ClaimSOPHandoffJob(job.RunID, job.ID, newAssistantSessionID(), time.Now().UTC(), sopHandoffLease)
				if err != nil || !ok {
					<-slots
					continue
				}
				workers.Add(1)
				go func(j domain.SOPHandoffJob) {
					defer workers.Done()
					defer func() { <-slots }()
					h.executeSOPHandoffJob(ctx, j)
				}(claimed)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done; workers.Wait() }) }
}

func sopHandoffModelSource(raw string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) == nil {
		delete(fields, "capturedAt")
		data, _ := json.Marshal(fields)
		raw = string(data)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}
func sopHandoffPreview(body string) string {
	lines := []string{}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "runId") || strings.Contains(line, "resumeNode") || strings.Contains(line, "运行信息") || strings.HasPrefix(line, "运行：") || strings.HasPrefix(line, "继续节点：") || strings.Contains(line, "资料截取时间") {
			continue
		}
		line = strings.NewReplacer("**", "", "`", "", "__", "").Replace(line)
		if strings.HasSuffix(line, "：") || strings.HasSuffix(line, ":") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		lines = append(lines, line)
		if len(lines) == 3 {
			break
		}
	}
	text := strings.Join(lines, " ")
	r := []rune(text)
	if len(r) > 260 {
		text = string(r[:260]) + "…"
	}
	return text
}

func (h *runsHandlers) executeSOPHandoffJob(parent context.Context, job domain.SOPHandoffJob) {
	ctx, cancel := context.WithTimeout(parent, 6*time.Minute)
	defer cancel()
	heartbeatDone := make(chan struct{})
	defer func() { cancel(); <-heartbeatDone }()
	go func() {
		defer close(heartbeatDone)
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				now := time.Now().UTC()
				ok, err := h.repo.RenewSOPHandoffJob(job.RunID, job.ID, job.Owner, now, now.Add(sopHandoffLease))
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	update := func(phase string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		job.Phase = phase
		job.Error = ""
		ok, err := h.repo.UpdateSOPHandoffJob(job, job.Owner, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return errSOPHandoffLeaseLost
		}
		return nil
	}
	operationCtx := engine.WithSOPHandoffOperation(ctx, job.ID, update)
	var workErr error
	run, exists := h.repo.RunState(job.RunID)
	ref := run.WorkflowSnapshot.GroupSOPHandoff
	saved := exists && ref != nil && ref.ID == job.ID && run.WorkflowSnapshot.CollaborationMode != "group"
	if !exists {
		workErr = fmt.Errorf("任务不存在")
	} else if !h.sopHandoffActorAllowed(job.ActorID, run.ExecutionContext.ProjectID) {
		workErr = fmt.Errorf("交接发起人已无权访问当前项目，请由有权限的成员重试")
	} else if !saved {
		if job.Phase == domain.SOPHandoffPhaseStarting {
			workErr = fmt.Errorf("原交接上下文已被替换，请重新选择 SOP 交接")
		} else if h.assistant == nil {
			workErr = fmt.Errorf("Main Bot 交接服务不可用")
		} else {
			workErr = h.engine.RestoreGroupSOPWithHandoff(operationCtx, job.RunID, job.NodeID, job.ActorID, func(callCtx context.Context, r domain.Run, s domain.AssistantSession, raw string) (string, error) {
				digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s", job.ActorID, s.Model, sopHandoffModelSource(raw)))))
				if job.Summary != "" && job.ModelSourceSHA == digest {
					if err := update(domain.SOPHandoffPhaseSaving); err != nil {
						return "", err
					}
					return job.Summary, nil
				}
				var brief string
				summary, err := h.assistant.generateGroupSOPHandoff(context.WithValue(callCtx, sopHandoffBriefKey{}, &brief), r, s, raw)
				if err != nil {
					return "", err
				}
				job.Summary = summary
				job.ModelSourceSHA = digest
				job.SummaryPreview = sopHandoffPreview(brief)
				if job.SummaryPreview == "" {
					job.SummaryPreview = sopHandoffPreview(summary)
				}
				if err := update(domain.SOPHandoffPhaseSaving); err != nil {
					return "", err
				}
				return summary, nil
			})
		}
	}
	if workErr == nil {
		run, exists = h.repo.RunState(job.RunID)
		ref = run.WorkflowSnapshot.GroupSOPHandoff
		if !exists || ref == nil || ref.ID != job.ID {
			workErr = fmt.Errorf("交接归档未保存，请重新整理")
		} else {
			job.SummaryPath = ref.SummaryPath
			job.SummaryArtifactID = ref.SummaryArtifactID
			if workErr = update(domain.SOPHandoffPhaseStarting); workErr == nil {
				if run.WorkflowSnapshot.CollaborationMode == "group" {
					workErr = fmt.Errorf("任务已回到自由协作，请重新整理")
				} else if run.PauseRequested && run.Status == "waiting" {
					workErr = h.engine.ResumePausedContext(ctx, job.RunID, "")
					if workErr != nil {
						fresh, _ := h.repo.RunState(job.RunID)
						if sopHandoffAlreadyStarted(fresh, job.ID) {
							workErr = nil
						}
					}
				} else if !sopHandoffAlreadyStarted(run, job.ID) {
					workErr = fmt.Errorf("SOP 当前状态不能自动继续，请查看运行详情")
				}
			}
		}
	}
	// Shutdown/lease loss leave recoverable work, not a false permanent failure.
	if parent.Err() != nil || errors.Is(workErr, errSOPHandoffLeaseLost) {
		return
	}
	if errors.Is(workErr, repo.ErrWorkspaceInUse) {
		return
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return
	}
	job.Status = domain.SOPHandoffJobSucceeded
	job.Phase = domain.SOPHandoffPhaseDone
	job.Error = ""
	if workErr != nil {
		job.Status = domain.SOPHandoffJobFailed
		job.Phase = domain.SOPHandoffPhaseSummarizing
		// Preserve the persisted stage, including a cached saved summary.
		if old, ok, err := h.repo.SOPHandoffJob(job.RunID); err == nil && ok && old.ID == job.ID {
			job.Phase = old.Phase
		}
		job.Error = workErr.Error()
	}
	if _, err := h.repo.FinishSOPHandoffJob(job, job.Owner, time.Now().UTC()); err != nil {
		log.Printf("finish SOP handoff %s: %v", job.RunID, err)
	}
}
func sopHandoffAlreadyStarted(run domain.Run, id string) bool {
	ref := run.WorkflowSnapshot.GroupSOPHandoff
	return ref != nil && ref.ID == id && run.WorkflowSnapshot.CollaborationMode != "group" && !run.PauseRequested && run.Status != "cancelled" && (run.Status == "running" || domain.TerminalRunStatus(run.Status))
}

func (h *runsHandlers) sopHandoffActorAllowed(actorID int64, projectID string) bool {
	user, ok := h.repo.User(actorID)
	if !ok {
		return false
	}
	project, ok := h.repo.Project(projectID)
	if !ok {
		return false
	}
	if h.repo.IsPlatformAdmin(user.LoginName) || project.OwnerID == actorID || project.OwnerID == 0 && len(project.Members) == 0 {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == actorID {
			return true
		}
	}
	return false
}
