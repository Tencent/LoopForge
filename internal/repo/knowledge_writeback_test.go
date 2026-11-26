package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestKnowledgeWritebackRoundTripAndSubmissionOverlay(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "kb-git-1", Kind: "git", GitURL: "https://github.com/example/repo.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	base := domain.KnowledgeSubmission{ID: "kbs-1", ProjectID: "p", Source: "kb-git-1", Path: "docs/a.md", Status: domain.KnowledgeSubmissionPublished}
	if err := s.PutKnowledgeSubmission(base); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	src := domain.KnowledgeWriteback{
		ID: "kwb-1", SubmissionID: "kbs-1", SourceID: "kb-git-1", ProjectID: "p",
		Path: "docs/a.md", Branch: "kb/writeback-kbs-1", Commit: "abc",
		Status: domain.KnowledgeWritebackMROpen, MRURL: "https://github.com/example/repo/pull/7",
		MRProvider: "github", MRIID: 7, Note: "合并后自动同步", Attempts: 1, CheckedAt: &now,
	}
	if err := s.PutKnowledgeWriteback(src); err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeWriteback("kwb-1")
	if !ok || got.Status != domain.KnowledgeWritebackMROpen || got.MRIID != 7 || got.Branch != "kb/writeback-kbs-1" || got.CheckedAt == nil {
		t.Fatalf("writeback = %+v ok=%v", got, ok)
	}
	bySubmission, ok := s.KnowledgeWritebackBySubmission("kbs-1")
	if !ok || bySubmission.ID != "kwb-1" {
		t.Fatalf("by submission = %+v ok=%v", bySubmission, ok)
	}
	// The review queue view is overlaid from the writeback row.
	item, ok := s.KnowledgeSubmission("kbs-1")
	if !ok || item.Branch != src.Branch || item.MRURL != src.MRURL || item.MRStatus != domain.KnowledgeWritebackMROpen || item.WriteNote != src.Note {
		t.Fatalf("submission overlay = %+v", item)
	}
	// Upsert keeps one row and updates state only.
	if err := s.UpdateKnowledgeWritebackState("kwb-1", domain.KnowledgeWritebackMerged, "", "", "merged", "已合并", "", 0, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.KnowledgeWriteback("kwb-1")
	if updated.Status != domain.KnowledgeWritebackMerged || updated.MRState != "merged" || updated.Branch != src.Branch || updated.MRURL != src.MRURL {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestKnowledgeWritebackLease(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "kb-git-lease", Kind: "git", GitURL: "https://github.com/example/repo.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimKnowledgeWrite("kb-git-lease", "one", time.Minute); err != nil || !ok {
		t.Fatalf("first claim = %v %v", ok, err)
	}
	if ok, err := s.ClaimKnowledgeWrite("kb-git-lease", "two", time.Minute); err != nil || ok {
		t.Fatalf("second claim = %v %v", ok, err)
	}
	if ok, err := s.RenewKnowledgeWrite("kb-git-lease", "one", time.Minute); err != nil || !ok {
		t.Fatalf("renew = %v %v", ok, err)
	}
	if ok, err := s.FinishKnowledgeWrite("kb-git-lease", "two"); err != nil || ok {
		t.Fatalf("other owner finish = %v %v", ok, err)
	}
	if ok, err := s.FinishKnowledgeWrite("kb-git-lease", "one"); err != nil || !ok {
		t.Fatalf("finish = %v %v", ok, err)
	}
	if ok, err := s.ClaimKnowledgeWrite("kb-git-lease", "three", time.Minute); err != nil || !ok {
		t.Fatalf("claim after finish = %v %v", ok, err)
	}
	// An expired lease is reclaimable even if the owner process died.
	past := time.Now().UTC().Add(-time.Minute)
	if err := s.DB.Model(&knowledgeSourceRow{}).Where("id = ?", "kb-git-lease").Update("lease_until", past).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimKnowledgeWrite("kb-git-lease", "four", time.Minute); err != nil || !ok {
		t.Fatalf("claim expired = %v %v", ok, err)
	}
	// Hosted sources never take a writeback lease.
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "kb-hosted-x", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimKnowledgeWrite("kb-hosted-x", "one", time.Minute); err != nil || ok {
		t.Fatalf("hosted claim = %v %v", ok, err)
	}
}

func TestQueueKnowledgeResyncPreservesActiveLease(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "kb-git-resync", Kind: "git", GitURL: "https://github.com/example/repo.git", Branch: "main", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.QueueKnowledgeResync("kb-git-resync"); err != nil || !ok {
		t.Fatalf("queue = %v %v", ok, err)
	}
	src, _ := s.KnowledgeSource("kb-git-resync")
	if src.Status != "pending" {
		t.Fatalf("status = %s", src.Status)
	}
	src.Status = "ready"
	s.PutKnowledgeSource(src)
	if ok, err := s.ClaimKnowledgeWrite("kb-git-resync", "owner", time.Minute); err != nil || !ok {
		t.Fatalf("claim = %v %v", ok, err)
	}
	if ok, err := s.QueueKnowledgeResync("kb-git-resync"); err != nil || !ok {
		t.Fatalf("queue with lease = %v %v", ok, err)
	}
	src, _ = s.KnowledgeSource("kb-git-resync")
	if src.Status != "pending" || src.LeaseOwner != "owner" || src.LeaseUntil == nil {
		t.Fatalf("queue erased lease: %+v", src)
	}
}

func TestDeleteKnowledgeWritebacksBySubmissionAndSource(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, wb := range []domain.KnowledgeWriteback{
		{ID: "kwb-a", SubmissionID: "kbs-a", SourceID: "src-a", Status: domain.KnowledgeWritebackFailed},
		{ID: "kwb-b", SubmissionID: "kbs-b", SourceID: "src-a", Status: domain.KnowledgeWritebackBranchPushed},
	} {
		if err := s.PutKnowledgeWriteback(wb); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteKnowledgeWritebackBySubmission("kbs-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.KnowledgeWriteback("kwb-a"); ok {
		t.Fatal("submission writeback was not deleted")
	}
	if _, ok := s.KnowledgeWriteback("kwb-b"); !ok {
		t.Fatal("unrelated writeback was deleted")
	}
	if err := s.DeleteKnowledgeWritebacksBySource("src-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.KnowledgeWriteback("kwb-b"); ok {
		t.Fatal("source writeback was not deleted")
	}
}

// submission_id 允许为空，且空值使用 NULL 而不是空串：MySQL 的唯一索引对
// NULL 不判重，历史数据或手工记录才不会互相撞唯一键。
func TestKnowledgeWritebackEmptySubmissionIDDoesNotCollide(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"kwb-empty-a", "kwb-empty-b"} {
		if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{ID: id, SourceID: "kb-git-" + id}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	for _, id := range []string{"kwb-empty-a", "kwb-empty-b"} {
		got, ok := s.KnowledgeWriteback(id)
		if !ok || got.SubmissionID != "" {
			t.Fatalf("writeback %s = %+v ok=%v", id, got, ok)
		}
		var raw *string
		if err := s.DB.Raw("SELECT submission_id FROM knowledge_writebacks WHERE id = ?", id).Row().Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw != nil {
			t.Fatalf("空 submission_id 应落 NULL，实际 %q", *raw)
		}
	}
}
