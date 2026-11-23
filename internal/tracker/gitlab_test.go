/**
 * 自托管 GitLab MR 适配器的回归测试：allowlist 凭据边界、merge 端点
 * 真合并校验、创建后合并、跨域重定向拒绝与 scheme 跟随仓库记录。
 * 认证使用平台的每主机 HTTPS Git 凭据，而非已废弃的单体 GitToken。
 * --- @ty.aicoding@1795418634080 ---
 */
package tracker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// gitLabFixture 起一个本地 GitLab（httptest），注册 owner 的每主机凭据与
// gitlab: 仓库行。凭据按平台规则只能绑定命名主机（不能是 IP），因此标识、
// allowlist 与凭据统一使用占位名 gitlab.example.test，实际请求经
// gitHostAPIBase 指向测试服务器。
func gitLabFixture(t *testing.T, handler http.HandlerFunc) (*Service, string) {
	t.Helper()
	return gitLabFixtureWithToken(t, handler, "gl-private-token")
}

const gitLabFixtureHost = "gitlab.example.test"

func gitLabFixtureWithToken(t *testing.T, handler http.HandlerFunc, token string) (*Service, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "gitlab-owner"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gitlab-owner")
	if err := st.PutProject(domain.Project{ID: "glproj", Name: "GL", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(domain.GitLabAllowedHostsEnv, gitLabFixtureHost)
	project := "gitlab:" + gitLabFixtureHost + "/group/repo"
	if err := st.PutRepository(domain.Repository{ID: "gl-repo", ProjectID: "glproj", Provider: "gitlab", Name: "repo", GongfengProjectID: project, DefaultBranch: "main", WebURL: "https://" + gitLabFixtureHost + "/group/repo"}); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
			GitHTTPCredential: domain.GitHTTPCredential{Name: gitLabFixtureHost, Platform: domain.GitPlatformGitLab, Host: gitLabFixtureHost},
			Token:             token,
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.gitHostAPIBase = srv.URL
	return s, project
}

// 回归（评审阻塞项）：实例未在管理员 allowlist 中时，连客户端都不能建，
// 更不能带着 owner 的 Git 令牌发请求。
func TestGitLabClientRejectsInstanceOutsideAllowlist(t *testing.T) {
	t.Setenv(domain.GitLabAllowedHostsEnv, "")
	if _, err := NewService().gitlabClient("gitlab:gitlab.attacker.example/group/repo"); err == nil ||
		!strings.Contains(err.Error(), domain.GitLabAllowedHostsEnv) {
		t.Fatalf("unlisted GitLab instance must be rejected with an actionable error, got %v", err)
	}
}

// 声明过的公网实例可以建客户端；没有用户凭据时不注入任何凭证。
func TestGitLabClientAllowsDeclaredInstanceWithoutToken(t *testing.T) {
	t.Setenv(domain.GitLabAllowedHostsEnv, "gitlab.example.test")
	g, err := NewService().gitlabClient("gitlab:gitlab.example.test/group/repo")
	if err != nil {
		t.Fatalf("declared instance must be accepted: %v", err)
	}
	if g.base != "https://gitlab.example.test" || g.token != "" {
		t.Fatalf("base = %q token = %q, want https base and no token", g.base, g.token)
	}
}

// 未配置主机凭据时，请求里不能出现 PRIVATE-TOKEN 头。
func TestGitLabFindBranchOmitsTokenWithoutCredential(t *testing.T) {
	var tokenHeaders int32
	s, project := gitLabFixtureWithToken(t, func(w http.ResponseWriter, r *http.Request) {
		if n := len(r.Header.Values("PRIVATE-TOKEN")); n > 0 {
			atomic.AddInt32(&tokenHeaders, int32(n))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"iid":3,"state":"merged","source_branch":"feat","target_branch":"main"}]`))
	}, "")
	mr, ok, err := s.gitlabFindBranch(project, "feat")
	if err != nil || !ok || mr.IID != 3 {
		t.Fatalf("find branch = %+v ok=%v err=%v", mr, ok, err)
	}
	if n := atomic.LoadInt32(&tokenHeaders); n != 0 {
		t.Fatalf("PRIVATE-TOKEN must not be sent without a user credential, saw %d", n)
	}
}

// 回归：EnsureAndMergeBranchMR 必须真正调用 merge 端点并校验 state=merged，
// 未合并绝不能报成功（评审 P1-3：只建 MR 返回 !7 让交付虚进）。
func TestGitLabMergeBranchMergesAndVerifies(t *testing.T) {
	mergeCalls := 0
	s, project := gitLabFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "gl-private-token" {
			t.Errorf("wrong credential: %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{"iid":7,"state":"opened","source_branch":"feat","target_branch":"main"}]`))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/merge_requests/7/merge"):
			mergeCalls++
			_, _ = w.Write([]byte(`{"iid":7,"state":"merged","merged_at":"2026-11-11T01:00:00Z"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ref, err := s.EnsureAndMergeBranchMR(project, "feat", "main", "Title", "")
	if err != nil || ref != "!7" {
		t.Fatalf("merge: %s %v", ref, err)
	}
	if mergeCalls != 1 {
		t.Fatalf("merge endpoint called %d times, want 1", mergeCalls)
	}
}

// 回归：merge 端点返回非 merged 状态时必须报错（HTTP 成功 ≠ 合并成功）。
func TestGitLabMergeBranchRejectsUnmergedState(t *testing.T) {
	s, project := gitLabFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{"iid":7,"state":"opened","source_branch":"feat","target_branch":"main"}]`))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/merge_requests/7/merge"):
			// GitLab 在流水线未过/冲突时返回 405；这里模拟 200 但未合并的病态响应。
			_, _ = w.Write([]byte(`{"iid":7,"state":"opened"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if _, err := s.EnsureAndMergeBranchMR(project, "feat", "main", "Title", ""); err == nil {
		t.Fatal("unmerged state must be reported as an error")
	}
}

// 回归：没有现成 MR 时先创建再合并，全程校验凭证只发给本实例。
func TestGitLabMergeBranchCreatesThenMerges(t *testing.T) {
	s, project := gitLabFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "gl-private-token" {
			t.Errorf("wrong credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`{"iid":9,"state":"opened","source_branch":"feat","target_branch":"main"}`))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/merge_requests/9/merge"):
			_, _ = w.Write([]byte(`{"iid":9,"state":"merged","merged_at":"2026-11-11T01:00:00Z"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if ref, err := s.EnsureAndMergeBranchMR(project, "feat", "main", "Title", ""); err != nil || ref != "!9" {
		t.Fatalf("create+merge: %s %v", ref, err)
	}
}

// 回归（评审 P1-2）：实例返回 302 指向第二个 origin 时，客户端必须拒绝跟随，
// PRIVATE-TOKEN 绝不能到达重定向目标。
func TestGitLabClientRefusesCrossOriginRedirect(t *testing.T) {
	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Errorf("PRIVATE-TOKEN leaked to redirect target")
		}
		atomic.AddInt32(&foreignHits, 1)
		w.WriteHeader(200)
	}))
	defer foreign.Close()
	s, project := gitLabFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/hook", http.StatusFound)
	})
	if _, err := s.EnsureAndMergeBranchMR(project, "feat", "main", "Title", ""); err == nil {
		t.Fatal("redirect must be rejected, not followed")
	}
	if n := atomic.LoadInt32(&foreignHits); n != 0 {
		t.Fatalf("redirect target was hit %d times", n)
	}
}

// 回归（评审 P1-1）：测试注入把请求指向本地 httptest（http），
// GitLabInstanceScheme 在无匹配记录时默认 https——scheme 不再硬编码。
func TestGitLabClientSchemeFollowsRepositoryRecord(t *testing.T) {
	s, project := gitLabFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	g, err := s.gitlabClient(project)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.base, "http://") {
		t.Fatalf("injected test base must be used, base=%s", g.base)
	}
	// 同一 host 但记录里没有匹配 URL 时默认 https。
	if s2 := domain.GitLabInstanceScheme("gitlab.example.test"); s2 != "https" {
		t.Fatalf("default scheme = %s, want https", s2)
	}
}

// 主机凭据声明的平台不是 GitLab 时拒绝构造客户端，避免把令牌以错误的
// 认证形态发出去。
func TestGitLabClientRejectsNonGitLabCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "owner"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("owner")
	if err := st.PutProject(domain.Project{ID: "p", Name: "P", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	host := gitLabFixtureHost
	t.Setenv(domain.GitLabAllowedHostsEnv, host)
	project := "gitlab:" + host + "/group/repo"
	if err := st.PutRepository(domain.Repository{ID: "r", ProjectID: "p", Provider: "gitlab", Name: "repo", GongfengProjectID: project, WebURL: "https://" + host + "/group/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: host, Platform: domain.GitPlatformGitHub, Host: host},
		Token:             "wrong-platform",
	}); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.gitHostAPIBase = srv.URL
	if _, err := s.gitlabClient(project); err == nil || !strings.Contains(err.Error(), "GitLab") {
		t.Fatalf("non-GitLab credential must be rejected, got %v", err)
	}
}
