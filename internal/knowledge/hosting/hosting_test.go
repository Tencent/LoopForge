package hosting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectHostingProviders(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		provider Provider
		path     string
		ok       bool
	}{
		{"github https", "https://github.com/Tencent/LoopForge.git", GitHub, "Tencent/LoopForge", true},
		{"github ssh", "git@github.com:Tencent/LoopForge.git", GitHub, "Tencent/LoopForge", true},
		{"github ssh url", "ssh://git@github.com/Tencent/LoopForge.git", GitHub, "Tencent/LoopForge", true},
		{"gitlab saas", "https://gitlab.com/team/sub/repo.git", GitLab, "team/sub/repo", true},
		{"gitlab self hosted", "https://gitlab.example.com/group/repo", GitLab, "group/repo", true},
		{"gitee unsupported", "https://gitee.com/team/repo.git", "", "", false},
		{"codeberg unsupported", "https://codeberg.org/team/repo.git", "", "", false},
		{"bitbucket unsupported", "https://bitbucket.org/team/repo.git", "", "", false},
		{"github nested path", "https://github.com/owner/group/repo.git", "", "", false},
		{"traversal", "https://github.com/owner/../repo.git", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, ok := Detect(tc.raw)
			if ok != tc.ok {
				t.Fatalf("Detect(%q) ok=%v want %v", tc.raw, ok, tc.ok)
			}
			if !ok {
				return
			}
			if repo.Provider != tc.provider || repo.Path != tc.path {
				t.Fatalf("Detect(%q) = %+v", tc.raw, repo)
			}
		})
	}
}

func TestGitHubCreateMergeRequest(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":12,"html_url":"https://github.com/octo/repo/pull/12","state":"open","merged":false}`))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Token: "gh-token", HTTP: srv.Client()}
	ref, err := client.CreateMergeRequest(context.Background(), Repo{Provider: GitHub, Host: "github.com", Path: "octo/repo"}, MergeRequest{
		Title: "知识：测试文档", Body: "来自知识中心审核队列", SourceBranch: "kb/writeback-kbs-1", TargetBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.IID != 12 || ref.URL != "https://github.com/octo/repo/pull/12" || ref.Provider != GitHub || ref.State != "opened" {
		t.Fatalf("ref = %+v", ref)
	}
	if gotPath != "/repos/octo/repo/pulls" || gotAuth != "Bearer gh-token" || !strings.Contains(gotAccept, "github") {
		t.Fatalf("request path=%q auth=%q accept=%q", gotPath, gotAuth, gotAccept)
	}
	if got["head"] != "kb/writeback-kbs-1" || got["base"] != "main" || got["title"] != "知识：测试文档" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestGitLabCreateMergeRequest(t *testing.T) {
	var gotPath, gotToken string
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"iid":7,"web_url":"https://gitlab.example.com/group/sub/repo/-/merge_requests/7","state":"opened"}`))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL + "/api/v4", Token: "gl-token", HTTP: srv.Client()}
	ref, err := client.CreateMergeRequest(context.Background(), Repo{Provider: GitLab, Host: "gitlab.example.com", Path: "group/sub/repo"}, MergeRequest{
		Title: "知识：测试文档", Body: "来自知识中心审核队列", SourceBranch: "kb/writeback-kbs-2", TargetBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.IID != 7 || ref.URL != "https://gitlab.example.com/group/sub/repo/-/merge_requests/7" || ref.State != "opened" {
		t.Fatalf("ref = %+v", ref)
	}
	if gotPath != "/api/v4/projects/group%2Fsub%2Frepo/merge_requests" || gotToken != "gl-token" {
		t.Fatalf("request path=%q token=%q", gotPath, gotToken)
	}
	if got["source_branch"] != "kb/writeback-kbs-2" || got["target_branch"] != "main" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestMergeRequestState(t *testing.T) {
	cases := []struct {
		provider Provider
		body     string
		want     string
	}{
		{GitHub, `{"number":1,"state":"open","merged":false}`, "opened"},
		{GitHub, `{"number":1,"state":"closed","merged":true}`, "merged"},
		{GitHub, `{"number":1,"state":"closed","merged":false}`, "closed"},
		{GitLab, `{"iid":1,"state":"opened"}`, "opened"},
		{GitLab, `{"iid":1,"state":"merged"}`, "merged"},
		{GitLab, `{"iid":1,"state":"closed"}`, "closed"},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider)+" "+tc.want, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.provider == GitHub && r.URL.Path != "/repos/octo/repo/pulls/1" {
					t.Fatalf("path = %s", r.URL.Path)
				}
				if tc.provider == GitLab && r.URL.EscapedPath() != "/api/v4/projects/group%2Frepo/merge_requests/1" {
					t.Fatalf("path = %s", r.URL.EscapedPath())
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			base := srv.URL
			if tc.provider == GitLab {
				base += "/api/v4"
			}
			client := &Client{BaseURL: base, Token: "tok", HTTP: srv.Client()}
			repo := Repo{Provider: tc.provider, Host: "example.com", Path: "octo/repo"}
			if tc.provider == GitLab {
				repo.Path = "group/repo"
			}
			got, err := client.MergeRequestState(context.Background(), repo, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("state = %q want %q", got, tc.want)
			}
		})
	}
}

func TestMergeRequestErrorsAndRedirectsDoNotLeakToken(t *testing.T) {
	const token = "secret-canary-token"
	var redirectTarget int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTarget++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("token=" + token))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Token: token, HTTP: srv.Client()}
	_, err := client.CreateMergeRequest(context.Background(), Repo{Provider: GitHub, Host: "github.com", Path: "octo/repo"}, MergeRequest{Title: "t", SourceBranch: "b", TargetBranch: "main"})
	if err == nil {
		t.Fatal("redirect must not be treated as success")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked token: %v", err)
	}
	if redirectTarget != 0 {
		t.Fatal("redirect was followed")
	}
	if !strings.Contains(err.Error(), "合并请求") {
		t.Fatalf("error should be user-facing Chinese: %v", err)
	}
}

func TestCreateMergeRequestRejectsInvalidRepo(t *testing.T) {
	client := &Client{BaseURL: "https://api.github.com", HTTP: http.DefaultClient}
	if _, err := client.CreateMergeRequest(context.Background(), Repo{Provider: GitHub, Path: "only-owner"}, MergeRequest{Title: "t", SourceBranch: "b", TargetBranch: "main"}); err == nil {
		t.Fatal("invalid repository path must be rejected")
	}
}

func TestProductionMergeRequestClientRejectsPrivateDNS(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{"state":"opened"}`)) }))
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "private-token"}
	_, err := client.MergeRequestState(context.Background(), Repo{Provider: GitLab, Host: "gitlab.example.com", Path: "team/repo"}, 1)
	if err == nil || calls != 0 {
		t.Fatalf("private endpoint received token: calls=%d err=%v", calls, err)
	}
}
