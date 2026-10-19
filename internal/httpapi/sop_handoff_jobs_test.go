package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"bytes"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"os"
	"strings"
	"testing"
	"time"
)

func durableHandoffFixture(t *testing.T) (*runsHandlers, *assistantHandlers, domain.User, domain.Run, domain.SOPHandoffJob) {
	t.Helper()
	h, a, u, r, _ := groupConversationFixture(t)
	h.assistant = a
	r.Status = "waiting"
	r.PauseRequested = true
	r.Nodes["dev-task"].Status = "succeeded"
	r.WorkflowSnapshot.SOPCheckpoint = &domain.SOPCheckpoint{Workflow: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "build", Name: "开发", Type: "agent"}}}, Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "succeeded", Attempt: 1}}}
	if err := h.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.engine.RunWorkspace(r.ID), 0700); err != nil {
		t.Fatal(err)
	}
	c, rec := workbenchContext(u, "POST", "/api/runs/"+r.ID+"/group/sop", bytes.NewBufferString(`{"nodeId":"build"}`))
	c.Params = gin.Params{{Key: "id", Value: r.ID}}
	h.groupControl(c)
	if rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	job, _, err := h.repo.SOPHandoffJob(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h, a, u, r, job
}
func TestSOPHandoffWorkerSurvivesRequestAndRecoversCommittedStart(t *testing.T) {
	h, a, u, r, job := durableHandoffFixture(t)
	// Duplicate submission is idempotent and does not start a model call.
	c, rec := workbenchContext(u, "POST", "/api/runs/"+r.ID+"/group/sop", bytes.NewBufferString(`{"nodeId":"build"}`))
	c.Params = gin.Params{{Key: "id", Value: r.ID}}
	cancelled, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(cancelled)
	h.groupControl(c)
	if rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	old, ok, err := h.repo.ClaimSOPHandoffJob(r.ID, job.ID, "old-process", time.Now().UTC(), time.Second)
	if err != nil || !ok {
		t.Fatal(err)
	}
	// Simulate termination after the durable graph/artifact transaction, before
	// the handler can save its starting state or issue ResumePaused.
	opCtx := engine.WithSOPHandoffOperation(context.Background(), job.ID, func(phase string) error {
		old.Phase = phase
		ok, err := h.repo.UpdateSOPHandoffJob(old, old.Owner, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("lease lost")
		}
		return nil
	})
	if err := h.engine.RestoreGroupSOPWithHandoff(opCtx, r.ID, "build", u.ID, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
		return "保留已确认要求；尚未执行检查", nil
	}); err != nil {
		t.Fatal(err)
	}
	a.generate = func(context.Context, string, domain.User) (string, error) {
		t.Error("recovery repeated model summarization")
		return "", fmt.Errorf("unexpected model")
	}
	next, ok, err := h.repo.ClaimSOPHandoffJob(r.ID, job.ID, "new-process", time.Now().UTC().Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	h.executeSOPHandoffJob(context.Background(), next)
	saved, _, _ := h.repo.SOPHandoffJob(r.ID)
	run, _ := h.repo.RunState(r.ID)
	if saved.Status != domain.SOPHandoffJobSucceeded || saved.Phase != domain.SOPHandoffPhaseDone || run.PauseRequested || run.Status != "running" {
		t.Fatal("committed handoff was not resumed", saved, run.Status)
	}
}
func TestSOPHandoffStartFailureRetriesWithoutRegenerating(t *testing.T) {
	h, a, u, r, job := durableHandoffFixture(t)
	old, ok, err := h.repo.ClaimSOPHandoffJob(r.ID, job.ID, "before-start", time.Now().UTC(), time.Second)
	if err != nil || !ok {
		t.Fatal(err)
	}
	opCtx := engine.WithSOPHandoffOperation(context.Background(), job.ID, func(phase string) error {
		old.Phase = phase
		_, err := h.repo.UpdateSOPHandoffJob(old, old.Owner, time.Now().UTC())
		return err
	})
	if err := h.engine.RestoreGroupSOPWithHandoff(opCtx, r.ID, "build", u.ID, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
		return "current decisions", nil
	}); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{ID: "stopping", RunID: r.ID, RunnerID: "runner", Provider: "codebuddy", Status: domain.RunnerTaskRunning}
	if err := h.repo.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	next, ok, err := h.repo.ClaimSOPHandoffJob(r.ID, job.ID, "resumed-process", time.Now().UTC().Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	a.generate = func(context.Context, string, domain.User) (string, error) {
		t.Error("starting retry invoked model")
		return "", fmt.Errorf("unexpected model")
	}
	h.executeSOPHandoffJob(context.Background(), next)
	failed, _, _ := h.repo.SOPHandoffJob(r.ID)
	if failed.Status != domain.SOPHandoffJobFailed || failed.Phase != domain.SOPHandoffPhaseStarting || failed.SummaryArtifactID == "" || !strings.Contains(failed.Error, "执行机器") {
		t.Fatal("lost saved-vs-starting error stage", failed)
	}
	task.Status = domain.RunnerTaskSuccess
	if err := h.repo.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	c, rec := workbenchContext(u, "POST", "/api/runs/"+r.ID+"/group/sop/handoff/retry", bytes.NewBufferString(`{}`))
	c.Params = gin.Params{{Key: "id", Value: r.ID}}
	h.retrySOPHandoffJob(c)
	if rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	done := executeTestSOPHandoffJob(t, h, r.ID)
	if done.Status != domain.SOPHandoffJobSucceeded {
		t.Fatal(done)
	}
	run, _ := h.repo.RunState(r.ID)
	if len(run.Artifacts) != 2 {
		t.Fatal("retry duplicated archive")
	}
}
func TestSOPHandoffLifecycleCancelsSafelyAndNewInstanceReclaims(t *testing.T) {
	h, a, _, r, job := durableHandoffFixture(t)
	entered := make(chan struct{})
	calls := 0
	a.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		calls++
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := h.startSOPHandoffJobs(ctx)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		stop()
		t.Fatal("background worker never claimed")
	}
	cancel()
	stop()
	old, _, _ := h.repo.SOPHandoffJob(r.ID)
	if old.Status != domain.SOPHandoffJobRunning {
		t.Fatal("shutdown became permanent failure", old)
	}
	a.generate = func(context.Context, string, domain.User) (string, error) {
		calls++
		submitHandoffReport(t, a, "latest decisions")
		return "", nil
	}
	claimed, ok, err := h.repo.ClaimSOPHandoffJob(r.ID, job.ID, "replacement", time.Now().UTC().Add(sopHandoffLease+time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	h.executeSOPHandoffJob(context.Background(), claimed)
	done, _, _ := h.repo.SOPHandoffJob(r.ID)
	if done.Status != domain.SOPHandoffJobSucceeded || calls != 2 {
		t.Fatal("restart did not recover work", done, calls)
	}
}

func TestSOPHandoffPreviewShowsDecisionsInsteadOfMetadata(t *testing.T) {
	raw := "# 自由协作交接\n**运行信息**：runId = run-123; resumeNode = build\n## 最新要求\n- **用户确认的要求**（来源：conversation/0）：\n1. 保留键盘无障碍。\n2. 文案使用`安全登录`。"
	got := sopHandoffPreview(raw)
	if strings.Contains(got, "run-123") || strings.Contains(got, "**") || !strings.Contains(got, "键盘无障碍") {
		t.Fatal(got)
	}
}

func TestSOPHandoffSavingRetryReusesSummaryOnlyForSameSource(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			h, a, _, r, _ := durableHandoffFixture(t)
			calls := 0
			a.generate = func(context.Context, string, domain.User) (string, error) {
				calls++
				submitHandoffReport(t, a, "Keep latest decisions")
				return "", nil
			}
			obstacle := h.engine.RunWorkspace(r.ID) + "/handoffs"
			if err := os.WriteFile(obstacle, []byte("simulate a save failure"), 0600); err != nil {
				t.Fatal(err)
			}
			failed := executeTestSOPHandoffJob(t, h, r.ID)
			if failed.Status != domain.SOPHandoffJobFailed || failed.Phase != domain.SOPHandoffPhaseSaving || failed.Summary == "" || calls != 1 {
				t.Fatal("summary was not checkpointed before file publication", failed, calls)
			}
			if err := os.Remove(obstacle); err != nil {
				t.Fatal(err)
			}
			if changed {
				run, _ := h.repo.RunState(r.ID)
				run.HumanContext = "new user correction"
				if err := h.repo.PutRun(run); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := h.repo.RetrySOPHandoffJob(r.ID, failed.ID, time.Now().UTC()); err != nil || !ok {
				t.Fatal(err)
			}
			done := executeTestSOPHandoffJob(t, h, r.ID)
			expected := 1
			if changed {
				expected = 2
			}
			if done.Status != domain.SOPHandoffJobSucceeded || calls != expected {
				t.Fatal("saving retry ignored cache source boundary", done, calls)
			}
		})
	}
}
