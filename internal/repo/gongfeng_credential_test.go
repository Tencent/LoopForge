package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestGongfengOAuthSurvivesLoginAndRefreshes(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "example-owner"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("example-owner")
	expires := time.Now().Add(time.Minute).UTC()
	if _, err := st.SaveGongfengOAuth(user.ID, "access-old", "refresh-old", expires); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{ID: user.ID, LoginName: "example-owner", ChineseName: "刷新"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := st.User(user.ID)
	if !ok || stored.GitToken != "access-old" || stored.GongfengRefreshToken != "refresh-old" {
		t.Fatalf("oauth grant did not survive login refresh: %+v", stored)
	}
	public := domain.PublicUser(stored)
	if !public.GitTokenOAuth || !public.GitTokenConfigured || public.GitToken != "" || public.GongfengRefreshToken != "" {
		t.Fatalf("public user = %+v", public)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"access-new","refresh_token":"refresh-new","expires_in":7200}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "app")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "sec")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "http://127.0.0.1/cb")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_TOKEN_URL", srv.URL)
	if got := st.GitCredential(context.Background(), user.ID); got != "oauth2:access-new" {
		t.Fatalf("refreshed token = %q", got)
	}
	stored, _ = st.User(user.ID)
	if stored.GitToken != "access-new" || stored.GongfengRefreshToken != "refresh-new" {
		t.Fatalf("saved refresh = %+v", stored)
	}

	if _, err := st.UpdateUserCredentials(user.ID, UserCredentialUpdate{GitToken: "pasted-pat"}); err != nil {
		t.Fatal(err)
	}
	stored, _ = st.User(user.ID)
	if stored.GitToken != "pasted-pat" || stored.GongfengRefreshToken != "" || !stored.GongfengTokenExpiresAt.IsZero() {
		t.Fatalf("paste should replace oauth grant: %+v", stored)
	}
	if got := st.GitCredential(context.Background(), user.ID); got != "pasted-pat" {
		t.Fatalf("pat = %q", got)
	}
}

// A Gongfeng token endpoint that never answers must not wedge every git
// operation of the user: the refresh gives up, keeps the current token and
// backs off, and callers queued on the user's refresh lock are released.
func TestGongfengRefreshGivesUpOnHangingEndpoint(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "slow-oauth"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("slow-oauth")
	t.Cleanup(func() { gongfengRefreshGates.Delete(user.ID) })
	if _, err := st.SaveGongfengOAuth(user.ID, "access-old", "refresh-old", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "app")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "sec")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "http://127.0.0.1/cb")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_TOKEN_URL", srv.URL)
	prev := gongfengRefreshTimeout
	gongfengRefreshTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gongfengRefreshTimeout = prev })

	start := time.Now()
	got := make([]string, 3)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = st.GitCredential(context.Background(), user.ID)
		}(i)
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("callers waited %s on a hanging refresh", elapsed)
	}
	for _, token := range got {
		if token != "oauth2:access-old" {
			t.Fatalf("caller got %q, want the current token", token)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("refresh attempts = %d, want one followed by the back-off", n)
	}
}
