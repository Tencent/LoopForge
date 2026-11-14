package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

// staleGeneration starts a turn whose last durable heartbeat is a minute old,
// as after a slow commit or a dead process.
func staleGeneration(t *testing.T, h *assistantHandlers, u domain.User, sessionID, generationID string) domain.AssistantGeneration {
	t.Helper()
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: sessionID, ProjectID: "workbench", Title: sessionID, Messages: []domain.AssistantMessage{{Role: "user", Content: "继续"}}}); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: generationID, SessionID: sessionID, UserID: u.ID, ProjectID: "workbench", Status: "running", MessageCount: 1}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	ageGeneration(t, h, sessionID)
	return g
}

func ageGeneration(t *testing.T, h *assistantHandlers, sessionID string) {
	t.Helper()
	if err := h.repo.DB.Table("assistant_generations").Where("session_id = ?", sessionID).UpdateColumn("updated_at", time.Now().Add(-time.Minute).UTC()).Error; err != nil {
		t.Fatal(err)
	}
}

func markLocal(t *testing.T, id string) {
	t.Helper()
	assistantLiveGenerations.Store(id, &assistantLiveGeneration{changed: make(chan struct{})})
	t.Cleanup(func() { assistantLiveGenerations.Delete(id) })
}

func listedStatus(t *testing.T, h *assistantHandlers, u domain.User, sessionID string) string {
	t.Helper()
	c, rec := workbenchContext(u, "GET", "/api/assistant/sessions?projectId=workbench", bytes.NewBuffer(nil))
	h.listSessions(c)
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list []struct {
		ID               string `json:"id"`
		GenerationStatus string `json:"generationStatus"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID == sessionID {
			return item.GenerationStatus
		}
	}
	t.Fatalf("session %s not listed: %s", sessionID, rec.Body)
	return ""
}

// A turn this process is still executing survives a late heartbeat on every
// read path: polls, the session list, a follow-up message and steering.
func TestLocalTurnSurvivesSlowHeartbeatOnEveryRead(t *testing.T) {
	h, u := workbenchFixture(t)
	g := staleGeneration(t, h, u, "slow-chat", "slow-turn")
	markLocal(t, g.ID)

	if got, ok := h.readGeneration(u.ID, "slow-chat"); !ok || got.Status != "running" {
		t.Fatalf("read reaped a local turn: %+v", got)
	}
	if got, ok := h.currentGeneration(u.ID, "slow-chat"); !ok || got.Status != "running" {
		t.Fatalf("poll reaped a local turn: %+v", got)
	}
	if status := listedStatus(t, h, u, "slow-chat"); status != "running" {
		t.Fatalf("list shows a local turn as %q", status)
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/chat", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"slow-chat","messages":[{"role":"user","content":"继续"},{"role":"user","content":"再来"}]}`))
	h.chat(c)
	if rec.Code != 409 {
		t.Fatalf("a second chat on a live turn was not refused: %d %s", rec.Code, rec.Body)
	}
	c, rec = workbenchContext(u, "POST", "/api/assistant/sessions/slow-chat/generation/steer", bytes.NewBufferString(`{"content":"改一下"}`))
	c.Params = gin.Params{{Key: "id", Value: "slow-chat"}}
	h.steerGeneration(c)
	if rec.Code == 409 {
		t.Fatalf("steering a live turn was refused as finished: %s", rec.Body)
	}
	if stored, _ := h.repo.PeekAssistantGeneration(u.ID, "slow-chat"); stored.Status != "running" {
		t.Fatalf("a read wrote the local turn as %q", stored.Status)
	}
}

// Without a local owner the late turn is reaped. Once that is stored, the list
// reports it as stored instead of hiding it behind a stale in-memory entry.
func TestListReportsAStoredInterruptionEvenWithALiveEntry(t *testing.T) {
	h, u := workbenchFixture(t)
	g := staleGeneration(t, h, u, "dead-chat", "dead-turn")
	if status := listedStatus(t, h, u, "dead-chat"); status != "interrupted" {
		t.Fatalf("late remote turn listed as %q", status)
	}
	if got, _ := h.readGeneration(u.ID, "dead-chat"); got.Status != "interrupted" {
		t.Fatalf("late remote turn not reaped: %+v", got)
	}
	markLocal(t, g.ID)
	if status := listedStatus(t, h, u, "dead-chat"); status != "interrupted" {
		t.Fatalf("a stored interruption listed as %q", status)
	}
}

// A reaped turn frees its own lease so the next message can start. A lease its
// holder still renews, or one a newer turn took, is never freed by the reap.
func TestReapReleasesOnlyTheAbandonedTurnsWorkspaceLease(t *testing.T) {
	h, u := workbenchFixture(t)
	now := time.Now().UTC()
	taken := now.Add(-2 * time.Minute)
	for _, id := range []string{"run-dead", "run-renewed", "run-replaced"} {
		if err := h.repo.PutRun(domain.Run{ID: id, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
			t.Fatal(err)
		}
	}
	begin := func(runID string, at time.Time) string {
		t.Helper()
		token, err := h.repo.BeginWorkspaceUse(runID, at)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	free := func(runID string) bool {
		t.Helper()
		token, err := h.repo.BeginWorkspaceUse(runID, time.Now().UTC())
		if errors.Is(err, repo.ErrWorkspaceInUse) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = h.repo.FinishWorkspaceUse(runID, token, time.Now().UTC(), nil, "")
		return true
	}

	// The process died: nothing renews the lease and nothing heartbeats.
	dead := staleGeneration(t, h, u, "lease-dead", "turn-dead")
	h.recordGenerationWorkspaceLease(dead, "run-dead", begin("run-dead", taken))
	ageGeneration(t, h, "lease-dead")
	if got, _ := h.readGeneration(u.ID, "lease-dead"); got.Status != "interrupted" {
		t.Fatalf("dead turn not reaped: %+v", got)
	}
	if !free("run-dead") {
		t.Fatal("the dead turn's lease still blocks the next message")
	}

	// Another replica is executing it: its heartbeat is late but it still
	// renews the lease. The turn is reaped as before; its lease stays.
	renewed := staleGeneration(t, h, u, "lease-renewed", "turn-renewed")
	token := begin("run-renewed", taken)
	h.recordGenerationWorkspaceLease(renewed, "run-renewed", token)
	ageGeneration(t, h, "lease-renewed")
	if err := h.repo.RenewWorkspaceUse("run-renewed", token, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	h.readGeneration(u.ID, "lease-renewed")
	if free("run-renewed") {
		t.Fatal("a lease its holder still renews was released")
	}

	// The reaped turn's lease already ended and a newer use took the
	// workspace before the reader got there.
	replaced := staleGeneration(t, h, u, "lease-replaced", "turn-replaced")
	old := begin("run-replaced", taken)
	h.recordGenerationWorkspaceLease(replaced, "run-replaced", old)
	ageGeneration(t, h, "lease-replaced")
	if err := h.repo.FinishWorkspaceUse("run-replaced", old, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	newer := begin("run-replaced", taken)
	h.readGeneration(u.ID, "lease-replaced")
	if kept, _ := h.repo.WorkspaceCheckpoint("run-replaced"); kept.LeaseToken != newer {
		t.Fatalf("a newer use lost its lease to the reap: %q", kept.LeaseToken)
	}
}

// Ordinary conversations take their lease during the turn; it is recorded on
// the generation as soon as it is taken.
func TestWorkspaceTurnRecordsEachLeaseItTakes(t *testing.T) {
	turn := newAssistantWorkspaceTurn(&workspaceTurnEngine{}, nil)
	t.Cleanup(func() { _ = turn.close(context.Background()) })
	var recorded []string
	turn.recordLeases(func(runID, token string) { recorded = append(recorded, runID+"="+token) })
	for range 2 {
		if _, err := turn.lease(context.Background(), "run-a"); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorded) != 1 || recorded[0] != "run-a=token-run-a" {
		t.Fatalf("recorded leases: %v", recorded)
	}
}
