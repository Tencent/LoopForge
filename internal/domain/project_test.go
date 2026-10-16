package domain

import "testing"

func TestIsSafeGongfengProjectID(t *testing.T) {
	for _, s := range []string{"12345", "gateway", "legacy-project_id", "repo.v2"} {
		if !IsSafeGongfengProjectID(s) {
			t.Fatalf("%q should be a safe project id", s)
		}
	}
	for _, s := range []string{"", " ", ".", "..", "repo..bak", "infra/cache", `infra\cache`, "repo name", "repo?x"} {
		if IsSafeGongfengProjectID(s) {
			t.Fatalf("%q should not be a safe project id", s)
		}
	}
}

func TestParseGongfengRepoURL(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		project string
		name2   string
		clone   string
		ok      bool
	}{
		{name: "web url", input: "https://git.example.com/infra/gateway", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "web url trailing slash", input: "https://git.example.com/infra/gateway/", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "web url git suffix", input: "https://git.example.com/infra/gateway.git", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "web url subpage", input: "https://git.example.com/infra/gateway/-/tree/master", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "web url mr list", input: "https://git.example.com/infra/gateway/merge_requests/12", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "nested group", input: "https://git.example.com/tencent/infra/gateway", project: "tencent/infra/gateway", name2: "gateway", ok: true},
		{name: "ssh scp form", input: "git@git.example.com:infra/gateway.git", project: "infra/gateway", name2: "gateway", clone: "git@git.example.com:infra/gateway.git", ok: true},
		{name: "ssh url with port", input: "ssh://git@git.example.com:22/infra/gateway.git", project: "infra/gateway", name2: "gateway", clone: "git@git.example.com:infra/gateway.git", ok: true},
		{name: "git clone https", input: "git clone https://git.example.com/infra/gateway.git", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "git clone ssh", input: "git clone git@git.example.com:infra/gateway.git", project: "infra/gateway", name2: "gateway", clone: "git@git.example.com:infra/gateway.git", ok: true},
		{name: "host without scheme", input: "git.example.com/infra/gateway", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "bare project path", input: "infra/gateway", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "bare nested path", input: "tencent/infra/gateway", project: "tencent/infra/gateway", name2: "gateway", ok: true},
		{name: "http url", input: "http://git.example.com/infra/gateway", project: "infra/gateway", name2: "gateway", ok: true},
		{name: "wrong host", input: "https://github.com/infra/gateway", ok: false},
		{name: "local absolute path", input: "/data/workspace/project", ok: false},
		{name: "single segment", input: "gateway", ok: false},
		{name: "traversal", input: "infra/../etc", ok: false},
		{name: "empty", input: "  ", ok: false},
		{name: "with spaces", input: "https://git.example.com/infra/gateway foo", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := ParseGongfengRepoURL(tc.input)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (info=%+v)", ok, tc.ok, info)
			}
			if !ok {
				return
			}
			if info.Project != tc.project {
				t.Fatalf("project = %q, want %q", info.Project, tc.project)
			}
			if info.Name != tc.name2 {
				t.Fatalf("name = %q, want %q", info.Name, tc.name2)
			}
			if want := "https://" + GongfengRepoHost + "/" + tc.project; info.WebURL != want {
				t.Fatalf("webURL = %q, want %q", info.WebURL, want)
			}
			wantClone := tc.clone
			if wantClone == "" {
				wantClone = info.WebURL + ".git"
			}
			if info.CloneURL != wantClone {
				t.Fatalf("cloneURL = %q, want %q", info.CloneURL, wantClone)
			}
		})
	}
}

func TestAssignRepositoryDirsUsesFirstLevelSubdirectory(t *testing.T) {
	single := []RepositoryContext{{RepositoryID: "id-1", Name: "gateway"}}
	AssignRepositoryDirs(single)
	if single[0].WorkspaceDir != "gateway" {
		t.Fatalf("single-repository WorkspaceDir = %q, want gateway", single[0].WorkspaceDir)
	}

	fallback := []RepositoryContext{{RepositoryID: "repo-id"}}
	AssignRepositoryDirs(fallback)
	if fallback[0].WorkspaceDir != "repo-id" {
		t.Fatalf("unnamed repository WorkspaceDir = %q, want repo-id", fallback[0].WorkspaceDir)
	}

	multi := []RepositoryContext{
		{RepositoryID: "a", Name: "frontend"},
		{RepositoryID: "b", Name: "frontend"},
		{RepositoryID: "c", Name: "backend"},
	}
	AssignRepositoryDirs(multi)
	if multi[0].WorkspaceDir != "frontend" || multi[1].WorkspaceDir != "frontend-2" || multi[2].WorkspaceDir != "backend" {
		t.Fatalf("multi-repository dirs = %q, %q, %q", multi[0].WorkspaceDir, multi[1].WorkspaceDir, multi[2].WorkspaceDir)
	}
}
