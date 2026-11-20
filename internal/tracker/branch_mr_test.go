package tracker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPickBranchMR(t *testing.T) {
	raw := `{"data":[
		{"id":101,"iid":7,"state":"closed","source_branch":"feature/x","target_branch":"master"},
		{"id":102,"iid":9,"state":"merged","source_branch":"feature/x","target_branch":"integrate"},
		{"id":103,"iid":11,"state":"opened","source_branch":"feature/other","target_branch":"master"},
		{"id":104,"iid":8,"state":"opened","source_branch":"feature/x","target_branch":"master"}
	]}`
	got, ok := pickBranchMR(parseGongfengMRRecords(raw), "feature/x")
	if !ok || got.IID != 9 {
		t.Fatalf("want newest non-closed MR of the branch (!9), got %+v ok=%v", got, ok)
	}
	closedOnly := `[{"id":1,"iid":3,"state":"closed","source_branch":"feature/x"},{"id":2,"iid":5,"state":"closed","source_branch":"feature/x"}]`
	if got, ok := pickBranchMR(parseGongfengMRRecords(closedOnly), "feature/x"); !ok || got.IID != 5 {
		t.Fatalf("want newest closed MR when nothing is live, got %+v ok=%v", got, ok)
	}
	if _, ok := pickBranchMR(parseGongfengMRRecords(`{"data":[]}`), "feature/x"); ok {
		t.Fatal("empty search must not resolve an MR")
	}
}

func TestBranchMRsIntoKeepOnlyTheTargetAndCarryHeads(t *testing.T) {
	raw := `{"data":[
		{"id":101,"iid":7,"state":"merged","source_branch":"feature/x","target_branch":"master","sha":"old","web_url":"https://git.example.com/g/r/merge_requests/7"},
		{"id":102,"iid":9,"state":"opened","source_branch":"feature/x","target_branch":"release","sha":"new"},
		{"id":103,"iid":11,"state":"opened","source_branch":"feature/other","target_branch":"master"},
		{"id":104,"iid":12,"state":"opened","target_branch":"master","sha":"tip"}
	]}`
	got := gongfengMRsInto(parseGongfengMRRecords(raw), "g/r", "feature/x", "master")
	if len(got) != 2 || got[0].IID != 7 || got[0].HeadSHA != "old" || got[0].GongfengState != "merged" || got[1].IID != 12 || got[1].HeadSHA != "tip" {
		t.Fatalf("gongfeng MRs into master = %+v", got)
	}
	if got := gongfengMRsInto(parseGongfengMRRecords(raw), "g/r", "feature/x", "main"); len(got) != 0 {
		t.Fatalf("no MR targets main, got %+v", got)
	}

	var pulls []githubPull
	for _, p := range []struct{ head, sha, base, state string }{{"feat", "old", "main", "closed"}, {"feat", "new", "release", "open"}, {"other", "x", "main", "open"}, {"feat", "tip", "main", "open"}} {
		var pull githubPull
		pull.Number, pull.State = len(pulls)+1, p.state
		pull.Head.Ref, pull.Head.SHA, pull.Base.Ref = p.head, p.sha, p.base
		pulls = append(pulls, pull)
	}
	prs := githubPullsInto(pulls, "github:o/r", "feat", "main")
	if len(prs) != 2 || prs[0].State != "closed" || prs[0].HeadSHA != "old" || prs[1].State != "opened" || prs[1].HeadSHA != "tip" || prs[1].TargetBranch != "main" {
		t.Fatalf("github pulls into main = %+v", prs)
	}
}

func TestFindBranchMRsIntoReadsWithTheRunCredentials(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer revoked" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/repos/o/r/pulls" || r.URL.Query().Get("base") != "main" || r.URL.Query().Get("head") != "o:feat" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(`[{"number":4,"state":"open","head":{"ref":"feat","sha":"tip"},"base":{"ref":"main"}},{"number":3,"state":"closed","merged_at":"2026-11-01T00:00:00Z","head":{"ref":"feat","sha":"old"},"base":{"ref":"main"}}]`))
	}))
	defer srv.Close()
	s := NewService()
	s.githubAPIBase = srv.URL
	s.projectAuth = projectAuthFixture{
		credentials: map[int64]map[string]domain.GitHTTPCredentialSecret{1: {"github.com": {Token: "first-owner"}}, 2: {"github.com": {Token: "run-owner"}}},
		projects:    map[string]domain.Project{"first": {ID: "first", OwnerID: 1}, "run": {ID: "run", OwnerID: 2}},
		users:       map[int64]domain.User{1: {ID: 1, GitToken: "first-owner"}, 2: {ID: 2, GitToken: "run-owner"}},
		repos: []domain.Repository{
			{ID: "a", ProjectID: "first", Provider: "github", GongfengProjectID: "github:o/r"},
			{ID: "b", ProjectID: "run", Provider: "github", GongfengProjectID: "github:o/r"},
		},
	}
	q := domain.BranchMRQuery{ProjectID: "run", RemoteProject: "github:o/r", SourceBranch: "feat", TargetBranch: "main", GitToken: "pushed-with"}
	mrs, err := s.FindBranchMRsInto(q)
	if err != nil || len(mrs) != 2 || mrs[0].State != "opened" || mrs[0].HeadSHA != "tip" || mrs[1].State != "merged" || mrs[1].HeadSHA != "old" {
		t.Fatalf("mrs = %+v %v", mrs, err)
	}
	q.GitToken = ""
	if _, err := s.FindBranchMRsInto(q); err != nil {
		t.Fatal(err)
	}
	if strings.Join(auth, ",") != "Bearer pushed-with,Bearer run-owner" {
		t.Fatalf("the run's push token comes first, then the Owner of the run's project, got %v", auth)
	}
	q.GitToken = "revoked"
	if _, err := s.FindBranchMRsInto(q); err == nil || !strings.Contains(err.Error(), "本轮责任人的 Git Token") {
		t.Fatalf("a rejected lookup names the identity it used: %v", err)
	}
	gf := domain.BranchMRQuery{ProjectID: "run", RemoteProject: "group/r", SourceBranch: "feat", TargetBranch: "main", ResponsibleUserID: 9}
	if _, err := s.FindBranchMRsInto(gf); err == nil || !strings.Contains(err.Error(), "本轮责任人 9") {
		t.Fatalf("Gongfeng lookups run as the run's responsible user: %v", err)
	}
}
