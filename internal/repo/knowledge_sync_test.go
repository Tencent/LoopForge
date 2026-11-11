package repo

import (
	"agentflow/internal/domain"
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKnowledgeSyncLease(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "lease-test"
	defer s.DeleteKnowledgeSource(id)
	if err = s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, owner := range []string{"a", "b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			ok, e := s.ClaimKnowledgeSync(id, owner, time.Second)
			if e != nil {
				t.Error(e)
			}
			if ok {
				wins.Add(1)
			}
		}(owner)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims=%d", wins.Load())
	}
	src, _ := s.KnowledgeSource(id)
	if ok, e := s.RenewKnowledgeSync(id, src.LeaseOwner, time.Second); e != nil || !ok {
		t.Fatalf("renew %v %v", ok, e)
	}
	if ok, _ := s.ClaimKnowledgeSync(id, "other", time.Second); ok {
		t.Fatal("claimed renewed lease")
	}
	time.Sleep(1100 * time.Millisecond)
	if ok, e := s.ClaimKnowledgeSync(id, "other", time.Second); e != nil || !ok {
		t.Fatalf("takeover %v %v", ok, e)
	}
	if ok, _ := s.FinishKnowledgeSync(id, src.LeaseOwner, "old", 1, ""); ok {
		t.Fatal("stale owner finished")
	}
}

func TestKnowledgePublicationRejectsStaleOwner(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "publication-test"
	defer s.DeleteKnowledgeSource(id)
	if err = s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	s.ClaimKnowledgeSync(id, "old", 100*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	if ok, err := s.ClaimKnowledgeSync(id, "new", time.Second); err != nil || !ok {
		t.Fatalf("takeover %v %v", ok, err)
	}
	wrote := false
	if ok, err := s.PublishKnowledgeSync(context.Background(), id, "old", "stale", 1, func(context.Context) error { wrote = true; return nil }); err != nil || ok || wrote {
		t.Fatalf("stale publish %v %v %v", ok, wrote, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := s.PublishKnowledgeSync(context.Background(), id, "new", "current", 1, func(context.Context) error { close(entered); <-release; return nil })
		done <- e
	}()
	<-entered
	time.Sleep(1100 * time.Millisecond)
	claimed := make(chan bool, 1)
	go func() { ok, _ := s.ClaimKnowledgeSync(id, "third", time.Second); claimed <- ok }()
	select {
	case ok := <-claimed:
		if !ok {
			t.Error("expired lease was not reclaimable during content work")
		}
	case <-time.After(time.Second):
		t.Error("content work blocked takeover")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	src, _ := s.KnowledgeSource(id)
	if src.HeadCommit == "current" || src.LeaseOwner != "third" {
		t.Fatalf("stale publication changed control state: %+v", src)
	}
}
