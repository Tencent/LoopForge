package knowledge

import (
	"strings"
	"sync"
	"testing"

	"agentflow/internal/apperr"
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
		"http://example.com/repo",
		"  https://github.com/example/repo  ",
	} {
		if _, err := validatePublicGitURL(raw); err != nil {
			t.Fatalf("公开地址 %q 应通过: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "ftp://example.com/repo", "github.com/repo",
		"https://u:p@example.com/repo.git",
	} {
		if _, err := validatePublicGitURL(raw); err == nil {
			t.Fatalf("非法地址 %q 应被拒绝", raw)
		}
	}
}

