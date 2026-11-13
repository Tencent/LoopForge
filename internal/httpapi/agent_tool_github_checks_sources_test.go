package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each source reports its own read problem and keeps the other source's
// checks: an unparsable body or a truncated list is a warning, never a silent
// "success".
func TestGitHubCombinedChecksWarnsOnUnparsableAndTruncatedSources(t *testing.T) {
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/commits/sha/status":     githubJSON(http.StatusOK, `not json`),
		"GET /repos/octo/app/commits/sha/check-runs": githubJSON(http.StatusOK, `{"total_count":1,"check_runs":[{"name":"b","status":"completed","conclusion":"success"}]}`),
	}}
	api.serve(t)
	checks, state, warnings := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha")
	if state != "unknown" || len(checks) != 1 || checks[0].Name != "b" || len(warnings) != 1 || !strings.Contains(warnings[0], "commit status 返回无法解析") {
		t.Fatalf("bad status body: state=%q checks=%v warnings=%v", state, checks, warnings)
	}

	api = &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/commits/sha/status":     githubJSON(http.StatusOK, `{"total_count":120,"statuses":[{"context":"a","state":"success"}]}`),
		"GET /repos/octo/app/commits/sha/check-runs": githubJSON(http.StatusOK, `{"check_runs":`),
	}}
	api.serve(t)
	checks, state, warnings = githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha")
	joined := strings.Join(warnings, "\n")
	if state != "unknown" || len(checks) != 1 || checks[0].Name != "a" || len(warnings) != 2 ||
		!strings.Contains(joined, "commit status 共 120 项，只统计了前 1 项") || !strings.Contains(joined, "check runs 返回无法解析") {
		t.Fatalf("truncated status + bad runs body: state=%q checks=%v warnings=%v", state, checks, warnings)
	}
}

// A transport failure on either API is reported per source.
func TestGitHubCombinedChecksReportsTransportErrors(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	previous := githubAPIBase
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = previous })
	checks, state, warnings := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha")
	joined := strings.Join(warnings, "\n")
	if state != "unknown" || len(checks) != 0 || len(warnings) != 2 ||
		!strings.Contains(joined, "commit status 读取失败") || !strings.Contains(joined, "check runs 读取失败") {
		t.Fatalf("transport errors: state=%q checks=%v warnings=%v", state, checks, warnings)
	}
}
