package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func gitLabTestMR(iid int, state string) gitLabMR {
	return gitLabMR{IID: iid, State: state, SourceProjectID: 42, TargetProjectID: 42, SourceBranch: "feature", TargetBranch: "main", SHA: "head"}
}

func TestGitLabFindRejectsForeignIdentityAndPrioritizesOpen(t *testing.T) {
	fork := gitLabTestMR(1, "opened")
	fork.SourceProjectID = 99
	foreignTarget := gitLabTestMR(2, "opened")
	foreignTarget.TargetProjectID = 99
	unknown := gitLabTestMR(3, "opened")
	unknown.SourceProjectID = 0
	closed := gitLabTestMR(4, "closed")
	merged := gitLabTestMR(5, "merged")
	wrongBranch := gitLabTestMR(6, "opened")
	wrongBranch.SourceBranch = "other"
	wrongTarget := gitLabTestMR(7, "opened")
	wrongTarget.TargetBranch = "other"
	for _, tc := range []struct {
		name string
		rows []gitLabMR
		want bool
	}{
		{"fork first", []gitLabMR{fork, gitLabTestMR(8, "opened")}, true},
		{"history before open", []gitLabMR{closed, merged, gitLabTestMR(8, "opened")}, true},
		{"unknown identity", []gitLabMR{unknown}, false},
		{"foreign target", []gitLabMR{foreignTarget}, false},
		{"wrong branches", []gitLabMR{wrongBranch, wrongTarget}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/merge_requests") {
					if r.URL.Query().Get("state") == "opened" {
						json.NewEncoder(w).Encode(tc.rows)
					} else {
						w.Write([]byte(`[]`))
					}
					return
				}
				if r.URL.Path == "/api/v4/projects/team/repo" {
					w.Write([]byte(`{"id":42,"path_with_namespace":"team/repo"}`))
					return
				}
				t.Errorf("unexpected request %s", r.URL)
				w.WriteHeader(500)
			}))
			defer srv.Close()
			g := gitLabClient{base: srv.URL, path: "team/repo", http: srv.Client()}
			m, found, err := g.find("feature", "main")
			if err != nil || found != tc.want || (found && m.IID != 8) {
				t.Fatalf("find = %+v, %v, %v", m, found, err)
			}
		})
	}
}

func TestGitLabMergeChecksCurrentHeadAndResponseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name              string
		history           bool
		base              string
		badCreate         string
		badMerge          string
		wantErr           bool
		wantRef           string
		wantPost, wantPut int
	}{
		{name: "advanced branch creates new MR", history: true, base: "old", wantRef: "!8", wantPost: 1, wantPut: 1},
		{name: "already delivered", history: true, base: "head", wantRef: "!7"},
		{name: "unknown ancestry", history: true, wantErr: true},
		{name: "fork create response", badCreate: "fork", wantErr: true, wantPost: 1},
		{name: "unknown create identity", badCreate: "unknown", wantErr: true, wantPost: 1},
		{name: "wrong merge source project", badMerge: "source", wantErr: true, wantPost: 1, wantPut: 1},
		{name: "wrong merge target project", badMerge: "target", wantErr: true, wantPost: 1, wantPut: 1},
		{name: "wrong merge IID", badMerge: "iid", wantErr: true, wantPost: 1, wantPut: 1},
		{name: "wrong merge SHA", badMerge: "sha", wantErr: true, wantPost: 1, wantPut: 1},
		{name: "wrong merge branch", badMerge: "branch", wantErr: true, wantPost: 1, wantPut: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, puts := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/team/sub/repo")
				switch {
				case p == "":
					w.Write([]byte(`{"id":42,"path_with_namespace":"team/sub/repo"}`))
				case p == "/merge_requests" && r.Method == "GET":
					rows := []gitLabMR{}
					if tc.history && r.URL.Query().Get("state") == "merged" {
						rows = append(rows, gitLabTestMR(7, "merged"))
					}
					json.NewEncoder(w).Encode(rows)
				case p == "/repository/branches/feature":
					w.Write([]byte(`{"commit":{"id":"head"}}`))
				case p == "/repository/branches/main":
					w.Write([]byte(`{"commit":{"id":"target-head"}}`))
				case p == "/repository/merge_base":
					if !reflect.DeepEqual(r.URL.Query()["refs[]"], []string{"head", "target-head"}) {
						t.Errorf("ancestry refs: %v", r.URL.Query())
					}
					json.NewEncoder(w).Encode(map[string]string{"id": tc.base})
				case p == "/merge_requests" && r.Method == "POST":
					posts++
					m := gitLabTestMR(8, "opened")
					if tc.badCreate == "fork" {
						m.SourceProjectID = 99
					}
					if tc.badCreate == "unknown" {
						m.TargetProjectID = 0
					}
					json.NewEncoder(w).Encode(m)
				case p == "/merge_requests/8/merge" && r.Method == "PUT":
					puts++
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["sha"] != "head" {
						t.Error("merge missing observed SHA")
					}
					m := gitLabTestMR(8, "merged")
					switch tc.badMerge {
					case "source":
						m.SourceProjectID = 99
					case "target":
						m.TargetProjectID = 99
					case "iid":
						m.IID = 99
					case "sha":
						m.SHA = "other"
					case "branch":
						m.SourceBranch = "other"
					}
					json.NewEncoder(w).Encode(m)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			s, project := gitLabTestService(t, srv.URL)
			ref, err := s.gitLabMergeBranch(project, "feature", "main", "", "")
			if (err != nil) != tc.wantErr || ref != tc.wantRef || posts != tc.wantPost || puts != tc.wantPut {
				t.Fatalf("ref=%q err=%v posts=%d puts=%d", ref, err, posts, puts)
			}
		})
	}
}

func TestGitLabFindFailsClosedWithoutConfiguredProjectIdentity(t *testing.T) {
	for _, payload := range []string{`{}`, `{"id":42}`, `{"id":42,"path_with_namespace":"other/repo"}`} {
		t.Run(payload, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "merge_requests") {
					t.Error("looked up MRs without identity")
				}
				w.Write([]byte(payload))
			}))
			defer srv.Close()
			g := gitLabClient{base: srv.URL, path: "team/repo", http: srv.Client()}
			if _, found, err := g.find("feature", "main"); err == nil || found {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestGitLabFindPaginatesOpenBeforeHistory(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/merge_requests") {
			w.Write([]byte(`{"id":42,"path_with_namespace":"team/repo"}`))
			return
		}
		requests++
		if r.URL.Query().Get("state") != "opened" {
			t.Error("queried history before exhausting opened MRs")
		}
		if r.URL.Query().Get("page") == "1" {
			rows := make([]gitLabMR, 100)
			for i := range rows {
				rows[i] = gitLabTestMR(i+1, "opened")
				rows[i].SourceProjectID = 99
			}
			json.NewEncoder(w).Encode(rows)
		} else {
			json.NewEncoder(w).Encode([]gitLabMR{gitLabTestMR(101, "opened")})
		}
	}))
	defer srv.Close()
	g := gitLabClient{base: srv.URL, path: "team/repo", http: srv.Client()}
	m, found, err := g.find("feature", "main")
	if err != nil || !found || m.IID != 101 || requests != 2 {
		t.Fatalf("MR=%+v found=%v err=%v requests=%d", m, found, err, requests)
	}
}
