package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The last page must be ceil(total/per_page): exact multiples of the page size
// stay on that page instead of requesting an empty page after it.
func TestGitHubIssueLatestCommentsRequestsExactLastPage(t *testing.T) {
	var page, perPage string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/5/comments": func(w http.ResponseWriter, r *http.Request) {
			page, perPage = r.URL.Query().Get("page"), r.URL.Query().Get("per_page")
			_, _ = w.Write([]byte(`[{"body":"a","user":{"login":"u1"},"created_at":"t1"},{"body":"b","user":{"login":"u2"},"created_at":"t2"}]`))
		},
	}}
	api.serve(t)

	for _, tc := range []struct {
		total    int
		wantPage int
	}{
		{1, 1},
		{githubListPageSize, 1},
		{githubListPageSize + 1, 2},
		{2 * githubListPageSize, 2},
	} {
		page, perPage = "", ""
		comments, warning := githubIssueLatestComments(context.Background(), githubHTTPClient(), "tok-833", "octo/app", 5, tc.total)
		if warning != "" {
			t.Fatalf("total %d: unexpected warning %q", tc.total, warning)
		}
		if page != strconv.Itoa(tc.wantPage) || perPage != strconv.Itoa(githubListPageSize) {
			t.Fatalf("total %d: page=%q per_page=%q, want page=%d per_page=%d", tc.total, page, perPage, tc.wantPage, githubListPageSize)
		}
		// Fewer than githubIssueMaxComments: everything is kept, oldest first.
		if len(comments) != 2 || comments[0]["body"] != "a" || comments[0]["author"] != "u1" || comments[1]["body"] != "b" || comments[1]["createdAt"] != "t2" {
			t.Fatalf("total %d: comments = %v", tc.total, comments)
		}
	}
	for i, got := range api.tokens {
		if got != "Bearer tok-833" {
			t.Fatalf("request %d authorization = %q, want the caller token", i, got)
		}
	}
}

func TestGitHubIssueLatestCommentsWarnsWhenGitHubUnreachable(t *testing.T) {
	previous := githubAPIBase
	githubAPIBase = "http://127.0.0.1:1"
	t.Cleanup(func() { githubAPIBase = previous })

	comments, warning := githubIssueLatestComments(context.Background(), githubHTTPClient(), "token", "octo/app", 5, 3)
	if !strings.HasPrefix(warning, "评论读取失败：无法连接 GitHub：") {
		t.Fatalf("warning = %q, want unreachable warning", warning)
	}
	if comments == nil || len(comments) != 0 {
		t.Fatalf("comments = %#v, want empty non-nil list", comments)
	}
}

// The HTTP failure warning keeps GitHub's error message so the agent can tell
// permission problems from outages.
func TestGitHubIssueLatestCommentsWarningIncludesGitHubDetail(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/1/comments": githubJSON(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`),
	}}
	api.serve(t)

	_, warning := githubIssueLatestComments(context.Background(), githubHTTPClient(), "token", "octo/app", 1, 3)
	if want := fmt.Sprintf("评论读取失败（HTTP %d）：Resource not accessible by integration", http.StatusForbidden); warning != want {
		t.Fatalf("warning = %q, want %q", warning, want)
	}
}

// A non-200 status is a failure even when the body happens to decode as a
// comment array; otherwise a rate-limited read would look like "no comments".
func TestGitHubIssueLatestCommentsTreatsNon200ArrayAsFailure(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/1/comments": githubJSON(http.StatusTooManyRequests, `[{"body":"stale","user":{"login":"x"}}]`),
	}}
	api.serve(t)

	comments, warning := githubIssueLatestComments(context.Background(), githubHTTPClient(), "token", "octo/app", 1, 3)
	if !strings.HasPrefix(warning, fmt.Sprintf("评论读取失败（HTTP %d）", http.StatusTooManyRequests)) {
		t.Fatalf("warning = %q, want HTTP %d failure", warning, http.StatusTooManyRequests)
	}
	if comments == nil || len(comments) != 0 {
		t.Fatalf("comments = %#v, want empty non-nil list", comments)
	}
}
