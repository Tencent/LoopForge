package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func followUpRepoFixture(t *testing.T) (*Store, domain.AssistantSession, AssistantGroupFollowUp) {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	session, err := s.EnsureRunAssistantSession("follow-run", "platform", "群聊", 1)
	if err != nil {
		t.Fatal(err)
	}
	session.Messages = []domain.AssistantMessage{{Role: "user", Content: "让大家处理", AuthorID: 2}}
	g := domain.AssistantGeneration{ID: "human-one", SessionID: session.ID, UserID: session.UserID, ActorID: 2, Status: "running"}
	row := AssistantGroupFollowUp{RunID: session.RunID, SessionID: session.ID, OwnerID: session.UserID, ActorID: 2, ProjectID: session.ProjectID, Model: "selected-model", Handled: []byte("[]")}
	if err = s.BeginAuthorizedGroupGeneration(g, session, row); err != nil {
		t.Fatal(err)
	}
	g.Status = "completed"
	if err = s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	session, _ = s.AssistantSession(session.UserID, session.ID)
	row, _ = s.AssistantGroupFollowUp(row.RunID)
	return s, session, row
}
func TestGroupFollowUpClaimAndGenerationAreDurableAndIdempotent(t *testing.T) {
	s, session, row := followUpRepoFixture(t)
	var count atomic.Int32
	var wg sync.WaitGroup
	var claimed AssistantGroupFollowUp
	var mu sync.Mutex
	now := time.Now().UTC().Add(time.Second)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := s.ClaimAssistantGroupFollowUps(now, 16)
			if err != nil {
				t.Error(err)
				return
			}
			for _, v := range rows {
				count.Add(1)
				mu.Lock()
				claimed = v
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("claimed by %d replicas", count.Load())
	}
	// A reconstructed domain repo uses the durable lease and cursor, not process memory.
	restarted := newStore(s.DB)
	g := domain.AssistantGeneration{ID: "automatic-one", SessionID: row.SessionID, UserID: row.OwnerID, ActorID: row.ActorID, Status: "running", Trigger: "group_followup"}
	if err := restarted.BeginAssistantGroupFollowUpGeneration(claimed, g, session, []string{"worker/1/succeeded"}, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginAssistantGroupFollowUpGeneration(claimed, g, session, []string{"worker/1/succeeded"}, 4); err == nil {
		t.Fatal("batch ran twice")
	}
	saved, _ := s.AssistantGroupFollowUp(row.RunID)
	var keys []string
	_ = json.Unmarshal(saved.Handled, &keys)
	if saved.Rounds != 1 || len(keys) != 1 || saved.Status != "running" {
		t.Fatalf("lost durable cursor: %+v", saved)
	}
	if current, _ := s.AssistantSession(row.OwnerID, row.SessionID); len(current.Messages) != 1 || current.Messages[0].Content != "让大家处理" {
		t.Fatal("automatic turn fabricated user history")
	}
	g.Status = "completed"
	if err := s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAssistantGroupFollowUp(row.RunID, row.HumanGenerationID, g.ID, "watching", ""); err != nil {
		t.Fatal(err)
	}
	next := domain.AssistantGeneration{ID: "human-two", SessionID: row.SessionID, UserID: row.OwnerID, ActorID: 3, Status: "running"}
	session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: "新的方向", AuthorID: 3})
	row.ActorID = 3
	if err := s.BeginAuthorizedGroupGeneration(next, session, row); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAssistantGroupFollowUp(row.RunID, "human-one", g.ID, "blocked", "stale"); err == nil {
		t.Fatal("old completion overwrote new user authorization")
	}
	saved, _ = s.AssistantGroupFollowUp(row.RunID)
	if saved.ActorID != 3 || saved.HumanGenerationID != "human-two" || saved.Rounds != 0 || saved.Status != "watching" {
		t.Fatalf("new authorization lost: %+v", saved)
	}
}
func TestGroupFollowUpDoesNotConsumeCursorWhenSessionIsBusyOrChanged(t *testing.T) {
	s, session, row := followUpRepoFixture(t)
	claims, err := s.ClaimAssistantGroupFollowUps(time.Now().Add(time.Second), 1)
	if err != nil || len(claims) != 1 {
		t.Fatal(err, claims)
	}
	row = claims[0]
	human := domain.AssistantGeneration{ID: "busy", SessionID: session.ID, UserID: row.OwnerID, Status: "running"}
	if err = s.BeginAssistantGeneration(human); err != nil {
		t.Fatal(err)
	}
	auto := human
	auto.ActorID = row.ActorID
	auto.ID = "auto"
	auto.Trigger = "group_followup"
	if err = s.BeginAssistantGroupFollowUpGeneration(row, auto, session, []string{"new"}, 4); err == nil {
		t.Fatal("automatic work entered busy slot")
	}
	saved, _ := s.AssistantGroupFollowUp(row.RunID)
	if saved.Rounds != 0 || string(saved.Handled) != "[]" {
		t.Fatal("busy slot consumed cursor")
	}
	human.Status = "completed"
	_ = s.CheckpointAssistantGeneration(human)
	changed := session
	changed.Messages = append(changed.Messages, domain.AssistantMessage{Role: "assistant", Content: "新结果"})
	if err = s.PutAssistantSession(row.OwnerID, changed); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginAssistantGroupFollowUpGeneration(row, auto, session, []string{"new"}, 4); err == nil {
		t.Fatal("stale transcript overwritten")
	}
}
