package repo

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantGenerationCoordinatesSteering(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var st *Store
			var err error
			if backend == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("isolated local Docker MySQL required")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			sessionID := fmt.Sprintf("steer-conflict-%d", time.Now().UnixNano())
			g := domain.AssistantGeneration{
				ID: sessionID + "-first", SessionID: sessionID,
				UserID: 91, Status: "running",
			}
			if err := st.BeginAssistantGeneration(g); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				st.DB.Where("session_id = ?", sessionID).Delete(&assistantGenerationRow{})
				st.DB.Where("session_id = ?", sessionID).Delete(&assistantDiagnosticsRow{})
			})

			type result struct {
				steer domain.AssistantSteer
				err   error
			}
			start := make(chan struct{})
			results := make(chan result, 2)
			for _, request := range []domain.AssistantSteer{
				{Content: "first tab", TriggerID: "steer-a"},
				{Content: "second tab", TriggerID: "steer-b"},
			} {
				go func() {
					<-start
					results <- result{request, st.SteerAssistantGeneration(g.UserID, g.SessionID, g.ID, request)}
				}()
			}
			close(start)
			accepted, rejected := <-results, <-results
			if accepted.err != nil {
				accepted, rejected = rejected, accepted
			}
			if accepted.err != nil || rejected.err == nil || !strings.Contains(rejected.err.Error(), "已有待处理的引导") {
				t.Fatalf("concurrent steers must accept one request and report one pending conflict: %v, %v", accepted.err, rejected.err)
			}
			saved, _ := st.PeekAssistantGeneration(g.UserID, g.SessionID)
			if !saved.CancelRequested || saved.Steer == nil ||
				saved.Steer.TriggerID != accepted.steer.TriggerID || saved.Steer.Content != accepted.steer.Content {
				t.Fatalf("the accepted steer was lost: %+v", saved.Steer)
			}
			pending, err := st.CloseAssistantGenerationSteering(g.UserID, g.SessionID, g.ID)
			if err != nil || pending == nil || pending.TriggerID != accepted.steer.TriggerID || pending.Content != accepted.steer.Content {
				t.Fatalf("finalization must receive the accepted steer: %+v %v", pending, err)
			}

			g.Status = "cancelled"
			if err := st.CheckpointAssistantGeneration(g); err != nil {
				t.Fatal(err)
			}
			g.ID, g.Status = sessionID+"-next", "running"
			if err := st.BeginAssistantGeneration(g); err != nil {
				t.Fatal(err)
			}
			pending, err = st.CloseAssistantGenerationSteering(g.UserID, g.SessionID, g.ID)
			if err != nil || pending != nil {
				t.Fatalf("finalization received a steer from the previous turn: %+v %v", pending, err)
			}
			if err := st.HeartbeatAssistantGeneration(g); err != nil {
				t.Fatal(err)
			}
			if err := st.SteerAssistantGeneration(g.UserID, g.SessionID, g.ID, rejected.steer); !errors.Is(err, errAssistantGenerationEnded) {
				t.Fatalf("a steer arriving after finalization must report an ended turn: %v", err)
			}
			saved, _ = st.PeekAssistantGeneration(g.UserID, g.SessionID)
			if saved.Status != "running" || saved.CancelRequested || saved.Steer != nil {
				t.Fatalf("the generation must stay running while its transcript is saved: %+v", saved)
			}
			g.Status = "completed"
			if err := st.CheckpointAssistantGeneration(g); err != nil {
				t.Fatal(err)
			}
			g.ID, g.Status = sessionID+"-third", "running"
			if err := st.BeginAssistantGeneration(g); err != nil {
				t.Fatal(err)
			}
			if err := st.SteerAssistantGeneration(g.UserID, g.SessionID, g.ID, rejected.steer); err != nil {
				t.Fatalf("next generation must accept a new steer: %v", err)
			}
		})
	}
}

func TestAssistantGenerationClaimCheckpointAndPrivacy(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := domain.AssistantGeneration{ID: "one", SessionID: "chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginAssistantGeneration(g); err == nil {
		t.Fatal("duplicate generation claimed")
	}
	if _, ok := s.AssistantGeneration(12, "chat"); ok {
		t.Fatal("other user accessed generation")
	}
	if err = s.CancelAssistantGeneration(12, "chat", "one"); err == nil {
		t.Fatal("other user cancelled generation")
	}
	if err = s.CancelAssistantGeneration(11, "chat", "one"); err != nil {
		t.Fatal(err)
	}
	g.Events = []domain.AssistantGenerationEvent{{Sequence: 1, Data: []byte(`{"kind":"status","text":"正在处理"}`)}}
	if err = s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.AssistantGeneration(11, "chat")
	if !saved.CancelRequested || len(saved.Events) != 1 {
		t.Fatal("checkpoint lost cancellation or events")
	}
	g.Status = "cancelled"
	if err = s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	g.ID = "two"
	g.Status = "running"
	if err = s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	stale := g
	stale.ID = "one"
	stale.Status = "completed"
	if err = s.CheckpointAssistantGeneration(stale); err == nil {
		t.Fatal("old worker overwrote new generation")
	}
	if err = s.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", "chat").Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	saved, _ = s.AssistantGeneration(11, "chat")
	if saved.Status != "interrupted" {
		t.Fatal("orphan generation did not become interrupted")
	}
}

func TestPeekAssistantGenerationLeavesStaleTurnRunning(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := domain.AssistantGeneration{ID: "live", SessionID: "live-chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", "live-chat").Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	peek, ok := s.PeekAssistantGeneration(11, "live-chat")
	if !ok || peek.Status != "running" {
		t.Fatalf("peek reaped a live turn: %+v", peek)
	}
	if err = s.HeartbeatAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	peek, _ = s.PeekAssistantGeneration(11, "live-chat")
	if peek.Status != "running" || time.Since(peek.UpdatedAt) > 5*time.Second {
		t.Fatalf("heartbeat did not refresh the live turn: %+v", peek)
	}
}

// Only the reader whose update reaps a turn gets its lease back, and the next
// turn starts without the previous turn's lease.
func TestReapAssistantGenerationReturnsRecordedLeaseOnce(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := domain.AssistantGeneration{ID: "dead", SessionID: "dead-chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAssistantGenerationWorkspaceLease(11, "dead-chat", "dead", "run-1", "token-1"); err != nil {
		t.Fatal(err)
	}
	// Recording a lease is not a heartbeat.
	stale := time.Now().Add(-time.Minute).UTC()
	if err = s.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", "dead-chat").Update("updated_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAssistantGenerationWorkspaceLease(11, "dead-chat", "dead", "run-1", "token-1"); err != nil {
		t.Fatal(err)
	}
	if peek, _ := s.PeekAssistantGeneration(11, "dead-chat"); time.Since(peek.UpdatedAt) < 50*time.Second {
		t.Fatalf("recording a lease refreshed the heartbeat: %+v", peek.UpdatedAt)
	}
	if states := s.AssistantGenerationStatuses(11, []string{"dead-chat"}); !states["dead-chat"].Stale || states["dead-chat"].Status != "interrupted" {
		t.Fatalf("late heartbeat not projected as stale: %+v", states["dead-chat"])
	}
	reaped, lease, ok := s.ReapAssistantGeneration(11, "dead-chat")
	if !ok || reaped.Status != "interrupted" || lease != (AssistantGenerationLease{RunID: "run-1", Token: "token-1"}) {
		t.Fatalf("reap = %+v %+v %v", reaped, lease, ok)
	}
	if states := s.AssistantGenerationStatuses(11, []string{"dead-chat"}); states["dead-chat"].Stale {
		t.Fatalf("a stored interruption is reported as stale: %+v", states["dead-chat"])
	}
	if _, lease, _ = s.ReapAssistantGeneration(11, "dead-chat"); lease.Token != "" {
		t.Fatalf("a second reader got the lease again: %+v", lease)
	}
	// Another user cannot record a lease on this turn.
	next := domain.AssistantGeneration{ID: "next", SessionID: "dead-chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(next); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAssistantGenerationWorkspaceLease(12, "dead-chat", "next", "run-1", "intruder"); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", "dead-chat").Update("updated_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	if _, lease, _ = s.ReapAssistantGeneration(11, "dead-chat"); lease.Token != "" {
		t.Fatalf("the next turn inherited a lease: %+v", lease)
	}
}
