package httpapi

import (
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// runSessionView decorates a shared SOP run conversation with what the history
// list needs to tell it apart: the requirement it serves, its run status and
// whether it already holds a discussion. The stored title is only the SOP name,
// so without this every run of one SOP looked identical and untouched runs
// looked ownerless.
type runSessionView struct {
	assistantConversation
	DisplayTitle   string `json:"displayTitle,omitempty"`
	RunStatus      string `json:"runStatus,omitempty"`
	RunStatusLabel string `json:"runStatusLabel,omitempty"`
	// SOPName is kept out of the title and shown on hover instead.
	SOPName string `json:"sopName,omitempty"`
	// MessageCount is omitted when it could not be read, so a failed count
	// never makes a discussed run look untouched and hide it.
	MessageCount *int `json:"messageCount,omitempty"`
}

// rootRunConversation reports whether the session is the shared conversation of
// a SOP run (branches keep their own "分支 · …" titles).
func rootRunConversation(item domain.AssistantSession) bool {
	return item.Shared && strings.TrimSpace(item.RunID) != "" && !strings.HasPrefix(item.ID, "sop-branch-")
}

// decorateRunSessions decorates the whole history list from three batched
// reads, so the endpoint's database work stays flat as the list grows.
func decorateRunSessions(st *repo.Store, items []assistantConversation) []runSessionView {
	views := make([]runSessionView, 0, len(items))
	runIDs, sessionIDs := []string{}, []string{}
	for _, item := range items {
		if !rootRunConversation(item) {
			continue
		}
		runIDs = append(runIDs, item.RunID)
		sessionIDs = append(sessionIDs, item.ID)
	}
	metas := st.SidebarRunMetas(runIDs)
	counts := st.AssistantSessionMessageCounts(sessionIDs)
	issueIDs := make([]string, 0, len(metas))
	for _, meta := range metas {
		if id := strings.TrimSpace(meta.IssueID); id != "" {
			issueIDs = append(issueIDs, id)
		}
	}
	issues := st.IssueSidebarIdentities(issueIDs)
	for _, item := range items {
		view := runSessionView{assistantConversation: item}
		if meta, ok := metas[item.RunID]; ok && rootRunConversation(item) {
			count, counted := counts[item.ID]
			applyRunSessionMeta(&view, meta, issues[meta.IssueID], count, counted)
		}
		views = append(views, view)
	}
	disambiguateRunTitles(views, metas)
	return views
}

// disambiguateRunTitles keeps derived names apart when several runs share one,
// for example a requirement sent through the product and then the backend
// SOP: the SOP name is added first, then the start time, and runs started in
// the same minute finally show the second.
func disambiguateRunTitles(views []runSessionView, metas map[string]repo.SidebarRunMeta) {
	derived := func(v runSessionView) bool { return v.DisplayTitle != "" && !v.TitleEdited && !v.TitleGenerated }
	withPart := func(title, part string) string {
		if part == "" || strings.Contains(title, part) {
			return title
		}
		return title + " · " + part
	}
	steps := []func(string, repo.SidebarRunMeta) string{
		func(title string, m repo.SidebarRunMeta) string {
			if !sopRun(m) {
				return title
			}
			return withPart(title, shortWorkflowName(m.WorkflowName))
		},
		func(title string, m repo.SidebarRunMeta) string { return withPart(title, runStartedLabel(m.CreatedAt)) },
		func(title string, m repo.SidebarRunMeta) string {
			minute := runStartedLabel(m.CreatedAt)
			if minute == "" || !strings.HasSuffix(title, minute) {
				return title
			}
			return strings.TrimSuffix(title, minute) + m.CreatedAt.In(assistantClock).Format("1月2日 15:04:05")
		},
	}
	for _, step := range steps {
		taken := map[string]int{}
		for _, v := range views {
			if derived(v) {
				taken[v.DisplayTitle]++
			}
		}
		for i := range views {
			v := &views[i]
			if derived(*v) && taken[v.DisplayTitle] > 1 {
				v.DisplayTitle = step(v.DisplayTitle, metas[v.RunID])
			}
		}
	}
}

// decorateRunSessionView decorates a single conversation opened by its URL, so
// the header shows the same name as the history row.
func decorateRunSessionView(st *repo.Store, item assistantConversation) runSessionView {
	view := runSessionView{assistantConversation: item}
	if !rootRunConversation(item) {
		return view
	}
	metas := st.SidebarRunMetas([]string{item.RunID})
	counts := st.AssistantSessionMessageCounts([]string{item.ID})
	meta, ok := metas[item.RunID]
	if !ok {
		return view
	}
	issue := repo.SidebarIssue{}
	if id := strings.TrimSpace(meta.IssueID); id != "" {
		issue = st.IssueSidebarIdentities([]string{id})[id]
	}
	count, counted := counts[item.ID]
	applyRunSessionMeta(&view, meta, issue, count, counted)
	return view
}

func applyRunSessionMeta(view *runSessionView, meta repo.SidebarRunMeta, issue repo.SidebarIssue, messageCount int, counted bool) {
	// A retired 自由协作 run or a chat workspace is a conversation, not a SOP:
	// it has no nodes and stays "waiting" for good, so neither a 待处理 chip
	// nor a SOP name would mean anything. Only its task names the row.
	if sopRun(meta) {
		view.RunStatus = meta.Status
		view.RunStatusLabel = runSessionStatusLabel(meta.Status)
		view.SOPName = shortWorkflowName(meta.WorkflowName)
	} else {
		meta.WorkflowName = ""
	}
	if counted {
		view.MessageCount = &messageCount
	}
	// A member rename or a generated summary wins over the derived SOP name.
	if !view.TitleEdited && !view.TitleGenerated {
		if sopRun(meta) {
			view.DisplayTitle = runSessionDisplayTitle(meta, issue)
		} else {
			view.DisplayTitle = runTaskTitle(meta, issue)
		}
	}
	// An untouched run conversation has no human messages, so its participant
	// list is empty and the row looked ownerless. Fall back to the run's
	// responsible user; the client resolves the name from its user directory.
	if len(view.Participants) == 0 {
		if uid := meta.ResponsibleUserID; uid != 0 {
			view.Participants = []domain.AssistantParticipant{{ID: uid}}
		} else if uid := view.UserID; uid != 0 {
			view.Participants = []domain.AssistantParticipant{{ID: uid}}
		}
	}
}

func sopRun(meta repo.SidebarRunMeta) bool {
	return meta.Kind != domain.RunKindWorkspace && meta.WorkflowID != domain.FreeCollaborationWorkflowID && meta.WorkflowID != domain.ConversationWorkspaceWorkflowID
}

// runSessionDisplayTitle names a run conversation after what it does, so the
// history reads as a list of tasks rather than repeated SOP names: the
// requirement it serves, else the task text it was started with, else the SOP
// and when it started.
func runSessionDisplayTitle(meta repo.SidebarRunMeta, issue repo.SidebarIssue) string {
	if title := runTaskTitle(meta, issue); title != "" {
		return title
	}
	sop := shortWorkflowName(meta.WorkflowName)
	if sop == "" {
		sop = "SOP 运行"
	}
	if started := runStartedLabel(meta.CreatedAt); started != "" {
		return sop + " · " + started
	}
	return sop
}

// runTaskTitle is the requirement or task text a run works on, or "".
func runTaskTitle(meta repo.SidebarRunMeta, issue repo.SidebarIssue) string {
	if title := runHeadline(issue.Title); title != "" {
		if issue.Number > 0 {
			return fmt.Sprintf("#%d %s", issue.Number, title)
		}
		return title
	}
	return runHeadline(meta.Input)
}

const runHeadlineRunes = 60

// runHeadline is the first non-empty line of a task text without Markdown
// markers, collapsed to one line and cut to a sidebar-sized length.
func runHeadline(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "#>*-•· \t")
		line = strings.Join(strings.Fields(strings.ReplaceAll(line, "**", "")), " ")
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > runHeadlineRunes {
			line = strings.TrimSpace(string(runes[:runHeadlineRunes])) + "…"
		}
		return line
	}
	return ""
}

// runStartedLabel is the start time as people read it: "10月22日 17:36".
func runStartedLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(assistantClock).Format("1月2日 15:04")
}

// shortWorkflowName drops the platform suffix so the requirement leads the row.
func shortWorkflowName(name string) string {
	name = strings.TrimSpace(name)
	for _, suffix := range []string{"（LoopForge）", "(LoopForge)"} {
		name = strings.TrimSpace(strings.TrimSuffix(name, suffix))
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, " SOP"))
	return name
}

// runSessionStatusLabel names the run state in the sidebar. "waiting" covers
// both approval gates and paused runs, so it stays neutral instead of claiming
// an approval is pending.
func runSessionStatusLabel(status string) string {
	switch status {
	case "running":
		return "运行中"
	case "waiting":
		return "待处理"
	case "succeeded":
		return "已完成"
	case "failed":
		return "运行失败"
	case "cancelled":
		return "已取消"
	case "pending", "queued":
		return "排队中"
	}
	return ""
}
