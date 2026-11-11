package knowledge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
	"agentflow/internal/repo"
)

type governanceUpstream struct {
	hits      []SearchHit
	retracted []string
	fetch     Document
}

func (u *governanceUpstream) Tools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (u *governanceUpstream) ListSources(context.Context) ([]Source, error) {
	return []Source{{ID: "s1", Name: "s1"}}, nil
}
func (u *governanceUpstream) Search(_ context.Context, query string, _ []string) (SearchResult, error) {
	return SearchResult{Query: query, Mode: "lexical", Results: append([]SearchHit(nil), u.hits...)}, nil
}
func (u *governanceUpstream) Fetch(context.Context, string, string, int, int) (Document, error) {
	return u.fetch, nil
}
func (u *governanceUpstream) WriteDocument(context.Context, string, string, string) (WriteResult, error) {
	return WriteResult{}, nil
}
func (u *governanceUpstream) RetractDocument(_ context.Context, source, path string) error {
	u.retracted = append(u.retracted, source+"\x00"+path)
	return nil
}

func governanceServiceStore(t *testing.T) *repo.Store {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutKnowledgeBase(domain.KnowledgeBase{ID: "b1", ProjectID: "p1", Name: "项目知识库", Layer: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p1", BaseID: "b1", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "s1", BaseID: "b1", Name: "托管", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func putGovernanceDoc(t *testing.T, s *repo.Store, path, title, body string) {
	t.Helper()
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{
		SourceID: "s1", Path: path, Title: title, Body: body, LineCount: strings.Count(body, "\n") + 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchAppliesDocumentLifecycleAfterRetrieval(t *testing.T) {
	s := governanceServiceStore(t)
	putGovernanceDoc(t, s, "normal.md", "正常", "---\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# 正常\n")
	putGovernanceDoc(t, s, "deprecated.md", "废弃", "---\nowner: alice\nstatus: deprecated\nupdated: 2026-11-01\n---\n# 废弃\n")
	putGovernanceDoc(t, s, "archived.md", "归档", "---\nowner: alice\nstatus: archived\nupdated: 2026-11-01\n---\n# 归档\n")
	up := &governanceUpstream{hits: []SearchHit{
		{Source: "s1", Path: "archived.md", Score: 100, Title: "归档"},
		{Source: "s1", Path: "deprecated.md", Score: 10, Title: "废弃"},
		{Source: "s1", Path: "normal.md", Score: 5, Title: "正常"},
	}}
	svc := NewService(s, func() string { return "id" }, up)
	svc.now = func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }

	out, err := svc.Search(context.Background(), "p1", "查询", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Results) != 2 {
		t.Fatalf("results = %+v", out.Result.Results)
	}
	if out.Result.Results[0].Path != "normal.md" || out.Result.Results[1].Path != "deprecated.md" {
		t.Fatalf("deprecated document was not downweighted: %+v", out.Result.Results)
	}
	if out.Result.Results[1].Score != 2.5 {
		t.Fatalf("deprecated score = %v, want 2.5", out.Result.Results[1].Score)
	}
	if out.Result.Results[1].DocStatus != domain.KnowledgeDocDeprecated || out.Result.Results[0].DocStatus != domain.KnowledgeDocVerified {
		t.Fatalf("hit statuses = %+v", out.Result.Results)
	}

	up.hits = nil
	if _, err := svc.Search(context.Background(), "p1", "从未命中", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Search(context.Background(), "p1", "从未命中", nil); err != nil {
		t.Fatal(err)
	}
	gaps := s.KnowledgeGaps("p1", 10)
	if len(gaps) != 1 || gaps[0].Query != "从未命中" || gaps[0].Count != 2 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestGovernanceScanPersistsAndSummarizes(t *testing.T) {
	s := governanceServiceStore(t)
	putGovernanceDoc(t, s, "stale.md", "过期", "---\nowner: alice\nstatus: verified\nupdated: 2020-01-01\n---\n# 过期\n")
	putGovernanceDoc(t, s, "owner.md", "缺负责人", "---\nstatus: draft\nupdated: 2026-11-01\n---\n# 缺负责人\n")
	putGovernanceDoc(t, s, "link.md", "死链", "---\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# 死链\n\n[不存在](missing.md)\n")
	putGovernanceDoc(t, s, "dup-a.md", "重复标题", "---\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# 重复标题\n")
	putGovernanceDoc(t, s, "dup-b.md", "重复标题", "---\nowner: bob\nstatus: draft\nupdated: 2026-11-01\n---\n# 重复标题\n")

	svc := NewService(s, func() string { return "id" }, &governanceUpstream{})
	svc.now = func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }
	overview, err := svc.ScanGovernance(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"stale", "missing_owner", "dead_link", "duplicate"} {
		if overview.Counts[kind] == 0 {
			t.Errorf("counts[%s] = 0, all = %+v, issues = %+v", kind, overview.Counts, overview.Issues)
		}
	}
	if overview.LastScanAt == nil || len(overview.Bases) != 1 || overview.Bases[0].DocCount != 5 {
		t.Fatalf("overview = %+v", overview)
	}
	got, err := svc.GovernanceOverview("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["dead_link"] != 1 || got.Counts["stale"] != 1 || got.Counts["missing_owner"] != 1 || got.Counts["duplicate"] != 2 {
		t.Fatalf("persisted counts = %+v, issues = %+v", got.Counts, got.Issues)
	}
}

func TestGovernanceFeedbackAndAgentIssue(t *testing.T) {
	s := governanceServiceStore(t)
	putGovernanceDoc(t, s, "a.md", "A", "---\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# A\n")
	seq := 0
	svc := NewService(s, func() string { seq++; return "id" + string(rune('0'+seq)) }, &governanceUpstream{})
	svc.now = func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }

	fb, err := svc.RecordFeedback(FeedbackInput{
		ProjectID: "p1", Source: "s1", Path: "a.md", Kind: domain.KnowledgeFeedbackHelpful, Surface: "knowledge_center",
	}, "alice", 7)
	if err != nil {
		t.Fatal(err)
	}
	if fb.ID == "" || fb.Title != "A" || fb.Kind != domain.KnowledgeFeedbackHelpful || fb.UserID != 7 {
		t.Fatalf("feedback = %+v", fb)
	}
	issue, err := svc.ReportIssue(context.Background(), Caller{ProjectID: "p1", AgentID: "agent-1", RunID: "run-1"}, "s1", "a.md", "命令已经过时")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Kind != domain.KnowledgeFeedbackIssue || issue.Note != "命令已经过时" || issue.AgentID != "agent-1" || issue.RunID != "run-1" {
		t.Fatalf("issue feedback = %+v", issue)
	}
	if _, err := svc.RecordFeedback(FeedbackInput{ProjectID: "p2", Source: "s1", Path: "a.md", Kind: domain.KnowledgeFeedbackHelpful}, "bob", 8); err == nil {
		t.Fatal("out-of-scope feedback must fail")
	}
	overview, err := svc.GovernanceOverview("p1")
	if err != nil {
		t.Fatal(err)
	}
	if overview.FeedbackCounts[domain.KnowledgeFeedbackHelpful] != 1 || overview.FeedbackCounts[domain.KnowledgeFeedbackIssue] != 1 || len(overview.Feedback) != 2 {
		t.Fatalf("feedback overview = %+v", overview)
	}
}

func TestGovernanceDispositionQueuesUpdateAndRetraction(t *testing.T) {
	s := governanceServiceStore(t)
	body := "---\nowner: alice\nstatus: verified\nupdated: 2026-11-01\n---\n# A\n\n正文\n"
	putGovernanceDoc(t, s, "a.md", "A", body)
	up := &governanceUpstream{}
	seq := 0
	svc := NewService(s, func() string { seq++; return fmt.Sprintf("id%d", seq) }, up)
	svc.now = func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }

	update, err := svc.DisposeGovernanceDocument(GovernanceDispositionInput{
		ProjectID: "p1", Source: "s1", Path: "a.md", Action: "update", Note: "内容需要更新",
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if update.Status != domain.KnowledgeSubmissionPending || update.Action != domain.KnowledgeSubmissionActionUpdate || update.Content != body {
		t.Fatalf("update submission = %+v", update)
	}
	again, err := svc.DisposeGovernanceDocument(GovernanceDispositionInput{
		ProjectID: "p1", Source: "s1", Path: "a.md", Action: "update",
	}, "alice")
	if err != nil || again.ID != update.ID {
		t.Fatalf("duplicate disposition should reuse the pending item: %+v err=%v", again, err)
	}

	offline, err := svc.DisposeGovernanceDocument(GovernanceDispositionInput{
		ProjectID: "p1", Source: "s1", Path: "a.md", Action: "retract", Note: "已经过时",
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if offline.Action != domain.KnowledgeSubmissionActionRetract || offline.Status != domain.KnowledgeSubmissionPending {
		t.Fatalf("retract submission = %+v", offline)
	}
	published, err := svc.Publish(context.Background(), offline.ID, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != domain.KnowledgeSubmissionRetracted || published.RetractedAt == nil {
		t.Fatalf("published retraction = %+v", published)
	}
	if len(up.retracted) != 1 || up.retracted[0] != "s1\x00a.md" {
		t.Fatalf("retracted = %+v", up.retracted)
	}
	if _, ok := s.KnowledgeTombstone("p1", KnowledgeFingerprint(body)); !ok {
		t.Fatal("retraction must write a tombstone")
	}
}

// The execution boundary must reject forged/legacy queue entries without HTTP.
func TestServiceRejectsCrossProjectRetraction(t *testing.T) {
	store := governanceServiceStore(t)
	up := &governanceUpstream{}
	svc := NewService(store, func() string { return "authz" }, up)
	putGovernanceDoc(t, store, "a.md", "A", "# A private document")
	// User B's project has an invalid legacy read binding to user A's base.
	if err := store.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "b", BaseID: "b1", Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{domain.KnowledgeSubmissionPending, domain.KnowledgeSubmissionPublished} {
		item := domain.KnowledgeSubmission{ID: "forged-" + string(status), ProjectID: "b", Source: "s1", Path: "a.md", Action: "retract", Status: status}
		if err := store.PutKnowledgeSubmission(item); err != nil {
			t.Fatal(err)
		}
		var err error
		if status == domain.KnowledgeSubmissionPending {
			_, err = svc.Publish(context.Background(), item.ID, "user-b")
		} else {
			_, err = svc.Retract(context.Background(), item.ID, "内容错误", "", "user-b")
		}
		if ae, _ := apperr.As(err); ae == nil || ae.Kind != apperr.Forbidden {
			t.Errorf("cross-project %s: %v", status, err)
		}
	}
	if len(up.retracted) != 0 {
		t.Fatal("cross-project execution reached content engine")
	}
	if _, ok := store.KnowledgeDocument("s1", "a.md"); !ok {
		t.Fatal("A's document removed")
	}
}
