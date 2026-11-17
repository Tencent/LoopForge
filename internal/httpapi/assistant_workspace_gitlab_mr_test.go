package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 回归（评审 P1-2）：工作区 MR client 遇到 302 时必须拒绝跟随，
// PRIVATE-TOKEN 绝不能被转发到重定向目标。
func TestCreateGitLabMRRefusesCrossOriginRedirect(t *testing.T) {
	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Errorf("PRIVATE-TOKEN leaked to redirect target")
		}
		atomic.AddInt32(&foreignHits, 1)
		w.WriteHeader(200)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/hook", http.StatusFound)
	}))
	defer origin.Close()
	// 实例必须由管理员显式声明：凭据只会发往 allowlist 内的 host。
	t.Setenv(domain.GitLabAllowedHostsEnv, strings.TrimPrefix(origin.URL, "http://"))
	if _, err := createGitLabMR(context.Background(), "secret-token", origin.URL, "group/repo", "feat", "main", "T", "D"); err == nil {
		t.Fatal("redirect must be rejected, not followed")
	}
	if n := atomic.LoadInt32(&foreignHits); n != 0 {
		t.Fatalf("redirect target was hit %d times", n)
	}
}

// 回归（评审阻塞项）：未在管理员 allowlist 中的 GitLab 实例，工作区 MR
// 客户端必须在发请求前就拒绝，绝不能把发起人 token 带过去。
func TestCreateGitLabMRRejectsUnlistedInstance(t *testing.T) {
	t.Setenv(domain.GitLabAllowedHostsEnv, "")
	_, err := createGitLabMR(context.Background(), "secret-token", "https://gitlab.attacker.example", "group/repo", "feat", "main", "T", "D")
	if err == nil || !strings.Contains(err.Error(), domain.GitLabAllowedHostsEnv) {
		t.Fatalf("unlisted instance must be rejected with an actionable error, got %v", err)
	}
}

// 未配置该实例凭据时，请求里不能出现 PRIVATE-TOKEN 头（空值也不行）。
func TestCreateGitLabMROmitsTokenWithoutCredential(t *testing.T) {
	var tokenHeaders int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := len(r.Header.Values("PRIVATE-TOKEN")); n > 0 {
			atomic.AddInt32(&tokenHeaders, int32(n))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"iid":4}`))
	}))
	defer srv.Close()
	t.Setenv(domain.GitLabAllowedHostsEnv, strings.TrimPrefix(srv.URL, "http://"))
	mr, err := createGitLabMR(context.Background(), "", srv.URL, "group/repo", "feat", "main", "T", "D")
	if err != nil || mr.IID != 4 {
		t.Fatalf("MR = %+v err = %v", mr, err)
	}
	if n := atomic.LoadInt32(&tokenHeaders); n != 0 {
		t.Fatalf("PRIVATE-TOKEN must not be sent without a credential, saw %d", n)
	}
}

// 回归（评审 P1-1）：MR 链接的 scheme 按仓库记录的 URL 对齐，默认 https，
// 不再硬编码 http://。
func TestGitLabRepoPathDerivesSchemeFromRecord(t *testing.T) {
	rc := domain.RepositoryContext{
		GongfengProjectID: "gitlab:gitlab.example.test:8443/group/repo",
		WebURL:            "https://gitlab.example.test:8443/group/repo",
	}
	base, path := gitlabRepoPath(rc)
	if base != "https://gitlab.example.test:8443" || path != "group/repo" {
		t.Fatalf("recorded https scheme must be kept, got base=%s path=%s", base, path)
	}
	rc.WebURL = ""
	rc.CloneURL = "http://gitlab.example.test:8443/group/repo.git"
	base, _ = gitlabRepoPath(rc)
	if base != "http://gitlab.example.test:8443" {
		t.Fatalf("explicit http record must be kept, got %s", base)
	}
	rc.WebURL, rc.CloneURL = "", ""
	base, _ = gitlabRepoPath(rc)
	if !strings.HasPrefix(base, "https://") {
		t.Fatalf("default scheme must be https, got %s", base)
	}
}
