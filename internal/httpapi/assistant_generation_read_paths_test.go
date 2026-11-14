package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

// readPathCase is one handler that refuses to act while a turn is running.
// Each one must go through readGeneration: a turn this process still executes
// keeps blocking it after a late heartbeat, and a turn nobody executes any
// more is reaped so it no longer blocks the conversation.
type readPathCase struct {
	name string
	// busy is the refusal the handler returns while the turn is running.
	busy string
	call func(t *testing.T, h *assistantHandlers, u domain.User, sessionID string) (int, string)
}

func callSession(h *assistantHandlers, u domain.User, method, sessionID string, body []byte) (int, string) {
	c, rec := workbenchContext(u, method, "/api/assistant/sessions/"+sessionID, bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	h.session(c)
	return rec.Code, rec.Body.String()
}

func generationReadPaths() []readPathCase {
	return []readPathCase{
		{name: "session PUT", busy: "生成期间不能改写会话", call: func(t *testing.T, h *assistantHandlers, u domain.User, id string) (int, string) {
			stored, ok := h.repo.AssistantSession(u.ID, id)
			if !ok {
				t.Fatalf("session %s missing", id)
			}
			body, _ := json.Marshal(stored)
			return callSession(h, u, "PUT", id, body)
		}},
		{name: "session DELETE", busy: "请先停止生成后删除会话", call: func(t *testing.T, h *assistantHandlers, u domain.User, id string) (int, string) {
			return callSession(h, u, "DELETE", id, nil)
		}},
		{name: "fork", busy: "请先停止原会话的生成", call: func(t *testing.T, h *assistantHandlers, u domain.User, id string) (int, string) {
			stored, ok := h.repo.AssistantSession(u.ID, id)
			if !ok {
				t.Fatalf("session %s missing", id)
			}
			body, _ := json.Marshal(gin.H{"messageIndex": 0, "content": "换个问法", "updatedAt": stored.UpdatedAt})
			c, rec := workbenchContext(u, "POST", "/api/assistant/sessions/"+id+"/fork", bytes.NewBuffer(body))
			c.Params = gin.Params{{Key: "id", Value: id}}
			h.forkSession(c)
			return rec.Code, rec.Body.String()
		}},
		{name: "execution mode switch", busy: "请先停止当前生成再切换模式", call: func(t *testing.T, h *assistantHandlers, u domain.User, id string) (int, string) {
			c, rec := workbenchContext(u, "PUT", "/api/assistant/sessions/"+id+"/mode", bytes.NewBufferString(`{"mode":"platform"}`))
			c.Params = gin.Params{{Key: "id", Value: id}}
			h.sessionMode(c)
			return rec.Code, rec.Body.String()
		}},
		{name: "proposal decision", busy: "请等待回复结束后再处理建议", call: func(t *testing.T, h *assistantHandlers, u domain.User, id string) (int, string) {
			c, rec := workbenchContext(u, "POST", "/api/assistant/sessions/"+id+"/proposals", bytes.NewBufferString(`{"messageIndex":0,"draftIndex":0,"decision":"declined"}`))
			c.Params = gin.Params{{Key: "id", Value: id}}
			h.decideProposal(c)
			return rec.Code, rec.Body.String()
		}},
	}
}

// The PR routes every non-owner read through readGeneration. Its own test
// covers polls, the list, chat and steering; these are the remaining
// handlers that refuse to act on a running turn.
func TestLocalTurnStillBlocksEveryRunningGuardAfterASlowHeartbeat(t *testing.T) {
	for i, tc := range generationReadPaths() {
		t.Run(tc.name, func(t *testing.T) {
			h, u := workbenchFixture(t)
			sessionID := "local-guard-" + string(rune('a'+i))
			g := staleGeneration(t, h, u, sessionID, sessionID+"-turn")
			markLocal(t, g.ID)

			code, body := tc.call(t, h, u, sessionID)
			if code != 409 || !strings.Contains(body, tc.busy) {
				t.Fatalf("a live local turn did not block %s: %d %s", tc.name, code, body)
			}
			if stored, ok := h.repo.PeekAssistantGeneration(u.ID, sessionID); !ok || stored.Status != "running" {
				t.Fatalf("%s wrote the live local turn as %q", tc.name, stored.Status)
			}
			if _, ok := h.repo.AssistantSession(u.ID, sessionID); !ok {
				t.Fatalf("%s removed the conversation of a live turn", tc.name)
			}
		})
	}
}

// A late turn nobody in this process executes is still reaped on each of
// those paths, so a dead replica cannot block the conversation for good.
func TestRemoteStaleTurnIsReapedOnEveryRunningGuard(t *testing.T) {
	for i, tc := range generationReadPaths() {
		t.Run(tc.name, func(t *testing.T) {
			h, u := workbenchFixture(t)
			sessionID := "remote-guard-" + string(rune('a'+i))
			staleGeneration(t, h, u, sessionID, sessionID+"-turn")

			code, body := tc.call(t, h, u, sessionID)
			if strings.Contains(body, tc.busy) {
				t.Fatalf("a dead remote turn still blocks %s: %d %s", tc.name, code, body)
			}
			if stored, ok := h.repo.PeekAssistantGeneration(u.ID, sessionID); ok && stored.Status != "interrupted" {
				t.Fatalf("%s left the dead remote turn as %q", tc.name, stored.Status)
			}
		})
	}
}

// The tool gateway asks whether a native tool call still belongs to a running
// turn. A slow heartbeat must not make it deny the tools of a live local turn.
func TestNativeToolTurnActiveKeepsALocalTurnThroughASlowHeartbeat(t *testing.T) {
	h, u := workbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	for _, local := range []bool{true, false} {
		sessionID := "tool-remote"
		if local {
			sessionID = "tool-local"
		}
		g := staleGeneration(t, h, u, sessionID, sessionID+"-turn")
		if local {
			markLocal(t, g.ID)
		}
		ctx := context.WithValue(context.Background(), assistantControlScopeKey{}, assistantControlScope{SessionID: sessionID, GenerationID: g.ID})
		s := &AgentToolSession{ChatOwnerID: u.ID, ChatSessionID: sessionID, ctx: ctx}
		active := gateway.nativeToolTurnActive(context.Background(), s)
		stored, _ := h.repo.PeekAssistantGeneration(u.ID, sessionID)
		if local && (!active || stored.Status != "running") {
			t.Fatalf("a live local turn lost its tools: active=%v status=%q", active, stored.Status)
		}
		if !local && (active || stored.Status != "interrupted") {
			t.Fatalf("a dead remote turn kept its tools: active=%v status=%q", active, stored.Status)
		}
	}
}

// A background turn on a SOP conversation takes the run's foreground
// workspace lease and records exactly that lease on its generation, so a
// reader that later reaps the turn can release it. The recorded lease does
// not outlive the turn: the next turn starts with none.
func TestStartGenerationRecordsTheWorkspaceLeaseItHolds(t *testing.T) {
	h, a, _, _, session := sharedWorkbenchFixture(t)
	h.engine = engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	entered, release := make(chan struct{}), make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		select {
		case <-release:
			return "完成", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	messages := append(append([]assistantMessage(nil), session.Messages...), assistantMessage{Role: "user", Content: "后台执行"})
	body, _ := json.Marshal(assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Messages: messages})
	c, rec := workbenchContext(a, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	var recorded struct {
		GenerationID   string
		UserID         int64
		WorkspaceRunID string
		WorkspaceLease string
	}
	if err := h.repo.DB.Table("assistant_generations").Select("generation_id, user_id, workspace_run_id, workspace_lease").Where("session_id = ?", session.ID).Take(&recorded).Error; err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := h.repo.WorkspaceCheckpoint(session.RunID)
	if recorded.WorkspaceRunID != session.RunID || recorded.WorkspaceLease == "" || recorded.WorkspaceLease != checkpoint.LeaseToken {
		t.Fatalf("recorded lease %q on %q, workspace holds %q", recorded.WorkspaceLease, recorded.WorkspaceRunID, checkpoint.LeaseToken)
	}
	close(release)
	released = true
	deadline := time.Now().Add(10 * time.Second)
	for {
		if g, ok := h.repo.PeekAssistantGeneration(recorded.UserID, session.ID); ok && g.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	next := domain.AssistantGeneration{ID: "after-" + recorded.GenerationID, SessionID: session.ID, UserID: recorded.UserID, ProjectID: session.ProjectID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(next); err != nil {
		t.Fatal(err)
	}
	ageGeneration(t, h, session.ID)
	if _, lease, _ := h.repo.ReapAssistantGeneration(recorded.UserID, session.ID); lease.Token != "" {
		t.Fatalf("the next turn inherited the finished turn's lease: %+v", lease)
	}
}
