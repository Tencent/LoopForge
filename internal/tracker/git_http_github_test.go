package tracker

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestGitHubTrackerUsesRegisteringUsersHostCredential(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "github-speaker"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("github-speaker")
	if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitHub", Platform: domain.GitPlatformGitHub, Host: "github.com"},
		Token:             "fake-github-http-credential",
	}); err != nil {
		t.Fatal(err)
	}
	project := "github:example/private-test"
	if err := st.EnsureMRDeliveryState(project, 5, "https://github.com/example/private-test/pull/5", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMRDeliveryRegisteredBy(project, 5, u.ID); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-github-http-credential" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":5,"state":"open","base":{"ref":"main"}}`))
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.githubAPIBase = srv.URL
	s.SyncMRDeliveryStatesOnce(st)
	state := st.MRDeliveryStatesMap([]domain.MRDeliveryKey{{Project: project, IID: 5}})[domain.MRDeliveryKey{Project: project, IID: 5}]
	if state.State != "opened" {
		t.Fatalf("new per-host credential was ignored: state=%s, registered_by=%d", state.State, state.RegisteredBy)
	}
}
