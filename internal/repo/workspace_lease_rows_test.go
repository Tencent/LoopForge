package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// MySQL reports changed rows, not matched rows. Parallel Main Bot roles touch
// the same lease within one DATETIME(3) millisecond; the repeated write
// changes nothing. Emulate that on SQLite: every checkpoint UPDATE reports 0.
func TestWorkspaceLeaseTouchesSurviveUnchangedRowsLikeMySQL(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := domain.Run{ID: "lease-rows", Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token, err := s.BeginWorkspaceUse(r.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Callback().Update().After("gorm:update").Register("test:mysql_changed_rows", func(db *gorm.DB) {
		if db.Statement.Table == (WorkspaceCheckpoint{}).TableName() {
			db.RowsAffected = 0
		}
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second touch of the same millisecond changes nothing
		if err = s.MarkWorkspaceFollowUp(r.ID, token, now); err != nil {
			t.Fatalf("parallel tool touch %d mistaken for a lost lease: %v", i, err)
		}
		if err = s.RenewWorkspaceUse(r.ID, token, now); err != nil {
			t.Fatalf("same-millisecond renewal %d mistaken for a lost lease: %v", i, err)
		}
	}
	if err = s.MarkWorkspaceFollowUp(r.ID, "foreign", now); err != ErrWorkspaceInUse {
		t.Fatalf("foreign token accepted: %v", err)
	}
	if err = s.MarkWorkspaceFollowUp(r.ID, "", now); err != ErrWorkspaceInUse {
		t.Fatalf("empty token accepted: %v", err)
	}
	if err = s.RenewWorkspaceUse(r.ID, "foreign", now); err != ErrWorkspaceInUse {
		t.Fatalf("foreign token renewed: %v", err)
	}
}
