package tracker

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestParseGongfengIssuesWrapped(t *testing.T) {
	issues, err := parseGongfengIssues(`{"data":[{"id":10,"iid":7,"title":"closed item","state":"closed"}]}`)
	if err != nil || len(issues) != 1 || issues[0].IID != 7 || issues[0].State != "closed" {
		t.Fatalf("unexpected result: %+v, %v", issues, err)
	}
}

func TestParseTAPDStoryURLDetailPage(t *testing.T) {
	ref, err := ParseTAPDStoryURL("https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000118483431")
	if err != nil || ref.WorkspaceID != "10000000" || ref.StoryID != "1010000000118483431" {
		t.Fatalf("ref=%+v err=%v", ref, err)
	}
}

func TestParseTAPDStoryURLIterationCard(t *testing.T) {
	ref, err := ParseTAPDStoryURL("https://tapd.example.com/tapd_fe/755/iteration/card/1000000755002210580?dialog_preview_id=story_1000000755129275824")
	if err != nil || ref.WorkspaceID != "755" || ref.StoryID != "1000000755129275824" {
		t.Fatalf("ref=%+v err=%v", ref, err)
	}
}

func TestParseTAPDStoryURLRejectsGongfeng(t *testing.T) {
	if _, err := ParseTAPDStoryURL("https://git.example.com/dns-ai/skillhub-agent-flow"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseTAPDStoriesHandlesFlatDataArray(t *testing.T) {
	stories, err := parseTAPDStories(`{"count":1,"data":[{"id":"1010000000118483431","name":"国际站取回","status":"status_38","priority":"3"}]}`)
	if err != nil || len(stories) != 1 || stories[0].ID != "1010000000118483431" || stories[0].Name != "国际站取回" {
		t.Fatalf("unexpected result: %+v, %v", stories, err)
	}
}

func TestParseTAPDStoriesHandlesWrappedStoryItems(t *testing.T) {
	stories, err := parseTAPDStories(`{"data":{"items":[{"Story":{"id":"1001","name":"wrapped","status":"open"}},{"Story":{"id":"1002","name":"second","status":"done"}}]}}`)
	if err != nil || len(stories) != 2 || stories[0].ID != "1001" || stories[1].Name != "second" {
		t.Fatalf("unexpected result: %+v, %v", stories, err)
	}
}

func TestParseTAPDStoriesEmptyIterationIsNotError(t *testing.T) {
	stories, err := parseTAPDStories("未找到匹配的需求记录，建议按以下步骤排查：\n可能原因及解决方案：\n1. 查询条件设置不当")
	if err != nil {
		t.Fatal(err)
	}
	if len(stories) != 0 {
		t.Fatalf("expected empty list, got %+v", stories)
	}
}

func TestParseTAPDStoriesNonJSONSurfacesReadableError(t *testing.T) {
	_, err := parseTAPDStories("认证失败，请重新登录 TAPD")
	if err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("expected readable TAPD error, got %v", err)
	}
}

func TestParseTAPDStoriesDedupesRepeatedIDs(t *testing.T) {
	stories, err := parseTAPDStories(`{"data":{"items":[{"Story":{"id":"1001","name":"once","status":"open"}}],"stories":[{"id":"1001","name":"once","status":"open"},{"id":"1002","name":"second","status":"done"}]}}`)
	if err != nil || len(stories) != 2 || stories[0].ID != "1001" || stories[1].ID != "1002" {
		t.Fatalf("unexpected result: %+v, %v", stories, err)
	}
}

func TestFindGongfengIssuePrefersLegacyID(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"gf-18":          {ID: "gf-18", Source: "gongfeng", RepositoryID: "skillhub", LatestTaskID: "task-1", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 18}},
		"gf-skillhub-18": {ID: "gf-skillhub-18", Source: "gongfeng", RepositoryID: "skillhub", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 18}},
	}}
	issue, found := findGongfengIssue(store, domain.Repository{ID: "skillhub"}, gongfengIssue{IID: 18})
	if !found || issue.ID != "gf-18" {
		t.Fatalf("expected legacy gf-18, got %+v found=%v", issue, found)
	}
}

func TestGongfengProjectPathFromURL(t *testing.T) {
	got := gongfengProjectPathFromURL("https://git.example.com/gslb/gslbweb/issues/1")
	if got != "gslb/gslbweb" {
		t.Fatalf("path=%q", got)
	}
	got = gongfengProjectPathFromURL("https://git.example.com/gslb/gslbweb/-/issues/1")
	if got != "gslb/gslbweb" {
		t.Fatalf("gitlab path=%q", got)
	}
}

func TestFindGongfengIssueDoesNotReuseOtherRepoIID(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"gf-1": {ID: "gf-1", Source: "gongfeng", RepositoryID: "skillhub", LatestTaskID: "task-1", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 1, ExternalURL: "https://git.example.com/example/team-project/issues/1"}},
	}}
	gslbRepo := domain.Repository{ID: "repo-gslbweb", ProjectID: "gslb", GongfengProjectID: "gslb/gslbweb", WebURL: "https://git.example.com/gslb/gslbweb"}
	if issue, found := findGongfengIssue(store, gslbRepo, gongfengIssue{IID: 1}); found {
		t.Fatalf("gslbweb #1 must not match SkillHub gf-1, got %+v", issue)
	}
	skillhub, found := findGongfengIssue(store, domain.Repository{ID: "skillhub", GongfengProjectID: "example/team-project"}, gongfengIssue{IID: 1})
	if !found || skillhub.ID != "gf-1" {
		t.Fatalf("skillhub #1 should still match gf-1, got %+v found=%v", skillhub, found)
	}
}

func TestPruneDuplicateExternalIssuesKeepsLegacy(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"gf-5":          {ID: "gf-5", Source: "gongfeng", RepositoryID: "skillhub", CreatedAt: time.Unix(1, 0).UTC(), ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 5}},
		"gf-skillhub-5": {ID: "gf-skillhub-5", Source: "gongfeng", RepositoryID: "skillhub", CreatedAt: time.Unix(2, 0).UTC(), ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 5}},
		"tapd-1001":     {ID: "tapd-1001", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}},
	}}
	pruneDuplicateExternalIssues(store)
	if _, ok := store.Issue("gf-skillhub-5"); ok {
		t.Fatal("duplicate gongfeng issue was not removed")
	}
	if _, ok := store.Issue("gf-5"); !ok {
		t.Fatal("legacy gongfeng issue should be kept")
	}
	if n := len(store.Issues()); n != 2 {
		t.Fatalf("expected 2 remaining issues, got %d", n)
	}
}

func TestPruneDuplicateExternalIssuesKeepsDeliveryFields(t *testing.T) {
	deliveredAt := time.Unix(100, 0).UTC()
	store := &memIssueStore{issues: map[string]domain.Issue{
		"gf-5": {ID: "gf-5", Source: "gongfeng", RepositoryID: "skillhub", LatestTaskID: "task-1", CreatedAt: time.Unix(1, 0).UTC(), ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 5}},
		"gf-skillhub-5": {
			ID:                "gf-skillhub-5",
			Source:            "gongfeng",
			RepositoryID:      "skillhub",
			CreatedAt:         time.Unix(2, 0).UTC(),
			DeliveryState:     "merged",
			DeliveryMRURL:     "https://git.example.com/org/repo/merge_requests/5",
			DeliveryMRState:   "merged",
			DeliveryUpdatedAt: deliveredAt,
			ExternalRef:       &domain.ExternalRef{Source: "gongfeng", ExternalIID: 5},
		},
	}}
	pruneDuplicateExternalIssues(store)
	keeper, ok := store.Issue("gf-5")
	if !ok || keeper.DeliveryState != "merged" || keeper.DeliveryMRState != "merged" {
		t.Fatalf("keeper lost delivery fields: %+v", keeper)
	}
}

func TestPruneDuplicateExternalIssuesKeepsSameIIDAcrossRepos(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"gf-1":              {ID: "gf-1", Source: "gongfeng", RepositoryID: "skillhub", LatestTaskID: "task-1", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 1, ExternalURL: "https://git.example.com/example/team-project/issues/1"}},
		"gf-repo-gslbweb-1": {ID: "gf-repo-gslbweb-1", Source: "gongfeng", RepositoryID: "repo-gslbweb", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 1, ExternalURL: "https://git.example.com/gslb/gslbweb/issues/1"}},
	}}
	pruneDuplicateExternalIssues(store)
	if _, ok := store.Issue("gf-1"); !ok {
		t.Fatal("SkillHub #1 should stay")
	}
	if _, ok := store.Issue("gf-repo-gslbweb-1"); !ok {
		t.Fatal("gslbweb #1 should stay")
	}
	if n := len(store.Issues()); n != 2 {
		t.Fatalf("expected both issues, got %d", n)
	}
}

type memIssueStore struct {
	issues       map[string]domain.Issue
	repositories map[string]domain.Repository
	projects     map[string]domain.Project
	users        []domain.User
}

func (m *memIssueStore) Issues() []domain.Issue {
	out := make([]domain.Issue, 0, len(m.issues))
	for _, issue := range m.issues {
		out = append(out, issue)
	}
	return out
}
func (m *memIssueStore) Issue(id string) (domain.Issue, bool) {
	v, ok := m.issues[id]
	return v, ok
}
func (m *memIssueStore) Repositories() []domain.Repository {
	out := make([]domain.Repository, 0, len(m.repositories))
	for _, repository := range m.repositories {
		out = append(out, repository)
	}
	return out
}
func (m *memIssueStore) Repository(id string) (domain.Repository, bool) {
	v, ok := m.repositories[id]
	return v, ok
}
func (m *memIssueStore) Projects() []domain.Project {
	out := make([]domain.Project, 0, len(m.projects))
	for _, project := range m.projects {
		out = append(out, project)
	}
	return out
}
func (m *memIssueStore) Project(id string) (domain.Project, bool) {
	v, ok := m.projects[id]
	return v, ok
}
func (m *memIssueStore) Users() []domain.User {
	return append([]domain.User(nil), m.users...)
}
func (m *memIssueStore) UserByLoginName(loginName string) (domain.User, bool) {
	loginName = strings.TrimSpace(loginName)
	if loginName == "" {
		return domain.User{}, false
	}
	for _, user := range m.users {
		if strings.EqualFold(user.LoginName, loginName) {
			return user, true
		}
	}
	return domain.User{}, false
}
func (m *memIssueStore) PutIssue(v domain.Issue) error {
	if m.issues == nil {
		m.issues = map[string]domain.Issue{}
	}
	m.issues[v.ID] = v
	return nil
}
func (m *memIssueStore) DeleteIssue(id string) error {
	delete(m.issues, id)
	return nil
}
func (m *memIssueStore) PutSyncConfig(domain.SyncConfig) error { return nil }
func (m *memIssueStore) SyncConfig() domain.SyncConfig         { return domain.SyncConfig{} }

func TestReconcileTAPDRemovesStoriesMissingFromIteration(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"tapd-old":  {ID: "tapd-old", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "old"}},
		"tapd-keep": {ID: "tapd-keep", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "keep"}},
		"gf-1":      {ID: "gf-1", Source: "gongfeng"},
		"other-1":   {ID: "other-1", Source: "gongfeng"},
	}}
	removed := reconcileTAPDIssues(store, map[string]bool{"keep": true})
	if removed != 1 {
		t.Fatalf("removed=%d", removed)
	}
	if _, ok := store.Issue("tapd-old"); ok {
		t.Fatal("stale TAPD issue should be removed")
	}
	if _, ok := store.Issue("tapd-keep"); !ok {
		t.Fatal("in-iteration TAPD issue should stay")
	}
	if _, ok := store.Issue("gf-1"); !ok {
		t.Fatal("gongfeng issue should stay")
	}
	if _, ok := store.Issue("other-1"); !ok {
		t.Fatal("non-TAPD issue should stay")
	}
}

func TestReconcileTAPDKeepsImportedStoryOutsideIteration(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"tapd-keep": {ID: "tapd-keep", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "keep"}},
		"tapd-import": {
			ID:            "tapd-import",
			Source:        "tapd",
			ExternalRef:   &domain.ExternalRef{Source: "tapd", ExternalID: "imported-out-of-iteration", Imported: true},
			Clarification: domain.ClarificationAssessment{Gated: true, Status: domain.ClarificationAnalyzing},
		},
		"tapd-legacy": {
			ID:            "tapd-legacy",
			Source:        "tapd",
			ExternalRef:   &domain.ExternalRef{Source: "tapd", ExternalID: "legacy-import"},
			Clarification: domain.ClarificationAssessment{Gated: true},
		},
	}}
	removed := reconcileTAPDIssues(store, map[string]bool{"keep": true})
	if removed != 0 {
		t.Fatalf("removed=%d", removed)
	}
	if _, ok := store.Issue("tapd-import"); !ok {
		t.Fatal("imported TAPD story outside the iteration must stay")
	}
	if _, ok := store.Issue("tapd-legacy"); !ok {
		t.Fatal("legacy gated import must stay")
	}
}

func TestReconcileTAPDClearsAllWhenIterationEmpty(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"tapd-a": {ID: "tapd-a", Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "a"}},
		"tapd-b": {ID: "tapd-b", Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "b"}},
		"gf-1":   {ID: "gf-1", Source: "gongfeng"},
	}}
	if removed := reconcileTAPDIssues(store, map[string]bool{}); removed != 2 {
		t.Fatalf("removed=%d", removed)
	}
	if n := len(store.Issues()); n != 1 {
		t.Fatalf("expected only gongfeng left, got %d", n)
	}
}

func TestScheduledTAPDIssueIDsOnlyReturnsPulledStoriesInProject(t *testing.T) {
	store := &memIssueStore{issues: map[string]domain.Issue{
		"tapd-current": {ID: "tapd-current", ProjectID: "platform", Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "current"}},
		"tapd-old":     {ID: "tapd-old", ProjectID: "platform", Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "old"}},
		"tapd-other":   {ID: "tapd-other", ProjectID: "other", Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "other"}},
	}}
	got := scheduledTAPDIssueIDs(store, "platform", map[string]bool{"current": true, "other": true, "missing": true})
	if len(got) != 1 || got[0] != "tapd-current" {
		t.Fatalf("issue ids=%v", got)
	}
}

func TestActionableAuthError(t *testing.T) {
	got := actionableAuthError("工蜂", assertError("backend HTTP 401"))
	if got == "backend HTTP 401" {
		t.Fatalf("expected actionable authentication error")
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }

func TestKeepLocalExecutionStatusPreservesDeliveredAndReopened(t *testing.T) {
	closed := domain.Issue{Status: "closed"}
	if !keepLocalExecutionStatus(closed, "in_progress") {
		t.Fatal("manual close should remain authoritative during tracker sync")
	}
	delivered := domain.Issue{Status: "done", DeliveryState: "merged"}
	if !keepLocalExecutionStatus(delivered, "in_progress") {
		t.Fatal("locked delivery should keep local done")
	}
	reopened := domain.Issue{Status: "todo", DeliveryState: "reopened"}
	if !keepLocalExecutionStatus(reopened, "done") {
		t.Fatal("reopened issue should keep local todo")
	}
}

func TestTrackerClosedStatusMapping(t *testing.T) {
	if got := gongfengStateToStatus("closed"); got != "closed" {
		t.Fatalf("Gongfeng closed = %q", got)
	}
	for _, state := range []string{"closed", "已关闭"} {
		if got := tapdStatusToStatus(state); got != "closed" {
			t.Fatalf("TAPD story %q = %q", state, got)
		}
		if got := tapdBugStatusToStatus(state); got != "closed" {
			t.Fatalf("TAPD bug %q = %q", state, got)
		}
	}
	if got := tapdStatusToStatus("已完成"); got != "done" {
		t.Fatalf("TAPD completion must remain done, got %q", got)
	}
}

func TestApplyExistingIssueExternalCloseAndReopen(t *testing.T) {
	existing := domain.Issue{ID: "gf-1", Source: "gongfeng", Status: "in_progress", AutomationState: "eligible", ExternalRef: &domain.ExternalRef{ExternalState: "opened"}}
	closed := domain.Issue{Source: "gongfeng", Status: "closed", CloseSource: "external", ExternalRef: &domain.ExternalRef{ExternalState: "closed"}}
	applyExistingIssue(&closed, existing)
	if closed.Status != "closed" || closed.CloseSource != "external" {
		t.Fatalf("external closure must override local execution: %+v", closed)
	}
	closed.AutomationState = "ignored" // PutIssue marks terminal statuses ignored.
	reopened := domain.Issue{Status: "todo", ExternalRef: &domain.ExternalRef{ExternalState: "opened"}}
	applyExistingIssue(&reopened, closed)
	if reopened.Status != "todo" || reopened.CloseSource != "" || reopened.AutomationState != "eligible" {
		t.Fatalf("external reopen must restore local eligibility: %+v", reopened)
	}

	manual := closed
	manual.CloseSource = "manual"
	remoteReopened := domain.Issue{Status: "todo", ExternalRef: &domain.ExternalRef{ExternalState: "opened"}}
	applyExistingIssue(&remoteReopened, manual)
	if remoteReopened.Status != "closed" || remoteReopened.CloseSource != "manual" {
		t.Fatalf("remote reopen must not undo a manual close: %+v", remoteReopened)
	}

	delivered := closed
	delivered.DeliveryState = "merged"
	reopenedAfterDelivery := domain.Issue{Status: "todo", ExternalRef: &domain.ExternalRef{ExternalState: "opened"}}
	applyExistingIssue(&reopenedAfterDelivery, delivered)
	if reopenedAfterDelivery.Status != "done" || reopenedAfterDelivery.CloseSource != "" || reopenedAfterDelivery.AutomationState != "ignored" {
		t.Fatalf("external reopen must retain completed delivery: %+v", reopenedAfterDelivery)
	}
}

func TestApplyExistingIssuePreservesStatusForReopened(t *testing.T) {
	existing := domain.Issue{ID: "tapd-1", Status: "todo", DeliveryState: "reopened", DeliveryUpdatedAt: time.Now().UTC()}
	dst := domain.Issue{Status: "done"}
	applyExistingIssue(&dst, existing)
	if dst.Status != "todo" {
		t.Fatalf("expected todo, got %s", dst.Status)
	}
}
