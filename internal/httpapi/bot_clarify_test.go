package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// clarifyChatFixture: a Feishu or DingTalk Bot of project LOOP with the
// project owner (42, ou_owner) and a member (43, ou_member) bound, the
// requirement LOOP-7, and scripted clarification rounds.
type clarifyChatFixture struct {
	s       *repo.Store
	h       *assistantHandlers
	bot     domain.NotificationBot
	mu      sync.Mutex
	rounds  []map[string]any
	inputs  []chatClarifyRequest
	classes []string
}

func newClarifyChatFixture(t *testing.T, channel string) *clarifyChatFixture {
	t.Helper()
	s, h, bot := botFixture(t)
	bot.ID, bot.BotID, bot.Channel, bot.TenantKey, bot.TenantState = "b-clarify-"+channel, "app-clarify-"+channel, channel, "tenant", domain.BotTenantLocked
	bot.WriteApproval = domain.BotWriteConfirm
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	bindBotTestUser(t, s, bot, "ou_member", 43)
	bindBotTestUser(t, s, bot, "ou_owner", 42)
	project, _ := s.Project("p")
	project.Key = "LOOP"
	s.PutProject(project)
	if err := s.PutIssue(domain.Issue{ID: "iss-7", ProjectID: "p", Number: 7, Title: "登录优化", Description: "支持手机号登录"}); err != nil {
		t.Fatal(err)
	}
	f := &clarifyChatFixture{s: s, h: h, bot: bot}
	stream := &streamHandlers{deps: h.deps}
	stream.jobs = newClarifyHub(stream)
	stream.clarifyRun = func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.inputs = append(f.inputs, request)
		if len(f.rounds) == 0 {
			t.Error("unexpected clarification round")
			return nil, "", nil, context.Canceled
		}
		round := f.rounds[0]
		f.rounds = f.rounds[1:]
		raw, _ := json.Marshal(round)
		return round, string(raw), nil, nil
	}
	h.clarify = stream
	h.clarifyClassify = func(ctx context.Context, user domain.User, payload string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.classes) == 0 {
			t.Errorf("unexpected classification of %s", payload)
			return "", context.Canceled
		}
		out := f.classes[0]
		f.classes = f.classes[1:]
		return out, nil
	}
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return "普通聊天回复", nil
	}
	return f
}

var clarifyQuestionsRound = map[string]any{"summary": "需要确认验收和短信", "ready": false, "questions": []any{
	map[string]any{"id": "q1", "question": "验收怎么确认？", "options": []any{"接口回归", "手工验收"}},
	map[string]any{"id": "q2", "question": "要不要短信验证码？", "options": []any{"要", "不要"}},
}}

var clarifyReadyRound = map[string]any{"summary": "只做手机号加短信验证码登录，接口回归验收。", "ready": true, "questions": []any{}}

// message persists a chat message and returns it with its conversation.
func (f *clarifyChatFixture) message(t *testing.T, kind, chat, sender, text string) (domain.BotInteraction, domain.BotConversation) {
	t.Helper()
	item := domain.BotInteraction{ID: "msg-" + time.Now().Format("150405.000000000"), BotID: f.bot.ID, SenderID: sender, Content: text}
	if _, err := f.s.EnqueueBotInteraction(item, kind, chat); err != nil {
		t.Fatal(err)
	}
	stored, _ := f.s.BotInteraction(item.ID)
	conv, _ := f.s.BotConversation(stored.ConversationID)
	return stored, conv
}

func (f *clarifyChatFixture) say(t *testing.T, kind, chat, sender, text string) (string, domain.BotInteraction) {
	t.Helper()
	item, conv := f.message(t, kind, chat, sender, text)
	reply, err := f.h.generateBotReply(context.Background(), f.bot, conv, nil, item)
	if err != nil {
		t.Fatal(err)
	}
	return reply, item
}

func (f *clarifyChatFixture) waitStatus(t *testing.T, conv string, want string) domain.BotClarifySession {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var sess domain.BotClarifySession
		var ok bool
		if sess, ok = f.s.ActiveBotClarifySession(conv); !ok {
			sess, _ = f.lastSession(conv)
		}
		if sess.Status == want {
			return sess
		}
		time.Sleep(20 * time.Millisecond)
	}
	sess, _ := f.lastSession(conv)
	t.Fatalf("session status = %q, want %q (%+v)", sess.Status, want, sess)
	return sess
}

func (f *clarifyChatFixture) lastSession(conv string) (domain.BotClarifySession, bool) {
	sessions, _ := f.s.OpenBotClarifySessions(50)
	for _, s := range sessions {
		if s.ConversationID == conv {
			return s, true
		}
	}
	return f.s.ActiveBotClarifySession(conv)
}

// posts lists the clarification messages queued in a conversation.
func (f *clarifyChatFixture) posts(t *testing.T, convID string) []domain.BotInteraction {
	t.Helper()
	records, _ := f.s.BotInteractionRecords(f.bot.ID)
	out := []domain.BotInteraction{}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Action == botinteraction.ActionClarifyPost && records[i].ConversationID == convID {
			out = append(out, records[i])
		}
	}
	return out
}

// Group clarification on Feishu: questions are posted to the group, numbered
// answers and confident judgements are recorded with their speaker, an
// uncertain message is confirmed by its speaker on a card, the next round
// starts once everything is answered, and only the responsible person,
// the starter or a maintainer saves the conclusion, once.
func TestChatClarificationInAGroup(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelFeishu)
	f.rounds = []map[string]any{clarifyQuestionsRound, clarifyReadyRound}
	// The owner starts it in the group.
	reply, start := f.say(t, "group", "oc_group", "ou_owner", "澄清 LOOP-7")
	if !strings.Contains(reply, "已开始澄清 LOOP-7「登录优化」") || !strings.Contains(reply, "@我 回答") {
		t.Fatalf("start = %q", reply)
	}
	conv := start.ConversationID
	sess := f.waitStatus(t, conv, domain.BotClarifyAsking)
	if sess.Round != 1 || len(sess.Questions) != 2 || sess.Questions[1].N != 2 || sess.StarterUserID != 42 {
		t.Fatalf("session = %+v", sess)
	}
	posts := f.posts(t, conv)
	if len(posts) != 1 || posts[0].Quote != botinteraction.ClarifyPostQuestions || posts[0].SendRoute != repo.BotSendRoutePushChat || posts[0].SenderID != "ou_owner" {
		t.Fatalf("posts = %+v", posts)
	}
	for _, want := range []string{"LOOP-7「登录优化」 · 第 1 轮澄清", "Q1 验收怎么确认？", "选项：接口回归 / 手工验收", "Q2 要不要短信验证码？", "@机器人 回复（引用）这条问题", "提交回答，进入下一轮"} {
		if !strings.Contains(posts[0].Content, want) {
			t.Fatalf("questions text misses %q: %s", want, posts[0].Content)
		}
	}
	if issue, _ := f.s.Issue("iss-7"); issue.Clarification.Status != domain.ClarificationInProgress || len(issue.Clarification.Questions) != 2 {
		t.Fatalf("issue clarification = %+v", issue.Clarification)
	}
	// A second start in the same group is refused.
	if reply, _ := f.say(t, "group", "oc_group", "ou_member", "澄清 LOOP-7"); !strings.Contains(reply, "已经在澄清") {
		t.Fatalf("second start = %q", reply)
	}
	// A sentence that merely starts with 澄清 is normal chat.
	f.classes = []string{`{"question":0,"guess":0,"confidence":0.95}`}
	if reply, _ := f.say(t, "group", "oc_group", "ou_member", "澄清一下今天谁值班"); reply != "普通聊天回复" {
		t.Fatalf("chatter = %q", reply)
	}
	// Rule ②: a numbered answer.
	if reply, _ := f.say(t, "group", "oc_group", "ou_member", "Q1：接口回归"); !strings.Contains(reply, "已记录为 Q1 的回答（other-user）") || !strings.Contains(reply, "还有 Q2 待回答") {
		t.Fatalf("numbered = %q", reply)
	}
	// Rule ⑤: the model is unsure, the speaker is asked.
	f.classes = []string{`{"question":0,"guess":2,"confidence":0.4}`}
	reply, unsure := f.say(t, "group", "oc_group", "ou_member", "我觉得得发个短信吧")
	if !strings.Contains(reply, "这是对 Q2 的回答吗？") || !strings.Contains(reply, "请点卡片上的按钮确认") {
		t.Fatalf("unsure = %q", reply)
	}
	sess, _ = f.s.ActiveBotClarifySession(conv)
	if len(sess.Pending) != 1 || sess.Pending[0].ID != unsure.ID || sess.Pending[0].N != 2 || sess.AllAnswered() {
		t.Fatalf("pending = %+v", sess)
	}
	// Only the speaker's click counts; it records Q2, everything is
	// answered and the next round starts with attributed answers.
	reply, err := f.h.handleBotCardAction(context.Background(), f.bot, domain.BotInteraction{SenderID: "ou_member", Action: botinteraction.ClarifyActionYes, DeliveryID: sess.ID, Quote: unsure.ID}, domain.BotConversation{ID: conv, ChatType: "group"})
	if err != nil || !strings.Contains(reply, "已记录为 Q2 的回答") || !strings.Contains(reply, "AI 正在分析第 2 轮") {
		t.Fatalf("confirm = %q %v", reply, err)
	}
	sess = f.waitStatus(t, conv, domain.BotClarifyConcluded)
	f.mu.Lock()
	second := f.inputs[1].Message
	f.mu.Unlock()
	for _, want := range []string{"Q1 验收怎么确认？\n- other-user：接口回归", "Q2 要不要短信验证码？\n- other-user：我觉得得发个短信吧", "矛盾点"} {
		if !strings.Contains(second, want) {
			t.Fatalf("round input misses %q: %s", want, second)
		}
	}
	if sess.Summary != "只做手机号加短信验证码登录，接口回归验收。" {
		t.Fatalf("summary = %q", sess.Summary)
	}
	// The conclusion is not in the requirement until someone saves it.
	issue, _ := f.s.Issue("iss-7")
	if issue.Clarification.Status == domain.ClarificationReady || strings.Contains(issue.Description, "只做手机号") {
		t.Fatalf("conclusion saved without a click: %+v", issue.Clarification)
	}
	posts = f.posts(t, conv)
	last := posts[len(posts)-1]
	if last.Quote != botinteraction.ClarifyPostConclusion || !strings.Contains(last.Content, "保存澄清结果") {
		t.Fatalf("conclusion post = %+v", last)
	}
	// A plain member may not save (the owner started it; nobody is assigned).
	if reply, _ := f.say(t, "group", "oc_group", "ou_member", "保存澄清"); !strings.Contains(reply, "只有需求负责人、发起澄清的人或项目维护者可以保存") {
		t.Fatalf("member save = %q", reply)
	}
	// Switched to read-only after the conclusion: neither the card nor the
	// text command writes the requirement or starts the SOP.
	readOnly := f.bot
	readOnly.WriteApproval = domain.BotWriteReadOnly
	if err := f.s.SaveNotificationBot(readOnly, "secret"); err != nil {
		t.Fatal(err)
	}
	runs := len(f.s.Runs())
	card, _ := f.h.handleBotCardAction(context.Background(), f.bot, domain.BotInteraction{SenderID: "ou_owner", Action: botinteraction.ClarifyActionSaveStart, DeliveryID: sess.ID, Quote: last.ID}, domain.BotConversation{ID: conv, ChatType: "group"})
	text, _ := f.say(t, "group", "oc_group", "ou_owner", "保存并启动")
	for _, got := range []string{card, text} {
		if !strings.Contains(got, "只读查询，澄清结果没有保存") {
			t.Fatalf("read-only save = %q", got)
		}
	}
	issue, _ = f.s.Issue("iss-7")
	if issue.Clarification.Status == domain.ClarificationReady || strings.Contains(issue.Description, "只做手机号") || len(f.s.Runs()) != runs {
		t.Fatalf("read-only bot wrote: %+v runs=%d", issue.Clarification, len(f.s.Runs()))
	}
	if current, ok := f.s.ActiveBotClarifySession(conv); !ok || current.Status != domain.BotClarifyConcluded {
		t.Fatalf("session after read-only save = %+v %v", current, ok)
	}
	if err := f.s.SaveNotificationBot(f.bot, "secret"); err != nil {
		t.Fatal(err)
	}
	reply, err = f.h.handleBotCardAction(context.Background(), f.bot, domain.BotInteraction{SenderID: "ou_owner", Action: botinteraction.ClarifyActionSave, DeliveryID: sess.ID, Quote: last.ID}, domain.BotConversation{ID: conv, ChatType: "group"})
	if err != nil || !strings.Contains(reply, "澄清结果已保存到需求 LOOP-7「登录优化」（bot-owner）") {
		t.Fatalf("save = %q %v", reply, err)
	}
	issue, _ = f.s.Issue("iss-7")
	if issue.Clarification.Status != domain.ClarificationReady || !strings.Contains(issue.Description, "只做手机号加短信验证码登录") || issue.Clarification.WriteBackStatus != "skipped" {
		t.Fatalf("saved issue = %+v", issue.Clarification)
	}
	if again, _ := f.h.handleBotCardAction(context.Background(), f.bot, domain.BotInteraction{SenderID: "ou_owner", Action: botinteraction.ClarifyActionSave, DeliveryID: sess.ID, Quote: last.ID}, domain.BotConversation{ID: conv, ChatType: "group"}); !strings.Contains(again, "已结束") {
		t.Fatalf("second save = %q", again)
	}
	if _, ok := f.s.ActiveBotClarifySession(conv); ok {
		t.Fatal("session still active after saving")
	}
}

// In a private chat every message is an answer (no judgement), commands
// work as text, and DingTalk texts name the commands instead of buttons.
func TestChatClarificationInPrivateChat(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelDingTalk)
	f.rounds = []map[string]any{clarifyQuestionsRound, clarifyQuestionsRound}
	if reply, _ := f.say(t, "single", "cid-p2p", "ou_member", "澄清进度"); reply != botClarifyNoSession {
		t.Fatalf("no session = %q", reply)
	}
	if reply, _ := f.say(t, "single", "cid-p2p", "ou_member", "澄清 LOOP-99"); !strings.Contains(reply, "没有找到需求「LOOP-99」") {
		t.Fatalf("unknown issue = %q", reply)
	}
	reply, start := f.say(t, "single", "cid-p2p", "ou_member", "澄清 #7")
	if !strings.Contains(reply, "已开始澄清 LOOP-7") {
		t.Fatalf("start = %q", reply)
	}
	conv := start.ConversationID
	sess := f.waitStatus(t, conv, domain.BotClarifyAsking)
	posts := f.posts(t, conv)
	if len(posts) != 1 || !strings.Contains(posts[0].Content, "直接回复即可") || !strings.Contains(posts[0].Content, "发「下一轮」") || strings.Contains(posts[0].Content, "卡片") {
		t.Fatalf("dingtalk questions = %+v", posts)
	}
	// Two numbered answers in one message, then an unnumbered addition.
	if reply, _ := f.say(t, "single", "cid-p2p", "ou_member", "1. 手工验收\n2. 不要"); !strings.Contains(reply, "已记录为 Q1、Q2 的回答") || !strings.Contains(reply, "AI 正在分析第 2 轮") {
		t.Fatalf("both answers = %q", reply)
	}
	sess = f.waitStatus(t, conv, domain.BotClarifyAsking)
	if sess.Round != 2 {
		t.Fatalf("round = %d", sess.Round)
	}
	if reply, _ := f.say(t, "single", "cid-p2p", "ou_member", "另外老用户也要能登录"); !strings.Contains(reply, "已记录补充说明") {
		t.Fatalf("general = %q", reply)
	}
	sess, _ = f.s.ActiveBotClarifySession(conv)
	if len(sess.General) != 1 || sess.General[0].Name != "other-user" {
		t.Fatalf("general answers = %+v", sess.General)
	}
	if status, _ := f.say(t, "single", "cid-p2p", "ou_member", "澄清进度"); !strings.Contains(status, "第 2 轮") || !strings.Contains(status, "Q1 验收怎么确认？：待回答") {
		t.Fatalf("status = %q", status)
	}
	// The starter may end it.
	if reply, _ := f.say(t, "single", "cid-p2p", "ou_member", "结束澄清"); !strings.Contains(reply, "已结束澄清") {
		t.Fatalf("end = %q", reply)
	}
	if reply, _ := f.say(t, "single", "cid-p2p", "ou_member", "下一轮"); reply != botClarifyNoSession {
		t.Fatalf("after end = %q", reply)
	}
}

// The timers: the next round three minutes after the last answer, a pause
// after thirty quiet minutes, and "继续澄清" resumes.
func TestChatClarificationTimers(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelFeishu)
	f.rounds = []map[string]any{clarifyQuestionsRound, clarifyQuestionsRound}
	_, start := f.say(t, "single", "oc_p2p", "ou_member", "澄清 LOOP-7")
	conv := start.ConversationID
	sess := f.waitStatus(t, conv, domain.BotClarifyAsking)
	now := time.Now().UTC()
	f.h.botClarifyTick(now)
	if current, _ := f.s.ActiveBotClarifySession(conv); current.Status != domain.BotClarifyAsking || current.Version != sess.Version {
		t.Fatalf("tick changed a fresh session: %+v", current)
	}
	f.say(t, "single", "oc_p2p", "ou_member", "Q1：手工验收")
	f.h.botClarifyTick(now.Add(4 * time.Minute))
	sess = f.waitStatus(t, conv, domain.BotClarifyAsking)
	if sess.Round != 2 {
		t.Fatalf("idle answers did not start the next round: %+v", sess)
	}
	posts := f.posts(t, conv)
	notice := false
	for _, p := range posts {
		if p.Quote == botinteraction.ClarifyPostNotice && strings.Contains(p.Content, "3 分钟没有新回答") {
			notice = true
		}
	}
	if !notice {
		t.Fatalf("no notice for the automatic round: %+v", posts)
	}
	f.h.botClarifyTick(time.Now().UTC().Add(31 * time.Minute))
	if paused, _ := f.s.ActiveBotClarifySession(conv); paused.Status != domain.BotClarifyPaused {
		t.Fatalf("not paused: %+v", paused)
	}
	if reply, _ := f.say(t, "single", "oc_p2p", "ou_member", "继续澄清"); !strings.Contains(reply, "已恢复澄清") {
		t.Fatalf("resume = %q", reply)
	}
	if resumed, _ := f.s.ActiveBotClarifySession(conv); resumed.Status != domain.BotClarifyAsking {
		t.Fatalf("resumed = %+v", resumed)
	}
}

// A requirement confirmed and started from a group remembers the group, so
// the run's progress is sent back there.
func TestDraftStartedFromGroupRecordsTheChat(t *testing.T) {
	s, h, bot, _, _ := confirmModeFixtureFor(t, domain.BotChannelFeishu)
	member, _ := s.User(43)
	if err := s.PutAgent(domain.AgentDefinition{ID: "dev-agent", Name: "开发"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "wf-dev", Name: "研发交付", ProjectID: "p", Status: "published", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "开发", Type: "agent", AgentID: "dev-agent"}}}); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("p")
	project.WorkflowID = "wf-dev"
	s.PutProject(project)
	item := domain.BotInteraction{ID: "group-ask", BotID: bot.ID, SenderID: "new-wecom-user", Content: "帮我建个需求"}
	if _, err := s.EnqueueBotInteraction(item, "group", "oc_team"); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.BotInteraction(item.ID)
	conv, _ := s.BotConversation(stored.ConversationID)
	reply, err := h.generateBotReply(context.Background(), bot, conv, nil, stored)
	if err != nil {
		t.Fatal(err)
	}
	m := botDraftLink.FindStringSubmatch(reply)
	if m == nil {
		t.Fatalf("no draft: %q", reply)
	}
	if code, out := draftChoice(t, h, member, m[1], `{"choices":[{"index":0,"workflowId":"","start":true}]}`); code != 200 || out["status"] != repo.BotDraftExecuted {
		t.Fatalf("confirm = %d %v", code, out)
	}
	issueID, ok := s.LatestBotChatIssue(conv.ID)
	if !ok {
		t.Fatal("requirement origin not recorded")
	}
	var runID string
	for _, task := range s.Tasks() {
		if task.IssueID == issueID {
			runID = task.RunID
		}
	}
	if runID == "" {
		t.Skip("dispatch did not report a run in this environment")
	}
	origin, ok := s.BotRunOrigin(runID)
	if !ok || origin.ChatType != "group" || origin.ChatID != "oc_team" || origin.SenderID != "new-wecom-user" || origin.ConfigID != bot.ID {
		t.Fatalf("run origin = %+v %v", origin, ok)
	}
	if run, _ := s.RunState(runID); run.ExecutionContext.BotOriginID != repo.BotIssueOriginID(conv.ID, issueID) {
		t.Fatalf("run start origin = %q", run.ExecutionContext.BotOriginID)
	}
}

// Edge paths: requirement references, quoted cards, a failed round and its
// retry, an interrupted analysis rerun by the timer, status lines, the end
// while analyzing, and saving a tracker requirement with "保存并启动".
func TestChatClarificationEdgePaths(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelFeishu)
	if text := botClarifyText("@_user_1 @机器人 澄清 LOOP-7"); text != "澄清 LOOP-7" {
		t.Fatalf("mentions = %q", text)
	}
	if botClarifyText("@机器人") != "" || botClarifyControl("下一轮。") != "next" || botClarifyControl("保存并启动 SOP") != "save_start" {
		t.Fatal("command parsing")
	}
	if got := parseBotClarifyAnswers("前言\nQ1 甲\n第2题：乙\n3、丙", 2); len(got) != 3 || got[0].N != 0 || got[1].Text != "甲" || got[2].N != 2 || !strings.Contains(got[2].Text, "3、丙") {
		t.Fatalf("parse = %+v", got)
	}
	// "这个需求" needs a requirement from this conversation.
	if reply, _ := f.say(t, "group", "oc_edge", "ou_owner", "澄清这个需求"); !strings.Contains(reply, "请带上需求编号") {
		t.Fatalf("this issue without origin = %q", reply)
	}
	// The issue is synced from TAPD; the project owner starts by title.
	issue, _ := f.s.Issue("iss-7")
	issue.Source, issue.ExternalRef = "tapd", &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}
	f.s.PutIssue(issue)
	f.rounds = []map[string]any{clarifyQuestionsRound}
	reply, start := f.say(t, "group", "oc_edge", "ou_owner", "澄清 登录优化")
	if !strings.Contains(reply, "已开始澄清 1001「登录优化」") {
		t.Fatalf("start by title = %q", reply)
	}
	conv := start.ConversationID
	if status, _ := f.say(t, "group", "oc_edge", "ou_owner", "澄清进度"); !strings.Contains(status, "正在分析") && !strings.Contains(status, "第 1 轮") {
		t.Fatalf("status = %q", status)
	}
	sess := f.waitStatus(t, conv, domain.BotClarifyAsking)
	// Rule ①: a quote of the question card is an answer; the model only
	// picks the question.
	post := f.posts(t, conv)[0]
	f.s.DB.Model(&domain.BotInteraction{}).Where("id = ?", post.ID).Update("platform_message_id", "om_questions")
	item := domain.BotInteraction{ID: "msg-quote", BotID: f.bot.ID, SenderID: "ou_member", Content: "要短信", ReplyTo: "om_questions"}
	f.s.EnqueueBotInteraction(item, "group", "oc_edge")
	quoted, _ := f.s.BotInteraction(item.ID)
	cconv, _ := f.s.BotConversation(quoted.ConversationID)
	f.classes = []string{`{"question":2,"guess":2,"confidence":0.9}`}
	if reply, err := f.h.generateBotReply(context.Background(), f.bot, cconv, nil, quoted); err != nil || !strings.Contains(reply, "已记录为 Q2 的回答") {
		t.Fatalf("quoted = %q %v", reply, err)
	}
	// A failed round keeps the answers and can be retried.
	f.rounds = []map[string]any{nil}
	f.h.clarify.clarifyRun = func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.inputs = append(f.inputs, request)
		round := f.rounds[0]
		f.rounds = f.rounds[1:]
		if round == nil {
			return nil, "", nil, context.DeadlineExceeded
		}
		raw, _ := json.Marshal(round)
		return round, string(raw), nil, nil
	}
	if reply, _ := f.say(t, "group", "oc_edge", "ou_member", "下一轮"); !strings.Contains(reply, "AI 正在分析第 2 轮") {
		t.Fatalf("next = %q", reply)
	}
	sess = f.waitStatus(t, conv, domain.BotClarifyAsking)
	if sess.Error == "" || sess.Round != 1 || sess.AnswerCount() != 1 {
		t.Fatalf("failed round = %+v", sess)
	}
	posts := f.posts(t, conv)
	if last := posts[len(posts)-1]; last.Quote != botinteraction.ClarifyPostNotice || !strings.Contains(last.Content, "没有完成") {
		t.Fatalf("failure notice = %+v", last)
	}
	if status, _ := f.say(t, "group", "oc_edge", "ou_member", "澄清进度"); !strings.Contains(status, "上一轮分析没有完成") || !strings.Contains(status, "已回答（other-user）") {
		t.Fatalf("status after failure = %q", status)
	}
	// An interrupted analysis is run again by the timer.
	f.rounds = []map[string]any{clarifyReadyRound}
	f.s.UpdateBotClarifySession(sess.ID, func(s *domain.BotClarifySession) error {
		s.Status, s.RoundMessage = domain.BotClarifyAnalyzing, "补充：要短信"
		return nil
	})
	f.h.botClarifyTick(time.Now().UTC().Add(botClarifyStaleRound + time.Minute))
	sess = f.waitStatus(t, conv, domain.BotClarifyConcluded)
	f.mu.Lock()
	rerun := f.inputs[len(f.inputs)-1].Message
	f.mu.Unlock()
	if rerun != "补充：要短信" {
		t.Fatalf("rerun input = %q", rerun)
	}
	if status, _ := f.say(t, "group", "oc_edge", "ou_member", "澄清进度"); !strings.Contains(status, "等待需求负责人") {
		t.Fatalf("concluded status = %q", status)
	}
	if reply, _ := f.say(t, "group", "oc_edge", "ou_owner", "继续澄清"); !strings.Contains(reply, "澄清结论重新发在下面") {
		t.Fatalf("resume concluded = %q", reply)
	}
	// Save and start: written back fails without tracker sync, the SOP
	// starts as the clicker and the run's chat is recorded.
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "dev-agent", Name: "开发"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "wf-dev", Name: "研发交付", ProjectID: "p", Status: "published", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "开发", Type: "agent", AgentID: "dev-agent"}}}); err != nil {
		t.Fatal(err)
	}
	issue, _ = f.s.Issue("iss-7")
	issue.WorkflowID = "wf-dev"
	f.s.PutIssue(issue)
	reply, _ = f.say(t, "group", "oc_edge", "ou_owner", "保存并启动")
	if !strings.Contains(reply, "澄清结果已保存") || !strings.Contains(reply, "写回原单失败") {
		t.Fatalf("save and start = %q", reply)
	}
	issue, _ = f.s.Issue("iss-7")
	if issue.Clarification.Status != domain.ClarificationReady || issue.Clarification.WriteBackStatus != "failed" {
		t.Fatalf("issue = %+v", issue.Clarification)
	}
	if id, ok := f.s.LatestBotChatIssue(conv); !ok || id != "iss-7" {
		t.Fatalf("origin of the started requirement = %q %v", id, ok)
	}
	// End while analyzing cancels the round; nothing is posted afterwards.
	f.rounds = []map[string]any{clarifyQuestionsRound}
	block := make(chan struct{})
	f.h.clarify.clarifyRun = func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		<-ctx.Done()
		close(block)
		return nil, "", nil, ctx.Err()
	}
	issue.Clarification.Status = domain.ClarificationInProgress
	f.s.PutIssue(issue)
	if reply, _ := f.say(t, "single", "oc_p2p_edge", "ou_member", "澄清 LOOP-7"); !strings.Contains(reply, "已开始澄清") {
		t.Fatalf("restart = %q", reply)
	}
	if reply, _ := f.say(t, "single", "oc_p2p_edge", "ou_member", "随便说一句"); !strings.Contains(reply, "AI 正在分析") {
		t.Fatalf("during analysis = %q", reply)
	}
	if reply, _ := f.say(t, "single", "oc_p2p_edge", "ou_member", "结束澄清"); !strings.Contains(reply, "已结束澄清") {
		t.Fatalf("end = %q", reply)
	}
	select {
	case <-block:
	case <-time.After(5 * time.Second):
		t.Fatal("analysis not cancelled")
	}
}

// WeCom clarifies like Feishu: cards with buttons, and a quote of the
// question message (WeCom quotes carry only the text) counts as an answer.
func TestChatClarificationOnWeCom(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelWeCom)
	f.rounds = []map[string]any{clarifyQuestionsRound}
	reply, start := f.say(t, "group", "wr_group", "ou_owner", "澄清 LOOP-7")
	if !strings.Contains(reply, "已开始澄清 LOOP-7") {
		t.Fatalf("start = %q", reply)
	}
	conv := start.ConversationID
	f.waitStatus(t, conv, domain.BotClarifyAsking)
	post := f.posts(t, conv)[0]
	if !strings.Contains(post.Content, "点卡片上的「提交回答，进入下一轮」") || post.SendRoute != repo.BotSendRoutePushChat {
		t.Fatalf("questions = %+v", post)
	}
	item := domain.BotInteraction{ID: "wecom-quote", BotID: f.bot.ID, SenderID: "ou_member", Content: "要短信", Quote: strings.SplitN(post.Content, "\n", 2)[0] + "\nQ1 验收怎么确认？"}
	f.s.EnqueueBotInteraction(item, "group", "wr_group")
	quoted, _ := f.s.BotInteraction(item.ID)
	cconv, _ := f.s.BotConversation(quoted.ConversationID)
	f.classes = []string{`{"question":2,"guess":2,"confidence":0.4}`}
	reply, err := f.h.generateBotReply(context.Background(), f.bot, cconv, nil, quoted)
	if err != nil || !strings.Contains(reply, "这是对 Q2 的回答吗？") || !strings.Contains(reply, "请点卡片上的按钮确认") {
		t.Fatalf("quoted = %q %v", reply, err)
	}
	// The speaker answers the question in text.
	if reply, _ := f.say(t, "group", "wr_group", "ou_member", "是"); !strings.Contains(reply, "已记录为 Q2 的回答") {
		t.Fatalf("yes = %q", reply)
	}
}

// A chat clarification runs as the bound account of the member who started
// it, not the requirement's responsible person, and stops once that member
// lost the project's execution permission. The platform clarification keeps
// the responsible person.
func TestChatClarificationRunsAsTheStarter(t *testing.T) {
	f := newClarifyChatFixture(t, domain.BotChannelDingTalk)
	f.rounds = []map[string]any{clarifyQuestionsRound}
	reply, start := f.say(t, "single", "cid-starter", "ou_member", "澄清 #7")
	if !strings.Contains(reply, "已开始澄清 LOOP-7") {
		t.Fatalf("start = %q", reply)
	}
	sess := f.waitStatus(t, start.ConversationID, domain.BotClarifyAsking)
	f.mu.Lock()
	actor := f.inputs[0].ActorUserID
	f.mu.Unlock()
	if actor != 43 {
		t.Fatalf("round ran as %d, want the starter 43", actor)
	}
	issue, _ := f.s.Issue("iss-7")
	if user, err := f.h.clarify.clarifyUser(issue, chatClarifyRequest{ActorUserID: 43}); err != nil || user.ID != 43 {
		t.Fatalf("chat clarification user = %+v %v", user, err)
	}
	responsible, _ := f.s.ResponsibleUser(issue)
	if user, err := f.h.clarify.clarifyUser(issue, chatClarifyRequest{}); err != nil || user.ID != responsible.ID {
		t.Fatalf("platform clarification user = %+v %v", user, err)
	}

	project, _ := f.s.Project("p")
	members := project.Members[:0]
	for _, m := range project.Members {
		if m.UserID != 43 {
			members = append(members, m)
		}
	}
	project.Members = members
	f.s.PutProject(project)
	if err := f.h.runBotClarifyRound(f.bot, sess, "补充：要短信"); err == nil || !strings.Contains(err.Error(), "发起澄清的成员不能继续") {
		t.Fatalf("round after removal = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) != 1 {
		t.Fatalf("analysis ran after the starter was removed: %d rounds", len(f.inputs))
	}
}
