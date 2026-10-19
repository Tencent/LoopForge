package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestMySQLSharedConversationSerialClaimAndReceiptCAS(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local isolated MySQL required")
	}
	a, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	runID := fmt.Sprintf("shared-mysql-%s", t.Name())
	session, err := a.EnsureRunAssistantSession(runID, "mysql-project", "共享测试", 91)
	if err != nil {
		t.Fatal(err)
	}
	defer a.DB.Where("session_id = ?", session.ID).Delete(&assistantGenerationRow{})
	defer a.DB.Where("id = ?", session.ID).Delete(&assistantSessionRow{})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for i, store := range []*Store{a, b} {
		wait.Add(1)
		go func(i int, s *Store) {
			defer wait.Done()
			<-start
			pending := session
			pending.Messages = []domain.AssistantMessage{{Role: "user", Content: fmt.Sprintf("成员 %d", i), AuthorID: int64(i + 1)}}
			results <- s.BeginAssistantGeneration(domain.AssistantGeneration{ID: fmt.Sprintf("job-%d", i), SessionID: session.ID, UserID: session.UserID, ActorID: int64(i + 1), Status: "running"}, pending)
		}(i, store)
	}
	close(start)
	wait.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("shared claim winners %d", wins)
	}
	g, _ := a.AssistantGeneration(session.UserID, session.ID)
	g.Status = "completed"
	if err := a.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	old, _ := a.AssistantSession(session.UserID, session.ID)
	one := old
	one.Messages = append(append([]domain.AssistantMessage{}, old.Messages...), domain.AssistantMessage{Role: "user", Content: "已通过 MCP 执行 test"})
	if err := a.PutSharedAssistantReceipt(session.UserID, one, old); err != nil {
		t.Fatal(err)
	}
	if err := b.PutSharedAssistantReceipt(session.UserID, old, old); err == nil {
		t.Fatal("stale receipt overwrote newer transcript")
	}
	saved, _ := b.AssistantSession(session.UserID, session.ID)
	if len(saved.Messages) != 2 || !saved.Shared {
		t.Fatal("shared transcript lost")
	}
}
