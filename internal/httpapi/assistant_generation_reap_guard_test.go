package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

// Opening a conversation whose turn this process still executes, after one
// slow heartbeat, shows the turn as running and does not reap it. Writes to
// the conversation are covered in assistant_generation_read_paths_test.go.
func TestOpeningAConversationKeepsALocalTurnAfterSlowHeartbeat(t *testing.T) {
	h, u := workbenchFixture(t)
	g := staleGeneration(t, h, u, "guarded-chat", "guarded-turn")
	markLocal(t, g.ID)
	c, rec := workbenchContext(u, "GET", "/api/assistant/sessions/guarded-chat", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "guarded-chat"}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	if stored, _ := h.repo.PeekAssistantGeneration(u.ID, "guarded-chat"); stored.Status != "running" {
		t.Fatalf("opening the conversation wrote the local turn as %q", stored.Status)
	}
}

// An ordinary chat turn (no SOP run) records the workspace lease its first
// tool takes, before the turn ends, so a reader that reaps the turn after its process died can free
// exactly that lease.
func TestGenerationRecordsTheWorkspaceLeaseItsToolTakes(t *testing.T) {
	h, user, fixtureSession, gateway, ids := workspaceTurnFixture(t)
	type recorded struct {
		RunID string `gorm:"column:workspace_run_id"`
		Lease string `gorm:"column:workspace_lease"`
	}
	seen := make(chan recorded, 1)
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
		if _, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_write", map[string]any{"path": "note.txt", "content": "after", "expectedSha256": runtime.WorkspaceContentSHA("before")}); err != nil {
			return "", err
		}
		var row recorded
		if err := h.repo.DB.Table("assistant_generations").Select("workspace_run_id, workspace_lease").Where("session_id = ?", fixtureSession.ChatSessionID).Scan(&row).Error; err != nil {
			return "", err
		}
		meta, _ := h.repo.WorkspaceCheckpoint(ids[0])
		if meta.LeaseToken != row.Lease {
			row.Lease = "mismatch:" + row.Lease + "!=" + meta.LeaseToken
		}
		seen <- row
		return "done", nil
	}
	body, _ := json.Marshal(assistantRequest{SessionID: fixtureSession.ChatSessionID, ProjectID: fixtureSession.ProjectID, Messages: []assistantMessage{{Role: "user", Content: "edit"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("generation start: %d %s", rec.Code, rec.Body)
	}
	select {
	case row := <-seen:
		if row.RunID != ids[0] || row.Lease == "" || len(row.Lease) > 9 && row.Lease[:9] == "mismatch:" {
			t.Fatalf("lease recorded on the generation: %+v", row)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("generation did not reach its tool")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if g, ok := h.repo.PeekAssistantGeneration(user.ID, fixtureSession.ChatSessionID); ok && g.Status != "running" {
			if g.Status != "completed" {
				t.Fatalf("generation outcome: %+v", g)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
