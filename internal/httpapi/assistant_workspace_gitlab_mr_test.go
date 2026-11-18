package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// 回归（agent-owned Git 后的 GitLab 等价能力）：turn 结束时只登记作者本人、
// head 仍在工作区、且在 workspace 创建之后打开/合并的 MR。
func TestGitLabTurnMRsFiltersByAuthorHeadAndTime(t *testing.T) {
	since := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "gl-token" {
			t.Errorf("PRIVATE-TOKEN = %q, want gl-token", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/api/v4/projects/group/repo/merge_requests") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if q := r.URL.Query(); q.Get("scope") != "created_by_me" || q.Get("state") != "all" {
			t.Errorf("query scope=%q state=%q, want created_by_me/all", q.Get("scope"), q.Get("state"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"iid":1,"web_url":"https://gl.example.test/group/repo/-/merge_requests/1","title":"keep","description":"d","state":"opened","created_at":"2026-11-18T01:00:00Z","sha":"aaa","target_branch":"main"},
			{"iid":2,"web_url":"https://gl.example.test/group/repo/-/merge_requests/2","title":"head gone","state":"opened","created_at":"2026-11-18T01:00:00Z","sha":"bbb","target_branch":"main"},
			{"iid":3,"web_url":"https://gl.example.test/group/repo/-/merge_requests/3","title":"too old","state":"opened","created_at":"2026-11-17T01:00:00Z","sha":"aaa","target_branch":"main"},
			{"iid":4,"web_url":"https://gl.example.test/group/repo/-/merge_requests/4","title":"closed unmerged","state":"closed","created_at":"2026-11-18T01:00:00Z","sha":"aaa","target_branch":"main"},
			{"iid":5,"web_url":"https://gl.example.test/group/repo/-/merge_requests/5","title":"merged via diff refs","state":"merged","merged_at":"2026-11-18T02:00:00Z","created_at":"2026-11-18T01:00:00Z","sha":"ccc","diff_refs":{"head_sha":"ddd"},"target_branch":"main"}
		]`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	t.Setenv(domain.GitLabAllowedHostsEnv, host)
	project := "gitlab:" + host + "/group/repo"
	heads := map[string]bool{"aaa": true, "ddd": true}

	mrs, err := gitlabTurnMRs(context.Background(), srv.Client(), "gl-token", project, srv.URL, heads, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 2 || mrs[0].IID != 1 || mrs[1].IID != 5 {
		t.Fatalf("deliveries = %+v, want MR !1 and !5", mrs)
	}
	if mrs[0].Project != project || mrs[0].TargetBranch != "main" || mrs[0].URL == "" {
		t.Fatalf("delivery fields = %+v", mrs[0])
	}
}

// 未在 allowlist 中声明的 GitLab 实例：不发请求、不发凭据。
func TestGitLabTurnMRsRejectsUnlistedInstance(t *testing.T) {
	t.Setenv(domain.GitLabAllowedHostsEnv, "")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()
	_, err := gitlabTurnMRs(context.Background(), srv.Client(), "gl-token",
		"gitlab:gitlab.attacker.example/group/repo", "https://gitlab.attacker.example",
		map[string]bool{"aaa": true}, time.Now().Add(-time.Hour))
	if err == nil || !strings.Contains(err.Error(), domain.GitLabAllowedHostsEnv) {
		t.Fatalf("unlisted instance must be rejected, got %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("unlisted instance received %d requests", n)
	}
}

// 没有用户令牌时不发送认证头（空头也算一次凭证提交）。
func TestGitLabTurnMRsOmitsTokenWithoutCredential(t *testing.T) {
	var tokenHeaders int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := len(r.Header.Values("PRIVATE-TOKEN")); n > 0 {
			atomic.AddInt32(&tokenHeaders, int32(n))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	t.Setenv(domain.GitLabAllowedHostsEnv, host)
	if _, err := gitlabTurnMRs(context.Background(), srv.Client(), "",
		"gitlab:"+host+"/group/repo", srv.URL, map[string]bool{}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&tokenHeaders); n != 0 {
		t.Fatalf("PRIVATE-TOKEN must not be sent without a credential, saw %d", n)
	}
}

// 工作区远端可能是 https、ssh 或带 .git 后缀；绑定记录里的 gitlab: 标识
// 要能按 host+路径匹配上，且不串项目。
func TestGitlabBoundProjectsMatchesRemoteForms(t *testing.T) {
	repos := []domain.Repository{
		{ID: "r1", ProjectID: "p1", GongfengProjectID: "gitlab:gitlab.example.test/group/repo",
			CloneURL: "https://gitlab.example.test/group/repo.git", WebURL: "https://gitlab.example.test/group/repo"},
		{ID: "r2", ProjectID: "p2", GongfengProjectID: "gitlab:other.test/x/y",
			CloneURL: "https://other.test/x/y.git"},
	}
	remotes := []string{
		"https://gitlab.example.test/group/repo.git",
		"git@gitlab.example.test:group/repo.git",
	}
	got := gitlabBoundProjects(remotes, repos, "p1")
	if len(got) != 1 || got[0] != "gitlab:gitlab.example.test/group/repo" {
		t.Fatalf("bound projects = %v", got)
	}
	if got := gitlabBoundProjects(remotes, repos, "p2"); len(got) != 0 {
		t.Fatalf("other project must not match, got %v", got)
	}
}

func TestSplitGitRemoteForms(t *testing.T) {
	cases := map[string][2]string{
		"https://gitlab.example.test/group/repo.git":          {"gitlab.example.test", "group/repo"},
		"https://user:token@gitlab.example.test:8443/g/r.git": {"gitlab.example.test:8443", "g/r"},
		"git@gitlab.example.test:group/repo.git":              {"gitlab.example.test", "group/repo"},
		"ssh://git@gitlab.example.test:2222/g/r.git":          {"gitlab.example.test:2222", "g/r"},
	}
	for raw, want := range cases {
		host, path, ok := splitGitRemote(raw)
		if !ok || host != want[0] || path != want[1] {
			t.Fatalf("splitGitRemote(%q) = %q %q %v, want %q %q", raw, host, path, ok, want[0], want[1])
		}
	}
}

// GitLab MR URL 不在 Gongfeng 的 URL 解析规则内：登记时必须使用发现阶段
// 直接给出的 project/iid，而不是回头从 URL 反解。
func TestRecordConversationMRsUsesProvidedGitLabIdentity(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutIssue(domain.Issue{ID: "iss-gl-1", ProjectID: "p1", Title: "gitlab delivery", Status: "open"}); err != nil {
		t.Fatal(err)
	}
	g := &agentToolGateway{deps: &deps{repo: st}}
	repos := []domain.Repository{{ID: "repo-gl-1", ProjectID: "p1", GongfengProjectID: "gitlab:gl.test/group/repo"}}
	mrs := []deliveredMR{{
		URL:          "https://gl.test/group/repo/-/merge_requests/7",
		Project:      "gitlab:gl.test/group/repo",
		IID:          7,
		TargetBranch: "main",
	}}
	if err := g.recordConversationMRs("iss-gl-1", mrs, repos); err != nil {
		t.Fatal(err)
	}
	issue, ok := st.Issue("iss-gl-1")
	if !ok {
		t.Fatal("issue missing")
	}
	if len(issue.DeliveryMRs) != 1 || issue.DeliveryMRs[0].Project != "gitlab:gl.test/group/repo" || issue.DeliveryMRs[0].IID != 7 {
		t.Fatalf("delivery MRs = %+v", issue.DeliveryMRs)
	}
	if issue.DeliveryMRs[0].RepositoryID != "repo-gl-1" {
		t.Fatalf("repository id = %q, want repo-gl-1", issue.DeliveryMRs[0].RepositoryID)
	}
	key := domain.MRDeliveryKey{Project: "gitlab:gl.test/group/repo", IID: 7}
	if got := st.MRDeliveryStatesMap([]domain.MRDeliveryKey{key})[key]; got.IssueID != "iss-gl-1" {
		t.Fatalf("delivery state = %+v, want issue iss-gl-1", got)
	}
}
