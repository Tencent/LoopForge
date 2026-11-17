package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func TestWorkspaceOpenParseWorkspaceRunID(t *testing.T) {
	c, err := parseWorkspaceOpenChoice(map[string]any{"workspaceRunId": " sop-1 "})
	if err != nil || c.WorkspaceRunID != "sop-1" || c.SandboxID != "" {
		t.Fatalf("unexpected choice: %+v err=%v", c, err)
	}
	c, err = parseWorkspaceOpenChoice(map[string]any{"sandboxProfileId": " business ", "workspaceRunId": " sop-2 "})
	if err != nil || c.SandboxID != "business" || c.WorkspaceRunID != "sop-2" {
		t.Fatalf("unexpected combined choice: %+v err=%v", c, err)
	}
	for _, bad := range []map[string]any{
		{"fromRunId": "sop-1"},
		{"issueId": "iss-1"},
		{"new": true},
		{"goal": "x"},
		{"workspaceRunId": 123},
	} {
		if _, err := parseWorkspaceOpenChoice(bad); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

func seedSavedSOPCheckpoint(t *testing.T, st *repo.Store, id string, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := runtime.CreateWorkspaceCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.BeginWorkspaceUse(id, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorkspaceCheckpoint(id, token, time.Now().UTC(), archive); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
}

func waitWorkspaceCheckpointSettled(t *testing.T, st *repo.Store, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		meta, ok := st.WorkspaceCheckpoint(runID)
		if !ok || meta.LeaseToken == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for workspace save on %s (lease=%q)", runID, meta.LeaseToken)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWorkspaceOpenBindsSOPChildAndPreservesAcrossConversationDelete(t *testing.T) {
	h, owner, _ := chatSandboxFixture(t)
	root := t.TempDir()
	en := engine.New(h.repo, runtime.NewManager(root))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")

	dockerSnapshot := &domain.RunnerSandboxSpec{
		Profile: domain.SandboxProfile{ID: "business", Name: "Business", Driver: domain.SandboxDriverDocker},
	}
	now := time.Now().UTC()
	cleanedAt := now.Add(-10 * time.Minute)

	sopDone := domain.Run{
		ID:                 "sop-parent-done",
		Status:             "succeeded",
		WorkspaceCleanedAt: cleanedAt,
		ExecutionContext:   domain.TelemetryContext{ProjectID: "workbench", IssueID: "iss-1", ExecutionMode: "cloud"},
		WorkflowSnapshot:   domain.WorkflowDefinition{ID: "wf-1", SandboxProfileID: "business", SandboxSnapshot: dockerSnapshot},
		Nodes:              map[string]*domain.NodeRun{},
	}
	sopChild := domain.Run{
		ID:               "sop-child",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ParentRunID: "sop-parent-done", WorkspaceRunID: "sop-parent-done", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-child", SandboxProfileID: "business", SandboxSnapshot: dockerSnapshot},
		Nodes:            map[string]*domain.NodeRun{},
	}
	sopRunning := domain.Run{
		ID:               "sop-running",
		Status:           "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", IssueID: "iss-2", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-1", SandboxProfileID: "business", SandboxSnapshot: dockerSnapshot},
		Nodes:            map[string]*domain.NodeRun{"dev": {NodeID: "dev", Status: "running"}},
	}
	foreignSOP := domain.Run{
		ID:               "sop-foreign",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "other", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "business", SandboxSnapshot: dockerSnapshot},
		Nodes:            map[string]*domain.NodeRun{},
	}
	historicalGroup := domain.Run{
		ID:               "group-legacy",
		Status:           "cancelled",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", SandboxProfileID: "business", SandboxSnapshot: dockerSnapshot},
		Nodes:            map[string]*domain.NodeRun{},
	}
	otherChatWS := domain.Run{
		ID:               "ws-other-chat",
		Kind:             domain.RunKindWorkspace,
		Status:           "waiting",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "business", SandboxSnapshot: dockerSnapshot},
		Nodes:            map[string]*domain.NodeRun{},
	}
	for _, r := range []domain.Run{sopDone, sopChild, sopRunning, foreignSOP, historicalGroup, otherChatWS} {
		if err := h.repo.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}
	seedSavedSOPCheckpoint(t, h.repo, sopDone.ID, map[string]string{"carried.txt": "from-sop-parent\n"})
	// Provide an empty workspace directory for sopRunning so tree check succeeds.
	if err := os.MkdirAll(filepath.Join(root, sopRunning.ID), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-opt1", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	turn := newAssistantWorkspaceTurn(en, nil)
	s := &AgentToolSession{
		Mode:          AgentToolAssistant,
		ProjectID:     "workbench",
		UserID:        owner.ID,
		User:          owner,
		ChatSessionID: "chat-opt1",
		ChatOwnerID:   owner.ID,
		workspaceTurn: turn,
	}
	t.Cleanup(func() {
		_ = turn.close(context.Background())
		waitWorkspaceCheckpointSettled(t, h.repo, sopDone.ID)
		waitWorkspaceCheckpointSettled(t, h.repo, sopRunning.ID)
	})

	// Rejects foreign project, historical group, another chat's private workspace, missing run, conflicting sandbox, task session switch.
	for _, id := range []string{"missing-run", foreignSOP.ID} {
		_, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": id})
		if err == nil || err.Error() != "工作区或运行 "+id+" 不存在或不属于当前项目" {
			t.Fatalf("missing/foreign run %s error = %v", id, err)
		}
	}
	for _, args := range []map[string]any{
		{"workspaceRunId": historicalGroup.ID},
		{"workspaceRunId": otherChatWS.ID},
		{"workspaceRunId": sopDone.ID, "sandboxProfileId": "shared"},
	} {
		if _, err := gateway.openConversationWorkspace(context.Background(), s, args); err == nil {
			t.Fatalf("expected rejection for %v", args)
		}
	}
	taskSess := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, RunID: sopRunning.ID}
	if _, err := gateway.openConversationWorkspace(context.Background(), taskSess, map[string]any{"workspaceRunId": sopDone.ID}); err == nil {
		t.Fatal("task conversation must reject switching to another workspaceRunId")
	}

	// Opening child SOP resolves to sopDone, restores reclaimed checkpoint to disk, and hands off.
	raw, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": sopChild.ID})
	if err != nil {
		t.Fatalf("open child SOP failed: %v", err)
	}
	var opened struct {
		RunID   string `json:"runId"`
		SOPID   string `json:"sopRunId"`
		IssueID string `json:"issueId"`
		Handoff bool   `json:"handoff"`
		Notice  string `json:"notice"`
	}
	if err := json.Unmarshal([]byte(raw), &opened); err != nil {
		t.Fatalf("decode open: %v (%s)", err, raw)
	}
	if opened.RunID != sopDone.ID || opened.SOPID != sopDone.ID || opened.IssueID != "iss-1" || !opened.Handoff {
		t.Fatalf("unexpected SOP bind response: %+v", opened)
	}
	if strings.Contains(opened.Notice, "空目录") {
		t.Fatalf("borrowed SOP must not claim empty dir: %s", opened.Notice)
	}
	if data, err := os.ReadFile(filepath.Join(root, sopDone.ID, "carried.txt")); err != nil || string(data) != "from-sop-parent\n" {
		t.Fatalf("restored file = %q err=%v", data, err)
	}
	if stored, _ := h.repo.AssistantSession(owner.ID, "chat-opt1"); stored.WorkspaceRunID != sopDone.ID || len(stored.OwnWorkspaceRunIDs) != 0 {
		t.Fatalf("borrowed SOP must not enter OwnWorkspaceRunIDs: %+v", stored)
	}

	// Switching to a sandbox creates an own workspace, leaving sopDone borrowed history out of OwnWorkspaceRunIDs.
	raw, err = gateway.openConversationWorkspace(context.Background(), s, map[string]any{"sandboxProfileId": "shared"})
	if err != nil {
		t.Fatalf("switch to own sandbox failed: %v", err)
	}
	var ownOpened struct {
		RunID      string `json:"runId"`
		PreviousID string `json:"previousRunId"`
		Created    bool   `json:"created"`
	}
	if err := json.Unmarshal([]byte(raw), &ownOpened); err != nil || !ownOpened.Created || ownOpened.PreviousID != sopDone.ID || ownOpened.RunID == sopDone.ID {
		t.Fatalf("switch to own workspace = %s (%v)", raw, err)
	}
	t.Cleanup(func() { waitWorkspaceCheckpointSettled(t, h.repo, ownOpened.RunID) })

	// Switching to a running SOP returns handoff=false and clears any pending workspace handoff.
	raw, err = gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": sopRunning.ID})
	if err != nil {
		t.Fatalf("open running SOP failed: %v", err)
	}
	var runningOpened struct {
		RunID   string `json:"runId"`
		Handoff bool   `json:"handoff"`
		Notice  string `json:"notice"`
	}
	if err := json.Unmarshal([]byte(raw), &runningOpened); err != nil || runningOpened.RunID != sopRunning.ID || runningOpened.Handoff || s.requestedMode() != "" {
		t.Fatalf("running SOP must stay read-only without handoff: %s mode=%q (%v)", raw, s.requestedMode(), err)
	}

	// Once the running SOP is paused, re-opening it hands off into workspace mode on the same SOP workspace.
	if _, err := h.repo.RequestRunPause(sopRunning.ID); err != nil {
		t.Fatal(err)
	}
	pausedSOP, _ := h.repo.RunState(sopRunning.ID)
	pausedSOP.Status = "waiting"
	pausedSOP.Nodes = map[string]*domain.NodeRun{"dev": {NodeID: "dev", Status: "waiting_approval"}}
	if err := h.repo.PutRun(pausedSOP); err != nil {
		t.Fatal(err)
	}
	raw, err = gateway.openConversationWorkspace(context.Background(), s, map[string]any{})
	if err != nil {
		t.Fatalf("re-open paused SOP failed: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &runningOpened); err != nil || runningOpened.RunID != sopRunning.ID || !runningOpened.Handoff || s.requestedMode() != domain.AssistantModeWorkspace {
		t.Fatalf("paused SOP must hand off to workspace mode: %s mode=%q (%v)", raw, s.requestedMode(), err)
	}
	if !strings.Contains(runningOpened.Notice, "runs_resume") {
		t.Fatalf("paused SOP notice must explain runs_resume: %s", runningOpened.Notice)
	}

	// Switch back to own workspace via workspaceRunId, close turn, and delete conversation:
	// own workspace must be removed while borrowed sopDone remains intact!
	if _, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": ownOpened.RunID}); err != nil {
		t.Fatalf("switch back to own workspace failed: %v", err)
	}
	_ = turn.close(context.Background())
	waitWorkspaceCheckpointSettled(t, h.repo, ownOpened.RunID)
	waitWorkspaceCheckpointSettled(t, h.repo, sopDone.ID)
	waitWorkspaceCheckpointSettled(t, h.repo, sopRunning.ID)

	c, w := workbenchContext(owner, "DELETE", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "chat-opt1"}}
	h.session(c)
	if w.Code != 200 {
		t.Fatalf("delete conversation = %d %s", w.Code, w.Body.String())
	}
	if _, ok := h.repo.RunState(ownOpened.RunID); ok {
		t.Fatalf("deleted conversation kept its own workspace %s", ownOpened.RunID)
	}
	if _, ok := h.repo.RunState(sopDone.ID); !ok {
		t.Fatalf("deleting conversation deleted borrowed SOP %s", sopDone.ID)
	}
}

func TestWorkspaceOpenBorrowedSOPMachineMismatchAndFailureRollback(t *testing.T) {
	h, owner, _ := chatSandboxFixture(t)
	root := t.TempDir()
	en := engine.New(h.repo, runtime.NewManager(root))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")

	for _, r := range []domain.Runner{
		{ID: "runner-a", Name: "机器A", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationWorkspace},
		{ID: "runner-b", Name: "机器B", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationWorkspace},
	} {
		if err := h.repo.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	dockerSnap := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "business", Driver: domain.SandboxDriverDocker}}
	localSnap := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "local-sb", Driver: domain.SandboxDriverLocal}}

	sopOnA := domain.Run{
		ID:               "sop-on-a",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ExecutionMode: "runner", ExecutionRunnerID: "runner-a"},
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "business", SandboxSnapshot: dockerSnap},
		Nodes:            map[string]*domain.NodeRun{},
	}
	sopLocal := domain.Run{
		ID:               "sop-local",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local-sb", SandboxSnapshot: localSnap},
		Nodes:            map[string]*domain.NodeRun{},
	}
	sopCleanedNoArchive := domain.Run{
		ID:                 "sop-cleaned-missing",
		Status:             "succeeded",
		WorkspaceCleanedAt: time.Now().UTC().Add(-time.Hour),
		ExecutionContext:   domain.TelemetryContext{ProjectID: "workbench", ExecutionMode: "cloud"},
		WorkflowSnapshot:   domain.WorkflowDefinition{SandboxProfileID: "business", SandboxSnapshot: dockerSnap},
		Nodes:              map[string]*domain.NodeRun{},
	}
	for _, r := range []domain.Run{sopOnA, sopLocal, sopCleanedNoArchive} {
		if err := h.repo.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}

	targetB := domain.AssistantExecutionTarget{Pool: "runner", RunnerID: "runner-b"}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-cloud", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-b", ProjectID: "workbench", ExecutionTarget: &targetB}); err != nil {
		t.Fatal(err)
	}

	// Runner A vs Cloud conversation.
	sCloud := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-cloud", ChatOwnerID: owner.ID}
	_, err := gateway.openConversationWorkspace(context.Background(), sCloud, map[string]any{"workspaceRunId": sopOnA.ID})
	if err == nil || !strings.Contains(err.Error(), "runner-a") || !strings.Contains(err.Error(), "不在同一执行机器") {
		t.Fatalf("expected honest machine mismatch error, got: %v", err)
	}

	// Runner A vs Runner B conversation.
	sB := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-b", ChatOwnerID: owner.ID, ExecutionRunnerID: "runner-b"}
	_, err = gateway.openConversationWorkspace(context.Background(), sB, map[string]any{"workspaceRunId": sopOnA.ID})
	if err == nil || !strings.Contains(err.Error(), "runner-a") || !strings.Contains(err.Error(), "runner-b") {
		t.Fatalf("expected both runner IDs in mismatch error, got: %v", err)
	}

	// Local driver refused cleanly.
	_, err = gateway.openConversationWorkspace(context.Background(), sCloud, map[string]any{"workspaceRunId": sopLocal.ID})
	if err == nil || !strings.Contains(err.Error(), "Docker 或 native") {
		t.Fatalf("expected sandbox driver refusal, got: %v", err)
	}

	// Failed preparation on target SOP rolls session binding back to previous own workspace.
	own, err := gateway.bindConversationWorkspace(context.Background(), sCloud, workspaceOpenChoice{SandboxID: "business"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.openConversationWorkspace(context.Background(), sCloud, map[string]any{"workspaceRunId": sopCleanedNoArchive.ID}); err == nil {
		t.Fatal("expected error for cleaned SOP with no archive or disk dir")
	}
	if sCloud.workspaceRunID != own.Run.ID {
		t.Fatalf("session workspaceRunID not rolled back: got %q want %q", sCloud.workspaceRunID, own.Run.ID)
	}
	if stored, _ := h.repo.AssistantSession(owner.ID, "chat-cloud"); stored.WorkspaceRunID != own.Run.ID {
		t.Fatalf("stored WorkspaceRunID not rolled back: got %q want %q", stored.WorkspaceRunID, own.Run.ID)
	}
}

// End-to-end verification of:
// SOP finishes & reclaims -> ordinary chat continues in SOP via workspaceRunId,
// modifies files & closes turn (and even gets reclaimed again) -> SOP RetryContext
// from the downstream "test" node sees the exact edits made during the chat.
func TestBorrowedSOPWorkspaceEditsSurviveReclaimAndNodeRetry(t *testing.T) {
	h, owner, _ := chatSandboxFixture(t)
	root := t.TempDir()
	en := engine.New(h.repo, runtime.NewManagerWithLimit(root, 1))
	en.ConfigureExecution("local-exec", "local-exec", time.Minute, false)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = en.Drain(ctx)
	})
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	h.deps.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"entries":[]}`))
	})
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")

	dockerSnap := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "business", Driver: domain.SandboxDriverDocker}}
	sop := domain.Run{
		ID:                 "sop-retry-e2e",
		Status:             "succeeded",
		Generation:         1,
		WorkspaceCleanedAt: time.Now().UTC().Add(-time.Hour),
		ExecutionContext:   domain.TelemetryContext{ProjectID: "workbench", IssueID: "iss-retry", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:               "wf-retry",
			SandboxProfileID: "business",
			SandboxSnapshot:  dockerSnap,
			Nodes: []domain.WorkflowNode{
				{ID: "impl", Name: "开发", Type: "script", Script: "true", MaxAttempts: 1},
				{ID: "test", Name: "测试", Type: "approval", MaxAttempts: 1},
			},
			Edges: []domain.WorkflowEdge{{ID: "impl-to-test", Source: "impl", Target: "test", When: "success"}},
		},
		Nodes: map[string]*domain.NodeRun{
			"impl": {NodeID: "impl", Status: "succeeded", Attempt: 1},
			"test": {NodeID: "test", Status: "succeeded", Attempt: 1},
		},
	}
	if err := h.repo.PutRun(sop); err != nil {
		t.Fatal(err)
	}
	seedSavedSOPCheckpoint(t, h.repo, sop.ID, map[string]string{"app.txt": "v1-from-sop"})

	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-e2e", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	turn := newAssistantWorkspaceTurn(en, nil)
	s := &AgentToolSession{
		Mode:          AgentToolAssistant,
		ProjectID:     "workbench",
		UserID:        owner.ID,
		User:          owner,
		ChatSessionID: "chat-e2e",
		ChatOwnerID:   owner.ID,
		workspaceTurn: turn,
	}

	// 1. Open SOP workspace in ordinary conversation; reclaimed checkpoint restores automatically.
	if _, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": sop.ID}); err != nil {
		t.Fatalf("open SOP workspace failed: %v", err)
	}
	wsDir := en.RunWorkspace(sop.ID)
	if body, err := os.ReadFile(filepath.Join(wsDir, "app.txt")); err != nil || string(body) != "v1-from-sop" {
		t.Fatalf("initial restored app.txt = %q err=%v", body, err)
	}

	// 2. Simulate workspace CLI modifying files under the turn lease, then finishing the turn.
	if err := os.WriteFile(filepath.Join(wsDir, "app.txt"), []byte("v2-edited-in-chat"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "chat-fix.txt"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := turn.close(context.Background()); err != nil {
		t.Fatalf("close workspace turn failed: %v", err)
	}
	waitWorkspaceCheckpointSettled(t, h.repo, sop.ID)

	// 3. Even if the workspace is reclaimed again before retrying, the saved checkpoint holds chat edits.
	meta, ok := h.repo.WorkspaceCheckpoint(sop.ID)
	if !ok || meta.SavedAt == nil {
		t.Fatalf("expected checkpoint archive saved after chat turn: %+v ok=%v", meta, ok)
	}
	if err := os.RemoveAll(wsDir); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := h.repo.RunState(sop.ID)
	reloaded.WorkspaceCleanedAt = time.Now().UTC()
	if err := h.repo.PutRun(reloaded); err != nil {
		t.Fatal(err)
	}

	// 4. Retry SOP from the downstream "test" node.
	if err := en.RetryContext(context.Background(), sop.ID, "test", "验证对话微调后的结果", ""); err != nil {
		t.Fatalf("RetryContext on test node failed: %v", err)
	}
	afterRetry, _ := h.repo.RunState(sop.ID)
	if !afterRetry.WorkspaceCleanedAt.IsZero() {
		t.Fatal("retried SOP workspace was not marked restored")
	}
	if afterRetry.Nodes["impl"].Status != "succeeded" || afterRetry.Nodes["test"].Status != "pending" {
		t.Fatalf("unexpected node states after retry from test: impl=%+v test=%+v", afterRetry.Nodes["impl"], afterRetry.Nodes["test"])
	}
	if body, err := os.ReadFile(filepath.Join(wsDir, "app.txt")); err != nil || string(body) != "v2-edited-in-chat" {
		t.Fatalf("app.txt after test node retry = %q err=%v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(wsDir, "chat-fix.txt")); err != nil || string(body) != "extra" {
		t.Fatalf("chat-fix.txt after test node retry = %q err=%v", body, err)
	}
}

func TestWorkspaceOpenSameRunnerBorrowedSOPRestores(t *testing.T) {
	h, owner, _ := chatSandboxFixture(t)
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	var hub *runnerControlHub
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = newWithNotifications(h.repo, en, tracker.NewService(), "", "", notification.New(h.repo), nil, func(_ *assistantHandlers, runs *runsHandlers) { hub = runs.runnerControls })

	if err := h.repo.PutRunner(domain.Runner{ID: "runner-same", Name: "同机执行器", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationWorkspace}); err != nil {
		t.Fatal(err)
	}
	dockerSnap := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "business", Driver: domain.SandboxDriverDocker}}
	sop := domain.Run{
		ID:                 "sop-same-runner",
		Status:             "succeeded",
		WorkspaceCleanedAt: time.Now().UTC().Add(-time.Hour),
		ExecutionContext: domain.TelemetryContext{
			ProjectID:             "workbench",
			IssueID:               "iss-runner",
			ExecutionMode:         "runner",
			ExecutionRunnerID:     "runner-same",
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-runner", SandboxProfileID: "business", SandboxSnapshot: dockerSnap},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := h.repo.PutRun(sop); err != nil {
		t.Fatal(err)
	}
	seedSavedSOPCheckpoint(t, h.repo, sop.ID, map[string]string{"service.go": "package main\n"})

	runnerDir := t.TempDir()
	var restored atomic.Bool
	fakeRunnerConnection(t, hub, "runner-same", func(req domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
		target := filepath.Join(runnerDir, req.RunID)
		switch req.Operation {
		case domain.RunnerWorkspaceOperationCheckpointStatus:
			if !restored.Load() {
				return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "missing"}
			}
			return domain.RunnerWorkspaceResponse{Status: 200}
		case domain.RunnerWorkspaceOperationRestore:
			if len(req.Checkpoint) == 0 {
				return domain.RunnerWorkspaceResponse{Status: 400, Error: "missing checkpoint bytes"}
			}
			if err := runtime.RestoreWorkspaceCheckpoint(target, req.Checkpoint); err != nil {
				return domain.RunnerWorkspaceResponse{Status: 500, Error: err.Error()}
			}
			restored.Store(true)
			return domain.RunnerWorkspaceResponse{Status: 200}
		case domain.RunnerWorkspaceOperationTree:
			if !restored.Load() {
				return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "not restored yet"}
			}
			return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"entries":[{"path":"service.go","name":"service.go","type":"file","size":13}],"truncated":false}`)}
		case domain.RunnerWorkspaceOperationCheckpointExport:
			return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported"}
		case domain.RunnerWorkspaceOperationCheckpoint:
			data, err := runtime.CreateWorkspaceCheckpoint(target)
			if err != nil {
				return domain.RunnerWorkspaceResponse{Status: 500, Error: err.Error()}
			}
			payload, _ := json.Marshal(map[string]any{"checkpoint": data})
			return domain.RunnerWorkspaceResponse{Status: 200, Payload: payload}
		}
		return domain.RunnerWorkspaceResponse{Status: 409, Error: "unexpected runner op: " + req.Operation}
	})

	target := domain.AssistantExecutionTarget{Pool: "runner", RunnerID: "runner-same"}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-same-runner", ProjectID: "workbench", ExecutionTarget: &target}); err != nil {
		t.Fatal(err)
	}
	turn := newAssistantWorkspaceTurn(en, nil)
	s := &AgentToolSession{
		Mode:              AgentToolAssistant,
		ProjectID:         "workbench",
		UserID:            owner.ID,
		User:              owner,
		ChatSessionID:     "chat-same-runner",
		ChatOwnerID:       owner.ID,
		ExecutionRunnerID: "runner-same",
		workspaceTurn:     turn,
	}
	t.Cleanup(func() {
		_ = turn.close(context.Background())
		waitWorkspaceCheckpointSettled(t, h.repo, sop.ID)
	})

	raw, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": sop.ID})
	if err != nil {
		t.Fatalf("open same-runner SOP failed: %v", err)
	}
	if !restored.Load() || !strings.Contains(raw, "service.go") || !strings.Contains(raw, `"handoff":true`) {
		t.Fatalf("expected restored runner tree and handoff: restored=%v raw=%s", restored.Load(), raw)
	}
	if got, err := os.ReadFile(filepath.Join(runnerDir, sop.ID, "service.go")); err != nil || string(got) != "package main\n" {
		t.Fatalf("restored runner file = %q err=%v", got, err)
	}
}
