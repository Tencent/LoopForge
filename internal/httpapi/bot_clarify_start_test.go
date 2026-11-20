package httpapi

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// clarifyStartFixture: a Feishu Bot in the web-confirm mode with scripted
// clarification rounds, the SOPs wf-dev (the project default, following
// the project setting) and wf-fix (clarification skipped), and a project
// that asks for clarification before SOP starts.
func clarifyStartFixture(t *testing.T) (*clarifyChatFixture, domain.User) {
	return clarifyStartFixtureFor(t, domain.BotChannelFeishu)
}

func clarifyStartFixtureFor(t *testing.T, channel string) (*clarifyChatFixture, domain.User) {
	t.Helper()
	s, h, bot, _, _ := confirmModeFixtureFor(t, channel)
	f := &clarifyChatFixture{s: s, h: h, bot: bot}
	f.stubClarify(t)
	if err := s.PutAgent(domain.AgentDefinition{ID: "dev-agent", Name: "开发"}); err != nil {
		t.Fatal(err)
	}
	for _, wf := range []domain.WorkflowDefinition{
		{ID: "wf-dev", Name: "研发交付", ProjectID: "p", Status: "published", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "开发", Type: "agent", AgentID: "dev-agent"}}},
		{ID: "wf-fix", Name: "缺陷修复", ProjectID: "p", Status: "published", ClarifyBeforeStart: domain.ClarifyBeforeStartSkip, Nodes: []domain.WorkflowNode{{ID: "n1", Name: "修复", Type: "agent", AgentID: "dev-agent"}}},
	} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := s.Project("p")
	project.WorkflowID, project.ClarifyBeforeStart = "wf-dev", domain.ClarifyBeforeStartRequired
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	member, _ := s.User(43)
	return f, member
}

// askDraft sends "帮我建个需求" in the group oc_team and returns the draft ID,
// the reply and the conversation.
func (f *clarifyChatFixture) askDraft(t *testing.T) (string, string, string) {
	t.Helper()
	item, conv := f.message(t, "group", "oc_team", "new-wecom-user", "帮我建个需求")
	reply, err := f.h.generateBotReply(context.Background(), f.bot, conv, nil, item)
	if err != nil {
		t.Fatal(err)
	}
	m := botDraftLink.FindStringSubmatch(reply)
	if m == nil {
		t.Fatalf("no draft: %q", reply)
	}
	return m[1], reply, conv.ID
}

func (f *clarifyChatFixture) createdIssue(t *testing.T) domain.Issue {
	t.Helper()
	for _, issue := range f.s.Issues() {
		if strings.HasPrefix(issue.Title, "网页确认的需求") {
			return issue
		}
	}
	t.Fatal("requirement not created")
	return domain.Issue{}
}

func (f *clarifyChatFixture) issueTasks(issueID string) []domain.Task {
	out := []domain.Task{}
	for _, task := range f.s.Tasks() {
		if task.IssueID == issueID {
			out = append(out, task)
		}
	}
	return out
}

// "创建后立即启动" of a requirement whose project asks for clarification:
// the requirement is saved, the clarification opens in the same group and
// the SOP is not started before its conclusion is saved, by no other path
// either. Saving starts the SOP exactly once, however many saves race, and
// the run reports back to the group.
func TestChatStartWaitsForClarification(t *testing.T) {
	f, member := clarifyStartFixture(t)
	f.rounds = []map[string]any{clarifyQuestionsRound, clarifyReadyRound}
	id, reply, conv := f.askDraft(t)
	if !strings.Contains(reply, "选择启动后会先在这个会话澄清需求") {
		t.Fatalf("summary does not announce the clarification: %q", reply)
	}
	code, out := draftChoice(t, f.h, member, id, `{"choices":[{"index":0,"workflowId":"","start":true}]}`)
	if code != 200 || out["status"] != repo.BotDraftExecuted {
		t.Fatalf("confirm = %d %v", code, out)
	}
	results := strings.Join(toStrings(out["results"]), "\n")
	if !strings.Contains(results, "已完成：保存需求：网页确认的需求") || !strings.Contains(results, "待澄清：") || !strings.Contains(results, "已在这个会话开始澄清") || strings.Contains(results, "已启动") {
		t.Fatalf("results = %q", results)
	}
	issue := f.createdIssue(t)
	sess := f.waitStatus(t, conv, domain.BotClarifyAsking)
	if sess.IssueID != issue.ID || !sess.StartOnSave || sess.StarterUserID != member.ID || sess.ChatType != "group" {
		t.Fatalf("session = %+v", sess)
	}
	if tasks := f.issueTasks(issue.ID); len(tasks) != 0 {
		t.Fatalf("SOP started before clarification: %+v", tasks)
	}
	// Answering everything runs the next round, which concludes; the SOP
	// still waits for the conclusion to be saved.
	if reply, _ := f.say(t, "group", "oc_team", "new-wecom-user", "Q1：接口回归\nQ2：要"); !strings.Contains(reply, "AI 正在分析第 2 轮") {
		t.Fatalf("answers = %q", reply)
	}
	sess = f.waitStatus(t, conv, domain.BotClarifyConcluded)
	posts := f.posts(t, conv)
	last := posts[len(posts)-1]
	if last.Quote != botinteraction.ClarifyPostConclusionStart || !strings.Contains(last.Content, "确认结论并启动 SOP") {
		t.Fatalf("conclusion post = %+v", last)
	}
	if status, _ := f.say(t, "group", "oc_team", "new-wecom-user", "澄清进度"); !strings.Contains(status, "保存后会启动 SOP") {
		t.Fatalf("status = %q", status)
	}
	// Other starts are held as well: a dispatch draft and a full-access
	// dispatch both leave the SOP alone.
	origin := domain.BotChatOrigin{ConfigID: f.bot.ID, ProjectID: "p", ConversationID: conv, ChatType: "group", ChatID: "oc_team", SenderID: "new-wecom-user"}
	if note, held := f.h.botHoldDispatch(f.bot, origin, member, issue.ID, ""); !held || !strings.Contains(note, "正在这个会话里澄清") {
		t.Fatalf("dispatch during clarification = %q %v", note, held)
	}
	if tasks := f.issueTasks(issue.ID); len(tasks) != 0 {
		t.Fatalf("SOP started before the conclusion was saved: %+v", tasks)
	}
	// Racing plain saves (like 「保存澄清」): the waiting start comes from
	// the save itself, once.
	var wg sync.WaitGroup
	replies := make([]string, 3)
	for i, action := range []string{botinteraction.ClarifyActionSave, botinteraction.ClarifyActionSave, botinteraction.ClarifyActionSave} {
		wg.Add(1)
		go func(i int, action string) {
			defer wg.Done()
			replies[i], _ = f.h.handleBotCardAction(context.Background(), f.bot, domain.BotInteraction{SenderID: "new-wecom-user", Action: action, DeliveryID: sess.ID, Quote: last.ID}, domain.BotConversation{ID: conv, ChatType: "group"})
		}(i, action)
	}
	wg.Wait()
	started := 0
	for _, r := range replies {
		if strings.Contains(r, "已启动 SOP") {
			started++
		}
	}
	tasks := f.issueTasks(issue.ID)
	if started != 1 || len(tasks) != 1 || tasks[0].WorkflowID != "wf-dev" {
		t.Fatalf("starts = %d tasks = %+v replies = %q", started, tasks, replies)
	}
	if again, _ := f.say(t, "group", "oc_team", "new-wecom-user", "保存澄清"); again != botClarifyNoSession {
		t.Fatalf("save after the start = %q", again)
	}
	if len(f.issueTasks(issue.ID)) != 1 {
		t.Fatal("a later save started the SOP again")
	}
	issue, _ = f.s.Issue(issue.ID)
	if issue.Clarification.Status != domain.ClarificationReady {
		t.Fatalf("clarification = %+v", issue.Clarification)
	}
	if runID := tasks[0].RunID; runID != "" {
		if origin, ok := f.s.BotRunOrigin(runID); !ok || origin.ChatID != "oc_team" {
			t.Fatalf("run origin = %+v %v", origin, ok)
		}
	}
	// Clarified now: later starts are not held.
	if _, held := f.h.botHoldDispatch(f.bot, origin, member, issue.ID, ""); held {
		t.Fatal("a clarified requirement is still held")
	}
}

// Ending the clarification an SOP start waits for starts nothing.
func TestChatStartHeldUntilClarificationEnds(t *testing.T) {
	f, member := clarifyStartFixture(t)
	f.rounds = []map[string]any{clarifyQuestionsRound}
	id, _, conv := f.askDraft(t)
	if code, out := draftChoice(t, f.h, member, id, `{"choices":[{"index":0,"workflowId":"","start":true}]}`); code != 200 || out["status"] != repo.BotDraftExecuted {
		t.Fatalf("confirm = %d %v", code, out)
	}
	issue := f.createdIssue(t)
	f.waitStatus(t, conv, domain.BotClarifyAsking)
	if reply, _ := f.say(t, "group", "oc_team", "new-wecom-user", "结束澄清"); !strings.Contains(reply, "已结束澄清") {
		t.Fatalf("end = %q", reply)
	}
	if tasks := f.issueTasks(issue.ID); len(tasks) != 0 {
		t.Fatalf("ended clarification started the SOP: %+v", tasks)
	}
}

// Without clarification (an SOP that skips it, or a project that does not
// ask for it) the start happens right away and no clarification opens.
func TestChatStartWithoutClarificationStartsAtOnce(t *testing.T) {
	t.Run("sop skips", func(t *testing.T) {
		f, member := clarifyStartFixture(t)
		id, _, conv := f.askDraft(t)
		_, out := draftChoice(t, f.h, member, id, `{"choices":[{"index":0,"workflowId":"wf-fix","start":true}]}`)
		if results := strings.Join(toStrings(out["results"]), "\n"); !strings.Contains(results, "已启动：缺陷修复") || strings.Contains(results, "待澄清") {
			t.Fatalf("results = %q", results)
		}
		if tasks := f.issueTasks(f.createdIssue(t).ID); len(tasks) != 1 {
			t.Fatalf("tasks = %+v", tasks)
		}
		if _, ok := f.s.ActiveBotClarifySession(conv); ok {
			t.Fatal("clarification opened for an SOP that skips it")
		}
	})
	t.Run("project does not ask", func(t *testing.T) {
		f, member := clarifyStartFixture(t)
		project, _ := f.s.Project("p")
		project.ClarifyBeforeStart = ""
		f.s.PutProject(project)
		id, reply, conv := f.askDraft(t)
		if strings.Contains(reply, "先在这个会话澄清") {
			t.Fatalf("summary = %q", reply)
		}
		_, out := draftChoice(t, f.h, member, id, `{"choices":[{"index":0,"workflowId":"","start":true}]}`)
		if results := strings.Join(toStrings(out["results"]), "\n"); !strings.Contains(results, "已启动：研发交付") {
			t.Fatalf("results = %q", results)
		}
		if _, ok := f.s.ActiveBotClarifySession(conv); ok {
			t.Fatal("clarification opened without the setting")
		}
	})
}

// Starting an existing, unclarified requirement through an issues_dispatch
// draft opens the clarification instead; without the chat it came from,
// nothing starts and the platform is named.
func TestChatDispatchDraftWaitsForClarification(t *testing.T) {
	f, member := clarifyStartFixture(t)
	if err := f.s.PutIssue(domain.Issue{ID: "iss-9", ProjectID: "p", Number: 9, Title: "导出报表", WorkflowID: "wf-dev"}); err != nil {
		t.Fatal(err)
	}
	f.rounds = []map[string]any{clarifyQuestionsRound}
	item, conv := f.message(t, "group", "oc_team", "new-wecom-user", "启动 P-9")
	draft, err := f.s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: f.bot.ID, IdentityScope: f.bot.IdentityScope(), SenderID: "new-wecom-user", ProjectID: "p", UserID: member.ID, InteractionID: item.ID, Summary: "1. 派发需求：iss-9", Drafts: `[{"tool":"issues_dispatch","path":{"id":"iss-9"},"body":{}}]`}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	code, out := draftChoice(t, f.h, member, draft.ID, `{}`)
	results := strings.Join(toStrings(out["results"]), "\n")
	if code != 200 || out["status"] != repo.BotDraftExecuted || !strings.Contains(results, "待澄清：") || strings.Contains(results, "已完成") {
		t.Fatalf("confirm = %d %v", code, out)
	}
	if tasks := f.issueTasks("iss-9"); len(tasks) != 0 {
		t.Fatalf("dispatch started before clarification: %+v", tasks)
	}
	if sess := f.waitStatus(t, conv.ID, domain.BotClarifyAsking); sess.IssueID != "iss-9" || !sess.StartOnSave {
		t.Fatalf("session = %+v", sess)
	}

	note := f.h.holdBotStart(f.bot, domain.BotChatOrigin{}, false, member, domain.Issue{ID: "iss-9", ProjectID: "p", Number: 9, Title: "导出报表"})
	if !strings.Contains(note, "SOP 没有启动") || !strings.Contains(note, "平台的需求详情里澄清") {
		t.Fatalf("without origin = %q", note)
	}
}

// "澄清 LOOP-7" stays its own entry: it never starts the SOP by itself, and
// its conclusion card keeps both save buttons.
func TestManualChatClarificationStartsNothing(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelFeishu)
	project, _ := f.s.Project("p")
	project.ClarifyBeforeStart = domain.ClarifyBeforeStartRequired
	f.s.PutProject(project)
	f.rounds = []map[string]any{clarifyReadyRound}
	_, start := f.say(t, "single", "oc_manual", "ou_owner", "澄清 LOOP-7")
	sess := f.waitStatus(t, start.ConversationID, domain.BotClarifyConcluded)
	if sess.StartOnSave {
		t.Fatal("manual clarification marked to start the SOP")
	}
	posts := f.posts(t, start.ConversationID)
	if last := posts[len(posts)-1]; last.Quote != botinteraction.ClarifyPostConclusion {
		t.Fatalf("conclusion post = %+v", last)
	}
	if reply, _ := f.say(t, "single", "oc_manual", "ou_owner", "保存澄清"); !strings.Contains(reply, "澄清结果已保存") || strings.Contains(reply, "SOP") {
		t.Fatalf("save = %q", reply)
	}
	if tasks := f.issueTasks("iss-7"); len(tasks) != 0 {
		t.Fatalf("manual clarification started the SOP: %+v", tasks)
	}
}

// A full-access chat Bot's issues_dispatch goes through the hold first.
func TestGatewayHoldsChatDispatch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	gw := &agentToolGateway{deps: &deps{repo: st}}
	asked := ""
	session := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", ExecMode: assistantExecMode{AutoWrite: true}, HoldDispatch: func(issueID, workflowID string) (string, bool) {
		asked = issueID + "/" + workflowID
		return "需求需要先澄清再启动 SOP", true
	}}
	var got string
	got, err = gw.dispatch(context.Background(), session, agentToolDescriptor{Name: "agentflow__issues_dispatch", Kind: "agentflow", UpstreamName: "issues_dispatch"}, map[string]any{"path": map[string]any{"id": "iss-1"}, "body": map[string]any{"workflowId": "wf-dev"}})
	if err != nil || got != "需求需要先澄清再启动 SOP" || asked != "iss-1/wf-dev" {
		t.Fatalf("dispatch = %q %v asked %q", got, err, asked)
	}
}

// WeCom has chat clarification too: the start waits for it the same way.
func TestWeComChatStartWaitsForClarification(t *testing.T) {
	f, member := clarifyStartFixtureFor(t, domain.BotChannelWeCom)
	f.rounds = []map[string]any{clarifyQuestionsRound}
	id, _, conv := f.askDraft(t)
	_, out := draftChoice(t, f.h, member, id, `{"choices":[{"index":0,"workflowId":"","start":true}]}`)
	if results := strings.Join(toStrings(out["results"]), "\n"); !strings.Contains(results, "已在这个会话开始澄清") || strings.Contains(results, "已启动") {
		t.Fatalf("results = %q", results)
	}
	if sess := f.waitStatus(t, conv, domain.BotClarifyAsking); !sess.StartOnSave {
		t.Fatalf("session = %+v", sess)
	}
	if tasks := f.issueTasks(f.createdIssue(t).ID); len(tasks) != 0 {
		t.Fatalf("SOP started before clarification: %+v", tasks)
	}
}
