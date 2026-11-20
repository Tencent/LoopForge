package httpapi

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

var botDraftLink = regexp.MustCompile(`/bot-approve\.html#draft=([a-f0-9]{32})`)

// confirmModeFixture: a WeCom Bot in the confirm write mode whose model run
// submits one issues_save draft.
func confirmModeFixture(t *testing.T) (*repo.Store, *assistantHandlers, domain.NotificationBot, func() string) {
	s, h, bot, ask, _ := confirmModeFixtureFor(t, domain.BotChannelWeCom)
	return s, h, bot, ask
}

// confirmModeFixtureFor also returns the last full reply text.
func confirmModeFixtureFor(t *testing.T, channel string) (*repo.Store, *assistantHandlers, domain.NotificationBot, func() string, *string) {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "bot-draft-test-session-secret")
	s, h, bot := botFixture(t)
	bot.WriteApproval = domain.BotWriteConfirm
	if channel != domain.BotChannelWeCom {
		bot.ID, bot.BotID, bot.Channel, bot.TenantKey, bot.TenantState = "b-"+channel, "app-"+channel, channel, "tenant", domain.BotTenantLocked
	}
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	if channel != domain.BotChannelWeCom {
		// Feishu/DingTalk identities are scoped by the configuration.
		bindBotTestUser(t, s, bot, "new-wecom-user", 43)
	}
	last := new(string)
	h.api = New(s, h.engine, tracker.NewService(), "", "")
	title := 0
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		if !strings.Contains(prompt, "网页确认模式") {
			t.Fatal("confirm policy missing from prompt")
		}
		session := botTestToolSession(t, user.ID)
		if session.ExecMode.AutoWrite || session.ExecMode.ReadOnly {
			t.Fatalf("exec mode = %+v", session.ExecMode)
		}
		title++
		body, _ := json.Marshal(map[string]any{"projectId": "p", "title": "网页确认的需求" + strings.Repeat("!", title)})
		session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &assistantReply{Message: "我会创建这个需求。", Drafts: []assistantDraft{{Tool: "issues_save", Body: body}}}})
		return "", nil
	}
	ask := func() string {
		reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, domain.BotInteraction{ID: "turn-" + time.Now().Format("150405.000000"), SenderID: "new-wecom-user", Content: "帮我建个需求"})
		if err != nil {
			t.Fatal(err)
		}
		*last = reply
		m := botDraftLink.FindStringSubmatch(reply)
		if m == nil {
			t.Fatalf("no confirmation link in %q", reply)
		}
		return m[1]
	}
	return s, h, bot, ask, last
}

// Feishu and DingTalk create requirements through web confirmation too. The
// reply is plain text, so the link stands alone on its own line.
func TestPlainTextChannelsConfirmWritesOnThePlatformPage(t *testing.T) {
	for _, channel := range []string{domain.BotChannelFeishu, domain.BotChannelDingTalk} {
		t.Run(channel, func(t *testing.T) {
			s, h, _, ask, last := confirmModeFixtureFor(t, channel)
			member, _ := s.User(43)
			before := len(s.Issues())
			id := ask()
			if strings.ContainsAny(*last, "[]()") {
				t.Fatalf("markdown in plain-text reply: %q", *last)
			}
			lines := strings.Split(*last, "\n")
			if lines[len(lines)-1] != "http://platform.example/bot-approve.html#draft="+id {
				t.Fatalf("link not alone on the last line: %q", *last)
			}
			if len(s.Issues()) != before {
				t.Fatal("draft executed before confirmation")
			}
			code, done := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true)
			if code != 200 || done["status"] != repo.BotDraftExecuted || len(s.Issues()) != before+1 {
				t.Fatalf("confirm = %d %v issues=%d", code, done, len(s.Issues()))
			}
			if _, again := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true); again["status"] != repo.BotDraftExecuted || len(s.Issues()) != before+1 {
				t.Fatalf("repeat = %v", again)
			}
		})
	}
}

func draftCall(t *testing.T, h *assistantHandlers, user domain.User, method, path string, origin bool) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req = authedRequest(t, req, user)
	if !origin {
		req.Header.Del("Origin")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: strings.Split(strings.TrimPrefix(path, "/api/bot-drafts/"), "/")[0]}}
	h.botDraft(c)
	out := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestWeComConfirmModeExecutesConfirmedDraftOnce(t *testing.T) {
	s, h, _, ask := confirmModeFixture(t)
	member, _ := s.User(43)
	owner, _ := s.User(42)
	before := len(s.Issues())
	id := ask()
	if len(s.Issues()) != before {
		t.Fatal("draft executed before confirmation")
	}
	if code, _ := draftCall(t, h, owner, "GET", "/api/bot-drafts/"+id, false); code != 404 {
		t.Fatalf("other user can read the draft: %d", code)
	}
	code, view := draftCall(t, h, member, "GET", "/api/bot-drafts/"+id, false)
	if code != 200 || view["status"] != repo.BotDraftPending || len(view["operations"].([]any)) != 1 || !strings.HasPrefix(view["operations"].([]any)[0].(string), "1. 保存需求：网页确认的需求!（SOP：") {
		t.Fatalf("view = %d %v", code, view)
	}
	if _, ok := view["drafts"]; ok {
		t.Fatal("frozen draft JSON exposed")
	}
	if code, _ := draftCall(t, h, owner, "POST", "/api/bot-drafts/"+id+"/confirm", true); code != 404 {
		t.Fatalf("other user confirmed: %d", code)
	}
	if code, _ := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", false); code != 403 {
		t.Fatalf("cross-origin confirm = %d", code)
	}
	code, done := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true)
	if code != 200 || done["status"] != repo.BotDraftExecuted {
		t.Fatalf("confirm = %d %v", code, done)
	}
	if len(s.Issues()) != before+1 {
		t.Fatalf("issues = %d, want %d", len(s.Issues()), before+1)
	}
	// A repeated click returns the current state and writes nothing.
	code, again := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true)
	if code != 200 || again["status"] != repo.BotDraftExecuted || len(s.Issues()) != before+1 {
		t.Fatalf("repeat = %d %v issues=%d", code, again, len(s.Issues()))
	}
	// Cancel by the user.
	second := ask()
	if _, out := draftCall(t, h, member, "POST", "/api/bot-drafts/"+second+"/cancel", true); out["status"] != repo.BotDraftCancelled {
		t.Fatalf("cancel = %v", out)
	}
	if _, out := draftCall(t, h, member, "POST", "/api/bot-drafts/"+second+"/confirm", true); out["status"] != repo.BotDraftCancelled || len(s.Issues()) != before+1 {
		t.Fatalf("confirm after cancel = %v", out)
	}
}

func TestWeComConfirmDraftsInvalidatedByRevocationAndModeChange(t *testing.T) {
	s, h, bot, ask := confirmModeFixture(t)
	member, _ := s.User(43)
	before := len(s.Issues())
	// Losing project permission invalidates at confirmation.
	id := ask()
	project, _ := s.Project("p")
	project.Members = []domain.ProjectMember{}
	s.PutProject(project)
	if code, out := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true); code != 409 || out["code"] != "draft_invalidated" {
		t.Fatalf("after revoke = %d %v", code, out)
	}
	project.Members = []domain.ProjectMember{{UserID: 43, Role: "member"}}
	s.PutProject(project)
	// Unbinding cancels pending drafts of that sender.
	id = ask()
	binding, err := s.BotUserBinding(bot.IdentityScope(), "new-wecom-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeBotUserBinding(binding.ID, 43, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, out := draftCall(t, h, member, "POST", "/api/bot-drafts/"+id+"/confirm", true); out["status"] != repo.BotDraftCancelled || !strings.Contains(out["reason"].(string), "绑定") {
		t.Fatalf("after unbind = %v", out)
	}
	// Leaving the confirm mode cancels pending drafts (another sender).
	bindBotTestUser(t, s, bot, "second-sender", 43)
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-session", ChatType: "single"}, nil, domain.BotInteraction{ID: "turn-mode", SenderID: "second-sender", Content: "帮我建个需求"})
	m := botDraftLink.FindStringSubmatch(reply)
	if err != nil || m == nil {
		t.Fatal(reply, err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	bot.WriteApproval = domain.BotWriteReadOnly
	if err := s.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	if _, out := draftCall(t, h, member, "GET", "/api/bot-drafts/"+m[1], false); out["status"] != repo.BotDraftCancelled {
		t.Fatalf("after mode change = %v", out)
	}
	if len(s.Issues()) != before {
		t.Fatal("an invalidated draft was executed")
	}
}

func TestBotPendingDraftStateMachine(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	d, err := s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: "c", IdentityScope: "c", SenderID: "u", ProjectID: "p", UserID: 7, Summary: "1. x", Drafts: `[{"tool":"issues_save"}]`}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginBotPendingDraft(d.ID, 8, now); err == nil {
		t.Fatal("other user began the draft")
	}
	if _, err := s.BeginBotPendingDraft(d.ID, 7, now); err != nil {
		t.Fatal(err)
	}
	if got, err := s.BeginBotPendingDraft(d.ID, 7, now); err != repo.ErrBotDraftNotPending || got.Status != repo.BotDraftExecuting {
		t.Fatalf("second begin = %+v %v", got, err)
	}
	// Concurrent confirmations: exactly one moves the draft to executing.
	c, _ := s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: "c", UserID: 7, Drafts: "[]"}, now)
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := s.BeginBotPendingDraft(c.ID, 7, now); results <- err }()
	}
	won := 0
	for i := 0; i < 8; i++ {
		if err := <-results; err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("concurrent confirmations executed %d times", won)
	}
	// Interrupted while executing: reported for review, never re-run.
	if got, _ := s.BotPendingDraft(d.ID, now.Add(11*time.Minute)); got.Status != repo.BotDraftNeedsReview {
		t.Fatalf("stale executing = %+v", got)
	}
	if _, err := s.BeginBotPendingDraft(d.ID, 7, now.Add(12*time.Minute)); err != repo.ErrBotDraftNotPending {
		t.Fatalf("needs_review began again: %v", err)
	}
	// Expiry.
	e, _ := s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: "c", UserID: 7, Drafts: "[]"}, now)
	if got, err := s.BeginBotPendingDraft(e.ID, 7, now.Add(repo.BotDraftTTL)); err != repo.ErrBotDraftNotPending || got.Status != repo.BotDraftExpired {
		t.Fatalf("expired begin = %+v %v", got, err)
	}
	// Tampered frozen parameters are refused.
	tampered, _ := s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: "c", UserID: 7, Drafts: `[{"tool":"issues_save"}]`}, now)
	if err := s.DB.Table("bot_pending_drafts").Where("id = ?", tampered.ID).Update("drafts", `[{"tool":"projects_delete"}]`).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := s.BeginBotPendingDraft(tampered.ID, 7, now); err != repo.ErrBotDraftNotPending || got.Status != repo.BotDraftCancelled {
		t.Fatalf("tampered = %+v %v", got, err)
	}
}

var _ = http.MethodGet

// New requirements from chat show their SOP; the confirmation page can
// switch to another SOP of the project and start it right away.
func TestBotDraftChoosesSOPAndStartsIt(t *testing.T) {
	s, h, _, ask, last := confirmModeFixtureFor(t, domain.BotChannelFeishu)
	member, _ := s.User(43)
	if err := s.PutAgent(domain.AgentDefinition{ID: "dev-agent", Name: "开发"}); err != nil {
		t.Fatal(err)
	}
	for _, wf := range []domain.WorkflowDefinition{
		{ID: "wf-dev", Name: "研发交付", ProjectID: "p", Status: "published", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "开发", Type: "agent", AgentID: "dev-agent"}}},
		{ID: "wf-fix", Name: "缺陷修复", ProjectID: "p", Status: "published", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "修复", Type: "agent", AgentID: "dev-agent"}}},
		{ID: "wf-other", Name: "别的项目", ProjectID: "q", Status: "published"},
	} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := s.Project("p")
	project.WorkflowID = "wf-dev"
	s.PutProject(project)
	id := ask()
	if !strings.Contains(*last, "（SOP：研发交付）") || !strings.Contains(*last, "确认页可以更换 SOP") {
		t.Fatalf("summary without SOP: %q", *last)
	}
	_, view := draftCall(t, h, member, "GET", "/api/bot-drafts/"+id, false)
	issues, _ := view["newIssues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["workflowId"] != "wf-dev" {
		t.Fatalf("new issues = %v", view["newIssues"])
	}
	names := []string{}
	for _, wf := range view["workflows"].([]any) {
		names = append(names, wf.(map[string]any)["id"].(string))
	}
	if strings.Contains(strings.Join(names, ","), "wf-other") || !strings.Contains(strings.Join(names, ","), "wf-fix") {
		t.Fatalf("workflows offered = %v", names)
	}
	// A foreign SOP is refused and nothing is created.
	before := len(s.Issues())
	other := ask()
	if _, out := draftChoice(t, h, member, other, `{"choices":[{"index":0,"workflowId":"wf-other","start":true}]}`); out["status"] != repo.BotDraftFailed || len(s.Issues()) != before {
		t.Fatalf("foreign SOP = %v", out)
	}
	code, out := draftChoice(t, h, member, id, `{"choices":[{"index":0,"workflowId":"wf-fix","start":true}]}`)
	if code != 200 {
		t.Fatalf("confirm = %d %v", code, out)
	}
	var created domain.Issue
	for _, issue := range s.Issues() {
		if strings.HasPrefix(issue.Title, "网页确认的需求") && issue.WorkflowID == "wf-fix" {
			created = issue
		}
	}
	if created.ID == "" {
		t.Fatalf("issue not bound to the chosen SOP: %v", out)
	}
	results := strings.Join(toStrings(out["results"]), "\n")
	if !strings.Contains(results, "已完成：保存需求：网页确认的需求") || !strings.Contains(results, "缺陷修复") {
		t.Fatalf("results = %q", results)
	}
	started := false
	for _, task := range s.Tasks() {
		if task.IssueID == created.ID && task.WorkflowID == "wf-fix" {
			started = true
		}
	}
	if !started || !strings.Contains(results, "已启动：缺陷修复") {
		t.Fatalf("SOP not started: %q", results)
	}
}

func draftChoice(t *testing.T, h *assistantHandlers, user domain.User, id, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/bot-drafts/"+id+"/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = authedRequest(t, req, user)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.botDraft(c)
	out := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// The Feishu confirmation card: "确认并启动 SOP" creates the requirement and
// starts its SOP once; other people's clicks and later clicks do nothing;
// "取消" cancels.
func TestFeishuDraftCardButtons(t *testing.T) {
	s, h, bot, ask, _ := confirmModeFixtureFor(t, domain.BotChannelFeishu)
	if err := s.PutAgent(domain.AgentDefinition{ID: "dev-agent", Name: "开发"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "wf-dev", Name: "研发交付", ProjectID: "p", Status: "published", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "开发", Type: "agent", AgentID: "dev-agent"}}}); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("p")
	project.WorkflowID = "wf-dev"
	s.PutProject(project)
	bindBotTestUser(t, s, bot, "ou-stranger", 42)
	click := func(sender, action, id string) string {
		t.Helper()
		reply, err := h.handleBotCardAction(context.Background(), bot, domain.BotInteraction{SenderID: sender, Action: action, DeliveryID: id}, domain.BotConversation{ChatID: sender, ChatType: "single"})
		if err != nil {
			t.Fatal(reply, err)
		}
		return reply
	}
	before := len(s.Issues())
	id := ask()
	if reply := click("ou-stranger", botinteraction.DraftActionConfirm, id); !strings.Contains(reply, "不是你发起的") || len(s.Issues()) != before {
		t.Fatalf("stranger = %q", reply)
	}
	reply := click("new-wecom-user", botinteraction.DraftActionConfirmStart, id)
	if !strings.Contains(reply, "操作已执行") || !strings.Contains(reply, "已启动：研发交付") || len(s.Issues()) != before+1 {
		t.Fatalf("confirm and start = %q", reply)
	}
	started := 0
	for _, task := range s.Tasks() {
		if task.WorkflowID == "wf-dev" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("tasks = %d", started)
	}
	if again := click("new-wecom-user", botinteraction.DraftActionConfirm, id); !strings.Contains(again, "操作已执行") || len(s.Issues()) != before+1 {
		t.Fatalf("repeat = %q", again)
	}
	second := ask()
	if reply := click("new-wecom-user", botinteraction.DraftActionCancel, second); !strings.Contains(reply, "已取消") {
		t.Fatalf("cancel = %q", reply)
	}
	if reply := click("new-wecom-user", botinteraction.DraftActionConfirm, second); !strings.Contains(reply, "已取消") || len(s.Issues()) != before+1 {
		t.Fatalf("confirm after cancel = %q", reply)
	}
}
