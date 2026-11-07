package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestRunSessionDisplayTitleSaysWhatTheRunDoes(t *testing.T) {
	started := time.Date(2026, 10, 22, 9, 36, 0, 0, time.UTC)
	backend := repo.SidebarRunMeta{WorkflowName: "通用后端交付 SOP（LoopForge）", CreatedAt: started}
	withInput := backend
	withInput.Input = "\n## **在 agent-test 仓库**根目录的 README.md   末尾追加一行\n\n不要改其他文件"
	cases := []struct {
		name  string
		meta  repo.SidebarRunMeta
		issue repo.SidebarIssue
		want  string
	}{
		{"requirement", withInput, repo.SidebarIssue{Number: 12, Title: "修复登录失败"}, "#12 修复登录失败"},
		{"requirement without number", backend, repo.SidebarIssue{Title: "修复登录失败"}, "修复登录失败"},
		{"multi-line requirement title", backend, repo.SidebarIssue{Number: 1, Title: "【显式分支验收】test-branch\r\n只在 agent-test 仓库"}, "#1 【显式分支验收】test-branch"},
		{"task text", withInput, repo.SidebarIssue{}, "在 agent-test 仓库根目录的 README.md 末尾追加一行"},
		{"long task text", repo.SidebarRunMeta{Input: strings.Repeat("改", 70)}, repo.SidebarIssue{}, strings.Repeat("改", runHeadlineRunes) + "…"},
		{"nothing but the SOP", backend, repo.SidebarIssue{}, "通用后端交付 · 10月22日 17:36"},
		{"no start time", repo.SidebarRunMeta{WorkflowName: "MCP测试"}, repo.SidebarIssue{}, "MCP测试"},
		{"no name at all", repo.SidebarRunMeta{}, repo.SidebarIssue{}, "SOP 运行"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runSessionDisplayTitle(tc.meta, tc.issue)
			if got != tc.want {
				t.Fatalf("title=%q want %q", got, tc.want)
			}
		})
	}
}

// One requirement sent through two SOPs, or twice through the same one, must
// not produce identical rows.
func TestRunSessionTitlesStayDistinct(t *testing.T) {
	first := time.Date(2026, 11, 3, 2, 5, 0, 0, time.UTC)
	metas := map[string]repo.SidebarRunMeta{
		"run-product":  {WorkflowName: "通用产品交付 SOP（LoopForge）", CreatedAt: first},
		"run-backend":  {WorkflowName: "通用后端交付 SOP（LoopForge）", CreatedAt: first.Add(time.Hour)},
		"run-backend2": {WorkflowName: "通用后端交付 SOP（LoopForge）", CreatedAt: first.Add(2 * time.Hour)},
		"run-other":    {WorkflowName: "通用后端交付 SOP（LoopForge）"},
	}
	view := func(runID, title string, edited bool) runSessionView {
		return runSessionView{assistantConversation: domain.AssistantSession{RunID: runID, TitleEdited: edited}, DisplayTitle: title}
	}
	views := []runSessionView{
		view("run-product", "#24 登录改版", false),
		view("run-backend", "#24 登录改版", false),
		view("run-backend2", "#24 登录改版", false),
		view("run-other", "#25 导出报表", false),
		view("run-renamed", "", true),
	}
	disambiguateRunTitles(views, metas)
	got := []string{}
	for _, v := range views {
		got = append(got, v.DisplayTitle)
	}
	want := []string{
		"#24 登录改版 · 通用产品交付",
		"#24 登录改版 · 通用后端交付 · 11月3日 11:05",
		"#24 登录改版 · 通用后端交付 · 11月3日 12:05",
		"#25 导出报表",
		"",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("titles:\n got %q\nwant %q", got, want)
	}

	// Test runs of one SOP started within the same minute show the second.
	gate := time.Date(2026, 10, 13, 2, 15, 7, 0, time.UTC)
	metas = map[string]repo.SidebarRunMeta{
		"run-g1": {WorkflowName: "AI 门禁测试 SOP", CreatedAt: gate},
		"run-g2": {WorkflowName: "AI 门禁测试 SOP", CreatedAt: gate.Add(31 * time.Second)},
		"run-g3": {WorkflowName: "AI 门禁测试 SOP", CreatedAt: gate.Add(time.Hour)},
	}
	views = []runSessionView{
		view("run-g1", runSessionDisplayTitle(metas["run-g1"], repo.SidebarIssue{}), false),
		view("run-g2", runSessionDisplayTitle(metas["run-g2"], repo.SidebarIssue{}), false),
		view("run-g3", runSessionDisplayTitle(metas["run-g3"], repo.SidebarIssue{}), false),
	}
	disambiguateRunTitles(views, metas)
	got = got[:0]
	for _, v := range views {
		got = append(got, v.DisplayTitle)
	}
	want = []string{"AI 门禁测试 · 10月13日 10:15:07", "AI 门禁测试 · 10月13日 10:15:38", "AI 门禁测试 · 10月13日 11:15"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("same-minute titles:\n got %q\nwant %q", got, want)
	}
}

func TestShortWorkflowNameDropsPlatformSuffix(t *testing.T) {
	for name, want := range map[string]string{
		"通用后端交付 SOP（LoopForge）":  "通用后端交付",
		"通用前端交付 SOP (LoopForge)": "通用前端交付",
		"产品交付 SOP":               "产品交付",
		"SOP":                    "SOP",
		"":                       "",
	} {
		if got := shortWorkflowName(name); got != want {
			t.Fatalf("shortWorkflowName(%q)=%q want %q", name, got, want)
		}
	}
}

func TestRunSessionStatusLabelStaysNeutralForWaiting(t *testing.T) {
	for status, want := range map[string]string{
		"running": "运行中", "waiting": "待处理", "succeeded": "已完成",
		"failed": "运行失败", "cancelled": "已取消", "queued": "排队中", "odd": "",
	} {
		if got := runSessionStatusLabel(status); got != want {
			t.Fatalf("label(%q)=%q want %q", status, got, want)
		}
	}
}

func TestAssistantSessionListDecoratesRunSessions(t *testing.T) {
	h, owner, viewer, prefix := sessionListFixture(t)
	putSessionListProject(t, h, domain.Project{ID: prefix, OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "member"}}})
	issue := domain.Issue{ID: prefix + "issue", ProjectID: prefix, Number: 7, Title: "修复登录失败"}
	if err := h.repo.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.repo.DB.Exec("DELETE FROM issues WHERE id=?", issue.ID) })

	empty := domain.Run{ID: prefix + "run-empty", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "generic-backend-delivery", Name: "通用后端交付 SOP（LoopForge）"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix, IssueID: issue.ID, IssueNumber: 7, ResponsibleUserID: 555}}
	putSessionListRun(t, h, empty)
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(empty.ID), RunID: empty.ID, ProjectID: prefix, Shared: true, Title: "通用后端交付 SOP（LoopForge）", UserID: owner.ID})

	used := domain.Run{ID: prefix + "run-used", Status: "running", WorkflowID: "generic-backend-delivery",
		Input:            "在 agent-test 仓库根目录的 README.md 末尾追加一行\n不要改其他文件",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "generic-backend-delivery", Name: "通用后端交付 SOP（LoopForge）"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix}}

	// Retired 自由协作 runs never finish and have no nodes: named by their
	// task, with no SOP name and no 待处理 chip.
	free := domain.Run{ID: prefix + "run-free", Status: "waiting", WorkflowID: domain.FreeCollaborationWorkflowID,
		Input:            "修一下登录页的样式",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.FreeCollaborationWorkflowID, Name: "自由协作"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix}}
	putSessionListRun(t, h, free)
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(free.ID), RunID: free.ID, ProjectID: prefix, Shared: true, Title: "自由协作", UserID: owner.ID,
		Messages: []domain.AssistantMessage{{Role: "user", Content: "修一下登录页的样式"}}})
	freeBare := domain.Run{ID: prefix + "run-free-bare", Status: "waiting", WorkflowID: domain.FreeCollaborationWorkflowID,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.FreeCollaborationWorkflowID, Name: "自由协作"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix}}
	putSessionListRun(t, h, freeBare)
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(freeBare.ID), RunID: freeBare.ID, ProjectID: prefix, Shared: true, Title: "测试团队协作", UserID: owner.ID})
	putSessionListRun(t, h, used)
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(used.ID), RunID: used.ID, ProjectID: prefix, Shared: true, Title: "自由协作", UserID: owner.ID,
		Messages: []domain.AssistantMessage{{Role: "assistant", Content: "已收到"}}})

	// Neither a requirement nor a task text: the SOP and its start time, read
	// back from the run row.
	bare := domain.Run{ID: prefix + "run-bare", Status: "succeeded",
		CreatedAt:        time.Date(2026, 10, 14, 7, 11, 0, 0, time.UTC),
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "sop-90s", Name: "模拟90s流程"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix}}
	putSessionListRun(t, h, bare)
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(bare.ID), RunID: bare.ID, ProjectID: prefix, Shared: true, Title: "模拟90s流程", UserID: owner.ID})

	renamed := domain.Run{ID: prefix + "run-renamed", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "generic-backend-delivery", Name: "通用后端交付 SOP（LoopForge）"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix}}
	putSessionListRun(t, h, renamed)
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(renamed.ID), RunID: renamed.ID, ProjectID: prefix, Shared: true, Title: "我的交付", TitleEdited: true, UserID: owner.ID})

	c, rec := workbenchContext(viewer, "GET", "/api/assistant/sessions", bytes.NewBuffer(nil))
	h.listSessions(c)
	if rec.Code != 200 {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body)
	}
	var list []struct {
		ID             string                        `json:"id"`
		DisplayTitle   string                        `json:"displayTitle"`
		RunStatus      string                        `json:"runStatus"`
		RunStatusLabel string                        `json:"runStatusLabel"`
		SOPName        string                        `json:"sopName"`
		MessageCount   int                           `json:"messageCount"`
		Participants   []domain.AssistantParticipant `json:"participants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		title  string
		status string
		label  string
		sop    string
		count  int
		owners []domain.AssistantParticipant
	}{}
	for _, item := range list {
		byID[item.ID] = struct {
			title  string
			status string
			label  string
			sop    string
			count  int
			owners []domain.AssistantParticipant
		}{item.DisplayTitle, item.RunStatus, item.RunStatusLabel, item.SOPName, item.MessageCount, item.Participants}
	}

	untouched, ok := byID[repo.RunAssistantSessionID(empty.ID)]
	if !ok {
		t.Fatal("empty run session missing from list")
	}
	if untouched.title != "#7 修复登录失败" || untouched.sop != "通用后端交付" || untouched.status != "waiting" || untouched.label != "待处理" {
		t.Fatalf("unexpected decoration: %+v", untouched)
	}
	if untouched.count != 0 {
		t.Fatalf("empty run must report no messages: %+v", untouched)
	}
	if len(untouched.owners) != 1 || untouched.owners[0].ID != 555 {
		t.Fatalf("empty run must fall back to the responsible user: %+v", untouched.owners)
	}

	if got := byID[repo.RunAssistantSessionID(free.ID)]; got.title != "修一下登录页的样式" || got.status != "" || got.label != "" || got.sop != "" {
		t.Fatalf("free collaboration decorated as a SOP: %+v", got)
	}
	if got := byID[repo.RunAssistantSessionID(freeBare.ID)]; got.title != "" || got.status != "" || got.sop != "" {
		t.Fatalf("free collaboration without a task must keep its own title: %+v", got)
	}

	discussed := byID[repo.RunAssistantSessionID(used.ID)]
	if discussed.count != 1 || discussed.status != "running" || discussed.title != "在 agent-test 仓库根目录的 README.md 末尾追加一行" {
		t.Fatalf("discussed run decoration: %+v", discussed)
	}

	if got := byID[repo.RunAssistantSessionID(bare.ID)].title; got != "模拟90s流程 · 10月14日 15:11" {
		t.Fatalf("SOP-only run title=%q", got)
	}

	renamedRun := byID[repo.RunAssistantSessionID(renamed.ID)]
	if renamedRun.title != "" {
		t.Fatalf("a member rename must win: %+v", renamedRun)
	}
}

// A count that could not be read is left out rather than reported as zero, so
// the sidebar never folds a discussed run as untouched.
func TestRunSessionMessageCountOmittedWhenUnread(t *testing.T) {
	view := runSessionView{assistantConversation: domain.AssistantSession{ID: "sop-x", RunID: "run-x", Shared: true}}
	applyRunSessionMeta(&view, repo.SidebarRunMeta{Status: "running"}, repo.SidebarIssue{}, 0, false)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "messageCount") {
		t.Fatalf("unread count serialized: %s", raw)
	}
	applyRunSessionMeta(&view, repo.SidebarRunMeta{Status: "running"}, repo.SidebarIssue{}, 0, true)
	if raw, _ = json.Marshal(view); !strings.Contains(string(raw), `"messageCount":0`) {
		t.Fatalf("empty run count missing: %s", raw)
	}
}
