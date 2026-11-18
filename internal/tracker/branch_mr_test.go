package tracker

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		projects: map[string]domain.Project{"first": {ID: "first", OwnerID: 1}, "run": {ID: "run", OwnerID: 2}},
		users:    map[int64]domain.User{1: {ID: 1, GitToken: "first-owner"}, 2: {ID: 2, GitToken: "run-owner"}},
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

func TestFindBranchMRsIntoCNBUsesRunCredentialsAndCollectsAllPRs(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	var methods []string
	var gfCalls atomic.Int32

	gfTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gfCalls.Add(1)
		t.Errorf("Gongfeng must never be queried for cnb: projects: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gfTrap.Close()

	cnbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("FindBranchMRsInto must not send write requests to CNB: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/group/sub/repo/-/pulls" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("base_ref") != "main" {
			t.Errorf("expected base_ref=main, got %q", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`[
				{"number":"9","state":"open","head":{"ref":"refs/heads/feat","sha":"fork-tip","repo":{"path":"someone/fork"}},"base":{"ref":"refs/heads/main"}},
				{"number":"10","state":"open","head":{"ref":"refs/heads/feat","sha":"other-base","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/release"}},
				{"number":"3","state":"closed","is_merged":true,"closed_at":"2026-11-01T00:00:00Z","head":{"ref":"refs/heads/feat","sha":"old-sha","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}}
			]`))
		case "2":
			_, _ = w.Write([]byte(`[
				{"number":"5","state":"closed","closed_at":"2026-11-02T00:00:00Z","head":{"ref":"refs/heads/feat","sha":"stale-sha","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}},
				{"number":"7","state":"open","head":{"ref":"refs/heads/feat","sha":"tip-sha","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}}
			]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer cnbSrv.Close()

	s := NewService()
	s.cnbAPIBase = cnbSrv.URL
	s.gongfeng = NewMCPClient(gfTrap.URL, nil)
	s.projectAuth = projectAuthFixture{
		projects: map[string]domain.Project{
			"first":        {ID: "first", OwnerID: 1},
			"run":          {ID: "run", OwnerID: 2},
			"run-no-token": {ID: "run-no-token", OwnerID: 3},
		},
		users: map[int64]domain.User{
			1:  {ID: 1, CNBToken: "cnb-first-owner", GitToken: "ghp_first"},
			2:  {ID: 2, CNBToken: "cnb-run-owner", GitToken: "ghp_run"},
			3:  {ID: 3, CNBToken: "", GitToken: "ghp_no_cnb"},
			10: {ID: 10, CNBToken: "cnb-resp-user", GitToken: "ghp_resp"},
			11: {ID: 11, CNBToken: "", GitToken: "ghp_resp_no_cnb"},
		},
		repos: []domain.Repository{
			{ID: "r1", ProjectID: "first", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
			{ID: "r2", ProjectID: "run", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
			{ID: "r3", ProjectID: "run-no-token", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
		},
	}

	mrs, err := s.FindBranchMRsInto(domain.BranchMRQuery{
		ProjectID:         "run",
		RemoteProject:     "cnb:group/sub/repo",
		SourceBranch:      "feat",
		TargetBranch:      "main",
		GitToken:          "ghp_MUST_NEVER_BE_SENT",
		ResponsibleUserID: 10,
	})
	if err != nil {
		t.Fatalf("FindBranchMRsInto err=%v", err)
	}
	if len(mrs) != 3 {
		t.Fatalf("want 3 matching MRs across pages, got %+v", mrs)
	}
	if mrs[0].IID != 3 || mrs[0].State != "merged" || mrs[0].HeadSHA != "old-sha" ||
		mrs[1].IID != 5 || mrs[1].State != "closed" || mrs[1].HeadSHA != "stale-sha" ||
		mrs[2].IID != 7 || mrs[2].State != "opened" || mrs[2].HeadSHA != "tip-sha" ||
		mrs[2].URL != "https://cnb.cool/group/sub/repo/-/pulls/7" {
		t.Fatalf("unexpected MRs: %+v", mrs)
	}

	if _, err := s.FindBranchMRsInto(domain.BranchMRQuery{
		ProjectID:         "run",
		RemoteProject:     "cnb:group/sub/repo",
		SourceBranch:      "feat",
		TargetBranch:      "main",
		GitToken:          "ghp_MUST_NEVER_BE_SENT",
		ResponsibleUserID: 11,
	}); err != nil {
		t.Fatalf("fallback to run project Owner failed: %v", err)
	}

	beforeCalls := len(auths)
	if _, err := s.FindBranchMRsInto(domain.BranchMRQuery{
		ProjectID:         "run-no-token",
		RemoteProject:     "cnb:group/sub/repo",
		SourceBranch:      "feat",
		TargetBranch:      "main",
		GitToken:          "ghp_MUST_NEVER_BE_SENT",
		ResponsibleUserID: 11,
	}); err == nil || !strings.Contains(err.Error(), "run-no-token") || !strings.Contains(err.Error(), "CNB 访问令牌") {
		t.Fatalf("bound project without owner CNB token must fail without crossing to another project: %v", err)
	}
	if len(auths) != beforeCalls {
		t.Fatalf("missing CNB token must fail before making any CNB HTTP call, got %v", auths[beforeCalls:])
	}

	for i, auth := range auths {
		if strings.Contains(auth, "ghp_") {
			t.Fatalf("GitToken leaked to CNB at request %d: %q", i, auth)
		}
		if i < 3 && auth != "Bearer cnb-resp-user" {
			t.Fatalf("first query must use responsible user CNB token, got %q", auth)
		}
		if i >= 3 && auth != "Bearer cnb-run-owner" {
			t.Fatalf("second query must use project run Owner CNB token, got %q", auth)
		}
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Fatalf("unexpected non-GET method %s", m)
		}
	}
	if gfCalls.Load() != 0 {
		t.Fatalf("Gongfeng received %d calls, want 0", gfCalls.Load())
	}
}

func TestFindBranchMRsIntoCNBErrorsOnAuthTimeoutTruncatedOrUnknownState(t *testing.T) {
	newServiceForServer := func(srvURL string) *Service {
		s := NewService()
		s.cnbAPIBase = srvURL
		s.projectAuth = projectAuthFixture{
			projects: map[string]domain.Project{"run": {ID: "run", OwnerID: 2}},
			users: map[int64]domain.User{
				2:  {ID: 2, CNBToken: "cnb-run-owner"},
				10: {ID: 10, CNBToken: "cnb-resp-user"},
			},
			repos: []domain.Repository{
				{ID: "r2", ProjectID: "run", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
			},
		}
		return s
	}

	for _, tc := range []struct {
		name     string
		respUser int64
		wantID   string
		handler  http.HandlerFunc
	}{
		{
			name:     "401 unauthorized",
			respUser: 10,
			wantID:   "本轮责任人的 CNB 访问令牌",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errmsg":"unauthorized"}`))
			},
		},
		{
			name:     "403 forbidden",
			respUser: 0,
			wantID:   "项目 run Owner 的 CNB 访问令牌",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errmsg":"forbidden"}`))
			},
		},
		{
			name:     "unknown PR state",
			respUser: 10,
			wantID:   "本轮责任人的 CNB 访问令牌",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "1" {
					_, _ = w.Write([]byte(`[{"number":"7","state":"mystery","head":{"ref":"refs/heads/feat","sha":"tip","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}}]`))
					return
				}
				_, _ = w.Write([]byte(`[]`))
			},
		},
		{
			name:     "truncated pagination",
			respUser: 10,
			wantID:   "本轮责任人的 CNB 访问令牌",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`[{"number":"` + strconv.Itoa(1) + `","state":"open","head":{"ref":"refs/heads/other","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}}]`))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			s := newServiceForServer(srv.URL)
			mrs, err := s.FindBranchMRsInto(domain.BranchMRQuery{
				ProjectID:         "run",
				RemoteProject:     "cnb:group/sub/repo",
				SourceBranch:      "feat",
				TargetBranch:      "main",
				ResponsibleUserID: tc.respUser,
			})
			if err == nil || mrs != nil || !strings.Contains(err.Error(), tc.wantID) {
				t.Fatalf("want error naming %q (never nil, nil), got mrs=%+v err=%v", tc.wantID, mrs, err)
			}
		})
	}

	prevTimeout := cnbCallTimeout
	cnbCallTimeout = 40 * time.Millisecond
	t.Cleanup(func() { cnbCallTimeout = prevTimeout })

	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slowSrv.Close()
	s := newServiceForServer(slowSrv.URL)
	if mrs, err := s.FindBranchMRsInto(domain.BranchMRQuery{
		ProjectID:         "run",
		RemoteProject:     "cnb:group/sub/repo",
		SourceBranch:      "feat",
		TargetBranch:      "main",
		ResponsibleUserID: 10,
	}); err == nil || mrs != nil || !strings.Contains(err.Error(), "本轮责任人的 CNB 访问令牌") {
		t.Fatalf("timeout must return explicit error, got mrs=%+v err=%v", mrs, err)
	}
}
