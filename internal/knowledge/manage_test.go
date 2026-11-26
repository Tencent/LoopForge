package knowledge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func newManagedTestService(t *testing.T) (*Service, *repo.Store) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// 直接构造 Service：管理面方法只用 store 字段，不依赖 NewService 的构造
	// 签名（外部切片会给 NewService 增加 Upstream 参数）。
	return &Service{store: s}, s
}

func TestEnsureHostedBaseIsIdempotentAndConcurrent(t *testing.T) {
	svc, s := newManagedTestService(t)

	const projectID = "proj-concurrent"
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.EnsureHostedBase(projectID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure failed: %v", err)
		}
	}

	bases := s.KnowledgeBases(projectID)
	if len(bases) != 1 {
		t.Fatalf("并发创建应只得到一个托管库，实际 %d: %+v", len(bases), bases)
	}
	if bases[0].ID != "kb-"+projectID || bases[0].Layer != "project" || bases[0].ProjectID != projectID {
		t.Fatalf("托管库字段错误: %+v", bases[0])
	}
	sources := s.KnowledgeSources(bases[0].ID)
	if len(sources) != 1 || sources[0].Kind != "hosted" || sources[0].Status != SourceStatusReady {
		t.Fatalf("应只有一个就绪的托管来源: %+v", sources)
	}
	if bindings := s.KnowledgeBindings(projectID); len(bindings) != 1 || bindings[0].Permission != "submit" {
		t.Fatalf("应只有一条 submit 绑定: %+v", bindings)
	}

	// 重复打开知识中心不得把托管来源的修订号/篇数清零。
	src := sources[0]
	src.HeadRevision, src.DocCount = 7, 3
	if err := s.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureHostedBase(projectID); err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeSource(src.ID)
	if !ok || got.HeadRevision != 7 || got.DocCount != 3 {
		t.Fatalf("重复保证不应重置来源进度: %+v (ok=%v)", got, ok)
	}
	if len(s.KnowledgeBases(projectID)) != 1 {
		t.Fatal("重复保证不应多出第二个托管库")
	}
}

func TestEnsureHostedBaseRejectsEmptyProject(t *testing.T) {
	svc, _ := newManagedTestService(t)
	if _, err := svc.EnsureHostedBase("  "); err == nil {
		t.Fatal("空项目标识应被拒绝")
	} else if ae, _ := apperr.As(err); ae == nil || ae.Kind != apperr.Invalid {
		t.Fatalf("want Invalid, got %v", err)
	}
}

func TestScopeToProject(t *testing.T) {
	cases := []struct {
		name               string
		allowed, requested []string
		want               []string
		wantErr            bool
	}{
		{"空请求扩展到全部授权来源", []string{"a", "b"}, nil, []string{"a", "b"}, false},
		{"空授权来源返回空且不放行", nil, nil, []string{}, false},
		{"点名子集被接受并去重", []string{"a", "b"}, []string{" b ", "b"}, []string{"b"}, false},
		{"点名范围外来源被拒绝", []string{"a"}, []string{"a", "c"}, nil, true},
	}
	for _, tc := range cases {
		got, err := scopeToProject(tc.allowed, tc.requested)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if !tc.wantErr && strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidatePublicGitURL(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/example/repo.git",
		"ssh://git@example.com/repo.git",
		"git@example.com:repo.git",
		"  https://github.com/example/repo  ",
	} {
		if _, err := validatePublicGitURL(raw); err != nil {
			t.Fatalf("公开地址 %q 应通过: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "http://example.com/repo", "ftp://example.com/repo", "github.com/repo",
		"ssh://git:secret@example.com/repo", "https://example.com/repo?token=secret",
		"https://u:p@example.com/repo.git",
	} {
		if _, err := validatePublicGitURL(raw); err == nil {
			t.Fatalf("非法地址 %q 应被拒绝", raw)
		}
	}
}

func TestRegisterGitSourceNormalizesRootPrefix(t *testing.T) {
	svc, s := newManagedTestService(t)
	base, err := svc.EnsureHostedBase("proj-prefix")
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "  ", "/", " / "} {
		src, err := svc.RegisterGitSource(GitSourceInput{
			ProjectID: "proj-prefix", BaseID: base.ID, Name: "样例",
			GitURL: "https://github.com/example/repo.git", Branch: "main", PathPrefix: prefix,
		})
		if err != nil {
			t.Fatalf("prefix %q 应登记成功: %v", prefix, err)
		}
		stored, ok := s.KnowledgeSource(src.ID)
		if !ok || stored.PathPrefix != "" {
			t.Fatalf("空前缀或 / 应存成空，实际 %+v (ok=%v)", stored, ok)
		}
	}
	for _, bad := range []string{"..", "../docs", "/docs", `docs\guide`, "a/../b", "a//b"} {
		if _, err := svc.RegisterGitSource(GitSourceInput{
			ProjectID: "proj-prefix", BaseID: base.ID, GitURL: "https://github.com/example/other.git", PathPrefix: bad,
		}); err == nil {
			t.Fatalf("非法路径前缀 %q 应被拒绝", bad)
		} else if ae, _ := apperr.As(err); ae == nil || ae.Kind != apperr.Invalid {
			t.Fatalf("非法路径前缀 %q 应返回 Invalid，实际 %v", bad, err)
		}
	}
}

func TestDocumentListIncludesGitSource(t *testing.T) {
	svc, s := newManagedTestService(t)
	base, err := svc.EnsureHostedBase("proj-docs")
	if err != nil {
		t.Fatal(err)
	}
	src, err := svc.RegisterGitSource(GitSourceInput{
		ProjectID: "proj-docs", BaseID: base.ID, Name: "Git 文档",
		GitURL: "https://github.com/example/repo.git", Branch: "main", PathPrefix: "docs/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{
		SourceID: src.ID, Path: "docs/guide.md", Title: "指南", LineCount: 2, Commit: "abcdef123456",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.DocumentList("proj-docs", src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Path != "docs/guide.md" || list[0].Commit != "abcdef123456" {
		t.Fatalf("文档页应能列出 Git 来源的已索引文档: %+v", list)
	}
	if _, err := svc.DocumentList("other-project", src.ID); err == nil {
		t.Fatal("其它项目不应读取该来源的文档清单")
	}
}

type deadlineSearchUpstream struct{ fakeUpstream }

func (u *deadlineSearchUpstream) Search(ctx context.Context, query string, sources []string) (SearchResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		return SearchResult{}, errors.New("search called without a timeout")
	}
	return SearchResult{Query: query, Mode: "lexical", Results: []SearchHit{}}, nil
}

func TestTrialSearchCallsUpstreamWithTimeout(t *testing.T) {
	svc, store := newManagedTestService(t)
	base, err := svc.EnsureHostedBase("proj-trial")
	if err != nil {
		t.Fatal(err)
	}
	sources := store.KnowledgeSources(base.ID)
	if len(sources) != 1 {
		t.Fatalf("hosted sources = %+v", sources)
	}
	out, err := svc.TrialSearch(context.Background(), &deadlineSearchUpstream{}, "proj-trial", "检索", nil)
	if err != nil {
		t.Fatalf("trial search failed: %v", err)
	}
	if len(out.Sources) != 1 || out.Sources[0] != sources[0].ID {
		t.Fatalf("trial sources = %v, want %s", out.Sources, sources[0].ID)
	}
}

type deleteOrderStore struct {
	*repo.Store
	calls []string
}

func (s *deleteOrderStore) DeleteKnowledgeSourceContent(id string) error {
	s.calls = append(s.calls, "content:"+id)
	return s.Store.DeleteKnowledgeSourceContent(id)
}

func (s *deleteOrderStore) DeleteKnowledgeSource(id string) error {
	s.calls = append(s.calls, "control:"+id)
	return s.Store.DeleteKnowledgeSource(id)
}

func (s *deleteOrderStore) DeleteKnowledgeSourceContentContext(ctx context.Context, id string) error {
	s.calls = append(s.calls, "content:"+id)
	return s.Store.DeleteKnowledgeSourceContentContext(ctx, id)
}
func (s *deleteOrderStore) DeleteKnowledgeSourceMarked(id, owner string) error {
	s.calls = append(s.calls, "control:"+id)
	return s.Store.DeleteKnowledgeSourceMarked(id, owner)
}

func TestDeleteSourceRemovesContentBeforeControl(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	store := &deleteOrderStore{Store: s}
	svc := &Service{store: store}
	base, err := svc.EnsureHostedBase("proj-delete")
	if err != nil {
		t.Fatal(err)
	}
	hosted := hostedSourceID("proj-delete")
	src, err := svc.RegisterGitSource(GitSourceInput{
		ProjectID: "proj-delete", BaseID: base.ID, Name: "失败来源",
		GitURL: "https://github.com/example/missing.git", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{
		SourceID: src.ID, Path: "README.md", Title: "Readme", Body: "# Readme\n", LineCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeWriteback(domain.KnowledgeWriteback{
		ID: "kwb-delete", SubmissionID: "kbs-delete", SourceID: src.ID, Path: "README.md",
		Branch: "kb/writeback-kbs-delete", Status: domain.KnowledgeWritebackBranchPushed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeRevision(domain.KnowledgeRevision{
		ID: "rev-git", SourceID: src.ID, Path: "README.md", Revision: 1, Body: "# Readme\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKnowledgeChunks(src.ID, "README.md", []domain.KnowledgeChunk{{
		SourceID: src.ID, Path: "README.md", Idx: 0, Title: "Readme", StartLine: 1, EndLine: 1, Text: "# Readme",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteSource("proj-delete", hosted); err == nil {
		t.Fatal("托管来源不可删除")
	} else if ae, _ := apperr.As(err); ae == nil || ae.Kind != apperr.Invalid {
		t.Fatalf("删除托管来源应返回 Invalid，实际 %v", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("托管来源被拒时不应触碰内容面: %v", store.calls)
	}

	if err := svc.DeleteSource("proj-delete", src.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{"content:" + src.ID, "control:" + src.ID}
	if strings.Join(store.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("删除顺序应为先内容后控制面: %v", store.calls)
	}
	if _, ok := s.KnowledgeSource(src.ID); ok {
		t.Fatal("控制面来源未删除")
	}
	if docs := s.KnowledgeDocuments(src.ID); len(docs) != 0 {
		t.Fatalf("内容面文档未删除: %+v", docs)
	}
	if _, ok := s.KnowledgeWriteback("kwb-delete"); ok {
		t.Fatal("来源删除后写回记录未清理")
	}
	if revs := s.KnowledgeRevisions(src.ID, "README.md"); len(revs) != 0 {
		t.Fatalf("内容面修订未删除: %+v", revs)
	}
	if chunks := s.KnowledgeChunks(src.ID, "README.md"); len(chunks) != 0 {
		t.Fatalf("内容面索引块未删除: %+v", chunks)
	}
}

func TestDeleteSourceRefusesActiveWritebackLease(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	svc := &Service{store: s, now: func() time.Time { return time.Now().UTC() }}
	base, err := svc.EnsureHostedBase("proj-delete-lease")
	if err != nil {
		t.Fatal(err)
	}
	src, err := svc.RegisterGitSource(GitSourceInput{
		ProjectID: "proj-delete-lease", BaseID: base.ID, Name: "写回中的来源",
		GitURL: "https://github.com/example/lease.git", Branch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimKnowledgeWrite(src.ID, "peer", time.Minute); err != nil || !ok {
		t.Fatalf("claim = %v %v", ok, err)
	}
	err = svc.DeleteSource("proj-delete-lease", src.ID)
	if ae, _ := apperr.As(err); ae == nil || ae.Kind != apperr.Conflict {
		t.Fatalf("delete during lease = %v", err)
	}
	if _, ok := s.KnowledgeSource(src.ID); !ok {
		t.Fatal("source was deleted while a writeback held the lease")
	}
	if _, err := s.FinishKnowledgeWrite(src.ID, "peer"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSource("proj-delete-lease", src.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSourceNamespaceCannotHijackPublicBase(t *testing.T) {
	svc, store := newManagedTestService(t)
	svc.newID = func() string { return "collision" }
	shared, err := svc.CreateSharedBase("Public A", 1)
	if err != nil {
		t.Fatal(err)
	}
	input := GitSourceInput{BaseID: shared.ID, GitURL: "https://example.com/docs.git", Branch: "main"}
	public, err := svc.RegisterGitSource(input)
	if err != nil {
		t.Fatal(err)
	}
	// User B chooses A's public base ID as their project ID.
	if _, err := svc.EnsureHostedBase(shared.ID); err == nil {
		t.Error("project ID matching an existing base must fail")
	}
	// A pre-existing collision project still cannot take the public source.
	project := domain.KnowledgeBase{ID: "legacy-project", ProjectID: shared.ID, Layer: "project"}
	if err := store.PutKnowledgeBase(project); err != nil {
		t.Fatal(err)
	}
	input.ProjectID = shared.ID
	input.BaseID = project.ID
	own, err := svc.RegisterGitSource(input)
	if err != nil {
		t.Fatal(err)
	}
	if own.ID == public.ID {
		t.Error("B's Git source collided with A's public source")
	}
	saved, _ := store.KnowledgeSource(public.ID)
	if saved.BaseID != shared.ID {
		t.Error("public source ownership hijacked")
	}
}

func TestHostedIDsFitStorageAndRejectForeignSources(t *testing.T) {
	svc, store := newManagedTestService(t)
	project := strings.Repeat("p", 64)
	base, err := svc.EnsureHostedBase(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.ID) > 64 {
		t.Errorf("base ID exceeds storage limit: %d", len(base.ID))
	}
	for _, src := range store.KnowledgeSources(base.ID) {
		if len(src.ID) > 64 {
			t.Errorf("source ID exceeds storage limit: %d", len(src.ID))
		}
	}
	// A corrupt deterministic source must fail instead of being silently adopted.
	store.PutKnowledgeBase(domain.KnowledgeBase{ID: "foreign", ProjectID: "a", Layer: "project"})
	store.PutKnowledgeSource(domain.KnowledgeSource{ID: hostedSourceID("b"), BaseID: "foreign", Kind: "hosted"})
	if _, err := svc.EnsureHostedBase("b"); err == nil {
		t.Error("adopted another project's hosted source")
	}
}

// Before 054 a hosted source was named kb-hosted-<projectID>. The new binary
// must refuse to add its hash-based source beside the old one, otherwise the
// project gets two "托管文档" sources and old documents become unreachable.
func TestEnsureHostedBaseRefusesPre039Source(t *testing.T) {
	svc, store := newManagedTestService(t)
	const project = "legacy-source-project"
	baseID := hostedBaseID(project)
	if err := store.PutKnowledgeBase(domain.KnowledgeBase{ID: baseID, ProjectID: project, Name: "项目知识库", Layer: "project"}); err != nil {
		t.Fatal(err)
	}
	oldSource := "kb-hosted-" + project
	if err := store.PutKnowledgeSource(domain.KnowledgeSource{ID: oldSource, BaseID: baseID, Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureHostedBase(project); err == nil || !strings.Contains(err.Error(), "054") {
		t.Fatalf("pre-054 source must fail with a migration instruction: %v", err)
	}
	sources := store.KnowledgeSources(baseID)
	if len(sources) != 1 || sources[0].ID != oldSource {
		t.Fatalf("must not create a second hosted source: %+v", sources)
	}
}

func TestRegisterGitSourcePreservesOwnedLegacyCredential(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "legacy-credential-test")
	svc, store := newManagedTestService(t)
	base, err := svc.EnsureHostedBase("legacy")
	if err != nil {
		t.Fatal(err)
	}
	const source = "kb-git-legacy-id"
	credential := GitCredential{Kind: "https", Username: "deploy", Token: "test-token"}
	ciphertext, err := encryptGitCredential(source, credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutKnowledgeSourceWithCredential(domain.KnowledgeSource{ID: source, BaseID: base.ID, Kind: "git", GitURL: "https://example.com/docs.git", Branch: "main"}, ciphertext); err != nil {
		t.Fatal(err)
	}
	got, err := svc.RegisterGitSource(GitSourceInput{ProjectID: "legacy", BaseID: base.ID, GitURL: "https://example.com/docs.git"})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := store.KnowledgeSource(got.ID)
	decoded, err := DecryptGitCredential(saved.ID, saved.CredentialCiphertext)
	if got.ID != source || err != nil || decoded == nil || decoded.Token != "test-token" {
		t.Fatalf("legacy grant lost: id=%s err=%v", got.ID, err)
	}
}
