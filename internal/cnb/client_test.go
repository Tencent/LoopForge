package cnb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const fakeToken = "cnb-fake-token-0000"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(fakeToken)
	c.BaseURL = srv.URL
	return c
}

func TestVerifyToken(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		wantLogin string
		wantErr   bool
	}{
		{http.StatusOK, `{"username":"alice"}`, "alice", false},
		{http.StatusForbidden, `{"errcode":1,"errmsg":"forbidden"}`, "", false},
		{http.StatusUnauthorized, `{"errcode":1,"errmsg":"invalid"}`, "", true},
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+fakeToken || r.Header.Get("Accept") != "application/vnd.cnb.api+json" {
				t.Errorf("unexpected request %s auth=%q accept=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept"))
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		login, err := c.VerifyToken(context.Background())
		if login != tc.wantLogin || (err != nil) != tc.wantErr {
			t.Errorf("status %d: login=%q err=%v", tc.status, login, err)
		}
	}
}

func TestDefaultBranchUsesMultiSegmentSlug(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/team/repo/-/git/head" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"main"}`))
	})
	if got, err := c.DefaultBranch(context.Background(), "org/team/repo"); err != nil || got != "main" {
		t.Fatalf("got %q %v", got, err)
	}
}

const forkAndOwnPulls = `[
 {"number":"9","state":"open","head":{"ref":"refs/heads/feature","repo":{"path":"someone/fork"}},"base":{"ref":"refs/heads/main"}},
 {"number":"7","state":"open","title":"mine","head":{"ref":"refs/heads/feature","sha":"abc","repo":{"path":"org/repo"}},"base":{"ref":"refs/heads/main"}},
 {"number":"3","state":"closed","is_merged":true,"closed_at":"2026-01-02T03:04:05Z","head":{"ref":"refs/heads/feature","repo":{"path":"org/repo"}},"base":{"ref":"refs/heads/main"}}
]`

func TestFindPullIgnoresForkBranchOfTheSameName(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/repo/-/pulls" || r.URL.Query().Get("state") != "all" || r.URL.Query().Get("base_ref") != "main" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		_, _ = w.Write([]byte(forkAndOwnPulls))
	})
	p, ok, err := c.FindPull(context.Background(), "org/repo", "feature", "main")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.Number != 7 || p.State != "open" || p.HeadSHA != "abc" || p.SourceBranch != "feature" || p.TargetBranch != "main" {
		t.Fatalf("matched the wrong pull request: %+v", p)
	}
	if _, ok, _ := c.FindPull(context.Background(), "org/repo", "other", "main"); ok {
		t.Fatal("an unrelated branch matched")
	}
}

func pullItem(number int, branch string) string {
	return `{"number":"` + strconv.Itoa(number) + `","state":"open","head":{"ref":"refs/heads/` + branch + `","repo":{"path":"org/repo"}},"base":{"ref":"refs/heads/main"}}`
}

// CNB may cap page_size below ours, so a short page is not the last page.
func TestFindPullKeepsPagingPastAShortPage(t *testing.T) {
	var pages []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		switch page {
		case "1":
			_, _ = w.Write([]byte("[" + pullItem(1, "a") + "," + pullItem(2, "b") + "]"))
		case "2":
			_, _ = w.Write([]byte("[" + pullItem(3, "wanted") + "]"))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	})
	p, ok, err := c.FindPull(context.Background(), "org/repo", "wanted", "main")
	if err != nil || !ok || p.Number != 3 {
		t.Fatalf("p=%+v ok=%v err=%v pages=%v", p, ok, err, pages)
	}
	if _, ok, _ := c.FindPull(context.Background(), "org/repo", "absent", "main"); ok {
		t.Fatal("an absent branch matched")
	}
	if last := pages[len(pages)-1]; last != "3" {
		t.Fatalf("the search must stop at the first empty page, pages=%v", pages)
	}
}

func TestFindPullOnlyForkBranchDoesNotMatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"number":"9","state":"open","head":{"ref":"refs/heads/feature","repo":{"path":"someone/fork"}},"base":{"ref":"refs/heads/main"}}]`))
	})
	if _, ok, err := c.FindPull(context.Background(), "org/repo", "feature", ""); ok || err != nil {
		t.Fatalf("a fork's pull request matched: ok=%v err=%v", ok, err)
	}
}

func TestPullNumberIsAStringAndMergedStateIsNormalised(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":"12","state":"closed","is_merged":true,"closed_at":"2026-01-02T03:04:05Z","head":{"ref":"refs/heads/f"},"base":{"ref":"refs/heads/main"}}`))
	})
	p, err := c.GetPull(context.Background(), "org/repo", 12)
	if err != nil || p.Number != 12 || p.State != "merged" || p.ClosedAt.IsZero() || p.TargetBranch != "main" {
		t.Fatalf("pull = %+v err=%v", p, err)
	}
	bad := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"number":12}`)) })
	if _, err := bad.GetPull(context.Background(), "org/repo", 12); err == nil {
		t.Fatal("a numeric number must be rejected, CNB sends a string")
	}
}

func TestCreateAndUpdatePull(t *testing.T) {
	var sawCreate, sawUpdate bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/org/repo/-/pulls":
			sawCreate = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":"5","state":"open","base":{"ref":"refs/heads/main"},"head":{"ref":"refs/heads/f"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/org/repo/-/pulls/5":
			sawUpdate = true
			_, _ = w.Write([]byte(`{"number":"5","state":"open","title":"new","base":{"ref":"refs/heads/main"},"head":{"ref":"refs/heads/f"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	p, err := c.CreatePull(context.Background(), "org/repo", "f", "main", "t", "b")
	if err != nil || p.Number != 5 || !sawCreate {
		t.Fatalf("create: %+v %v", p, err)
	}
	if p, err = c.UpdatePull(context.Background(), "org/repo", 5, "new", "b"); err != nil || p.Title != "new" || !sawUpdate {
		t.Fatalf("update: %+v %v", p, err)
	}
}

func TestMergePullRequiresMergedTrue(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantErr    bool
	}{
		{"merged", `{"merged":true,"sha":"abc"}`, 200, false},
		{"not merged", `{"merged":false,"message":"checks pending"}`, 200, true},
		{"empty body", `{}`, 200, true},
		{"forbidden", `{"errcode":1,"errmsg":"no permission"}`, 403, true},
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut || r.URL.Path != "/org/repo/-/pulls/4/merge" {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if req["merge_style"] != "merge" || req["commit_title"] != "Merge pull request #4" {
				t.Errorf("unexpected merge body: %v", req)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		if err := c.MergePull(context.Background(), "org/repo", 4); (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
}

func TestRedirectIsNotFollowedAndTokenStaysOnTheAPIHost(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
	}))
	t.Cleanup(other.Close)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	})
	_, err := c.DefaultBranch(context.Background(), "org/repo")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("redirect must surface as an error, got %v", err)
	}
	if leaked {
		t.Fatal("client followed the redirect to another host")
	}
}

func TestErrorsNeverEchoTheToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errmsg":"bad token ` + fakeToken + `"}`))
	})
	_, err := c.DefaultBranch(context.Background(), "org/repo")
	if err == nil || strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestResponseBodyIsBounded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(" ", maxBody+1) + `{"name":"main"}`))
	})
	if _, err := c.DefaultBranch(context.Background(), "org/repo"); err == nil {
		t.Fatal("a response past the size limit must not be parsed")
	}
}
