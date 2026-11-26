package knowledge

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

type gitAwareStore struct {
	*fakeStore
	source domain.KnowledgeSource
}

func (s *gitAwareStore) KnowledgeSource(id string) (domain.KnowledgeSource, bool) {
	if id == s.source.ID {
		return s.source, true
	}
	return domain.KnowledgeSource{}, false
}

type fakeGitUpstream struct {
	*fakeUpstream
	request WritebackRequest
	result  WriteResult
	err     error
	calls   int
}

func (u *fakeGitUpstream) WriteGitDocument(_ context.Context, req WritebackRequest) (WriteResult, error) {
	u.calls++
	u.request = req
	if u.err != nil {
		return WriteResult{}, u.err
	}
	return u.result, nil
}

func TestPublishGitSourceUsesWritebackPath(t *testing.T) {
	store := &gitAwareStore{
		fakeStore: newFakeStore(),
		source:    domain.KnowledgeSource{ID: "docs", BaseID: "base", Kind: "git", GitURL: "https://github.com/example/repo.git"},
	}
	store.submissions["s"] = domain.KnowledgeSubmission{
		ID: "s", ProjectID: "p1", Source: "docs", Path: "guides/a.md",
		Title: "迁移经验", Content: "正文", Status: domain.KnowledgeSubmissionPending,
	}
	up := &fakeGitUpstream{
		fakeUpstream: &fakeUpstream{},
		result: WriteResult{
			Source: "docs", Path: "guides/a.md", Commit: "0123456789abcdef0123456789abcdef01234567",
			Branch: "kb/writeback-kbs-1", MRURL: "https://github.com/example/repo/pull/3",
			MRProvider: "github", MRStatus: domain.KnowledgeWritebackMROpen, Message: "已创建合并请求",
		},
	}
	svc := NewService(store, func() string { return "id1" }, up)
	item, err := svc.Publish(context.Background(), "s", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if up.calls != 1 || up.fakeUpstream.written.source != "" {
		t.Fatalf("hosted WriteDocument must not run for a Git source: %+v", up.fakeUpstream.written)
	}
	if up.request.SubmissionID != "s" || up.request.Source != "docs" || up.request.Path != "guides/a.md" || up.request.ProjectID != "p1" {
		t.Fatalf("request = %+v", up.request)
	}
	if !strings.Contains(up.request.Content, "迁移经验") {
		t.Fatalf("title heading missing from content: %q", up.request.Content)
	}
	if item.Status != domain.KnowledgeSubmissionPublished || item.Branch != "kb/writeback-kbs-1" || item.MRURL != up.result.MRURL || item.MRStatus != domain.KnowledgeWritebackMROpen || item.WriteNote != up.result.Message || item.Commit != up.result.Commit || item.ReviewedBy != "reviewer" || item.PublishedAt.IsZero() {
		t.Fatalf("item = %+v", item)
	}
}

func TestPublishGitSourceRecordsWritebackFailure(t *testing.T) {
	store := &gitAwareStore{
		fakeStore: newFakeStore(),
		source:    domain.KnowledgeSource{ID: "docs", BaseID: "base", Kind: "git", GitURL: "https://github.com/example/repo.git"},
	}
	store.submissions["s"] = domain.KnowledgeSubmission{
		ID: "s", ProjectID: "p1", Source: "docs", Path: "guides/a.md",
		Title: "迁移经验", Content: "正文", Status: domain.KnowledgeSubmissionPending,
	}
	up := &fakeGitUpstream{fakeUpstream: &fakeUpstream{}, err: errString("来源凭据未授予写入权限")}
	svc := NewService(store, func() string { return "id1" }, up)
	item, err := svc.Publish(context.Background(), "s", "reviewer")
	if err == nil {
		t.Fatal("expected failure")
	}
	if item.Status == domain.KnowledgeSubmissionPublished || item.Error == "" || item.MRStatus != domain.KnowledgeWritebackFailed {
		t.Fatalf("item = %+v", item)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
