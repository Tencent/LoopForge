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
	"time"
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

func TestCreatePull(t *testing.T) {
	var sawCreate bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/org/repo/-/pulls" {
			sawCreate = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":"5","state":"open","base":{"ref":"refs/heads/main"},"head":{"ref":"refs/heads/f"}}`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	p, err := c.CreatePull(context.Background(), "org/repo", "f", "main", "t", "b")
	if err != nil || p.Number != 5 || !sawCreate {
		t.Fatalf("create: %+v %v", p, err)
	}
}

func TestListPullsCollectsAllPagesAndFilters(t *testing.T) {
	var requests []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/group/sub/repo/-/pulls" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("state") != "all" || q.Get("order_by") != "-updated_at" || q.Get("page_size") != strconv.Itoa(pageSize) {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		requests = append(requests, q.Get("page")+":"+q.Get("base_ref"))
		switch q.Get("page") {
		case "1":
			_, _ = w.Write([]byte(`[
				{"number":"9","state":"open","head":{"ref":"refs/heads/feat","sha":"fork-sha","repo":{"path":"someone/fork"}},"base":{"ref":"refs/heads/main"}},
				{"number":"10","state":"open","head":{"ref":"refs/heads/feat","sha":"other-target","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/release"}},
				{"number":"3","state":"closed","is_merged":true,"closed_at":"2026-01-02T03:04:05Z","head":{"ref":"refs/heads/feat","sha":"old-sha","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}}
			]`))
		case "2":
			_, _ = w.Write([]byte(`[
				{"number":"11","state":"open","head":{"ref":"refs/heads/other","sha":"other-branch","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}},
				{"number":"5","state":"closed","closed_at":"2026-01-03T04:05:06Z","head":{"ref":"refs/heads/feat","sha":"stale-sha","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}},
				{"number":"7","state":"opened","head":{"ref":"refs/heads/feat","sha":"tip-sha","repo":{"path":"group/sub/repo"}},"base":{"ref":"refs/heads/main"}}
			]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})

	pulls, err := c.ListPulls(context.Background(), "group/sub/repo", "feat", "main")
	if err != nil {
		t.Fatalf("ListPulls err=%v", err)
	}
	if len(pulls) != 3 {
		t.Fatalf("want 3 matching pulls across pages, got %+v", pulls)
	}
	if pulls[0].Number != 3 || pulls[0].State != "merged" || pulls[0].HeadSHA != "old-sha" || pulls[0].TargetBranch != "main" || pulls[0].ClosedAt.IsZero() {
		t.Fatalf("pulls[0] = %+v", pulls[0])
	}
	if pulls[1].Number != 5 || pulls[1].State != "closed" || pulls[1].HeadSHA != "stale-sha" || pulls[1].TargetBranch != "main" {
		t.Fatalf("pulls[1] = %+v", pulls[1])
	}
	if pulls[2].Number != 7 || pulls[2].State != "open" || pulls[2].HeadSHA != "tip-sha" || pulls[2].TargetBranch != "main" {
		t.Fatalf("pulls[2] = %+v", pulls[2])
	}
	if strings.Join(requests, ",") != "1:main,2:main,3:main" {
		t.Fatalf("requests = %v", requests)
	}

	allTargets, err := c.ListPulls(context.Background(), "group/sub/repo", "feat", "")
	if err != nil || len(allTargets) != 4 {
		t.Fatalf("empty target should collect all targets without base_ref filter, got %+v err=%v", allTargets, err)
	}
	if strings.HasSuffix(requests[len(requests)-1], "main") {
		t.Fatalf("empty target must omit base_ref, got %v", requests)
	}
}

func TestListPullsErrorsWhenMaxPagesNotExhausted(t *testing.T) {
	fullPage := make([]string, 0, pageSize)
	for i := 1; i <= pageSize; i++ {
		fullPage = append(fullPage, pullItem(i, "other"))
	}
	fullJSON := "[" + strings.Join(fullPage, ",") + "]"

	for _, tc := range []struct {
		name string
		body string
	}{
		{"full pageSize pages", fullJSON},
		{"capped short pages", "[" + pullItem(1, "other") + "," + pullItem(2, "other") + "]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				pages++
				_, _ = w.Write([]byte(tc.body))
			})
			if _, err := c.ListPulls(context.Background(), "org/repo", "feature", "main"); err == nil {
				t.Fatal("truncated listing past maxFindPages must return an explicit error")
			}
			if pages != maxFindPages {
				t.Fatalf("pages = %d, want %d", pages, maxFindPages)
			}
		})
	}
}

func TestPullStateValidationAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		isMerged bool
		want     string
	}{
		{"open", false, "open"},
		{"opened", false, "open"},
		{" OPEN ", false, "open"},
		{"closed", false, "closed"},
		{"merged", false, "merged"},
		{"closed", true, "merged"},
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			merged := "false"
			if tc.isMerged {
				merged = "true"
			}
			_, _ = w.Write([]byte(`{"number":"12","state":"` + tc.raw + `","is_merged":` + merged + `,"head":{"ref":"refs/heads/f","repo":{"path":"org/repo"}},"base":{"ref":"refs/heads/main"}}`))
		})
		p, err := c.GetPull(context.Background(), "org/repo", 12)
		if err != nil || p.State != tc.want {
			t.Fatalf("raw=%q merged=%v: got %q err=%v want %q", tc.raw, tc.isMerged, p.State, err, tc.want)
		}
	}

	for _, badState := range []string{"", "mystery", "draft"} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/org/repo/-/pulls/12" {
				_, _ = w.Write([]byte(`{"number":"12","state":"` + badState + `","head":{"ref":"refs/heads/f"},"base":{"ref":"refs/heads/main"}}`))
				return
			}
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write([]byte(`[{"number":"12","state":"` + badState + `","head":{"ref":"refs/heads/f","repo":{"path":"org/repo"}},"base":{"ref":"refs/heads/main"}}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		})
		if p, err := c.GetPull(context.Background(), "org/repo", 12); err == nil {
			t.Fatalf("GetPull must reject unknown state %q, got %+v", badState, p)
		}
		if pulls, err := c.ListPulls(context.Background(), "org/repo", "f", "main"); err == nil {
			t.Fatalf("ListPulls must reject unknown state %q, got %+v", badState, pulls)
		}
	}
}

func TestListPullsSurfacesAuthAndTimeoutErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errmsg":"denied"}`))
		})
		_, err := c.ListPulls(context.Background(), "org/repo", "feat", "main")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Fatalf("status %d: got err=%v", status, err)
		}
	}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.ListPulls(ctx, "org/repo", "feat", "main"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout must return DeadlineExceeded error, got %v", err)
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
