package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func cvSTrackerGateway(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{}
		if req.Method == "tools/call" {
			text := `{}`
			switch req.Params.Name {
			case "search_project_issues":
				if req.Params.Arguments["state"] == "opened" {
					text = `[{"id":101,"iid":7,"title":"Issue","description":"Body","state":"opened","priority":2,"created_at":"2026-01-01"}]`
				} else {
					text = `[]`
				}
			case "create_issue":
				text = `{"id":102,"iid":8,"title":"Created","state":"opened"}`
			case "create_issue_note":
				text = `{"id":1}`
			case "search_merge_request":
				text = `[{"id":22,"iid":9,"state":"opened","source_branch":"feature","target_branch":"main","source_commit":"abc123","sha":"abc123","web_url":"https://git.example.com/group/repo/merge_requests/9"}]`
			case "get_commit_refs":
				text = `[{"name":"refs/heads/main"},{"name":"release"}]`
			case "proxy_execute_tool":
				switch req.Params.Arguments["tool_name"] {
				case "stories_get":
					text = `{"data":[{"id":"100","name":"Story","description":"Story body","status":"open","priority":"high","owner":"alice","iteration_id":"it"}]}`
				case "bugs_get":
					text = `{"data":[{"id":"200","title":"Bug","description":"Bug body","status":"new","priority":"middle","current_owner":"bob","iteration_id":"it"}]}`
				case "stories_create":
					text = `{"id":"300","name":"Created story","status":"open"}`
				default:
					text = `{}`
				}
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, cvSJSON(result))
	}))
}

type cvSLandingStore struct {
	*fakeMRDeliveryStore
	rows     []domain.MRDeliveryState
	landings []bool
	rowsErr  error
}

func (s *cvSLandingStore) MergedWorkMRs(string) ([]domain.MRDeliveryState, error) {
	return s.rows, s.rowsErr
}

func (s *cvSLandingStore) ApplyDefaultBranchLanding(_, _ string, landed bool) error {
	s.landings = append(s.landings, landed)
	return nil
}

func TestWorkCommitLanding(t *testing.T) {
	gateway := cvSTrackerGateway(t)
	defer gateway.Close()
	client := NewMCPClient(gateway.URL, nil)
	lookup := NewService()
	state, target, _ := lookup.lookupMRState(client, "group/repo", 9)
	if state != "opened" || target != "main" {
		t.Fatalf("lookup = %q %q", state, target)
	}
	if cached, cachedTarget, _ := lookup.lookupMRState(nil, "group/repo", 9); cached != state || cachedTarget != target {
		t.Fatalf("cached lookup = %q %q", cached, cachedTarget)
	}
	if landed, ok := workCommitOnBranch(client, "group/repo", 9, "main"); !ok || !landed {
		t.Fatalf("work commit = %v %v", landed, ok)
	}
	if landed, ok := workCommitOnBranch(client, "group/repo", 9, "missing"); !ok || landed {
		t.Fatalf("missing branch = %v %v", landed, ok)
	}
	store := &cvSLandingStore{
		fakeMRDeliveryStore: &fakeMRDeliveryStore{repos: []domain.Repository{{
			ID: "repo", ProjectID: "project", GongfengProjectID: "group/repo", DefaultBranch: "main",
		}}},
		rows: []domain.MRDeliveryState{
			{IssueID: "invalid", MRIID: 0},
			{IssueID: domain.BaselinePromoteIssueID, MRIID: 8},
			{IssueID: "mainline", MRIID: 7, TargetBranch: "main"},
			{IssueID: "issue", MRProject: "group/repo", MRIID: 9, MRURL: "https://git.example.com/group/repo/merge_requests/9", TargetBranch: "integration"},
		},
	}
	svc := NewService()
	svc.Configure(domain.SyncConfig{GongfengMCPURL: gateway.URL})
	svc.syncWorkCommitsOnDefaultBranch(store)
	svc.syncWorkCommitsOnDefaultBranch(store)
	if len(store.landings) != 1 || !store.landings[0] {
		t.Fatalf("landings = %#v", store.landings)
	}
	badRepo := domain.Repository{ID: "bad", ProjectID: "bad", GongfengProjectID: "bad/repo", DefaultBranch: "main"}
	NewService().syncWorkCommitsOnDefaultBranch(&cvSLandingStore{
		fakeMRDeliveryStore: &fakeMRDeliveryStore{repos: []domain.Repository{{DefaultBranch: "main"}, {GongfengProjectID: "x"}, badRepo}},
		rows:                []domain.MRDeliveryState{{IssueID: "x", MRIID: 1, TargetBranch: "integration"}},
	})
	svc.syncWorkCommitsOnDefaultBranch(&cvSLandingStore{
		fakeMRDeliveryStore: &fakeMRDeliveryStore{repos: []domain.Repository{badRepo}},
		rowsErr:             errors.New("rows"),
	})
}

func TestTAPDScheduledPoolRoundTrip(t *testing.T) {
	gateway := cvSTrackerGateway(t)
	defer gateway.Close()
	s := NewService()
	s.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		issues:       map[string]domain.Issue{},
		repositories: map[string]domain.Repository{},
		projects:     map[string]domain.Project{"project": {ID: "project", TAPDWorkspaceID: "ws", TAPDIterationID: "it"}},
		users:        []domain.User{{ID: 1, LoginName: "alice"}, {ID: 2, LoginName: "bob"}},
	}
	n, ids, err := s.PullScheduleJob(store, domain.ScheduleJob{Source: "tapd", ProjectID: "project", TAPDWorkspaceID: "ws", TAPDIterationID: "it"})
	if err != nil || n != 2 || len(ids) != 2 {
		t.Fatalf("scheduled TAPD = %d %#v %v", n, ids, err)
	}
	if issue, ok := store.Issue("tapd-100"); !ok || issue.Title != "Story" {
		t.Fatalf("story = %#v, %v", issue, ok)
	}
	if issue, ok := store.Issue("tapd-200"); !ok || issue.Kind != "bug" {
		t.Fatalf("bug = %#v, %v", issue, ok)
	}
	if _, _, err := s.PullScheduleJob(store, domain.ScheduleJob{Source: "tapd"}); err == nil {
		t.Fatal("TAPD job without pool accepted")
	}
}

func TestTAPDManualImports(t *testing.T) {
	gateway := cvSTrackerGateway(t)
	defer gateway.Close()
	s := NewService()
	s.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		issues:       map[string]domain.Issue{},
		repositories: map[string]domain.Repository{},
		projects:     map[string]domain.Project{"project": {ID: "project", TAPDWorkspaceID: "123", TAPDIterationID: "it"}},
		users:        []domain.User{{ID: 7, LoginName: "alice"}, {ID: 8, LoginName: "bob"}},
	}
	story, err := s.ImportTAPDStory(store, "project", "https://www.tapd.cn/123/prong/stories/view/100", 7)
	if err != nil || story.ID != "tapd-100" {
		t.Fatalf("story import = %#v, %v", story, err)
	}
	bug, err := s.ImportTAPDStory(store, "project", "https://www.tapd.cn/123/bugs/view/200", 8)
	if err != nil || bug.ID != "tapd-200" {
		t.Fatalf("bug import = %#v, %v", bug, err)
	}
	if refreshed, err := s.ResyncTAPDIssue(store, story); err != nil || refreshed.ID != story.ID {
		t.Fatalf("story resync = %#v, %v", refreshed, err)
	}
	if refreshed, err := s.ResyncTAPDIssue(store, bug); err != nil || refreshed.ID != bug.ID {
		t.Fatalf("bug resync = %#v, %v", refreshed, err)
	}
	if _, err := s.ResyncTAPDIssue(store, domain.Issue{}); err == nil {
		t.Fatal("invalid resync accepted")
	}
	created, err := s.CreateTAPDStory(store, domain.Issue{ID: "local-tapd", ProjectID: "project", Title: "Create", Description: "Body"})
	if err != nil || created.ID != "tapd-300" || created.ExternalRef == nil {
		t.Fatalf("create TAPD = %#v, %v", created, err)
	}
	if _, err := s.CreateTAPDStory(store, domain.Issue{ProjectID: "missing"}); err == nil {
		t.Fatal("TAPD create without pool accepted")
	}
	if _, err := s.ImportTAPDStory(store, "", "bad", 0); err == nil {
		t.Fatal("empty project accepted")
	}
	if _, err := s.ImportTAPDStory(store, "missing", "https://www.tapd.cn/123/prong/stories/view/100", 0); err == nil {
		t.Fatal("missing project accepted")
	}
}

func TestMRStateSyncLifecycleBranches(t *testing.T) {
	var nilService *Service
	nilService.StartMRStateSync(nil)
	nilService.SyncMRDeliveryStatesOnce(nil)
	nilService.discoverPromoteMRs(&fakeMRDeliveryStore{})
	NewService().discoverPromoteMRs(nil)
	store := &fakeMRDeliveryStore{}
	svc := NewService()
	svc.SyncMRDeliveryStatesOnce(nil)
	svc.SyncMRDeliveryStatesOnce(store)
	svc.StartMRStateSync(store)
	svc.StartMRStateSync(store)
	svc.StopMRStateSync()
	svc.StopMRStateSync()
	workCommitOnBranch(nil, "", 0, "")
	failing := &fakeMRDeliveryStore{discoverErr: errors.New("discover")}
	NewService().SyncMRDeliveryStatesOnce(failing)
	pending := &fakeMRDeliveryStore{pending: []domain.MRDeliveryState{{MRProject: "missing/repo", MRIID: 1}}}
	NewService().SyncMRDeliveryStatesOnce(pending)
	unknown := NewService()
	if state, _, _ := unknown.lookupMRState(nil, "missing/repo", 2); state != "unknown" {
		t.Fatalf("nil lookup = %q", state)
	}
}

func cvSJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestTAPDParsingAndMessages(t *testing.T) {
	raw := `{"data":{"items":[{"Story":{"id":"1","name":"one"}},{"Bug":{"id":"2","title":"two"}},{"Task":{"id":"1","name":"duplicate"}}]}}`
	records, err := parseTAPDRecords(raw, "records")
	if err != nil || len(records) != 2 || records[1].Name != "two" {
		t.Fatalf("records = %#v, %v", records, err)
	}
	for _, empty := range []string{"", "未找到匹配的需求记录", `{"data":[]}`} {
		got, err := parseTAPDRecords(empty, "stories")
		if err != nil || len(got) != 0 {
			t.Fatalf("empty %q = %#v, %v", empty, got, err)
		}
	}
	if _, err := parseTAPDRecords("<html>\nmore", "stories"); err == nil || !strings.Contains(err.Error(), "TAPD") {
		t.Fatalf("invalid error = %v", err)
	}
	if !isTAPDEmptyError(errors.New("未查询到符合条件的记录")) || isTAPDEmptyError(nil) {
		t.Fatal("empty error classification")
	}
	for _, tc := range []struct {
		iter           string
		count, removed int
	}{
		{"", 0, 0}, {"i", 0, 0}, {"i", 0, 2}, {"i", 3, 2}, {"i", 3, 0},
	} {
		if got := tapdPullMessage(tc.iter, tc.count, tc.removed); got == "" {
			t.Fatal("empty pull message")
		}
	}
	long := strings.Repeat("界", 130)
	if got := compactTAPDMessage(long); !strings.HasSuffix(got, "…") {
		t.Fatalf("compact = %q", got)
	}
	if story := extractTAPDStory(`{"data":{"Story":{"id":"42","name":"nested"}}}`); story.ID != "42" {
		t.Fatalf("extracted = %#v", story)
	}
	if story := extractTAPDStory(`not-json`); story.ID != "" {
		t.Fatalf("invalid extracted = %#v", story)
	}
	svc := NewService()
	if got := svc.authError("TAPD", "catalog", errors.New("HTTP 401")); !strings.Contains(got, "catalog") {
		t.Fatalf("auth error = %q", got)
	}
	if got := svc.authError("TAPD", "", errors.New("plain")); got != "plain" {
		t.Fatalf("plain auth error = %q", got)
	}
}

func TestCodeBuddyAuthLoading(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codebuddy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"gongfeng":{"headers":{"Authorization":"Bearer token"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadAuthFromCodeBuddy("gongfeng"); got != "Bearer token" {
		t.Fatalf("auth = %q", got)
	}
	if got := LoadAuthFromCodeBuddy("missing"); got != "" {
		t.Fatalf("missing auth = %q", got)
	}
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadAuthFromCodeBuddy("gongfeng"); got != "" {
		t.Fatalf("malformed auth = %q", got)
	}
}

func TestTrackerMappingsAndIDs(t *testing.T) {
	for _, state := range []string{"opened", "closed", "other"} {
		if gongfengStateToStatus(state) == "" {
			t.Fatal(state)
		}
	}
	if parseTime("not-a-time").IsZero() {
		t.Fatal("invalid time fallback")
	}
	for _, priority := range []*int{nil, cvSInt(1), cvSInt(3), cvSInt(4), cvSInt(9)} {
		if gongfengPriority(priority) == "" {
			t.Fatal(priority)
		}
	}
	for _, status := range []string{"open", "resolved", "closed", "done", "rejected", "planned", "unknown"} {
		if tapdStatusToStatus(status) == "" {
			t.Fatal(status)
		}
	}
	for _, status := range []string{"new", "in_progress", "reopened", "resolved", "verified", "closed", "invalid", "postponed", "unknown"} {
		if tapdBugStatusToStatus(status) == "" {
			t.Fatal(status)
		}
	}
	if tapdTaskStatusToStatus("open") != "todo" || tapdTaskStatusToStatus("progressing") != "in_progress" {
		t.Fatal("task statuses")
	}
	if tapdTaskPriority("high") != "high" || tapdTaskPriority("low") != "low" || tapdPriority("紧急") != "urgent" {
		t.Fatal("priorities")
	}
	if tapdExternalID(domain.Issue{ID: "tapd-workspace-123"}) != "123" {
		t.Fatal("external ID")
	}
	for _, tc := range []struct {
		issue domain.Issue
		want  string
	}{
		{domain.Issue{ExternalRef: &domain.ExternalRef{ExternalType: "bug"}}, "bug"},
		{domain.Issue{ExternalRef: &domain.ExternalRef{ExternalURL: "https://www.tapd.cn/123/tasks/view/10"}}, "task"},
		{domain.Issue{ID: "tapd-task-2", ExternalRef: &domain.ExternalRef{ExternalType: "unknown", ExternalURL: "bad"}}, "task"},
		{domain.Issue{ID: "tapd-task-1"}, "task"},
		{domain.Issue{Kind: "bug"}, "bug"},
		{domain.Issue{}, "story"},
	} {
		if got := tapdExternalType(tc.issue); got != tc.want {
			t.Fatalf("external type = %q, want %q", got, tc.want)
		}
	}
	for _, tc := range []struct {
		id   string
		repo string
		iid  int
	}{{"gf-repo-name-12", "repo-name", 12}, {"gf-7", "", 7}, {"bad", "", 0}, {"gf-x", "", 0}} {
		repo, iid := parseGongfengIssueID(tc.id)
		if repo != tc.repo || iid != tc.iid {
			t.Fatalf("%s = %q,%d", tc.id, repo, iid)
		}
	}
}

func cvSInt(v int) *int { return &v }

type cvSAuthDirectory struct{ projectAuthFixture }

func (cvSAuthDirectory) Users() []domain.User { return []domain.User{{ID: 99, LoginName: "auth-user"}} }
func (cvSAuthDirectory) UserByLoginName(name string) (domain.User, bool) {
	return domain.User{ID: 99, LoginName: name}, true
}

func TestProjectAuthUserDirectoryFallback(t *testing.T) {
	svc := NewService()
	svc.projectAuth = cvSAuthDirectory{}
	dir := svc.tapdUserDirectory(nil)
	if dir == nil {
		t.Fatal("project auth directory missing")
	}
	if got := svc.resolveTAPDAssignee(nil, "", "auth-user", 0); got != 99 {
		t.Fatalf("auth assignee = %d", got)
	}
}

func TestGongfengParsingVariants(t *testing.T) {
	raw := `{"data":[{"iid":1,"state":"closed","source_branch":"other"},{"iid":2,"state":"opened","source_branch":"feature","target_branch":"main","source_commit":"abc","merged_at":"2026-01-01T00:00:00Z"}]}`
	items := parseGongfengMRRecords(raw)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if issues, err := parseGongfengIssues(`{"issues":[{"id":4,"iid":4,"title":"wrapped"}]}`); err != nil || len(issues) != 1 {
		t.Fatalf("wrapped issues = %#v, %v", issues, err)
	}
	if issues, err := parseGongfengIssues(`{"data":[]}`); err != nil || issues == nil {
		t.Fatalf("data issues = %#v, %v", issues, err)
	}
	if _, err := parseGongfengIssues(`bad`); err == nil {
		t.Fatal("malformed issues accepted")
	}
	picked, ok := pickBranchMR(items, "feature")
	if !ok || picked.IID != 2 {
		t.Fatalf("picked = %#v, %v", picked, ok)
	}
	if id, iid, ok := parseGongfengOpenMR(raw, "main"); !ok || id != 0 || iid != 2 {
		t.Fatalf("open = %d,%d,%v", id, iid, ok)
	}
	for _, variant := range []string{
		`{"data":{"iid":2,"state":"merged","target_branch":"main","resolved_at":"2026-01-01 01:02:03"}}`,
		`{"items":[{"iid":2,"state":"reopened","target_branch":"dev"}]}`,
		`[{"iid":2,"state":"opened","target_branch":"main"}]`,
		`{"iid":2,"state":"closed","target_branch":"main"}`,
	} {
		state, _, _ := parseGongfengMRDetails(variant, 2)
		if state == "" {
			t.Fatalf("details failed: %s", variant)
		}
	}
	if got := parseGongfengSourceCommit(`{"data":{"iid":2,"source_commit":" sha "}}`, 2); got != "sha" {
		t.Fatalf("source commit = %q", got)
	}
	if refs, ok := parseGongfengRefNames(`{"data":[{"type":"branch","name":"main"},{"type":"tag","name":"v1"},{"name":" dev "}]}`); !ok || len(refs) != 2 {
		t.Fatalf("refs = %#v, %v", refs, ok)
	}
	if _, ok := parseGongfengRefNames("bad"); ok {
		t.Fatal("bad refs accepted")
	}
	if !gongfengMRMergedAt(gongfengListedMR{State: "merged", ResolvedAt: "2026-01-01T00:00:00Z"}).Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("merged time")
	}
	if !parseGongfengTime("bad").IsZero() || normalizeMRState("unknown") != "" {
		t.Fatal("invalid MR metadata")
	}
}

func TestCatalogBindingHelpers(t *testing.T) {
	servers := []domain.MCPServerDefinition{
		{ID: "z-tapd", Name: "tapd", Transport: "http", URL: "https://mcp.example/tapd", Scope: "project", ProjectID: "p"},
		{ID: "gongfeng", Transport: "http", URL: "https://mcp.example/gf"},
		{ID: "ignored", Transport: "stdio", URL: "https://ignored"},
	}
	binding := bindTrackerMCP("tapd", domain.SyncConfig{}, servers, "token", "")
	if binding.URL == "" || !headerHas(binding.Headers, "authorization") {
		t.Fatalf("binding = %#v", binding)
	}
	if _, err := bindProjectUserTrackerMCP("tapd", domain.SyncConfig{}, servers, "p", domain.User{}, ""); err == nil {
		t.Fatal("missing PAT accepted")
	}
	got, err := bindProjectUserTrackerMCP("tapd", domain.SyncConfig{}, servers, "p", domain.User{LoginName: "u", TaihuPAT: "pat"}, "Owner")
	if err != nil || got.Headers["Authorization"] != "Bearer pat" {
		t.Fatalf("project binding = %#v, %v", got, err)
	}
	for _, market := range []string{"tapd", "tapd-delivery", "70", "tapd_taihu"} {
		if !projectTrackerKindMatch("tapd", domain.MCPServerDefinition{MarketID: market}) {
			t.Fatalf("market %s", market)
		}
	}
	if trackerDisplayName("tapd") != "TAPD" || trackerDisplayName("x") != "工蜂" {
		t.Fatal("display names")
	}
	if sameMCPHost("bad", "https://x") || !sameMCPHost("https://EXAMPLE/a", "https://example/b") {
		t.Fatal("host matching")
	}
	if bearerHeader(" bearer x ") != "bearer x" || bearerHeader("x") != "Bearer x" {
		t.Fatal("bearer")
	}
}

func TestIssueHelpers(t *testing.T) {
	ref := &domain.ExternalRef{ExternalID: "9", ExternalType: "bug", ExternalURL: "https://tapd.example/1/bugtrace/bugs/view?bug_id=9", Imported: true}
	existing := domain.Issue{ID: "old", Status: "done", Number: 8, ProjectID: "p", RepositoryID: "r", RepositoryIDs: []string{"r", "r2"}, BaseBranch: "main", ExternalRef: ref}
	dst := domain.Issue{ExternalRef: &domain.ExternalRef{}}
	applyExistingIssue(&dst, existing)
	if dst.ID != "old" || !dst.ExternalRef.Imported || len(dst.RepositoryIDs) != 2 {
		t.Fatalf("applied = %#v", dst)
	}
	if tapdExternalType(domain.Issue{ID: "tapd-task-9"}) != "task" || tapdExternalType(domain.Issue{Kind: "bug"}) != "bug" {
		t.Fatal("external types")
	}
	gf := domain.Issue{ID: "gf-repo-4", Source: "gongfeng", RepositoryID: "repo", ExternalRef: &domain.ExternalRef{ExternalIID: 4}}
	if externalIssueKey(gf) != "gongfeng:repo:4" {
		t.Fatalf("key = %s", externalIssueKey(gf))
	}
	if got := gongfengProjectPathFromURL("https://git.example/group/repo/-/issues/4"); got != "group/repo" {
		t.Fatalf("path = %q", got)
	}
	a := domain.Issue{ID: "a", CreatedAt: time.Unix(2, 0)}
	b := domain.Issue{ID: "bb", LatestTaskID: "t", CreatedAt: time.Unix(3, 0)}
	if !betterIssue(b, a) || pickIssueKeeper([]domain.Issue{a, b}).ID != "bb" {
		t.Fatal("keeper")
	}
	merged := mergeIssueKeep(a, domain.Issue{LatestTaskID: "t", WorkflowID: "wf", AssigneeID: "u", RepositoryID: "r", DeliveryState: "merged", DeliveryUpdatedAt: time.Now()})
	if merged.LatestTaskID != "t" || merged.DeliveryState != "merged" {
		t.Fatalf("merged = %#v", merged)
	}
	repository := domain.Repository{ID: "repo", GongfengProjectID: "group/repo", WebURL: "https://git.example.com/group/repo"}
	for _, tc := range []struct {
		issue domain.Issue
		want  bool
	}{
		{domain.Issue{RepositoryID: "repo"}, true},
		{domain.Issue{RepositoryID: "other"}, false},
		{domain.Issue{ExternalRef: &domain.ExternalRef{ExternalURL: "https://git.example.com/group/repo/issues/9"}}, true},
		{domain.Issue{ID: "gf-repo-9"}, true},
		{domain.Issue{ID: "gf-other-9"}, false},
	} {
		if got := sameGongfengRepo(tc.issue, repository, 9); got != tc.want {
			t.Fatalf("same repo %#v = %v", tc.issue, got)
		}
	}
	if sameGongfengRepo(domain.Issue{}, domain.Repository{}, 1) {
		t.Fatal("empty repo matched")
	}
	if !sameGongfengRepo(
		domain.Issue{ExternalRef: &domain.ExternalRef{ExternalURL: "https://git.example.com/group/repo/issues/9"}},
		domain.Repository{ID: "web", GongfengProjectID: "other/repo", WebURL: "https://git.example.com/group/repo"}, 9,
	) {
		t.Fatal("repository web URL did not match")
	}
	if gongfengIssueScope(domain.Issue{ExternalRef: &domain.ExternalRef{ExternalURL: "https://git.example.com/group/repo/issues/1"}}) != "group/repo" ||
		gongfengIssueScope(domain.Issue{ID: "gf-repo-1"}) != "repo" ||
		gongfengIssueScope(domain.Issue{}) != "" {
		t.Fatal("gongfeng issue scope")
	}
	if !betterIssue(domain.Issue{WorkflowID: "wf"}, domain.Issue{}) ||
		!betterIssue(domain.Issue{ID: "a"}, domain.Issue{ID: "long"}) ||
		!betterIssue(domain.Issue{CreatedAt: time.Unix(1, 0)}, domain.Issue{CreatedAt: time.Unix(2, 0)}) ||
		!betterIssue(domain.Issue{ID: "a"}, domain.Issue{ID: "b"}) {
		t.Fatal("issue preference branches")
	}
	if parseGongfengIID("gf-repo-12") != 12 || NewService().tapdUserDirectory(nil) != nil {
		t.Fatal("tracker wrapper helpers")
	}
}

func TestDeliveryFinalize(t *testing.T) {
	items := []domain.AnalyticsDelivery{{}}
	(&Service{}).EnrichDeliveryMRStates(items)
}

func TestServiceStateAndEmptyPull(t *testing.T) {
	s := NewService()
	s.SetAuthToken(" token ")
	s.Configure(domain.SyncConfig{})
	if _, _, err := s.trackerClientForProject("tapd", ""); err == nil {
		t.Fatal("unconfigured TAPD client returned")
	}
	if _, _, err := s.trackerClientForProject("gongfeng", ""); err == nil {
		t.Fatal("unconfigured Gongfeng client returned")
	}
	if !s.beginSync() || s.beginSync() || !s.IsSyncing() {
		t.Fatal("sync lock")
	}
	s.endSync()
	if s.IsSyncing() {
		t.Fatal("sync remained active")
	}
	for i := 0; i < 205; i++ {
		s.addLog(domain.SyncLog{ID: strconv.Itoa(i)})
	}
	logs := s.Logs()
	if len(logs) != 100 || logs[0].ID != "204" {
		t.Fatalf("logs = %d, %#v", len(logs), logs[0])
	}
	store := &memIssueStore{issues: map[string]domain.Issue{}, repositories: map[string]domain.Repository{}, projects: map[string]domain.Project{}}
	if err := s.PullAll(store); err != nil {
		t.Fatalf("empty pull = %v", err)
	}
	s.Configure(domain.SyncConfig{AutoSyncEnabled: true, AutoSyncIntervalSec: 1})
	s.StartAutoSync(store)
	s.StartAutoSync(store)
	s.StopAutoSync()
	s.StopAutoSync()
}

func TestGongfengServiceRoundTrip(t *testing.T) {
	gateway := cvSTrackerGateway(t)
	defer gateway.Close()
	s := NewService()
	s.Configure(domain.SyncConfig{GongfengMCPURL: gateway.URL})
	repository := domain.Repository{ID: "repo", ProjectID: "project", Provider: "gongfeng", GongfengProjectID: "group/repo", WebURL: "https://git.example/group/repo"}
	store := &memIssueStore{
		issues:       map[string]domain.Issue{},
		repositories: map[string]domain.Repository{"repo": repository},
		projects:     map[string]domain.Project{"project": {ID: "project"}},
	}
	count, err := s.pullGongfengIssues(s.gongfeng, store, repository)
	if err != nil || count != 1 {
		t.Fatalf("pull = %d, %v", count, err)
	}
	pulled, ok := store.Issue("gf-repo-7")
	if !ok || pulled.Title != "Issue" || pulled.Priority != "urgent" {
		t.Fatalf("pulled = %#v", pulled)
	}
	created, err := s.CreateGongfengIssue(store, domain.Issue{ID: "local", RepositoryID: "repo", ProjectID: "project", Title: "New", Priority: "high"})
	if err != nil || created.ID != "gf-repo-8" {
		t.Fatalf("created = %#v, %v", created, err)
	}
	if _, ok := store.Issue("local"); ok {
		t.Fatal("old local issue remained")
	}
	if err := s.WriteBackClarification(store, pulled, " clarified "); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteBackClarification(store, pulled, ""); err == nil {
		t.Fatal("empty clarification accepted")
	}
	if err := s.WriteBackClarification(store, domain.Issue{}, "x"); err != nil {
		t.Fatalf("unlinked clarification = %v", err)
	}
	if err := s.PullAll(store); err != nil {
		t.Fatalf("pull all Gongfeng = %v", err)
	}
	if mr, ok, err := s.FindBranchMR("group/repo", "feature"); err != nil || !ok || mr.IID != 9 {
		t.Fatalf("branch MR = %#v, %v, %v", mr, ok, err)
	}
	if merged, err := s.EnsureAndMergeBranchMR("group/repo", "feature", "main", "merge title", ""); err != nil || merged != "!9" {
		t.Fatalf("merge branch MR = %q, %v", merged, err)
	}
	if merged, err := s.EnsureAndMergeBranchMR("group/repo", "same", "same", "", ""); err != nil || merged != "" {
		t.Fatalf("same branch MR = %q, %v", merged, err)
	}
	if _, err := s.EnsureAndMergeBranchMR("", "", "", "", ""); err == nil {
		t.Fatal("empty source MR accepted")
	}
	if _, err := s.EnsureAndMergeBranchMR("", "feature", "main", "", ""); err == nil {
		t.Fatal("MR without project accepted")
	}
	if _, err := s.CreateGongfengIssue(store, domain.Issue{RepositoryID: "missing"}); err == nil {
		t.Fatal("Gongfeng issue without repository accepted")
	}
	if _, ok, err := s.FindBranchMR("", "feature"); err != nil || ok {
		t.Fatalf("empty branch MR = %v, %v", ok, err)
	}
	job := domain.ScheduleJob{Source: "gongfeng", ProjectID: "project"}
	if n, _, err := s.PullScheduleJob(store, job); err != nil || n != 1 {
		t.Fatalf("scheduled pull = %d, %v", n, err)
	}
	if _, _, err := s.PullScheduleJob(store, domain.ScheduleJob{Source: "unknown"}); err == nil {
		t.Fatal("unknown source accepted")
	}
}
