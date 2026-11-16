package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubCloseCommentArg(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{name: "missing", args: map[string]any{}, want: ""},
		{name: "trimmed", args: map[string]any{"comment": "  由 #7 解决  "}, want: "由 #7 解决"},
		{name: "at limit", args: map[string]any{"comment": strings.Repeat("评", 10000)}, want: strings.Repeat("评", 10000)},
		{name: "over limit", args: map[string]any{"comment": strings.Repeat("评", 10001)}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := githubCloseCommentArg(tc.args)
			if tc.wantErr {
				if err == nil || err.Error() != "comment must be at most 10000 characters" {
					t.Fatalf("expected length error, got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("githubCloseCommentArg() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGitHubAddCloseCommentReturnsURL(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /repos/octo/app/issues/3/comments": githubJSON(http.StatusCreated, `{"id":11,"html_url":"https://github.com/octo/app/issues/3#issuecomment-11"}`),
	}}
	api.serve(t)
	url, err := githubAddCloseComment(context.Background(), githubToolTestToken, "octo/app", 3, "done")
	if err != nil || url != "https://github.com/octo/app/issues/3#issuecomment-11" {
		t.Fatalf("githubAddCloseComment() = %q, %v", url, err)
	}
	if len(api.bodies) != 1 || !strings.Contains(api.bodies[0], `"body":"done"`) {
		t.Fatalf("unexpected comment payload: %v", api.bodies)
	}
}

func TestGitHubAddCloseCommentPropagatesError(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /repos/octo/app/issues/3/comments": githubJSON(http.StatusForbidden, `{"message":"Resource not accessible"}`),
	}}
	api.serve(t)
	url, err := githubAddCloseComment(context.Background(), githubToolTestToken, "octo/app", 3, "done")
	if err == nil || url != "" || !strings.Contains(err.Error(), "添加评论") {
		t.Fatalf("expected comment error, got %q, %v", url, err)
	}
}
