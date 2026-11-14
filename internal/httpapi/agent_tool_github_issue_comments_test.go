package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubIssueLatestCommentsReadsLastPageAndKeepsNewest(t *testing.T) {
	var query string
	items := make([]map[string]any, 0, githubIssueMaxComments+5)
	for i := 1; i <= githubIssueMaxComments+5; i++ {
		items = append(items, map[string]any{"body": fmt.Sprintf("c%d", i), "user": map[string]string{"login": "alice"}, "created_at": "2026-11-14T00:00:00Z"})
	}
	items[len(items)-1]["body"] = strings.Repeat("长", githubIssueCommentRunes+10)
	payload, _ := json.Marshal(items)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/5/comments": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			_, _ = w.Write(payload)
		},
	}}
	api.serve(t)

	total := 2*githubListPageSize + len(items)
	comments, warning := githubIssueLatestComments(context.Background(), githubHTTPClient(), "token", "octo/app", 5, total)
	if warning != "" {
		t.Fatalf("unexpected warning %q", warning)
	}
	if !strings.Contains(query, "page=3") || !strings.Contains(query, fmt.Sprintf("per_page=%d", githubListPageSize)) {
		t.Fatalf("query = %q, want last page 3", query)
	}
	if len(comments) != githubIssueMaxComments {
		t.Fatalf("got %d comments, want %d", len(comments), githubIssueMaxComments)
	}
	if comments[0]["body"] != "c6" || comments[0]["author"] != "alice" || comments[0]["createdAt"] != "2026-11-14T00:00:00Z" {
		t.Fatalf("first kept comment = %v, want c6", comments[0])
	}
	if last := comments[len(comments)-1]["body"]; len([]rune(last)) != githubIssueCommentRunes+1 || !strings.HasSuffix(last, "…") {
		t.Fatalf("last comment body not truncated: %d runes", len([]rune(last)))
	}
}

func TestGitHubIssueLatestCommentsWarnsOnFailure(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/1/comments": githubJSON(http.StatusInternalServerError, `{"message":"boom"}`),
		"GET /repos/octo/app/issues/2/comments": githubJSON(http.StatusOK, `not-json`),
	}}
	api.serve(t)

	for _, tc := range []struct {
		number int
		want   string
	}{
		{1, "评论读取失败（HTTP 500）"},
		{2, "评论读取失败（HTTP 200）"},
	} {
		comments, warning := githubIssueLatestComments(context.Background(), githubHTTPClient(), "token", "octo/app", tc.number, 3)
		if !strings.HasPrefix(warning, tc.want) {
			t.Fatalf("issue %d warning = %q, want prefix %q", tc.number, warning, tc.want)
		}
		if comments == nil || len(comments) != 0 {
			t.Fatalf("issue %d comments = %#v, want empty non-nil list", tc.number, comments)
		}
	}
}

func TestGitHubIssueGetWithoutCommentsSkipsCommentRequest(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/9": githubJSON(http.StatusOK, `{"number":9,"title":"t","state":"open","html_url":"u","user":{"login":"bob"},"labels":[],"body":"b","comments":0}`),
		"GET /repos/octo/app/issues/9/comments": func(w http.ResponseWriter, _ *http.Request) {
			t.Error("comments must not be requested when the issue has none")
			_, _ = io.WriteString(w, `[]`)
		},
	}}
	api.serve(t)

	text, err := githubIssueGet(context.Background(), "token", "octo/app", map[string]any{"number": float64(9)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"comments":[]`) || strings.Contains(text, "commentsWarning") {
		t.Fatalf("issue get = %s", text)
	}
}

func TestGitHubIssueGetReportsCommentWarning(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/9":          githubJSON(http.StatusOK, `{"number":9,"title":"t","state":"open","html_url":"u","user":{"login":"bob"},"labels":[],"body":"b","comments":2}`),
		"GET /repos/octo/app/issues/9/comments": githubJSON(http.StatusForbidden, `{"message":"no"}`),
	}}
	api.serve(t)

	text, err := githubIssueGet(context.Background(), "token", "octo/app", map[string]any{"number": float64(9)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"comments":[]`) || !strings.Contains(text, "评论读取失败（HTTP 403）") || !strings.Contains(text, `"commentCount":2`) {
		t.Fatalf("issue get = %s", text)
	}
}
