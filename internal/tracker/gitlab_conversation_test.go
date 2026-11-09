package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateGitLabBranchMR(t *testing.T) {
	for _, tc := range []struct {
		name, existing, badResponse string
		wantErr, wantUpdated        bool
		posts, updates              int
	}{
		{name: "new request", posts: 1},
		{name: "existing open", existing: "opened", wantUpdated: true, updates: 1},
		{name: "fork does not match", existing: "fork", posts: 1},
		{name: "closed does not match", existing: "closed", posts: 1},
		{name: "merged history does not match", existing: "merged", posts: 1},
		{name: "advanced SHA not confirmed", existing: "stale", wantErr: true},
		{name: "unknown project", badResponse: "project", wantErr: true},
		{name: "foreign create", badResponse: "source", wantErr: true, posts: 1},
		{name: "unknown target create", badResponse: "target", wantErr: true, posts: 1},
		{name: "wrong branch create", badResponse: "branch", wantErr: true, posts: 1},
		{name: "wrong SHA create", badResponse: "sha", wantErr: true, posts: 1},
		{name: "merged create", badResponse: "state", wantErr: true, posts: 1},
		{name: "unverified update", existing: "opened", badResponse: "source", updates: 1},
		{name: "failed update", existing: "opened", badResponse: "update-error", updates: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, updates := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("PRIVATE-TOKEN") != "requester-secret" {
					t.Error("did not use requester token")
				}
				if strings.Contains(r.URL.String(), "secret") {
					t.Error("credential in URL")
				}
				p := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/team/repo")
				if strings.Contains(p, "/merge") && strings.HasSuffix(p, "/merge") {
					t.Fatal("must never merge")
				}
				m := gitLabTestMR(8, "opened")
				switch {
				case p == "":
					if tc.badResponse == "project" {
						w.Write([]byte(`{}`))
					} else {
						w.Write([]byte(`{"id":42,"path_with_namespace":"team/repo"}`))
					}
					return
				case p == "/merge_requests" && r.Method == "GET":
					if r.URL.Query().Get("state") != "opened" {
						t.Error("history queried")
					}
					rows := []gitLabMR{}
					switch tc.existing {
					case "opened":
						rows = append(rows, m)
					case "fork":
						m.SourceProjectID = 99
						rows = append(rows, m)
					case "closed", "merged":
						m.State = tc.existing
						rows = append(rows, m)
					case "stale":
						m.SHA = "old"
						rows = append(rows, m)
					}
					json.NewEncoder(w).Encode(rows)
					return
				case p == "/merge_requests" && r.Method == "POST":
					posts++
				case p == "/merge_requests/8" && r.Method == "PUT":
					updates++
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["title"] != "title" || body["description"] != "description" {
					t.Error("wrong MR content")
				}
				switch tc.badResponse {
				case "source":
					m.SourceProjectID = 99
				case "target":
					m.TargetProjectID = 0
				case "branch":
					m.TargetBranch = "other"
				case "sha":
					m.SHA = "old"
				case "state":
					m.State = "merged"
				case "update-error":
					w.WriteHeader(403)
					w.Write([]byte("requester-secret"))
					return
				}
				json.NewEncoder(w).Encode(m)
			}))
			defer srv.Close()
			got, err := CreateGitLabBranchMR(context.Background(), srv.URL, "requester-secret", "gitlab:"+srv.URL+"/team/repo", "feature", "main", "head", "title", "description")
			if (err != nil) != tc.wantErr || posts != tc.posts || updates != tc.updates {
				t.Fatalf("result=%+v err=%v posts=%d updates=%d", got, err, posts, updates)
			}
			if err != nil {
				if strings.Contains(err.Error(), "requester-secret") {
					t.Error("token leak")
				}
				return
			}
			if got.IID != 8 || got.URL != srv.URL+"/team/repo/-/merge_requests/8" || got.Updated != tc.wantUpdated || got.Existing != (updates > 0) {
				t.Fatalf("result=%+v", got)
			}
			if updates > 0 && !tc.wantUpdated && got.UpdateError == "" {
				t.Error("missing update diagnostic")
			}
			if strings.Contains(got.UpdateError, "requester-secret") {
				t.Error("token leak in update error")
			}
		})
	}
}

func TestCreateGitLabBranchMRRejectsRedirectAndWrongHost(t *testing.T) {
	calls := 0
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("redirect recipient contacted") }))
	defer sink.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, http.StatusFound) }))
	defer srv.Close()
	if _, err := CreateGitLabBranchMR(context.Background(), srv.URL, "secret", "gitlab:"+srv.URL+"/team/repo", "feature", "main", "head", "title", ""); err == nil {
		t.Error("accepted redirect")
	}
	if _, err := CreateGitLabBranchMR(context.Background(), srv.URL, "secret", "gitlab:"+sink.URL+"/team/repo", "feature", "main", "head", "title", ""); err == nil {
		t.Error("accepted wrong host")
	}
	if calls != 0 {
		t.Fatal("credential destination contacted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateGitLabBranchMR(ctx, srv.URL, "secret", "gitlab:"+srv.URL+"/team/repo", "feature", "main", "head", "title", ""); err == nil {
		t.Error("ignored cancellation")
	}
}
