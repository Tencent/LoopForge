package repo

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// MySQL stamps heartbeats with UTC_TIMESTAMP(3), so their age must be measured
// on the database clock. With this host's time.Now() instead, a database clock
// behind the API host reaps a turn that just checked in, and one ahead of it
// never reaps a dead turn. SET timestamp skews the database clock of the one
// connection the store is pinned to.
func TestMySQLHeartbeatAgeIsMeasuredOnTheDatabaseClock(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local isolated MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sqlDB, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	skew := func(d time.Duration) {
		t.Helper()
		if err := s.DB.Exec("SET timestamp = ?", float64(time.Now().Add(d).UnixMicro())/1e6).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer s.DB.Exec("SET timestamp = DEFAULT")

	const owner = 41
	session := fmt.Sprintf("clock-skew-%d", time.Now().UnixNano())
	defer s.DB.Where("session_id = ?", session).Delete(&assistantGenerationRow{})
	approval := func(id string) error {
		return s.CreateNativeApproval(owner, session, "phase", domain.AssistantNativeApproval{ID: id, GenerationID: session, ActorID: owner, Tool: "Write", Input: json.RawMessage(`{"path":"a.txt"}`)})
	}

	// Database clock two minutes behind this host: the turn just checked in.
	skew(-2 * time.Minute)
	g := domain.AssistantGeneration{ID: session, SessionID: session, UserID: owner, ActorID: owner, Status: "running"}
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if states := s.AssistantGenerationStatuses(owner, []string{session}); states[session].Stale || states[session].Status != "running" {
		t.Fatalf("a fresh turn is projected as stale: %+v", states[session])
	}
	if got, lease, _ := s.ReapAssistantGeneration(owner, session); got.Status != "running" || lease.Token != "" {
		t.Fatalf("a fresh turn was reaped: %+v", got)
	}
	if err := s.HeartbeatAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := approval("fresh"); err != nil {
		t.Fatalf("a live turn refused an approval: %v", err)
	}
	if items := s.PendingNativeApprovals(owner, session); len(items) != 1 {
		t.Fatalf("a live turn hid its approval: %+v", items)
	}

	// Database clock two minutes ahead: silent for a minute on that clock.
	skew(2 * time.Minute)
	if err := s.DB.Exec("UPDATE assistant_generations SET updated_at = UTC_TIMESTAMP(3) - INTERVAL 60 SECOND WHERE session_id = ?", session).Error; err != nil {
		t.Fatal(err)
	}
	if items := s.PendingNativeApprovals(owner, session); len(items) != 0 {
		t.Fatalf("a dead turn kept its approval: %+v", items)
	}
	if err := approval("late"); err == nil {
		t.Fatal("a dead turn accepted an approval")
	}
	if states := s.AssistantGenerationStatuses(owner, []string{session}); !states[session].Stale || states[session].Status != "interrupted" {
		t.Fatalf("a dead turn is not projected as stale: %+v", states[session])
	}
	if got, _, _ := s.ReapAssistantGeneration(owner, session); got.Status != "interrupted" {
		t.Fatalf("a dead turn was not reaped: %+v", got)
	}
}
