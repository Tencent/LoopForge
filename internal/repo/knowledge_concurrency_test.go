package repo

import (
	"agentflow/internal/domain"
	"context"
	"gorm.io/gorm"
	"os"
	"testing"
	"time"
)

func concurrencyStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPublicationDoesNotBlockRenewal(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-renew"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DeleteKnowledgeSource(id) })
	if ok, err := s.ClaimKnowledgeSync(id, "worker", time.Minute); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.PublishKnowledgeSync(context.Background(), id, "worker", "head", 1, func(context.Context) error { close(entered); <-release; return nil })
		done <- err
	}()
	<-entered
	renewed := make(chan bool, 1)
	go func() { ok, _ := s.RenewKnowledgeSync(id, "worker", time.Minute); renewed <- ok }()
	select {
	case ok := <-renewed:
		if !ok {
			t.Error("renew rejected")
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("content publication blocked lease renewal")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReregisterPreservesActiveLease(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-register"
	src := domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending", HeadCommit: "old"}
	if err := s.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DeleteKnowledgeSource(id) })
	if ok, err := s.ClaimKnowledgeSync(id, "worker", time.Minute); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	src.HeadCommit = ""
	if err := s.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	got, _ := s.KnowledgeSource(id)
	if got.LeaseOwner != "worker" || got.HeadCommit != "old" {
		t.Fatalf("registration erased active state: %+v", got)
	}
}

func TestHostedWriteRejectsStaleRevision(t *testing.T) {
	s := concurrencyStore(t)
	source := "concurrency-revision"
	path := "test.md"
	t.Cleanup(func() { s.DeleteKnowledgeSourceContent(source) })
	doc := domain.KnowledgeDocument{SourceID: source, Path: path, Body: "first", Revision: 1, Commit: "1"}
	rev := domain.KnowledgeRevision{ID: "concurrency-rev-1", SourceID: source, Path: path, Body: doc.Body, Revision: 1}
	if err := s.WriteHostedDocument(doc, rev, nil); err != nil {
		t.Fatal(err)
	}
	doc.Body = "stale"
	rev.ID = "concurrency-rev-2"
	rev.Body = doc.Body
	if err := s.WriteHostedDocument(doc, rev, nil); err == nil {
		t.Fatal("stale revision overwrote document")
	}
	got, _ := s.KnowledgeDocument(source, path)
	if got.Body != "first" || len(s.KnowledgeRevisions(source, path)) != 1 {
		t.Fatal("stale write changed content or history")
	}
}

func TestPendingWritebackIsRecoverable(t *testing.T) {
	s := concurrencyStore(t)
	item := domain.KnowledgeWriteback{ID: "concurrency-pending", SourceID: "crashed-source", Branch: "kb/writeback-crashed", Status: domain.KnowledgeWritebackPending}
	if err := s.PutKnowledgeWriteback(item); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Where("id = ?", item.ID).Delete(&knowledgeWritebackRow{})
	// A crashed process's attempt is older than the recovery threshold.
	s.DB.Model(&knowledgeWritebackRow{}).Where("id = ?", item.ID).Update("updated_at", time.Now().Add(-time.Hour))
	items, err := s.KnowledgeWritebacksNeedingRefresh(time.Now().Add(-time.Minute), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range items {
		if got.ID == item.ID {
			return
		}
	}
	t.Fatal("crashed pending attempt is excluded from recovery")
}
func TestWritebackOldObservationCannotOverwriteMerged(t *testing.T) {
	s := concurrencyStore(t)
	item := domain.KnowledgeWriteback{ID: "concurrency-observation", Status: domain.KnowledgeWritebackMROpen}
	if err := s.PutKnowledgeWriteback(item); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Where("id = ?", item.ID).Delete(&knowledgeWritebackRow{})
	now := time.Now().UTC()
	if err := s.UpdateKnowledgeWritebackState(item.ID, domain.KnowledgeWritebackMerged, "", "", "merged", "", "", 0, now); err != nil {
		t.Fatal(err)
	}
	_ = s.UpdateKnowledgeWritebackState(item.ID, domain.KnowledgeWritebackMROpen, "", "", "opened", "", "", 0, now.Add(-time.Second))
	got, _ := s.KnowledgeWriteback(item.ID)
	if got.Status != domain.KnowledgeWritebackMerged {
		t.Fatal("stale observer reopened merged writeback")
	}
}

func TestRetractionFencesLaterHostedWrites(t *testing.T) {
	s := concurrencyStore(t)
	source := "concurrency-retract"
	path := "test.md"
	defer s.DeleteKnowledgeSourceContent(source)
	doc := domain.KnowledgeDocument{SourceID: source, Path: path, Body: "first", Revision: 1, Commit: "1"}
	rev := domain.KnowledgeRevision{ID: "concurrency-retract-1", SourceID: source, Path: path, Body: doc.Body, Revision: 1}
	if err := s.WriteHostedDocument(doc, rev, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKnowledgeDocument(source, path); err != nil {
		t.Fatal(err)
	}
	rev.ID = "concurrency-retract-2"
	if err := s.WriteHostedDocument(doc, rev, nil); err == nil {
		t.Fatal("late publisher revived retracted document")
	}
	if _, found := s.KnowledgeDocument(source, path); found {
		t.Fatal("retracted document is still visible")
	}
}

func TestConcurrentFingerprintClaimsOneSubmission(t *testing.T) {
	s := concurrencyStore(t)
	project := "concurrency-fingerprint"
	defer s.DB.Where("project_id = ?", project).Delete(&knowledgeSubmissionRow{})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"fingerprint-a", "fingerprint-b"} {
		go func(id string) {
			<-start
			results <- s.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: id, ProjectID: project, Fingerprint: "same-content", Status: domain.KnowledgeSubmissionPending, SedimentSignals: []string{domain.KnowledgeSignalRunSucceeded}})
		}(id)
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("fingerprint claims=%d", wins)
	}
	if _, ok := s.KnowledgeSubmissionByFingerprint(project, "same-content"); !ok {
		t.Fatal("winning submission cannot be reused")
	}
}
func TestWritebackReconcileHasSingleClaimant(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-reconcile"
	if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{ID: id, Status: domain.KnowledgeWritebackMROpen}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Where("id = ?", id).Delete(&knowledgeWritebackRow{})
	start := make(chan struct{})
	wins := make(chan bool, 2)
	for _, owner := range []string{"one", "two"} {
		go func(owner string) {
			<-start
			ok, err := s.ClaimKnowledgeWriteback(id, owner, time.Minute)
			if err != nil {
				t.Error(err)
			}
			wins <- ok
		}(owner)
	}
	close(start)
	n := 0
	for i := 0; i < 2; i++ {
		if <-wins {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reconcile claimants=%d", n)
	}
}
func TestDeletingSourceFencesClaimsAndRegistration(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-deleting"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSource(id)
	if ok, err := s.BeginKnowledgeSourceDelete(id, "deleter"); err != nil || !ok {
		t.Fatalf("delete marker: %v %v", ok, err)
	}
	if ok, err := s.ClaimKnowledgeSync(id, "syncer", time.Minute); err != nil || ok {
		t.Fatalf("sync claimed deleting source: %v %v", ok, err)
	}
	if ok, err := s.ClaimKnowledgeWrite(id, "writer", time.Minute); err != nil || ok {
		t.Fatalf("writer claimed deleting source: %v %v", ok, err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err == nil {
		t.Error("registration accepted deleting source")
	}

	got, _ := s.KnowledgeSource(id)
	if got.Status != "deleting" || got.LeaseOwner != "deleter" {
		t.Fatalf("registration revived deleting source: %+v", got)
	}
	if err := s.DeleteKnowledgeSourceMarked(id, "other"); err == nil {
		t.Fatal("non-owner deleted source")
	}
	if err := s.DeleteKnowledgeSourceMarked(id, "deleter"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredPublisherCannotOverwriteNewSnapshot(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-stale-snapshot"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSource(id)
	defer s.DeleteKnowledgeSourceContent(id)
	if ok, err := s.ClaimKnowledgeSync(id, "old", time.Minute); err != nil || !ok {
		t.Fatalf("old claim: %v %v", ok, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.PublishKnowledgeSync(context.Background(), id, "old", "old", 1, func(ctx context.Context) error {
			close(entered)
			<-release
			return s.ReplaceGitKnowledge(ctx, id, []domain.KnowledgeDocument{{SourceID: id, Path: "a.md", Body: "stale"}}, nil)
		})
		done <- err
	}()
	<-entered
	if err := s.DB.Exec("UPDATE knowledge_sources SET lease_until = TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6)) WHERE id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimKnowledgeSync(id, "new", time.Minute); err != nil || !ok {
		t.Fatalf("new claim: %v %v", ok, err)
	}
	if ok, err := s.PublishKnowledgeSync(context.Background(), id, "new", "new", 1, func(ctx context.Context) error {
		return s.ReplaceGitKnowledge(ctx, id, []domain.KnowledgeDocument{{SourceID: id, Path: "a.md", Body: "current"}}, nil)
	}); err != nil || !ok {
		t.Fatalf("new publish: %v %v", ok, err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Error("expired writer was not fenced")
	}
	doc, ok := s.KnowledgeDocument(id, "a.md")
	if !ok || doc.Body != "current" {
		t.Fatalf("stale content visible: %+v", doc)
	}
}

func TestReconcileCompletesCrashedReviewPublication(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-crashed-review"
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: id, Status: domain.KnowledgeSubmissionPublishing}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSubmission(id)
	if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{ID: id, SubmissionID: id, Status: domain.KnowledgeWritebackBranchPushed, Branch: "kb/recovered", Commit: "pushed"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeWritebackBySubmission(id)
	if err := s.RecoverKnowledgeWritebackSubmission(id); err != nil {
		t.Fatal(err)
	}
	got, _ := s.KnowledgeSubmission(id)
	if got.Status != domain.KnowledgeSubmissionPublished || got.Commit != "pushed" {
		t.Fatalf("durable push did not finish review: %+v", got)
	}
}

func TestExpiredPublicationWithoutWritebackIsRecovered(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-expired-publication"
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: id, Status: domain.KnowledgeSubmissionPublishing}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSubmission(id)
	recovery, ok := any(s).(interface{ RecoverExpiredKnowledgePublications() error })
	if !ok {
		t.Fatal("publishing has no crash recovery")
	}
	if err := s.DB.Exec("UPDATE knowledge_submissions SET publish_until=TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6)) WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	if err := recovery.RecoverExpiredKnowledgePublications(); err != nil {
		t.Fatal(err)
	}
	item, _ := s.KnowledgeSubmission(id)
	if item.Status != domain.KnowledgeSubmissionPending || item.Version < 2 {
		t.Fatalf("expired publication remains stuck: %+v", item)
	}
}
func TestReconcileClaimBlocksNewWriteAttempt(t *testing.T) {
	s := concurrencyStore(t)
	source := "concurrency-reconcile-source"
	id := "concurrency-reconcile-write"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: source, Kind: "git", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSource(source)
	if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{ID: id, SourceID: source, Status: domain.KnowledgeWritebackPending}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeWritebacksBySource(source)
	if ok, err := s.ClaimKnowledgeWriteback(id, "reconcile", time.Minute); err != nil || !ok {
		t.Fatalf("reconcile claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimKnowledgeWrite(source, "new-publish", time.Minute); err != nil || ok {
		t.Fatalf("new publisher overlapped reconcile claim: %v %v", ok, err)
	}
}
func TestTakeoverFencesContentBeforeReadingBaseline(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-baseline-fence"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSource(id)
	defer s.DeleteKnowledgeSourceContent(id)
	barrier, ok := any(s).(interface {
		FenceKnowledgeSource(context.Context, string, int64) error
	})
	if !ok {
		t.Fatal("takeover has no content baseline barrier")
	}
	if ok, err := s.ClaimKnowledgeSync(id, "old", time.Minute); err != nil || !ok {
		t.Fatalf("old claim %v %v", ok, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.PublishKnowledgeSync(context.Background(), id, "old", "old", 1, func(ctx context.Context) error {
			close(entered)
			<-release
			return s.ReplaceGitKnowledge(ctx, id, []domain.KnowledgeDocument{{SourceID: id, Path: "removed.md", Body: "stale"}}, nil)
		})
		done <- err
	}()
	<-entered
	s.DB.Exec("UPDATE knowledge_sources SET lease_until=TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6)) WHERE id=?", id)
	if ok, err := s.ClaimKnowledgeSync(id, "new", time.Minute); err != nil || !ok {
		t.Fatalf("new claim %v %v", ok, err)
	}
	src, _ := s.KnowledgeSource(id)
	if err := barrier.FenceKnowledgeSource(context.Background(), id, src.LeaseVersion); err != nil {
		t.Fatal(err)
	}
	if docs := s.KnowledgeDocuments(id); len(docs) != 0 {
		t.Fatal("unexpected baseline")
	}
	close(release)
	if err := <-done; err == nil {
		t.Error("old writer crossed takeover barrier")
	}
	if docs := s.KnowledgeDocuments(id); len(docs) != 0 {
		t.Fatal("late writer contaminated incremental baseline")
	}
}

func TestOwnedWritebackPersistsWithMillisecondLeaseColumn(t *testing.T) {
	s := concurrencyStore(t)
	source := "concurrency-owned-write"
	id := "concurrency-owned-record"
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: source, Kind: "git", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSource(source)
	defer s.DeleteKnowledgeWritebacksBySource(source)
	if ok, err := s.ClaimKnowledgeWrite(source, "writer", time.Minute); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	item := domain.KnowledgeWriteback{ID: id, SourceID: source, Branch: "kb/persisted", Status: domain.KnowledgeWritebackPending}
	if err := s.PutKnowledgeWritebackOwned(item, "writer"); err != nil {
		t.Fatalf("live lease could not persist attempt: %v", err)
	}
	s.FinishKnowledgeWrite(source, "writer")
	item.Branch = "stale"
	if err := s.PutKnowledgeWritebackOwned(item, "writer"); err == nil {
		t.Fatal("expired writer overwrote attempt")
	}
	got, _ := s.KnowledgeWriteback(id)
	if got.Branch != "kb/persisted" {
		t.Fatalf("stale branch persisted: %+v", got)
	}
}

func TestStaleReviewCASDoesNotOverwriteStagedBody(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-staged-cas"
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: id, Content: "original", Status: domain.KnowledgeSubmissionPending}); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKnowledgeSubmission(id)
	stale, _ := s.KnowledgeSubmission(id)
	winner := stale
	winner.Content = "winning body"
	if err := s.PutKnowledgeSubmission(winner); err != nil {
		t.Fatal(err)
	}
	stale.Content = "stale body"
	if err := s.PutKnowledgeSubmission(stale); err == nil {
		t.Error("stale control CAS accepted")
	}
	got, _ := s.KnowledgeSubmission(id)
	if got.Content != "winning body" {
		t.Fatalf("failed CAS changed staged body: %q", got.Content)
	}
}

func TestExpiredReviewWithDurablePushRecoversBeforeReconcile(t *testing.T) {
	s := concurrencyStore(t)
	for _, status := range []string{domain.KnowledgeWritebackBranchPushed, domain.KnowledgeWritebackMROpen, domain.KnowledgeWritebackMerged, domain.KnowledgeWritebackClosed} {
		t.Run(status, func(t *testing.T) {
			id := "expired-durable-" + status
			if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: id, Status: domain.KnowledgeSubmissionPublishing, Content: "durable reviewed content"}); err != nil {
				t.Fatal(err)
			}
			defer s.DeleteKnowledgeSubmission(id)
			defer s.DeleteKnowledgeWritebackBySubmission(id)
			if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{ID: id, SubmissionID: id, Status: status, Commit: "durable-push", Branch: "kb/recover"}); err != nil {
				t.Fatal(err)
			}
			if err := s.DB.Exec("UPDATE knowledge_submissions SET publish_until=TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6)) WHERE id=?", id).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.RecoverExpiredKnowledgePublications(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecoverKnowledgeWritebackSubmission(id); err != nil {
				t.Fatal(err)
			}
			got, _ := s.KnowledgeSubmission(id)
			if got.Status != domain.KnowledgeSubmissionPublished || got.Commit != "durable-push" || got.Content != "durable reviewed content" {
				t.Fatalf("expiry/reconcile lost durable publication: %+v", got)
			}
		})
	}
}

func TestReconcileUsesUTCWithNonUTCDatabaseSession(t *testing.T) {
	s := concurrencyStore(t)
	id := "concurrency-reconcile-timezone"
	if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{ID: id, Status: domain.KnowledgeWritebackMROpen}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Where("id = ?", id).Delete(&knowledgeWritebackRow{})
	if err := s.DB.Connection(func(db *gorm.DB) error {
		var previous string
		if err := db.Raw("SELECT @@session.time_zone").Scan(&previous).Error; err != nil {
			return err
		}
		if err := db.Exec("SET time_zone = '+08:00'").Error; err != nil {
			return err
		}
		defer db.Exec("SET time_zone = ?", previous)
		scoped := &KnowledgeRepo{deps: &deps{DB: db.Session(&gorm.Session{NewDB: true})}}
		if ok, err := scoped.ClaimKnowledgeWriteback(id, "owner", time.Minute); err != nil || !ok {
			t.Fatalf("claim %v %v", ok, err)
		}
		return scoped.UpdateKnowledgeWritebackState(id, domain.KnowledgeWritebackMerged, "", "", "merged", "", "", 0, time.Now().UTC(), "owner")
	}); err != nil {
		t.Fatalf("live UTC lease rejected in non-UTC session: %v", err)
	}
}
