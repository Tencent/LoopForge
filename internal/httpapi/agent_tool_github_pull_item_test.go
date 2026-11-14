package httpapi

import (
	"encoding/json"
	"testing"
)

func TestGitHubPullItemOut(t *testing.T) {
	var it githubPullItem
	raw := `{"number":7,"title":"Feature","state":"open","draft":true,"html_url":"https://github.com/octo/app/pull/7","user":{"login":"bob"},"head":{"ref":"topic"},"base":{"ref":"main"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
	if err := json.Unmarshal([]byte(raw), &it); err != nil {
		t.Fatal(err)
	}
	want := githubPullOut{Number: 7, Title: "Feature", State: "open", Draft: true, URL: "https://github.com/octo/app/pull/7", Author: "bob", Head: "topic", Base: "main", Created: "2026-01-01T00:00:00Z", Updated: "2026-01-02T00:00:00Z"}
	if got := it.out(); got != want {
		t.Fatalf("out() = %+v, want %+v", got, want)
	}
}
