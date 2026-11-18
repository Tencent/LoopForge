package tracker

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/cnb"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

const cnbOwnerToken = "cnb-fake-owner-token"

type cnbFixture struct {
	svc   *Service
	mu    sync.Mutex
	calls []string
}

func (f *cnbFixture) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// newCNBFixture binds cnb:org/team/repo to a project whose owner holds
// cnbOwnerToken (when withToken) and serves api through a local CNB.
func newCNBFixture(t *testing.T, withToken bool, api func(w http.ResponseWriter, r *http.Request)) *cnbFixture {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	owner := domain.User{LoginName: "cnb-owner", GitToken: "ghp_FAKEGLOBALPAT"}
	if withToken {
		owner.CNBToken = cnbOwnerToken
	}
	if err := st.PutUser(owner); err != nil {
		t.Fatal(err)
	}
	if withToken {
		u, _ := st.UserByLoginName("cnb-owner")
		if _, err := st.UpdateUserCredentials(u.ID, repo.UserCredentialUpdate{CNBToken: cnbOwnerToken}); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := st.UserByLoginName("cnb-owner")
	if err := st.PutProject(domain.Project{ID: "cnb-proj", Name: "CNB", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "cnb-repo", ProjectID: "cnb-proj", Provider: "cnb", Name: "repo", GongfengProjectID: "cnb:org/team/repo", DefaultBranch: "main", WebURL: "https://cnb.cool/org/team/repo"}); err != nil {
		t.Fatal(err)
	}
	f := &cnbFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer "+cnbOwnerToken {
			t.Errorf("CNB must be called with the project owner's CNB token, got %q", got)
		}
		api(w, r)
	}))
	t.Cleanup(srv.Close)
	f.svc = NewService()
	f.svc.ConfigureWithStore(domain.SyncConfig{}, st)
	f.svc.cnbAPIBase = srv.URL
	return f
}

const cnbForkAndOwn = `[
 {"number":"9","state":"open","head":{"ref":"refs/heads/feature","repo":{"path":"someone/fork"}},"base":{"ref":"refs/heads/main"}},
 {"number":"7","state":"open","head":{"ref":"refs/heads/feature","sha":"abc","repo":{"path":"org/team/repo"}},"base":{"ref":"refs/heads/main"}}
]`

func TestCNBMergeBranchMergesOwnPullNotTheForks(t *testing.T) {
	var merged []string
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/org/team/repo/-/pulls":
			_, _ = w.Write([]byte(cnbForkAndOwn))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/merge"):
			merged = append(merged, r.URL.Path)
			_, _ = w.Write([]byte(`{"merged":true,"sha":"deadbeef"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ref, err := f.svc.EnsureAndMergeBranchMR("cnb:org/team/repo", "feature", "main", "Title", "")
	if err != nil || ref != "#7" {
		t.Fatalf("merge: %q %v", ref, err)
	}
	if len(merged) != 1 || merged[0] != "/org/team/repo/-/pulls/7/merge" {
		t.Fatalf("merged %v, want only pull 7 (never the fork's 9)", merged)
	}
}

func TestCNBMergeBranchShortCircuitsAnAlreadyMergedPull(t *testing.T) {
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("an already merged pull must not be merged or recreated: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"number":"11","state":"closed","is_merged":true,"head":{"ref":"refs/heads/feature","repo":{"path":"org/team/repo"}},"base":{"ref":"refs/heads/main"}}]`))
	})
	if ref, err := f.svc.EnsureAndMergeBranchMR("cnb:org/team/repo", "feature", "main", "Title", ""); err != nil || ref != "#11" {
		t.Fatalf("got %q %v", ref, err)
	}
}

func TestCNBMergeBranchFailsUnlessCNBConfirmsMerged(t *testing.T) {
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(cnbForkAndOwn))
			return
		}
		_, _ = w.Write([]byte(`{"merged":false,"message":"checks pending"}`))
	})
	if ref, err := f.svc.EnsureAndMergeBranchMR("cnb:org/team/repo", "feature", "main", "Title", ""); err == nil {
		t.Fatalf("HTTP 200 without merged=true was reported as merged: %q", ref)
	}
}

func TestCNBMergeBranchOpensAPullWhenOnlyAForkHasTheBranch(t *testing.T) {
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			_, _ = w.Write([]byte(`[{"number":"9","state":"open","head":{"ref":"refs/heads/feature","repo":{"path":"someone/fork"}},"base":{"ref":"refs/heads/main"}}]`))
		case r.Method == "POST" && r.URL.Path == "/org/team/repo/-/pulls":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":"21","state":"open","base":{"ref":"refs/heads/main"},"head":{"ref":"refs/heads/feature"}}`))
		case r.Method == "PUT" && r.URL.Path == "/org/team/repo/-/pulls/21/merge":
			_, _ = w.Write([]byte(`{"merged":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	if ref, err := f.svc.EnsureAndMergeBranchMR("cnb:org/team/repo", "feature", "main", "", ""); err != nil || ref != "#21" {
		t.Fatalf("got %q %v", ref, err)
	}
	if f.called("PUT /org/team/repo/-/pulls/9/") != 0 {
		t.Fatal("the fork's pull request was merged")
	}
}

func TestCNBRefreshDeliveryStoresStateAndCNBMergeTime(t *testing.T) {
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/team/repo/-/pulls/7" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"number":"7","state":"closed","is_merged":true,"closed_at":"2026-11-13T04:00:00Z","head":{"ref":"refs/heads/feature"},"base":{"ref":"refs/heads/main"}}`))
	})
	store := &fakeMRDeliveryStore{}
	if err := f.svc.refreshHostedDelivery(store, "cnb:org/team/repo", 7); err != nil {
		t.Fatal(err)
	}
	if len(store.updates) != 1 {
		t.Fatalf("updates = %+v", store.updates)
	}
	u := store.updates[0]
	if u.project != "cnb:org/team/repo" || u.iid != 7 || u.state != "merged" || u.targetBranch != "main" || !u.mergedAt.Equal(time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("update = %+v", u)
	}
}

func TestCNBFindBranchMRBuildsTheCNBPullURL(t *testing.T) {
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(cnbForkAndOwn)) })
	mr, ok, err := f.svc.FindBranchMR("cnb:org/team/repo", "feature")
	if err != nil || !ok || mr.IID != 7 || mr.URL != "https://cnb.cool/org/team/repo/-/pulls/7" || mr.State != "opened" {
		t.Fatalf("got %+v ok=%v err=%v", mr, ok, err)
	}
}

func TestCNBHostNeedsTheOwnersCNBToken(t *testing.T) {
	f := newCNBFixture(t, false, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may leave without a CNB token: %s %s", r.Method, r.URL.Path)
	})
	_, err := f.svc.EnsureAndMergeBranchMR("cnb:org/team/repo", "feature", "main", "T", "")
	if err == nil || !strings.Contains(err.Error(), "CNB") {
		t.Fatalf("want an error naming CNB, got %v", err)
	}
}

func TestCNBMergedPullWithoutUsableClosedAtStillGetsAMergeTime(t *testing.T) {
	for _, body := range []string{
		`{"number":"7","state":"closed","is_merged":true,"head":{"ref":"refs/heads/f"},"base":{"ref":"refs/heads/main"}}`,
		`{"number":"7","state":"closed","is_merged":true,"closed_at":"not-a-time","head":{"ref":"refs/heads/f"},"base":{"ref":"refs/heads/main"}}`,
	} {
		f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		store := &fakeMRDeliveryStore{}
		before := time.Now().UTC()
		if err := f.svc.refreshHostedDelivery(store, "cnb:org/team/repo", 7); err != nil {
			t.Fatal(err)
		}
		if u := store.updates[0]; u.state != "merged" || u.mergedAt.Before(before) {
			t.Fatalf("body %s: update = %+v", body, u)
		}
	}
}

func TestSyncMRDeliveryStatesRefreshesCNBRow(t *testing.T) {
	f := newCNBFixture(t, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":"7","state":"closed","is_merged":true,"closed_at":"2026-11-13T04:00:00Z","head":{"ref":"refs/heads/feature"},"base":{"ref":"refs/heads/main"}}`))
	})
	store := &fakeMRDeliveryStore{pending: []domain.MRDeliveryState{{MRProject: "cnb:org/team/repo", MRIID: 7, State: "opened"}}}
	f.svc.syncMRDeliveryStates(store)
	if len(store.updates) != 1 {
		t.Fatalf("updates = %+v", store.updates)
	}
	u := store.updates[0]
	if u.project != "cnb:org/team/repo" || u.iid != 7 || u.state != "merged" || !u.mergedAt.Equal(time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("update = %+v", u)
	}
}

func TestSyncMRDeliveryStatesLogsWhenCNBOwnerHasNoToken(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := newCNBFixture(t, false, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may leave without a CNB token: %s %s", r.Method, r.URL.Path)
	})
	store := &fakeMRDeliveryStore{pending: []domain.MRDeliveryState{{MRProject: "cnb:org/team/repo", MRIID: 7, State: "opened"}}}
	f.svc.syncMRDeliveryStates(store)
	if len(store.updates) != 0 || !strings.Contains(logs.String(), "CNB") {
		t.Fatalf("updates = %+v, log = %q", store.updates, logs.String())
	}
}

func TestHostedFromCNBNormalizesAndDoesNotDefaultUnknownToOpen(t *testing.T) {
	mergedAt := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		in        cnb.Pull
		wantState string
		wantHead  string
	}{
		{"open", cnb.Pull{Number: 1, State: "open", TargetBranch: "main", HeadSHA: " sha1 "}, "opened", "sha1"},
		{"opened", cnb.Pull{Number: 2, State: "opened", TargetBranch: "main", HeadSHA: "sha2"}, "opened", "sha2"},
		{"merged", cnb.Pull{Number: 3, State: "merged", TargetBranch: "main", HeadSHA: "sha3", ClosedAt: mergedAt}, "merged", "sha3"},
		{"closed", cnb.Pull{Number: 4, State: "closed", TargetBranch: "main", HeadSHA: "sha4"}, "closed", "sha4"},
		{"unknown string", cnb.Pull{Number: 5, State: "mystery", TargetBranch: "main", HeadSHA: "sha5"}, "unknown", "sha5"},
		{"empty state", cnb.Pull{Number: 6, State: "", TargetBranch: "main", HeadSHA: "sha6"}, "unknown", "sha6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hp := hostedFromCNB(tc.in)
			if hp.State != tc.wantState {
				t.Fatalf("hostedFromCNB state=%q want %q", hp.State, tc.wantState)
			}
			if tc.wantState == "merged" && !hp.MergedAt.Equal(mergedAt) {
				t.Fatalf("mergedAt = %v, want %v", hp.MergedAt, mergedAt)
			}
			mr := hp.delivery("cnb:org/team/repo")
			if mr.State != tc.wantState || mr.GongfengState != tc.wantState || mr.HeadSHA != tc.wantHead {
				t.Fatalf("delivery = %+v", mr)
			}
		})
	}
}

func TestCNBHostForRunTokenPrecedenceAndErrors(t *testing.T) {
	s := NewService()
	s.cnbAPIBase = "https://cnb.example.test"
	s.projectAuth = projectAuthFixture{
		projects: map[string]domain.Project{
			"first":    {ID: "first", OwnerID: 1},
			"run":      {ID: "run", OwnerID: 2},
			"no-token": {ID: "no-token", OwnerID: 3},
		},
		users: map[int64]domain.User{
			1:  {ID: 1, CNBToken: "first-owner-cnb"},
			2:  {ID: 2, CNBToken: "run-owner-cnb"},
			3:  {ID: 3, CNBToken: ""},
			10: {ID: 10, CNBToken: "resp-cnb", GitToken: "ghp_resp"},
			11: {ID: 11, CNBToken: "", GitToken: "ghp_no_cnb"},
		},
		repos: []domain.Repository{
			{ID: "r1", ProjectID: "first", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
			{ID: "r2", ProjectID: "run", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
			{ID: "r3", ProjectID: "no-token", Provider: "cnb", GongfengProjectID: "cnb:group/sub/repo"},
		},
	}

	h, id, err := s.cnbHostForRun("cnb:group/sub/repo", domain.BranchMRQuery{ProjectID: "run", ResponsibleUserID: 10, GitToken: "ghp_MUST_IGNORE"})
	if err != nil || h.client.Token != "resp-cnb" || h.slug != "group/sub/repo" || id != "本轮责任人的 CNB 访问令牌" {
		t.Fatalf("1st priority failed: h=%+v id=%q err=%v", h, id, err)
	}

	h, id, err = s.cnbHostForRun("cnb:group/sub/repo", domain.BranchMRQuery{ProjectID: "run", ResponsibleUserID: 11, GitToken: "ghp_MUST_IGNORE"})
	if err != nil || h.client.Token != "run-owner-cnb" || id != "项目 run Owner 的 CNB 访问令牌" {
		t.Fatalf("2nd priority failed: h=%+v id=%q err=%v", h, id, err)
	}

	if h, id, err = s.cnbHostForRun("cnb:group/sub/repo", domain.BranchMRQuery{ProjectID: "no-token", ResponsibleUserID: 11, GitToken: "ghp_MUST_IGNORE"}); err == nil || !strings.Contains(err.Error(), "no-token") || !strings.Contains(err.Error(), "CNB 访问令牌") {
		t.Fatalf("bound project without CNB token must fail instead of falling back to another project's owner: h=%+v id=%q err=%v", h, id, err)
	}

	h, id, err = s.cnbHostForRun("cnb:group/sub/repo", domain.BranchMRQuery{ProjectID: "", ResponsibleUserID: 0, GitToken: "ghp_MUST_IGNORE"})
	if err != nil || h.client.Token != "first-owner-cnb" || id != "首个绑定该仓库的项目 Owner 的 CNB 访问令牌" {
		t.Fatalf("3rd priority failed: h=%+v id=%q err=%v", h, id, err)
	}

	h, id, err = s.cnbHostForRun("cnb:group/sub/repo", domain.BranchMRQuery{ProjectID: "unbound", ResponsibleUserID: 0})
	if err != nil || h.client.Token != "first-owner-cnb" || id != "首个绑定该仓库的项目 Owner 的 CNB 访问令牌" {
		t.Fatalf("3rd priority for unbound project failed: h=%+v id=%q err=%v", h, id, err)
	}
}
