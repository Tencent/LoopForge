package repo

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// Use only a disposable local MySQL database. SQLite cannot reproduce the
// gap-lock deadlock caused by creating two previously missing session IDs.
func TestMySQLConcurrentAssistantSessionCreation(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_ASSISTANT_CONCURRENCY_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable local Docker MySQL required")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatal("a local MySQL DSN is required")
	}
	st, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	prefix := fmt.Sprintf("zzzz-session-concurrency-%d-", time.Now().UnixNano())
	ids := []string{prefix + "a", prefix + "b", prefix + "z"}
	t.Cleanup(func() { st.DB.Where("id IN ?", ids).Delete(&assistantSessionRow{}) })
	if err := st.PutAssistantSession(1, domain.AssistantSession{ID: ids[2]}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var arrivals atomic.Int32
	const callback = "test:concurrent_session_gap_locks"
	if err := st.DB.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table != "assistant_sessions" || !errors.Is(db.Error, gorm.ErrRecordNotFound) {
			return
		}
		if _, locked := db.Statement.Clauses["FOR"]; locked && arrivals.Add(1) <= 2 {
			entered <- struct{}{}
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer st.DB.Callback().Query().Remove(callback)
	results := make(chan error, 2)
	for _, id := range ids[:2] {
		go func(id string) {
			results <- st.PutAssistantSession(1, domain.AssistantSession{ID: id, Title: id})
		}(id)
	}
	ready := true
	for i := 0; i < 2 && ready; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Error("concurrent session writers did not acquire their gap locks")
			ready = false
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Error("concurrent creation failed", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("session creation did not finish")
		}
	}
	for _, id := range ids[:2] {
		if got, ok := st.AssistantSession(1, id); !ok || got.Title != id {
			t.Error("session creation lost its payload", id)
		}
	}
}
