package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestGitHubPRStatusReportsSizeAuthorAndReviews(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/pulls/7": githubJSON(http.StatusOK, `{"number":7,"title":"Feature","body":"Fixes #3","state":"open","mergeable":true,"mergeable_state":"clean","additions":12,"deletions":3,"changed_files":2,"commits":1,"user":{"login":"alice"},"head":{"sha":"abc123","ref":"topic"},"base":{"ref":"main"}}`),
		"GET /repos/octo/app/pulls/7/reviews": githubJSON(http.StatusOK, `[
			{"state":"CHANGES_REQUESTED","user":{"login":"bob"}},
			{"state":"APPROVED","user":{"login":"bob"}},
			{"state":"COMMENTED","user":{"login":"bob"}},
			{"state":"COMMENTED","user":{"login":"carol"}}
		]`),
		"GET /repos/octo/app/commits/abc123/status":     githubJSON(http.StatusOK, `{"statuses":[]}`),
		"GET /repos/octo/app/commits/abc123/check-runs": githubJSON(http.StatusOK, `{"check_runs":[]}`),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPRStatus, `{"repository":"app","number":7}`)
	for _, want := range []string{`"author":"alice"`, `"additions":12`, `"deletions":3`, `"changedFiles":2`, `"headSha":"abc123"`, `"mergeable":true`, `"approvals":1`, `{"reviewer":"bob","state":"APPROVED"}`, `{"reviewer":"carol","state":"COMMENTED"}`, `"body":"Fixes #3"`} {
		if isErr || !strings.Contains(text, want) {
			t.Fatalf("pr status missing %s: %s (error=%t)", want, text, isErr)
		}
	}
}

func TestGitHubPullsListFiltersByAuthorAcrossPages(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	var pages []string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/pulls": func(w http.ResponseWriter, r *http.Request) {
			pages = append(pages, r.URL.Query().Get("page"))
			if r.URL.Query().Get("page") == "1" {
				items := make([]string, 0, githubListPageSize)
				for i := 0; i < githubListPageSize; i++ {
					items = append(items, `{"number":1000,"title":"other","user":{"login":"bot"}}`)
				}
				_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
				return
			}
			_, _ = io.WriteString(w, `[{"number":5,"title":"mine","user":{"login":"Alice"}},{"number":4,"title":"theirs","user":{"login":"bob"}}]`)
		},
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPullsList, `{"repository":"app","author":"alice"}`)
	if isErr || !strings.Contains(text, `"count":1`) || !strings.Contains(text, `"mine"`) || strings.Contains(text, `"theirs"`) || strings.Contains(text, `"other"`) || !strings.Contains(text, `"hasMore":false`) {
		t.Fatalf("author filter = %s (error=%t)", text, isErr)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v", pages)
	}
}

func TestGitHubPRFilesTruncatesPatches(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	long := strings.Repeat("+x\n", githubPRFilePatchRunes)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/pulls/7/files": githubJSON(http.StatusOK, `[{"filename":"a.go","status":"modified","additions":1,"deletions":0,"patch":"@@ -1 +1 @@\n+ok"},{"filename":"big.txt","status":"added","additions":3000,"deletions":0,"patch":"`+strings.ReplaceAll(long, "\n", `\n`)+`"}]`),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPRFiles, `{"repository":"app","number":7}`)
	if isErr || !strings.Contains(text, `"filename":"a.go"`) || !strings.Contains(text, `+ok`) || !strings.Contains(text, "已截断") || !strings.Contains(text, `"count":2`) {
		t.Fatalf("pr files = %.400s (error=%t)", text, isErr)
	}
}

func TestGitHubMergeReviewCloseNeedDirectWriteMode(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){}}
	api.serve(t)
	for _, tc := range []struct{ name, args string }{
		{mainBotGitHubPRMerge, `{"repository":"app","number":7}`},
		{mainBotGitHubPRReview, `{"repository":"app","number":7,"event":"APPROVE"}`},
		{mainBotGitHubIssueClose, `{"repository":"app","number":3}`},
	} {
		text, isErr := callGitHubTool(t, f, tc.name, tc.args)
		if !isErr || !strings.Contains(text, "「请求批准」") || !strings.Contains(text, "「完全权限」") {
			t.Fatalf("%s in confirm mode = %s (error=%t)", tc.name, text, isErr)
		}
	}
	if len(api.requests) != 0 {
		t.Fatalf("confirm mode called GitHub: %v", api.requests)
	}
}

func TestGitHubMergeReviewCloseInFullAccess(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	f.session.ExecMode = assistantExecModeForApproval(AssistantApprovalAuto)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"PUT /repos/octo/app/pulls/7/merge":      githubJSON(http.StatusOK, `{"sha":"m1","merged":true,"message":"Pull Request successfully merged"}`),
		"PUT /repos/octo/app/pulls/8/merge":      githubJSON(http.StatusMethodNotAllowed, `{"message":"Required status check \"ci\" is expected."}`),
		"PUT /repos/octo/app/pulls/9/merge":      githubJSON(http.StatusConflict, `{"message":"Head branch was modified."}`),
		"POST /repos/octo/app/pulls/7/reviews":   githubJSON(http.StatusOK, `{"id":5,"state":"APPROVED","html_url":"https://github.com/octo/app/pull/7#r5"}`),
		"POST /repos/octo/app/pulls/8/reviews":   githubJSON(http.StatusUnprocessableEntity, `{"message":"Unprocessable Entity","errors":["Can not approve your own pull request"]}`),
		"GET /repos/octo/app/issues/3":           githubJSON(http.StatusOK, `{"number":3,"state":"open","html_url":"https://github.com/octo/app/issues/3"}`),
		"POST /repos/octo/app/issues/3/comments": githubJSON(http.StatusCreated, `{"id":1,"html_url":"https://github.com/octo/app/issues/3#c1"}`),
		"PATCH /repos/octo/app/issues/3":         githubJSON(http.StatusOK, `{"number":3,"state":"closed"}`),
		"GET /repos/octo/app/issues/4":           githubJSON(http.StatusOK, `{"number":4,"state":"closed"}`),
		"GET /repos/octo/app/issues/7":           githubJSON(http.StatusOK, `{"number":7,"state":"open","pull_request":{"url":"x"}}`),
	}}
	api.serve(t)

	text, isErr := callGitHubTool(t, f, mainBotGitHubPRMerge, `{"repository":"app","number":7,"method":"squash","sha":"abc123"}`)
	if isErr || !strings.Contains(text, `"merged":true`) || !strings.Contains(text, `"method":"squash"`) {
		t.Fatalf("merge = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRMerge, `{"repository":"app","number":8}`); !isErr || !strings.Contains(text, "当前不能合并") || !strings.Contains(text, "Required status check") {
		t.Fatalf("blocked merge = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRMerge, `{"repository":"app","number":9,"sha":"old"}`); !isErr || !strings.Contains(text, "head 已变化") {
		t.Fatalf("stale merge = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRMerge, `{"repository":"app","number":7,"method":"fast-forward"}`); !isErr || !strings.Contains(text, "method") {
		t.Fatalf("invalid method = %s (error=%t)", text, isErr)
	}

	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRReview, `{"repository":"app","number":7,"event":"approve"}`); isErr || !strings.Contains(text, `"state":"APPROVED"`) {
		t.Fatalf("review = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRReview, `{"repository":"app","number":8,"event":"APPROVE"}`); !isErr || !strings.Contains(text, "event=COMMENT") {
		t.Fatalf("self approval = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRReview, `{"repository":"app","number":7,"event":"REQUEST_CHANGES"}`); !isErr || !strings.Contains(text, "body 必填") {
		t.Fatalf("request changes without body = %s (error=%t)", text, isErr)
	}

	if text, isErr = callGitHubTool(t, f, mainBotGitHubIssueClose, `{"repository":"app","number":3,"comment":"由 #7 解决"}`); isErr || !strings.Contains(text, `"state":"closed"`) || !strings.Contains(text, `"reason":"completed"`) || !strings.Contains(text, "commentUrl") {
		t.Fatalf("close = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubIssueClose, `{"repository":"app","number":4}`); isErr || !strings.Contains(text, `"alreadyClosed":true`) {
		t.Fatalf("already closed = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubIssueClose, `{"repository":"app","number":7}`); !isErr || !strings.Contains(text, "是 PR") {
		t.Fatalf("close PR = %s (error=%t)", text, isErr)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	bodies := strings.Join(api.bodies, "\n")
	for _, want := range []string{`"merge_method":"squash"`, `"sha":"abc123"`, `"event":"APPROVE"`, `{"state":"closed","state_reason":"completed"}`, `{"body":"由 #7 解决"}`} {
		if !strings.Contains(bodies, want) {
			t.Fatalf("payloads missing %s: %s", want, bodies)
		}
	}
	for _, req := range api.requests {
		if req == "PATCH /repos/octo/app/issues/4" || req == "PATCH /repos/octo/app/issues/7" {
			t.Fatalf("closed an already-closed issue or a PR: %v", api.requests)
		}
	}
}

func TestGitHubMergeReviewedInAssistedMode(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	f.session.ExecMode = assistantExecModeForApproval(AssistantApprovalAssisted)
	var described string
	f.session.approvalReviewer = func(_ context.Context, tool, description string, args map[string]any) assistantApprovalReview {
		described = description
		return assistantApprovalReview{Outcome: "deny", RiskLevel: "high", UserAuthorization: "low", Rationale: "未授权合并"}
	}
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"PUT /repos/octo/app/pulls/7/merge": githubJSON(http.StatusOK, `{"merged":true}`),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPRMerge, `{"repository":"app","number":7}`)
	if !isErr || !strings.Contains(text, "帮我批准未执行") || !strings.Contains(described, "合并") {
		t.Fatalf("assisted merge = %s (error=%t) description=%q", text, isErr, described)
	}
	if len(api.requests) != 0 {
		t.Fatalf("merge executed without approval: %v", api.requests)
	}
}

func TestGitHubIssueGetReturnsBodyAndLatestComments(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	var commentPage string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues/9": githubJSON(http.StatusOK, `{"number":9,"title":"multiply","body":"Add multiply and a test_m5.py","state":"open","html_url":"https://github.com/octo/app/issues/9","user":{"login":"alice"},"labels":[{"name":"auto-fix"}],"comments":140}`),
		"GET /repos/octo/app/issues/9/comments": func(w http.ResponseWriter, r *http.Request) {
			commentPage = r.URL.Query().Get("page")
			items := make([]string, 0, 40)
			for i := 0; i < 40; i++ {
				items = append(items, `{"body":"note`+strconv.Itoa(i)+`","user":{"login":"bob"}}`)
			}
			_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
		},
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubIssueGet, `{"repository":"app","number":9}`)
	for _, want := range []string{`"body":"Add multiply and a test_m5.py"`, `"labels":["auto-fix"]`, `"commentCount":140`, `"isPullRequest":false`, `"body":"note10"`, `"body":"note39"`} {
		if isErr || !strings.Contains(text, want) {
			t.Fatalf("issue get missing %s: %.600s (error=%t)", want, text, isErr)
		}
	}
	if strings.Contains(text, `"body":"note9"`) || commentPage != "2" {
		t.Fatalf("issue get should keep the latest %d comments from the last page (page=%s): %.300s", githubIssueMaxComments, commentPage, text)
	}
}

func TestGitHubIssuesListIncludesBodyExcerpt(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues": githubJSON(http.StatusOK, `[{"number":9,"title":"multiply","body":"`+strings.Repeat("a", githubIssueListBodyRunes+50)+`","state":"open"}]`),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app"}`)
	if isErr || !strings.Contains(text, `"body":"`+strings.Repeat("a", githubIssueListBodyRunes)+`…"`) {
		t.Fatalf("issues list = %.200s (error=%t)", text, isErr)
	}
}

func TestGitHubIssueCreateAndPRClose(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /repos/octo/app/issues":            githubJSON(http.StatusCreated, `{"number":21,"html_url":"https://github.com/octo/app/issues/21"}`),
		"GET /repos/octo/app/pulls/7":            githubJSON(http.StatusOK, `{"state":"open","merged":false,"html_url":"https://github.com/octo/app/pull/7"}`),
		"GET /repos/octo/app/pulls/8":            githubJSON(http.StatusOK, `{"state":"closed","merged":true}`),
		"GET /repos/octo/app/pulls/9":            githubJSON(http.StatusOK, `{"state":"closed","merged":false}`),
		"POST /repos/octo/app/issues/7/comments": githubJSON(http.StatusCreated, `{"id":1,"html_url":"https://github.com/octo/app/pull/7#c1"}`),
		"PATCH /repos/octo/app/pulls/7":          githubJSON(http.StatusOK, `{"state":"closed"}`),
	}}
	api.serve(t)
	lastBody := func(request string) string {
		for i := len(api.requests) - 1; i >= 0; i-- {
			if api.requests[i] == request {
				return api.bodies[i]
			}
		}
		return ""
	}
	text, isErr := callGitHubTool(t, f, mainBotGitHubIssueCreate, `{"repository":"app","title":"Bug","body":"steps","labels":["bug"]}`)
	if isErr || !strings.Contains(text, `"number":21`) || !strings.Contains(lastBody("POST /repos/octo/app/issues"), `"labels":["bug"]`) {
		t.Fatalf("issue create = %s body=%s (error=%t)", text, lastBody("POST /repos/octo/app/issues"), isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubIssueCreate, `{"repository":"app","title":" "}`); !isErr {
		t.Fatalf("blank title accepted: %s", text)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRClose, `{"repository":"app","number":7}`); !isErr || !strings.Contains(text, "「请求批准」") {
		t.Fatalf("pr close in confirm mode = %s", text)
	}
	f.session.ExecMode = assistantExecModeForApproval(AssistantApprovalAuto)
	text, isErr = callGitHubTool(t, f, mainBotGitHubPRClose, `{"repository":"app","number":7,"comment":"superseded"}`)
	if isErr || !strings.Contains(text, `"commentUrl":"https://github.com/octo/app/pull/7#c1"`) || lastBody("PATCH /repos/octo/app/pulls/7") != `{"state":"closed"}` {
		t.Fatalf("pr close = %s (error=%t)", text, isErr)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRClose, `{"repository":"app","number":8}`); !isErr || !strings.Contains(text, "已经合并") {
		t.Fatalf("merged pr close = %s", text)
	}
	if text, isErr = callGitHubTool(t, f, mainBotGitHubPRClose, `{"repository":"app","number":9}`); isErr || !strings.Contains(text, `"alreadyClosed":true`) {
		t.Fatalf("closed pr close = %s", text)
	}
}
