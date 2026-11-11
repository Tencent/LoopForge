package tracker

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// gitLabFixture 起一个本地 GitLab（httptest），注册 owner 凭证与 gitlab: 仓库行。
// 仓库行的 WebURL 指向测试服务器，因此实例 scheme 为 http（scheme 以记录为准）。
func gitLabFixture(t *testing.T, handler http.HandlerFunc) (*Service, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "gitlab-owner", GitToken: "gl-private-token"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gitlab-owner")
	if err := st.PutProject(domain.Project{ID: "glproj", Name: "GL", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	project := "gitlab:" + host + "/group/repo"
	if err := st.PutRepository(domain.Repository{ID: "gl-repo", ProjectID: "glproj", Provider: "gitlab", Name: "repo", GongfengProjectID: project, DefaultBranch: "main", WebURL: srv.URL + "/group/repo"}); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	return s, project
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

// 回归（评审 P1-1）：实例 scheme 以仓库记录的 URL 为准（此处 http），
// 无记录时默认 https——两端都不再硬编码。
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
		t.Fatalf("recorded http scheme must be used, base=%s", g.base)
	}
	// 同一 host 但记录里没有匹配 URL 时默认 https。
	if s2 := domain.GitLabInstanceScheme("gitlab.example.test"); s2 != "https" {
		t.Fatalf("default scheme = %s, want https", s2)
	}
}
