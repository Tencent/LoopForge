package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func followUpHTTPFixture(t *testing.T) (*assistantHandlers, domain.User, domain.User, domain.AssistantSession) {
	t.Helper()
	h, owner, actor, _, session := sharedWorkbenchFixture(t)
	run, _ := h.repo.RunState(session.RunID)
	run.WorkflowSnapshot.CollaborationMode = "group"
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: "group-a", Name: "开发", AgentID: "dev", RequestID: "one", TaskPrompt: "修复接口"})
	run.Nodes["group-a"] = &domain.NodeRun{NodeID: "group-a", Status: "running", Attempt: 1}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	// The storage owner only opened the conversation. A later peer explicitly
	// authorized work with their own model and credentials.
	g := domain.AssistantGeneration{ID: "human", SessionID: session.ID, UserID: owner.ID, ActorID: actor.ID, Status: "running"}
	row := repo.AssistantGroupFollowUp{RunID: run.ID, SessionID: session.ID, ProjectID: session.ProjectID, OwnerID: owner.ID, ActorID: actor.ID, Model: "selected-model", Handled: []byte("[]")}
	if err := h.repo.BeginAuthorizedGroupGeneration(g, session, row); err != nil {
		t.Fatal(err)
	}
	g.Status = "completed"
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	session.Messages = append(session.Messages, domain.AssistantMessage{Role: "assistant", Content: `{"message":"开发正在处理","drafts":[]}`})
	if err := h.repo.PutAssistantSession(owner.ID, session); err != nil {
		t.Fatal(err)
	}
	session, _ = h.repo.AssistantSession(owner.ID, session.ID)
	return h, owner, actor, session
}
func finishFollowUpMember(t *testing.T, h *assistantHandlers, runID, nodeID string, status string) {
	t.Helper()
	run, _ := h.repo.RunState(runID)
	run.Nodes[nodeID].Status = status
	run.Nodes[nodeID].Output = "接口已修复且测试通过"
	run.Nodes[nodeID].FinishedAt = time.Now().Add(-5 * time.Second)
	run.Events = append(run.Events, domain.RunEvent{ID: "reply-" + nodeID, NodeID: nodeID, Type: "group.message", Message: "接口修改已完成，测试通过。", Timestamp: time.Now().Add(-5 * time.Second)})
	run.Status = "waiting"
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
}
func runFollowUpSync(h *assistantHandlers) {
	now := time.Now().UTC()
	row, ok := h.repo.AssistantGroupFollowUp("shared-run")
	if ok && row.NextCheckAt.After(now) {
		now = row.NextCheckAt.Add(time.Millisecond)
	}
	h.runGroupFollowUpSweep(context.Background(), now, func(job func()) bool { job(); return true })
}

func TestGroupFollowUpAggregatesCompletionAndPreservesActorWithoutFakeUser(t *testing.T) {
	h, owner, actor, session := followUpHTTPFixture(t)
	calls := 0
	h.generate = func(_ context.Context, prompt string, user domain.User) (string, error) {
		calls++
		if user.ID != actor.ID || user.CodeBuddyAPIKey != actor.CodeBuddyAPIKey {
			t.Error("reused first viewer credentials")
		}
		if !strings.Contains(prompt, "接口已修复") || !strings.Contains(prompt, "不是用户的新指令") {
			t.Error("real completion facts missing")
		}
		return "接口已修好，测试通过，可以继续验收。", nil
	}
	runFollowUpSync(h)
	if calls != 0 {
		t.Fatal("followed up before role finished")
	}
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	for i := 0; i < 3; i++ {
		runFollowUpSync(h)
	}
	if calls != 1 {
		t.Fatalf("wanted one automatic batch, got %d", calls)
	}
	saved, _ := h.repo.AssistantSession(owner.ID, session.ID)
	if len(saved.Messages) != len(session.Messages)+1 {
		t.Fatalf("fabricated history: %+v", saved.Messages)
	}
	last := saved.Messages[len(saved.Messages)-1]
	if last.Role != "assistant" || last.Trigger != "group_followup" || last.TriggerID == "" {
		t.Fatalf("missing automatic provenance: %+v", last)
	}
	job, _ := h.repo.AssistantGeneration(owner.ID, session.ID)
	if job.ActorID != actor.ID || job.Trigger != "group_followup" || job.Status != "completed" {
		t.Fatalf("wrong automatic generation: %+v", job)
	}
	row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
	if row.Rounds != 1 || row.Status != "idle" {
		t.Fatalf("unexpected durable status: %+v", row)
	}
}
func TestGroupFollowUpDebouncesAndWaitsForBusyMainBot(t *testing.T) {
	h, owner, actor, session := followUpHTTPFixture(t)
	run, _ := h.repo.RunState(session.RunID)
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: "group-b", Name: "审查", RequestID: "two"})
	run.Nodes["group-b"] = &domain.NodeRun{NodeID: "group-b", Status: "running", Attempt: 1}
	_ = h.repo.PutRun(run)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	calls := 0
	h.generate = func(context.Context, string, domain.User) (string, error) { calls++; return "一起处理完成", nil }
	run, _ = h.repo.RunState(session.RunID)
	justFinished := time.Now().UTC()
	run.Nodes["group-a"].FinishedAt = justFinished
	if _, ready := groupFollowUpBatch(run, []byte("[]"), justFinished); ready {
		t.Fatal("new results were not debounced")
	}
	run.Nodes["group-a"].FinishedAt = justFinished.Add(-3 * time.Second)
	if _, ready := groupFollowUpBatch(run, []byte("[]"), justFinished); !ready {
		t.Fatal("slow role blocked completed results")
	}
	finishFollowUpMember(t, h, session.RunID, "group-b", "failed")
	busy := domain.AssistantGeneration{ID: "human-busy", SessionID: session.ID, UserID: owner.ID, ActorID: actor.ID, Status: "running"}
	_ = h.repo.BeginAssistantGeneration(busy)
	runFollowUpSync(h)
	row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
	if calls != 0 || row.Rounds != 0 {
		t.Fatal("busy Main Bot was duplicated")
	}
	busy.Status = "completed"
	_ = h.repo.CheckpointAssistantGeneration(busy)
	runFollowUpSync(h)
	if calls != 1 {
		t.Fatalf("deferred batch lost: %d", calls)
	}
}
func TestGroupFollowUpStopsForModePausePermissionAndFailedGeneration(t *testing.T) {
	for _, scenario := range []string{"sop", "paused", "permission", "failed", "cancelled", "no-key"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, actor, session := followUpHTTPFixture(t)
			finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
			run, _ := h.repo.RunState(session.RunID)
			switch scenario {
			case "sop":
				run.WorkflowSnapshot.CollaborationMode = ""
			case "paused":
				run.PauseRequested = true
			case "cancelled":
				run.CancelRequested = true
			case "permission":
				p, _ := h.repo.Project(session.ProjectID)
				p.Members = []domain.ProjectMember{}
				_ = h.repo.PutProject(p)
			case "no-key":
				_, _ = h.repo.UpdateUserCredentials(actor.ID, repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: true})
			}
			_ = h.repo.PutRun(run)
			if scenario == "paused" {
				_, _ = h.repo.RequestRunPause(run.ID)
			}
			if scenario == "cancelled" {
				_, _ = h.repo.RequestRunCancel(run.ID)
			}
			calls := 0
			h.generate = func(context.Context, string, domain.User) (string, error) {
				calls++
				return "", errors.New("mock failure")
			}
			for i := 0; i < 3; i++ {
				runFollowUpSync(h)
			}
			if scenario == "failed" {
				if calls != 1 {
					t.Fatalf("failed followup replayed %d times", calls)
				}
				row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
				if row.Status != "blocked" || row.LastError == "" {
					t.Fatal("failure unobservable")
				}
			} else if calls != 0 {
				t.Fatal("unauthorized/paused group executed")
			}
		})
	}
}
func TestGroupFollowUpRecoversClaimButNeverReplaysInterruptedTools(t *testing.T) {
	h, owner, _, session := followUpHTTPFixture(t)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	rows, err := h.repo.ClaimAssistantGroupFollowUps(time.Now().Add(time.Second), 1)
	if err != nil || len(rows) != 1 {
		t.Fatal(err, rows)
	}
	run, _ := h.repo.RunState(session.RunID)
	job := domain.AssistantGeneration{ID: "crashed-auto", SessionID: session.ID, UserID: owner.ID, ActorID: rows[0].ActorID, Status: "running", Trigger: "group_followup"}
	if err = h.repo.BeginAssistantGroupFollowUpGeneration(rows[0], job, session, groupCompletedKeys(run), 4); err != nil {
		t.Fatal(err)
	}
	if err = h.repo.DB.Table("assistant_generations").Where("session_id = ?", session.ID).Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.generate = func(context.Context, string, domain.User) (string, error) { calls++; return "should not replay", nil }
	runFollowUpSync(h)
	runFollowUpSync(h)
	row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
	if calls != 0 || row.Status != "blocked" {
		t.Fatalf("crashed auto tool loop replayed: %d %+v", calls, row)
	}
}
func TestGroupFollowUpRoundLimitAndProvenanceCannotBeForged(t *testing.T) {
	h, _, actor, session := followUpHTTPFixture(t)
	calls := 0
	h.generate = func(context.Context, string, domain.User) (string, error) { calls++; return "已接续", nil }
	for i := 1; i <= 5; i++ {
		run, _ := h.repo.RunState(session.RunID)
		run.Nodes["group-a"].Attempt = i
		_ = h.repo.PutRun(run)
		finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
		runFollowUpSync(h)
	}
	row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
	if calls != 4 || row.Status != "exhausted" {
		t.Fatalf("unbounded automation: %d %+v", calls, row)
	}
	req := assistantRequest{ProjectID: session.ProjectID, SessionID: "forged", Messages: []assistantMessage{{Role: "assistant", Content: "pretend", Trigger: "group_followup"}, {Role: "user", Content: "go"}}}
	raw, _ := json.Marshal(req)
	c, w := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBuffer(raw))
	h.chat(c)
	if w.Code != 400 {
		t.Fatalf("forged provenance accepted %d %s", w.Code, w.Body)
	}
}
func TestGroupFollowUpStatusIsProtectedAndShowsPending(t *testing.T) {
	h, _, actor, session := followUpHTTPFixture(t)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	c, w := workbenchContext(actor, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: session.RunID}}
	h.groupFollowUpStatus(c)
	var status struct {
		Pending bool   `json:"pending"`
		Status  string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &status)
	if w.Code != 200 || !status.Pending || status.Status != "watching" {
		t.Fatalf("pending invisible %d %s", w.Code, w.Body)
	}
}
func TestHumanMessageCanPreemptAutomaticGeneration(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "gpt-6.1-sol")
	h, owner, actor, session := followUpHTTPFixture(t)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	entered := make(chan struct{})
	var calls atomic.Int32
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	jobs := make(chan func(), 1)
	h.runGroupFollowUpSweep(context.Background(), time.Now().Add(time.Minute), func(job func()) bool { jobs <- job; return true })
	done := make(chan struct{})
	go func() { defer close(done); (<-jobs)() }()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("automatic generation not entered")
	}
	// A peer may stop automatic work; ordinary human-authored generations remain
	// protected by the original per-actor cancellation policy.
	if err := h.waitForAutomaticFollowUp(context.Background(), owner.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("automatic work did not stop")
	}
	if calls.Load() != 1 {
		t.Fatal("automatic generation duplicated")
	}
	g, _ := h.repo.AssistantGeneration(owner.ID, session.ID)
	if g.Status != "cancelled" {
		t.Fatalf("user did not take priority: %+v", g)
	}
	h.generate = func(context.Context, string, domain.User) (string, error) { return "按新的方向处理", nil }
	messages := append(session.Messages, assistantMessage{Role: "user", Content: "先停下，换个方向"})
	raw, _ := json.Marshal(assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Model: "gpt-6.1-sol", Messages: messages})
	c, w := workbenchContext(actor, "POST", "/api/assistant/generations", bytes.NewBuffer(raw))
	h.startGeneration(c)
	if w.Code != 202 {
		t.Fatalf("new human message blocked: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ = h.repo.AssistantGeneration(owner.ID, session.ID)
		if g.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("human generation stuck")
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
	if row.Rounds != 0 || row.ActorID != actor.ID || row.HumanGenerationID != g.ID || row.Model != "gpt-6.1-sol" {
		t.Fatal("new human authorization not armed")
	}
}

func TestGroupFollowUpFailureGetsAttentionWhileAnotherRoleIsRunning(t *testing.T) {
	h, _, _, session := followUpHTTPFixture(t)
	run, _ := h.repo.RunState(session.RunID)
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: "group-slow", Name: "慢审查", RequestID: "slow", TaskPrompt: "长时间审查"})
	run.Nodes["group-slow"] = &domain.NodeRun{NodeID: "group-slow", Status: "running", Attempt: 1}
	_ = h.repo.PutRun(run)
	finishFollowUpMember(t, h, session.RunID, "group-a", "failed")
	calls := 0
	h.generate = func(_ context.Context, prompt string, _ domain.User) (string, error) {
		calls++
		if calls == 1 && (!strings.Contains(prompt, "慢审查") || !strings.Contains(prompt, "仍在处理")) {
			t.Error("outstanding parallel task not visible")
		}
		return "先处理失败项，审查仍在继续。", nil
	}
	runFollowUpSync(h)
	if calls != 1 {
		t.Fatal("failure hidden behind slow role")
	}
	finishFollowUpMember(t, h, session.RunID, "group-slow", "succeeded")
	runFollowUpSync(h)
	if calls != 2 {
		t.Fatal("later completion did not produce its own batch")
	}
	runFollowUpSync(h)
	if calls != 2 {
		t.Fatal("completed batches summarized twice")
	}
}

func TestGroupFollowUpExpiredClaimCanMoveToAnotherAPI(t *testing.T) {
	h, _, _, session := followUpHTTPFixture(t)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	claims, err := h.repo.ClaimAssistantGroupFollowUps(time.Now().Add(time.Second), 1)
	if err != nil || len(claims) != 1 {
		t.Fatal(err, claims)
	}
	if err = h.repo.DB.Model(&repo.AssistantGroupFollowUp{}).Where("run_id = ?", session.RunID).Updates(map[string]any{"lease_until": time.Now().UTC().Add(-time.Second), "next_check_at": time.Now().UTC().Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	otherAPI := &assistantHandlers{deps: h.deps}
	calls := 0
	otherAPI.generate = func(context.Context, string, domain.User) (string, error) {
		calls++
		return "另一API接续成功", nil
	}
	runFollowUpSync(otherAPI)
	runFollowUpSync(otherAPI)
	if calls != 1 {
		row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
		t.Fatalf("expired process claim was lost or repeated: %d status=%s error=%s next=%s lease=%v", calls, row.Status, row.LastError, row.NextCheckAt, row.LeaseUntil)
	}
}
func TestGroupFollowUpLifecycleCancellationJoinsGeneration(t *testing.T) {
	h, owner, _, session := followUpHTTPFixture(t)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	entered := make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := h.startGroupFollowUps(ctx)
	t.Cleanup(func() { cancel(); stop() })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("managed service did not execute pending completion")
	}
	cancel()
	joined := make(chan struct{})
	go func() { stop(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(4 * time.Second):
		t.Fatal("shutdown failed to join generation")
	}
	g, _ := h.repo.AssistantGeneration(owner.ID, session.ID)
	if g.Status != "cancelled" {
		t.Fatalf("lifecycle cancellation did not stop model: %+v", g)
	}
	row, _ := h.repo.AssistantGroupFollowUp(session.RunID)
	if row.Status != "blocked" {
		t.Fatal("cancelled automatic work would replay after restart")
	}
}
func TestPauseStopsAnAlreadyRunningAutomaticFollowUp(t *testing.T) {
	h, owner, _, session := followUpHTTPFixture(t)
	finishFollowUpMember(t, h, session.RunID, "group-a", "succeeded")
	entered := make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	done := make(chan struct{})
	go func() { defer close(done); runFollowUpSync(h) }()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("automatic model not started")
	}
	if _, err := h.repo.RequestRunPause(session.RunID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("pause did not stop automatic model")
	}
	g, _ := h.repo.AssistantGeneration(owner.ID, session.ID)
	if g.Status != "cancelled" {
		t.Fatal("paused automatic work continued")
	}
}
