package tracker

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// GitHub Enterprise PRs and GitLab MRs are refreshed with the registering
// user's credential for their host; rows without one are left unchecked.
func TestSyncRefreshesExternalDeliveriesWithRegisteringUserCredential(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "alice"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("alice")
	for _, c := range []struct{ host, platform, token string }{
		{"ghe.example.com", domain.GitPlatformGitHub, "ghe-token"},
		{"gitlab.example.org", domain.GitPlatformGitLab, "gl-token"},
	} {
		if err := st.SaveGitHTTPCredential(user.ID, "", domain.GitHTTPCredentialSecret{
			GitHTTPCredential: domain.GitHTTPCredential{Name: c.host, Platform: c.platform, Host: c.host},
			Token:             c.token,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct {
		project, url string
		iid          int
		by           int64
	}{
		{"ghe:ghe.example.com/team/svc", "https://ghe.example.com/team/svc/pull/4", 4, user.ID},
		{"gitlab:gitlab.example.org/group/sub/app", "https://gitlab.example.org/group/sub/app/-/merge_requests/3", 3, user.ID},
		{"gitlab:gitlab.example.org/group/other", "https://gitlab.example.org/group/other/-/merge_requests/1", 1, 0},
	}
	for _, r := range rows {
		if err := st.EnsureMRDeliveryState(r.project, r.iid, r.url, "ISSUE-1"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMRDeliveryRegisteredBy(r.project, r.iid, r.by); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Header.Get("Authorization") == "Bearer ghe-token" && r.URL.Path == "/repos/team/svc/pulls/4":
			_, _ = w.Write([]byte(`{"number":4,"state":"closed","merged":true,"merged_at":"2026-11-01T10:00:00Z","base":{"ref":"main"}}`))
		case r.Header.Get("PRIVATE-TOKEN") == "gl-token" && r.URL.EscapedPath() == "/projects/group%2Fsub%2Fapp/merge_requests/3":
			_, _ = w.Write([]byte(`{"iid":3,"state":"opened","target_branch":"develop"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.gitHostAPIBase = srv.URL
	s.SyncMRDeliveryStatesOnce(st)

	states := st.MRDeliveryStatesMap([]domain.MRDeliveryKey{
		{Project: rows[0].project, IID: 4}, {Project: rows[1].project, IID: 3}, {Project: rows[2].project, IID: 1},
	})
	ghe := states[domain.MRDeliveryKey{Project: rows[0].project, IID: 4}]
	if ghe.State != "merged" || ghe.TargetBranch != "main" || ghe.MergedAt.IsZero() {
		t.Fatalf("GHE state = %+v", ghe)
	}
	if gl := states[domain.MRDeliveryKey{Project: rows[1].project, IID: 3}]; gl.State != "opened" || gl.TargetBranch != "develop" || gl.CheckedAt.IsZero() {
		t.Fatalf("GitLab state = %+v", gl)
	}
	if orphan := states[domain.MRDeliveryKey{Project: rows[2].project, IID: 1}]; !orphan.CheckedAt.IsZero() {
		t.Fatalf("row without a registering user was checked: %+v", orphan)
	}
}
