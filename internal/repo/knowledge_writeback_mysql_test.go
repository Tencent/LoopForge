package repo

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The write lease is taken through a row lock on MySQL. Only one concurrent
// claimant may win, and an explicit finish makes it reclaimable.
func TestKnowledgeWriteLeaseMySQL(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "kb-git-lease-mysql"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", GitURL: "https://github.com/example/repo.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSource(id)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ok, err := s.ClaimKnowledgeWrite(id, fmt.Sprintf("owner-%d-%d", time.Now().UnixNano(), n), time.Minute)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent claims = %d, want 1", wins.Load())
	}
	src, _ := s.KnowledgeSource(id)
	if _, err := s.FinishKnowledgeWrite(id, src.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimKnowledgeWrite(id, "after", time.Minute); err != nil || !ok {
		t.Fatalf("claim after finish = %v %v", ok, err)
	}
}
