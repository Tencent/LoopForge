package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
)

type gitWritebackUpstream struct {
	staticKnowledgeUpstream
	request knowledge.WritebackRequest
}

func (u *gitWritebackUpstream) WriteGitDocument(_ context.Context, req knowledge.WritebackRequest) (knowledge.WriteResult, error) {
	u.request = req
	return knowledge.WriteResult{
		Source: req.Source, Path: req.Path, Commit: "0123456789abcdef0123456789abcdef01234567",
		Branch: "kb/writeback-kbs-http", MRURL: "https://github.com/example/repo/pull/11",
		MRProvider: "github", MRStatus: domain.KnowledgeWritebackMROpen,
		Message: "已创建合并请求，合并后会自动同步索引。",
	}, nil
}

func TestKnowledgePublishGitSourceReturnsBranchAndMR(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// Use the same idempotent provisioning as a real project: the Git source
	// belongs to a layer=project base owned by p-git. The review publish rule
	// validates base ownership, not just a binding.
	svc := knowledge.NewService(s, func() string { return "fixture" }, nil)
	base, err := svc.EnsureHostedBase("p-git")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{
		ID: "src-git", BaseID: base.ID, Kind: "git", Name: "Git 来源",
		GitURL: "https://github.com/example/repo.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{
		ID: "kbs-http", ProjectID: "p-git", Source: "src-git", Path: "docs/writeback.md",
		Title: "写回知识", Content: "正文", Status: domain.KnowledgeSubmissionPending,
	}); err != nil {
		t.Fatal(err)
	}
	up := &gitWritebackUpstream{}
	h := newKnowledgeTestHandlerWithUpstream(t, s, up)
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/submissions/kbs-http/publish", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	var item domain.KnowledgeSubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.KnowledgeSubmissionPublished || item.Branch != "kb/writeback-kbs-http" || item.MRURL != "https://github.com/example/repo/pull/11" || item.MRStatus != domain.KnowledgeWritebackMROpen || item.WriteNote == "" {
		t.Fatalf("item = %+v", item)
	}
	if up.request.SubmissionID != "kbs-http" || up.request.Source != "src-git" || up.request.Path != "docs/writeback.md" {
		t.Fatalf("request = %+v", up.request)
	}
	// The review list and drawer carry the same branch/MR fields.
	list := doJSON(t, h, http.MethodGet, "/api/knowledge/submissions?projectId=p-git", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var items []domain.KnowledgeSubmission
	if err := json.Unmarshal(list.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Branch != item.Branch || items[0].MRURL != item.MRURL || items[0].MRStatus != item.MRStatus {
		t.Fatalf("list = %+v", items)
	}
}

func TestKnowledgePublishRefusesReadOnlySharedSource(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	svc := knowledge.NewService(s, func() string { return "fixture" }, nil)
	if _, err := svc.EnsureHostedBase("p-shared"); err != nil {
		t.Fatal(err)
	}
	shared, err := svc.CreateSharedBase("公共规范", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{
		ID: "src-shared-git", BaseID: shared.ID, Kind: "git", Name: "公共 Git 来源",
		GitURL: "https://github.com/example/shared.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p-shared", BaseID: shared.ID, Permission: "read"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{
		ID: "kbs-shared", ProjectID: "p-shared", Source: "src-shared-git", Path: "docs/shared.md",
		Title: "公共知识", Content: "正文", Status: domain.KnowledgeSubmissionPending,
	}); err != nil {
		t.Fatal(err)
	}
	h := newKnowledgeTestHandlerWithUpstream(t, s, &gitWritebackUpstream{})
	rec := doJSON(t, h, http.MethodPost, "/api/knowledge/submissions/kbs-shared/publish", "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("publish shared source: %d %s", rec.Code, rec.Body.String())
	}
	item, _ := s.KnowledgeSubmission("kbs-shared")
	if item.Status == domain.KnowledgeSubmissionPublished {
		t.Fatalf("shared source was published: %+v", item)
	}
}
