package repo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAssistantSessionListsDoNotTransferTranscripts(t *testing.T) {
	s := queryTestStore(t)
	prefix := fmt.Sprintf("session-query-%d", time.Now().UnixNano())
	userID := time.Now().UnixNano()
	runID := prefix + "-run"
	rootID := RunAssistantSessionID(runID)
	transcript := strings.Repeat("large-chat-history-", 100000)
	items := []domain.AssistantSession{
		{ID: prefix, Title: "个人会话", ProjectID: prefix, Model: "test-model", Archived: true},
		{ID: rootID, Title: "共享会话", ProjectID: prefix, RunID: runID, Shared: true},
		{ID: "sop-branch-" + prefix, Title: "共享分支", ProjectID: prefix, RunID: runID, Shared: true, ParentSessionID: rootID, RootSessionID: rootID, HistoryRevision: 3, ForkMessageIndex: 2},
	}
	want := map[string]domain.AssistantSession{}
	for _, item := range items {
		item.Messages = []domain.AssistantMessage{{Role: "user", Content: transcript}}
		if err := s.PutAssistantSession(userID, item); err != nil {
			t.Fatal(err)
		}
		id := item.ID
		t.Cleanup(func() { _ = s.DB.Where("id = ?", id).Delete(&assistantSessionRow{}).Error })
		full, ok := s.AssistantSession(userID, id)
		if !ok {
			t.Fatal("missing session")
		}
		full.Messages = nil
		want[id] = full
	}
	// Legacy rows may retain title/model/project/time only in their columns.
	legacy := items[0]
	legacy.Title, legacy.ProjectID, legacy.Model = "", "", ""
	legacy.Messages = []domain.AssistantMessage{{Role: "user", Content: transcript}}
	if err := s.DB.Model(&assistantSessionRow{}).Where("id = ?", legacy.ID).Update("payload", mustJSON(legacy)).Error; err != nil {
		t.Fatal(err)
	}
	captured := 0
	// Databases without JSON_TABLE backfill legacy participant summaries once;
	// the steady-state lists below must never transfer transcripts.
	s.ListAssistantSessions(userID)
	s.ListSharedAssistantSessions()
	callback := prefix + "-projection"
	if err := s.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		rows, ok := tx.Statement.Dest.(*[]assistantSessionRow)
		if !ok {
			return
		}
		for _, row := range *rows {
			if _, ok := want[row.ID]; ok {
				captured++
				if len(row.Payload) > 2048 || strings.Contains(string(row.Payload), "large-chat-history-") {
					t.Errorf("list transferred transcript for %s: %d bytes", row.ID, len(row.Payload))
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Callback().Query().Remove(callback) })
	for _, list := range [][]domain.AssistantSession{s.ListAssistantSessions(userID), s.ListSharedAssistantSessions()} {
		for _, item := range list {
			if expected, ok := want[item.ID]; ok && !reflect.DeepEqual(item, expected) {
				t.Fatalf("list metadata changed: got=%+v want=%+v", item, expected)
			}
		}
	}
	if captured != 5 { // Three owned sessions, plus shared root and branch.
		t.Fatalf("expected five projected rows, got %d", captured)
	}
	full, ok := s.AssistantSession(userID, rootID)
	if !ok || len(full.Messages) != 1 || full.Messages[0].Content != transcript {
		t.Fatal("list projection changed the stored transcript or detail read")
	}
}

func TestAssistantAuthorizationMetadataBatches(t *testing.T) {
	s := queryTestStore(t)
	prefix := fmt.Sprintf("session-scope-%d-", time.Now().UnixNano())
	ownerID := int64(42)
	var runs []runRow
	var projects []projectRow
	var ids []string
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("%s%d", prefix, i)
		ids = append(ids, id)
		runs = append(runs, runRow{ID: id, ExecutionContext: mustJSON(domain.TelemetryContext{ProjectID: id})})
		projects = append(projects, projectRow{ID: id, OwnerUserID: &ownerID, Payload: mustJSON(domain.Project{ID: id, Name: "not needed for authorization"})})
	}
	if err := s.DB.CreateInBatches(&runs, 100).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Where("id IN ?", ids).Delete(&runRow{}) })
	if err := s.DB.CreateInBatches(&projects, 100).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Where("id IN ?", ids).Delete(&projectRow{}) })
	if err := s.DB.Create(&projectMemberRow{ProjectID: ids[0], UserID: 43, Role: "member"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Where("project_id = ?", ids[0]).Delete(&projectMemberRow{}) })
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	requested := append(append([]string{}, ids...), prefix+"missing")
	gotRuns, err := s.RunProjectIDs(requested)
	if err != nil || len(gotRuns) != len(ids) {
		t.Fatalf("run scopes: count=%d err=%v", len(gotRuns), err)
	}
	gotProjects, err := s.ProjectMemberships(requested)
	if err != nil || len(gotProjects) != len(ids) {
		t.Fatalf("project memberships: count=%d err=%v", len(gotProjects), err)
	}
	for _, id := range ids {
		if gotRuns[id] != id || gotProjects[id].ID != id || gotProjects[id].OwnerID != ownerID || gotProjects[id].Name != "" {
			t.Fatalf("incorrect authorization metadata for %s", id)
		}
	}
	if members := gotProjects[ids[0]].Members; len(members) != 1 || members[0].UserID != 43 {
		t.Fatalf("project membership lost: %+v", members)
	}
	if len(recorder.queries) != 6 {
		t.Fatalf("expected two batches of three metadata queries, got %d", len(recorder.queries))
	}
	before := len(recorder.queries)
	if empty, err := s.RunProjectIDs(nil); err != nil || len(empty) != 0 {
		t.Fatal("empty run scopes")
	}
	if empty, err := s.ProjectMemberships(nil); err != nil || len(empty) != 0 {
		t.Fatal("empty project memberships")
	}
	if len(recorder.queries) != before {
		t.Fatal("empty metadata lookup queried the database")
	}
}

func TestAssistantParticipantBackfillForLegacyRows(t *testing.T) {
	s := queryTestStore(t)
	id := fmt.Sprintf("session-backfill-%d", time.Now().UnixNano())
	userID := time.Now().UnixNano()
	item := domain.AssistantSession{ID: id, Title: "legacy", Messages: []domain.AssistantMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ok"}}}
	if err := s.PutAssistantSession(userID, item); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DB.Where("id = ?", id).Delete(&assistantSessionRow{}).Error })
	// Simulate a row saved before participants were cached.
	legacy := item
	legacy.UserID = userID
	if err := s.DB.Model(&assistantSessionRow{}).Where("id = ?", id).Update("payload", mustJSON(legacy)).Error; err != nil {
		t.Fatal(err)
	}
	var before assistantSessionRow
	if err := s.DB.Where("id = ?", id).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	if n := s.AssistantRepo.backfillAssistantParticipants([]assistantSessionRow{{ID: id, Payload: []byte(`{}`)}}); n != 1 {
		t.Fatalf("backfilled %d rows", n)
	}
	var after assistantSessionRow
	if err := s.DB.Where("id = ?", id).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !after.Updated.Equal(*before.Updated) {
		t.Fatalf("backfill changed updated_at: %v -> %v", before.Updated, after.Updated)
	}
	var meta struct {
		Version      int                           `json:"participantsVersion"`
		Participants []domain.AssistantParticipant `json:"participants"`
		Messages     []domain.AssistantMessage     `json:"messages"`
	}
	if err := json.Unmarshal(after.Payload, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Version != 1 || len(meta.Participants) != 1 || meta.Participants[0].ID != userID || len(meta.Messages) != 2 {
		t.Fatalf("unexpected backfill: %+v", meta)
	}
}
