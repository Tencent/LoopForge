package tracker

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubFindPullPrefersOpenPullForReusedBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number":1,"state":"closed","merged":true,"head":{"ref":"work"},"base":{"ref":"main"}},
			{"number":2,"state":"open","head":{"ref":"work"},"base":{"ref":"other"}},
			{"number":3,"state":"open","head":{"ref":"work"},"base":{"ref":"main"}}
		]`))
	}))
	defer server.Close()
	client := githubClient{base: server.URL, path: "owner/repo", http: server.Client()}
	got, ok, err := client.findPull("work", "main")
	if err != nil || !ok || got.Number != 3 {
		t.Fatalf("findPull selected historical delivery instead of current PR: %+v, %v, %v", got, ok, err)
	}
}

func TestGitHubFindPullRetainsHistoricalFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"number":1,"state":"closed","merged":true,"head":{"ref":"work"},"base":{"ref":"main"}}]`))
	}))
	defer server.Close()
	client := githubClient{base: server.URL, path: "owner/repo", http: server.Client()}
	got, ok, err := client.findPull("work", "main")
	if err != nil || !ok || got.Number != 1 || !got.merged() {
		t.Fatalf("historical fallback: %+v, %v, %v", got, ok, err)
	}
}
