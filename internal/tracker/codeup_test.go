package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestCodeupMRStateMapping(t *testing.T) {
	cases := map[string]string{
		"UNDER_DEV":    "opened",
		"UNDER_REVIEW": "opened",
		"TO_BE_MERGED": "opened",
		"MERGED":       "merged",
		"CLOSED":       "closed",
		"under_review": "opened",
		"UNKNOWN":      "",
		"":             "",
	}
	for state, want := range cases {
		if got := codeupMRState(state); got != want {
			t.Errorf("codeupMRState(%q) = %q want %q", state, got, want)
		}
	}
}

// codeupTestStore wires a project owner with a per-host Codeup credential and a
// codeup repository, mirroring the GitHub adapter test fixtures.
func codeupTestStore(t *testing.T) (*Service, domain.Repository) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "codeup-owner"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("codeup-owner")
	if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "Codeup", Host: domain.CodeupRepoHost, Platform: domain.GitPlatformCodeup, Username: "clone-account"},
		Token:             "codeup-pat",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "pub", Name: "Pub", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	r := domain.Repository{ID: "codeup-repo", ProjectID: "pub", Provider: domain.RepositoryProviderCodeup, Name: "app", GongfengProjectID: "codeup:60d54f3daccf2bbd6659f3ad/team/app", DefaultBranch: "master", WebURL: "https://codeup.aliyun.com/60d54f3daccf2bbd6659f3ad/team/app"}
	if err := st.PutRepository(r); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	return s, r
}

const (
	codeupTestOrg      = "60d54f3daccf2bbd6659f3ad"
	codeupTestRepoBase = "/oapi/v1/codeup/organizations/" + codeupTestOrg + "/repositories/" + codeupTestOrg + "%2Fteam%2Fapp"
	codeupTestListPath = "/oapi/v1/codeup/organizations/" + codeupTestOrg + "/changeRequests"
)

// codeupTestServer simulates the Codeup OpenAPI: repository id resolution, the
// organization-scoped change-request list, create and merge. It records writes
// and the decoded create/merge bodies for assertions.
func codeupTestServer(t *testing.T, listJSON string) (*httptest.Server, *[]string, *map[string]any, *map[string]any) {
	t.Helper()
	var writes []string
	var createBody, mergeBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-yunxiao-token") != "codeup-pat" {
			t.Errorf("missing or wrong Codeup token header: %q", r.Header.Get("x-yunxiao-token"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == codeupTestRepoBase:
			_, _ = w.Write([]byte(`{"id":2813489}`))
		case r.Method == http.MethodGet && r.URL.Path == codeupTestListPath:
			if r.URL.Query().Get("projectIds") != "2813489" {
				t.Errorf("list must filter by the resolved repository id, got %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(listJSON))
		case r.Method == http.MethodPost && r.URL.EscapedPath() == codeupTestRepoBase+"/changeRequests":
			writes = append(writes, "POST create")
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = w.Write([]byte(`{"localId":21,"state":"UNDER_REVIEW","sourceBranch":"feature/work","targetBranch":"master","detailUrl":"https://codeup.aliyun.com/` + codeupTestOrg + `/team/app/change/21"}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.EscapedPath(), codeupTestRepoBase+"/changeRequests/") && strings.HasSuffix(r.URL.EscapedPath(), "/merge"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.EscapedPath(), codeupTestRepoBase+"/changeRequests/"), "/merge")
			writes = append(writes, "POST merge "+id)
			_ = json.NewDecoder(r.Body).Decode(&mergeBody)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &writes, &createBody, &mergeBody
}

func TestCodeupEnsureAndMergeBranchMR(t *testing.T) {
	project := "codeup:" + codeupTestOrg + "/team/app"

	t.Run("create then merge", func(t *testing.T) {
		s, _ := codeupTestStore(t)
		srv, writes, createBody, mergeBody := codeupTestServer(t, `[]`)
		s.codeupAPIBase = srv.URL
		ref, err := s.EnsureAndMergeBranchMR(project, "feature/work", "master", "Title", "desc")
		if err != nil || ref != "!21" {
			t.Fatalf("ref=%q err=%v", ref, err)
		}
		if len(*writes) != 2 || (*writes)[0] != "POST create" || (*writes)[1] != "POST merge 21" {
			t.Fatalf("writes=%v", *writes)
		}
		if (*createBody)["sourceProjectId"] != float64(2813489) || (*createBody)["targetProjectId"] != float64(2813489) {
			t.Fatalf("create must bind integer repo ids: %v", *createBody)
		}
		if (*createBody)["sourceBranch"] != "feature/work" || (*createBody)["targetBranch"] != "master" || (*createBody)["title"] != "Title" {
			t.Fatalf("create body: %v", *createBody)
		}
		if (*mergeBody)["mergeType"] != "no-fast-forward" || (*mergeBody)["removeSourceBranch"] != false {
			t.Fatalf("merge body: %v", *mergeBody)
		}
	})

	t.Run("merge existing open without create", func(t *testing.T) {
		s, _ := codeupTestStore(t)
		list := `[{"localId":7,"state":"TO_BE_MERGED","sourceBranch":"feature/work","targetBranch":"master","detailUrl":"https://codeup.aliyun.com/` + codeupTestOrg + `/team/app/change/7"}]`
		srv, writes, _, mergeBody := codeupTestServer(t, list)
		s.codeupAPIBase = srv.URL
		ref, err := s.EnsureAndMergeBranchMR(project, "feature/work", "master", "", "")
		if err != nil || ref != "!7" {
			t.Fatalf("ref=%q err=%v", ref, err)
		}
		if len(*writes) != 1 || (*writes)[0] != "POST merge 7" {
			t.Fatalf("open MR must be merged without re-create: %v", *writes)
		}
		if (*mergeBody)["mergeType"] != "no-fast-forward" {
			t.Fatalf("merge body: %v", *mergeBody)
		}
	})

	t.Run("short-circuit already merged", func(t *testing.T) {
		s, _ := codeupTestStore(t)
		list := `[{"localId":9,"state":"MERGED","sourceBranch":"feature/work","targetBranch":"master","mergedRevision":"abc"}]`
		srv, writes, _, _ := codeupTestServer(t, list)
		s.codeupAPIBase = srv.URL
		ref, err := s.EnsureAndMergeBranchMR(project, "feature/work", "master", "", "")
		if err != nil || ref != "!9" {
			t.Fatalf("ref=%q err=%v", ref, err)
		}
		if len(*writes) != 0 {
			t.Fatalf("merged MR must not be re-created or re-merged: %v", *writes)
		}
	})
}

func TestCodeupFindBranchMRAndInto(t *testing.T) {
	s, r := codeupTestStore(t)
	list := `[
		{"localId":7,"state":"UNDER_REVIEW","sourceBranch":"feature/work","targetBranch":"master","detailUrl":"https://codeup.aliyun.com/` + codeupTestOrg + `/team/app/change/7"},
		{"localId":5,"state":"MERGED","sourceBranch":"feature/work","targetBranch":"master","mergedRevision":"old"},
		{"localId":8,"state":"CLOSED","sourceBranch":"feature/work","targetBranch":"master"},
		{"localId":9,"state":"UNDER_DEV","sourceBranch":"other","targetBranch":"master"}
	]`
	srv, _, _, _ := codeupTestServer(t, list)
	s.codeupAPIBase = srv.URL

	mr, ok, err := s.FindBranchMR(r.GongfengProjectID, "feature/work")
	if err != nil || !ok {
		t.Fatalf("FindBranchMR ok=%v err=%v", ok, err)
	}
	if mr.IID != 7 || mr.GongfengState != "opened" || mr.URL != "https://codeup.aliyun.com/"+codeupTestOrg+"/team/app/change/7" {
		t.Fatalf("FindBranchMR = %+v", mr)
	}

	into, err := s.FindBranchMRsInto(domain.BranchMRQuery{ProjectID: "pub", RemoteProject: r.GongfengProjectID, SourceBranch: "feature/work", TargetBranch: "master"})
	if err != nil {
		t.Fatal(err)
	}
	if len(into) != 3 {
		t.Fatalf("FindBranchMRsInto = %+v", into)
	}
	for _, mr := range into {
		if mr.TargetBranch != "master" || mr.Project != r.GongfengProjectID {
			t.Fatalf("unexpected MR in result: %+v", mr)
		}
	}
}