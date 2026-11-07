package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"github.com/go-sql-driver/mysql"
)

type workspaceTurnEngine struct {
	begin, prepare, renewals int
	ended                    []string
	renewErr, saveErr        error
}

func (e *workspaceTurnEngine) BeginConversationWorkspaceUse(id string) (string, error) {
	e.begin++
	return "token-" + id, nil
}
func (e *workspaceTurnEngine) MaterializeConversationWorkspace(context.Context, string) error {
	e.prepare++
	return nil
}
func (e *workspaceTurnEngine) RenewConversationWorkspaceUse(string, string) error {
	e.renewals++
	return e.renewErr
}
func (e *workspaceTurnEngine) EndConversationWorkspaceUse(ctx context.Context, id, token string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("checkpoint inherited cancellation: %w", err)
	}
	if token != "token-"+id {
		return errors.New("wrong workspace lease")
	}
	e.ended = append(e.ended, id)
	return e.saveErr
}

func TestAssistantWorkspaceTurnReusesAndRenewsLease(t *testing.T) {
	en := &workspaceTurnEngine{}
	turn := newAssistantWorkspaceTurn(en, nil)
	t.Cleanup(func() { _ = turn.close(context.Background()) })
	var calls sync.WaitGroup
	for range 24 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			release := turn.lockOperation()
			defer release()
			ctx, err := turn.lease(context.Background(), "first")
			if err != nil || engine.ConversationWorkspaceToken(ctx, "first") != "token-first" {
				t.Errorf("tool did not reuse the turn lease: %v", err)
			}
		}()
	}
	calls.Wait()
	if err := turn.renew(); err != nil {
		t.Fatal(err)
	}
	if en.begin != 1 || en.prepare != 1 || en.renewals != 1 || len(en.ended) != 0 {
		t.Fatalf("tools took new leases or saved checkpoints: %+v", en)
	}
	if engine.ConversationWorkspaceToken(turn.withLease(context.Background(), "other"), "other") != "" {
		t.Fatal("run control borrowed another workspace's lease")
	}
	if err := turn.checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.lease(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := turn.close(cancelled); err != nil {
		t.Fatal(err)
	}
	if err := turn.close(cancelled); err != nil || strings.Join(en.ended, ",") != "first,second" {
		t.Fatalf("switch/end must save once each, including cancellation: %+v %v", en, err)
	}
	if _, err := turn.lease(context.Background(), "second"); err == nil {
		t.Fatal("closed turn accepted a late tool")
	}
}

func TestAssistantWorkspaceTurnLosesLeaseAndReportsSaveFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	en := &workspaceTurnEngine{renewErr: repo.ErrWorkspaceInUse, saveErr: errors.New("storage unavailable")}
	turn := newAssistantWorkspaceTurn(en, cancel)
	if _, err := turn.lease(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := turn.renew(); err == nil || ctx.Err() == nil {
		t.Fatal("lease loss did not stop model execution")
	}
	if _, err := turn.lease(context.Background(), "first"); err == nil {
		t.Fatal("tools continued after losing the workspace lease")
	}
	if err := turn.close(ctx); err == nil || !strings.Contains(err.Error(), "租约续期失败") || !strings.Contains(err.Error(), "storage unavailable") {
		t.Fatalf("lease/save failures were lost: %v", err)
	}
}

// A storage hiccup must not abort a long answer while the 15-minute lease is
// still valid; a lease that is about to lapse still stops the turn.
func TestAssistantWorkspaceTurnRetriesTransientRenewFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	en := &workspaceTurnEngine{renewErr: errors.New("database is locked")}
	turn := newAssistantWorkspaceTurn(en, cancel)
	t.Cleanup(func() { _ = turn.close(context.Background()) })
	if _, err := turn.lease(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := turn.renew(); err != nil || ctx.Err() != nil {
			t.Fatalf("a transient renewal error stopped the turn: %v", err)
		}
	}
	if _, err := turn.lease(ctx, "first"); err != nil {
		t.Fatalf("tools stopped after a transient renewal error: %v", err)
	}
	en.renewErr = nil
	turn.mu.Lock()
	turn.renewedAt = time.Now().Add(-time.Hour)
	turn.mu.Unlock()
	if err := turn.renew(); err != nil {
		t.Fatal(err)
	}
	turn.mu.Lock()
	renewed := time.Since(turn.renewedAt) < time.Minute
	turn.mu.Unlock()
	if !renewed {
		t.Fatal("a successful renewal did not restart the retry window")
	}
	en.renewErr = errors.New("database is locked")
	turn.mu.Lock()
	turn.renewedAt = time.Now().Add(-assistantWorkspaceRenewGrace)
	turn.mu.Unlock()
	if err := turn.renew(); err == nil || ctx.Err() == nil {
		t.Fatal("a lease about to lapse did not stop the turn")
	}
	if _, err := turn.lease(context.Background(), "first"); err == nil {
		t.Fatal("tools continued after the lease could not be kept")
	}
}

func workspaceTurnFixture(t *testing.T) (*assistantHandlers, domain.User, *AgentToolSession, *agentToolGateway, []string) {
	t.Helper()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	var st *repo.Store
	var err error
	if dsn := os.Getenv("AGENTFLOW_WORKSPACE_TURN_TEST_MYSQL_DSN"); dsn != "" {
		cfg, parseErr := mysql.ParseDSN(dsn)
		if parseErr != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.Addr, "127.0.0.1:") || !strings.HasPrefix(cfg.DBName, "agentflow_workspace_turn_test") {
			t.Fatal("workspace turn tests require a disposable loopback MySQL database")
		}
		st, err = repo.Open(dsn)
	} else {
		st, err = repo.OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	id := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	if err := st.PutUser(domain.User{LoginName: id, CodeBuddyAPIKey: "test-only"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName(id)
	if err := st.PutProject(domain.Project{ID: id, Name: "Workspace turn test", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	en := engine.New(st, runtime.NewManager(root))
	h := &assistantHandlers{deps: &deps{repo: st, engine: en}}
	profile := domain.SandboxProfile{ID: id + "-sandbox", ProjectID: id, Name: "Workspace turn test", Driver: "local"}
	if err := st.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	ids := []string{id + "-first", id + "-second"}
	for _, rid := range ids {
		run := domain.Run{ID: rid, Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: profile.ID}, ExecutionContext: domain.TelemetryContext{ProjectID: id}, Nodes: map[string]*domain.NodeRun{}}
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, rid), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rid, "note.txt"), []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutAssistantSession(user.ID, domain.AssistantSession{ID: id, ProjectID: id, WorkspaceRunID: ids[0], OwnWorkspaceRunIDs: ids}); err != nil {
		t.Fatal(err)
	}
	for _, rid := range []string{ids[1], ids[0]} {
		if err := st.SetAssistantSessionWorkspaceRun(user.ID, id, rid, true); err != nil {
			t.Fatal(err)
		}
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: id, UserID: user.ID, User: user, ChatSessionID: id, ChatOwnerID: user.ID, workspaceRunID: ids[0], workspaceTurn: newAssistantWorkspaceTurn(en, nil)}
	t.Cleanup(func() { _ = s.workspaceTurn.close(context.Background()) })
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(st, en, tracker.NewService(), "", "")
	return h, user, s, gateway, ids
}

func workspaceTurnCheckpointText(t *testing.T, h *assistantHandlers, id string) string {
	t.Helper()
	archive, err := h.repo.WorkspaceCheckpointArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "restored")
	if err := runtime.RestoreWorkspaceCheckpoint(root, archive); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAssistantWorkspaceTurnCheckpointsOnlyOnSwitchAndEnd(t *testing.T) {
	h, _, s, gateway, ids := workspaceTurnFixture(t)
	call := func(name string, args map[string]any) {
		t.Helper()
		if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_"+name, args); err != nil {
			t.Fatal(err)
		}
	}
	before := "before"
	for _, text := range []string{"edit-one", "edit-two", "final-first"} {
		call("read", map[string]any{"path": "note.txt"})
		call("write", map[string]any{"path": "note.txt", "content": text, "expectedSha256": runtime.WorkspaceContentSHA(before)})
		before = text
		meta, _ := h.repo.WorkspaceCheckpoint(ids[0])
		if meta.LeaseToken == "" || meta.SavedAt != nil {
			t.Fatalf("tool released its lease or took a checkpoint: %+v", meta)
		}
	}
	// A switch waits for operations already using the old workspace.
	release := s.workspaceTurn.lockOperation()
	switched := make(chan error, 1)
	go func() {
		_, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{WorkspaceRunID: ids[1]})
		switched <- err
	}()
	select {
	case err := <-switched:
		t.Fatalf("workspace switched while a tool still used it: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if got := workspaceTurnCheckpointText(t, h, ids[0]); got != "final-first" {
		t.Fatalf("switch lost the latest edits: %q", got)
	}
	oldScope := context.WithValue(context.Background(), mainBotWorkspaceKey{}, ids[0])
	if _, err := gateway.callMainBotWorkspace(oldScope, s, "workbench_workspace_tree", nil); err == nil {
		t.Fatal("an open racing another switch read the wrong workspace")
	}
	call("write", map[string]any{"path": "note.txt", "content": "final-second", "expectedSha256": runtime.WorkspaceContentSHA("before")})
	meta, _ := h.repo.WorkspaceCheckpoint(ids[1])
	if meta.SavedAt != nil {
		t.Fatal("tool in the new workspace took a premature checkpoint")
	}
	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{WorkspaceRunID: ids[1]}); err != nil {
		t.Fatal(err)
	}
	meta, _ = h.repo.WorkspaceCheckpoint(ids[1])
	if meta.SavedAt != nil {
		t.Fatal("reopening the same workspace took a checkpoint")
	}
	if err := s.workspaceTurn.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := workspaceTurnCheckpointText(t, h, ids[1]); got != "final-second" {
		t.Fatalf("turn end lost the latest edits: %q", got)
	}
	meta, _ = h.repo.WorkspaceCheckpoint(ids[1])
	if meta.LeaseToken != "" {
		t.Fatal("turn end did not release its lease")
	}
}

func TestAssistantWorkspaceTurnGenerationAlwaysCheckpoints(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "cancelled", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			h, user, fixtureSession, gateway, ids := workspaceTurnFixture(t)
			h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
				var session *AgentToolSession
				agentToolSessions.mu.Lock()
				for _, candidate := range agentToolSessions.sessions {
					if candidate.ChatSessionID == fixtureSession.ChatSessionID {
						session = candidate
					}
				}
				agentToolSessions.mu.Unlock()
				if session == nil || session.workspaceTurn == nil {
					return "", errors.New("generation did not own its workspace lifetime")
				}
				if _, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_write", map[string]any{"path": "note.txt", "content": outcome, "expectedSha256": runtime.WorkspaceContentSHA("before")}); err != nil {
					return "", err
				}
				meta, _ := h.repo.WorkspaceCheckpoint(ids[0])
				if meta.SavedAt != nil {
					return "", errors.New("tool saved before the turn ended")
				}
				switch outcome {
				case "failed":
					return "", errors.New("model failed")
				case "cancelled":
					g, _ := h.repo.AssistantGeneration(user.ID, fixtureSession.ChatSessionID)
					if err := h.repo.CancelAssistantGeneration(user.ID, g.SessionID, g.ID); err != nil {
						return "", err
					}
					<-ctx.Done()
					return "", ctx.Err()
				case "panic":
					panic("model panic")
				}
				return "done", nil
			}
			body, _ := json.Marshal(assistantRequest{SessionID: fixtureSession.ChatSessionID, ProjectID: fixtureSession.ProjectID, Messages: []assistantMessage{{Role: "user", Content: "edit"}}})
			c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
			h.startGeneration(c)
			if rec.Code != 202 {
				t.Fatalf("generation start: %d %s", rec.Code, rec.Body)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				g, ok := h.repo.AssistantGeneration(user.ID, fixtureSession.ChatSessionID)
				if ok && g.Status != "running" {
					want := outcome
					if want == "panic" {
						want = "failed"
					}
					if g.Status != want || g.WorkspaceWarning != "" {
						t.Fatalf("generation outcome: %+v", g)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("generation did not finish")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got := workspaceTurnCheckpointText(t, h, ids[0]); got != outcome {
				t.Fatalf("%s lost edits: %q", outcome, got)
			}
		})
	}
}

type workspaceTurnCheckpointBarrier struct {
	assistantWorkspaceLeaseEngine
	entered chan struct{}
	release chan struct{}
	err     error
}

func (e *workspaceTurnCheckpointBarrier) EndConversationWorkspaceUse(ctx context.Context, id, token string) error {
	if e.entered != nil {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.Join(e.assistantWorkspaceLeaseEngine.EndConversationWorkspaceUse(ctx, id, token), e.err)
}

// A workspace that cannot be saved (too large, or slower than the save
// timeout) must not strand the turn: the switch proceeds and the old
// workspace keeps its files for the idle sweep, which saves before reclaiming.
func TestAssistantWorkspaceTurnSwitchesDespiteFailedCheckpoint(t *testing.T) {
	h, _, s, gateway, ids := workspaceTurnFixture(t)
	if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_read", map[string]any{"path": "note.txt"}); err != nil {
		t.Fatal(err)
	}
	s.workspaceTurn.mu.Lock()
	s.workspaceTurn.engine = &workspaceTurnCheckpointBarrier{assistantWorkspaceLeaseEngine: h.engine, err: errors.New("checkpoint exceeds size limit")}
	s.workspaceTurn.mu.Unlock()
	bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{WorkspaceRunID: ids[1]})
	if err != nil || bound.Previous != ids[0] || !strings.Contains(bound.CheckpointError, "exceeds size limit") {
		t.Fatalf("a failed save blocked or hid the switch: %+v %v", bound, err)
	}
	conv, _ := h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	if s.workspaceRun() != ids[1] || conv.WorkspaceRunID != ids[1] {
		t.Fatal("the switch did not bind the new workspace")
	}
	meta, _ := h.repo.WorkspaceCheckpoint(ids[0])
	if meta.LeaseToken != "" || !meta.Released() {
		t.Fatalf("the old workspace was not handed to the idle sweep: %+v", meta)
	}
	s.workspaceTurn.mu.Lock()
	s.workspaceTurn.engine = h.engine
	_, pending := s.workspaceTurn.saveErrors[ids[0]]
	s.workspaceTurn.mu.Unlock()
	if !pending {
		t.Fatal("the unsaved old workspace would not be reported at turn end")
	}
	if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_read", map[string]any{"path": "note.txt"}); err != nil {
		t.Fatalf("the new workspace is unusable after the switch: %v", err)
	}
	// Switching back and saving it successfully resolves that warning.
	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{WorkspaceRunID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_read", map[string]any{"path": "note.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := s.workspaceTurn.close(context.Background()); err != nil {
		t.Fatalf("a successful save left a stale checkpoint warning: %v", err)
	}
}

// Opening a new workspace after using one this turn is the path the
// missingRepositories hint steers Main Bot to. A save that keeps failing must
// neither block it nor leave behind a created but unbound workspace run.
func TestAssistantWorkspaceTurnNewWorkspaceSurvivesFailedSave(t *testing.T) {
	h, owner, s := chatSandboxFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	en := &workspaceTurnEngine{saveErr: errors.New("workspace checkpoint exceeds size limit")}
	s.workspaceTurn = newAssistantWorkspaceTurn(en, nil)
	t.Cleanup(func() { _ = s.workspaceTurn.close(context.Background()) })
	workspaceRuns := func() []string {
		var ids []string
		for _, run := range h.repo.Runs() {
			if run.IsWorkspace() && run.ExecutionContext.ProjectID == s.ProjectID {
				ids = append(ids, run.ID)
			}
		}
		return ids
	}
	first, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.workspaceTurn.lease(context.Background(), first.Run.ID); err != nil {
		t.Fatal(err)
	}
	second, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "second", New: true})
	if err != nil || !second.Created || second.Previous != first.Run.ID || !strings.Contains(second.CheckpointError, "exceeds size limit") {
		t.Fatalf("a failed save blocked opening a new workspace: %+v %v", second, err)
	}
	stored, _ := h.repo.AssistantSession(owner.ID, s.ChatSessionID)
	if stored.WorkspaceRunID != second.Run.ID || len(stored.OwnWorkspaceRunIDs) != 2 {
		t.Fatalf("the new workspace was not bound: %+v", stored)
	}
	if runs := workspaceRuns(); len(runs) != 2 {
		t.Fatalf("workspace runs = %v, want only the two bound ones", runs)
	}
	if strings.Join(en.ended, ",") != first.Run.ID {
		t.Fatalf("checkpoints = %v, want one for the workspace switched away from", en.ended)
	}
	if err := s.workspaceTurn.close(context.Background()); err == nil || !strings.Contains(err.Error(), first.Run.ID) {
		t.Fatalf("the unsaved workspace was not reported at turn end: %v", err)
	}
	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "late", New: true}); !errors.Is(err, errAssistantTurnClosed) {
		t.Fatalf("a finished turn opened a workspace: %v", err)
	}
	if runs := workspaceRuns(); len(runs) != 2 {
		t.Fatalf("a refused open left a workspace run behind: %v", runs)
	}
}

func TestAssistantWorkspaceTurnClosedRefusesSwitch(t *testing.T) {
	h, _, s, gateway, ids := workspaceTurnFixture(t)
	if err := s.workspaceTurn.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{WorkspaceRunID: ids[1]}); !errors.Is(err, errAssistantTurnClosed) {
		t.Fatalf("a finished turn switched workspaces: %v", err)
	}
	conv, _ := h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	if s.workspaceRun() != ids[0] || conv.WorkspaceRunID != ids[0] {
		t.Fatal("a refused switch changed the binding")
	}
}

func TestAssistantWorkspaceTurnGenerationWaitsForCheckpoint(t *testing.T) {
	h, user, fixtureSession, gateway, ids := workspaceTurnFixture(t)
	barrier := &workspaceTurnCheckpointBarrier{assistantWorkspaceLeaseEngine: h.engine, entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("test checkpoint warning")}
	var release sync.Once
	unblock := func() { release.Do(func() { close(barrier.release) }) }
	defer unblock()
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		var session *AgentToolSession
		agentToolSessions.mu.Lock()
		for _, candidate := range agentToolSessions.sessions {
			if candidate.ChatSessionID == fixtureSession.ChatSessionID {
				session = candidate
			}
		}
		agentToolSessions.mu.Unlock()
		if session == nil || session.workspaceTurn == nil {
			return "", errors.New("generation has no workspace lifetime")
		}
		session.workspaceTurn.mu.Lock()
		session.workspaceTurn.engine = barrier
		session.workspaceTurn.mu.Unlock()
		_, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_write", map[string]any{"path": "note.txt", "content": "latest edit", "expectedSha256": runtime.WorkspaceContentSHA("before")})
		return "done", err
	}
	body, _ := json.Marshal(assistantRequest{SessionID: fixtureSession.ChatSessionID, ProjectID: fixtureSession.ProjectID, Messages: []assistantMessage{{Role: "user", Content: "edit"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("generation start: %d %s", rec.Code, rec.Body)
	}
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("turn never reached its final checkpoint")
	}
	g, _ := h.repo.AssistantGeneration(user.ID, fixtureSession.ChatSessionID)
	meta, _ := h.repo.WorkspaceCheckpoint(ids[0])
	if g.Status != "running" || meta.LeaseToken == "" {
		t.Fatal("generation completed or released the workspace before its checkpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := inflightTasks.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deployment drain did not wait for the checkpoint: %v", err)
	}
	unblock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, _ = h.repo.AssistantGeneration(user.ID, fixtureSession.ChatSessionID)
		if g.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("checkpoint did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if g.Status != "completed" || !strings.Contains(g.WorkspaceWarning, "test checkpoint warning") {
		t.Fatalf("final checkpoint warning was not reported: status=%s warning=%s", g.Status, g.WorkspaceWarning)
	}
	if got := workspaceTurnCheckpointText(t, h, ids[0]); got != "latest edit" {
		t.Fatalf("checkpoint lost edits: %q", got)
	}
}
