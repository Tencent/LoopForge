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
	if _, err := createGitLabMR(context.Background(), "secret-token", origin.URL, "group/repo", "feat", "main", "T", "D"); err == nil {
		t.Fatal("redirect must be rejected, not followed")
	}
	if n := atomic.LoadInt32(&foreignHits); n != 0 {
		t.Fatalf("redirect target was hit %d times", n)
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
