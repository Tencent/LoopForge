package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
)

type nodeQuestionFixture struct {
	t    *testing.T
	s    *repo.Store
	h    *assistantHandlers
	bot  domain.NotificationBot
	conv domain.BotConversation
}

const nodeQuestionText = "登录方式只支持手机号，还是也要支持邮箱？"

// newNodeQuestionFixture starts from a run launched in a group chat of a
// Bot of the given channel: ou-member (project member) and ou-viewer
// (viewer) are bound.
func newNodeQuestionFixture(t *testing.T, channel string) *nodeQuestionFixture {
	t.Helper()
	s, h, bot := botFixture(t)
	bot.ID, bot.BotID, bot.Channel, bot.TenantKey, bot.TenantState = "b-q-"+channel, "app-q-"+channel, channel, "tk", domain.BotTenantLocked
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	s.PutUser(domain.User{ID: 44, LoginName: "viewer-user"})
	project, _ := s.Project("p")
	project.Members = append(project.Members, domain.ProjectMember{UserID: 44, Role: "viewer"})
	s.PutProject(project)
	bindBotTestUser(t, s, bot, "ou-member", 43)
	bindBotTestUser(t, s, bot, "ou-viewer", 44)
	if err := s.PutIssue(domain.Issue{ID: "iss-q", ProjectID: "p", Number: 9, Title: "登录优化"}); err != nil {
		t.Fatal(err)
	}
	return &nodeQuestionFixture{t: t, s: s, h: h, bot: bot, conv: domain.BotConversation{ID: "conv-q", BotID: bot.ID, ChatID: "oc_group", ChatType: "group"}}
}

// ask records the run as started from the group and lets its node "define"
// submit a human decision with the question, as the engine does.
func (f *nodeQuestionFixture) ask(runID, question string) {
	f.t.Helper()
	if err := f.s.RecordBotChatOrigin(domain.BotChatOrigin{ConfigID: f.bot.ID, ProjectID: "p", ConversationID: f.conv.ID, ChatType: "group", ChatID: "oc_group", SenderID: "ou-member", RunID: runID}); err != nil {
		f.t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "define", Name: "需求定型", Type: "agent"}
	run := domain.Run{ID: runID, WorkflowID: "wf", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "p", IssueID: "iss-q"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", ProjectID: "p", Name: "通用交付", Nodes: []domain.WorkflowNode{node}},
		Nodes:  map[string]*domain.NodeRun{"define": {NodeID: "define", Status: "waiting_approval", Attempt: 1, Activity: "等待人工确认", GateResult: &domain.GateResult{Decision: "human", Reason: "需求没写登录方式", ActionRequired: question}}},
		Events: []domain.RunEvent{{ID: runID + "-e1", NodeID: "define", Type: "gate.waiting", Message: "Agent 请求人工确认：" + question, Timestamp: time.Now().UTC()}}}
	if err := f.s.PutRun(run); err != nil {
		f.t.Fatal(err)
	}
}

// question sends the pending messages (Feishu message om_<run>) and
// returns the question sent for the run.
func (f *nodeQuestionFixture) question(runID string) domain.NotificationDelivery {
	f.t.Helper()
	for {
		claimed, err := f.s.ClaimNotification(time.Now().UTC().Add(time.Second), "lease-"+runID)
		if err != nil || claimed == nil {
			break
		}
		if err := f.s.FinishNotificationPush(*claimed, repo.NotificationPushResult{Status: "sent", PlatformMessageID: "om_" + claimed.RunID, Next: time.Now().UTC()}); err != nil {
			f.t.Fatal(err)
		}
	}
	items, _ := f.s.NotificationDeliveries("p")
	for _, item := range items {
		if item.RunID == runID && item.Origin == domain.NotificationOriginChat && item.EventType == "waiting" {
			return item
		}
	}
	f.t.Fatalf("no question sent for %s: %+v", runID, items)
	return domain.NotificationDelivery{}
}

func (f *nodeQuestionFixture) say(sender, text string, quote ...domain.NotificationDelivery) string {
	f.t.Helper()
	item := domain.BotInteraction{ID: "msg-" + time.Now().Format("150405.000000000"), BotID: f.bot.ID, ConversationID: f.conv.ID, SenderID: sender, Content: "@Bot " + text}
	if len(quote) > 0 {
		item.Content, item.ReplyTo = text, quote[0].PlatformMessageID
	}
	reply, err := f.h.generateBotReply(context.Background(), f.bot, f.conv, nil, item)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply
}

func (f *nodeQuestionFixture) node(runID string) *domain.NodeRun {
	run, _ := f.s.RunState(runID)
	return run.Nodes["define"]
}

func (f *nodeQuestionFixture) runs() int {
	return len(f.s.Runs())
}

// The acceptance case: the node asks, the group receives the question,
// a member answers in the group, and the same node of the same run gets the
// answer and continues. No other run is created.
func TestGroupAnswersNodeQuestionOnTheSameRun(t *testing.T) {
	for _, channel := range []string{domain.BotChannelFeishu, domain.BotChannelDingTalk} {
		t.Run(channel, func(t *testing.T) {
			f := newNodeQuestionFixture(t, channel)
			f.ask("run-q", nodeQuestionText)
			sent := f.question("run-q")
			if sent.TargetType != "group" || sent.TargetID != "oc_group" || !strings.Contains(sent.Content, "节点需要补充信息") || !strings.Contains(sent.Content, "问题："+nodeQuestionText) || !strings.Contains(sent.Content, "回答：你的答案") {
				t.Fatalf("question = %+v", sent)
			}
			quote := strings.Contains(sent.Content, "引用本条消息")
			if quote != (channel == domain.BotChannelFeishu) {
				t.Fatalf("quote hint on %s = %v: %q", channel, quote, sent.Content)
			}
			card := f.h.notifications.service.Interactions.PushCardFor(sent, sent.Content, "")
			if card == nil || card.Tag != "需要补充信息" || card.Required != nodeQuestionText || len(card.Actions) != 1 || card.Actions[0].Key != "answer" {
				t.Fatalf("card = %+v", card)
			}
			if wantHint := map[string]string{domain.BotChannelFeishu: "在下方填写答案", domain.BotChannelDingTalk: "@我 发送「回答：你的答案」"}[channel]; !strings.Contains(card.Hint, wantHint) {
				t.Fatalf("hint = %q", card.Hint)
			}
			before := f.runs()

			reply := f.say("ou-member", "回答：手机号和邮箱都要支持")
			if !strings.Contains(reply, "已把答案提交给「需求定型」") || !strings.Contains(reply, "回答人") {
				t.Fatalf("reply = %q", reply)
			}
			run, _ := f.s.RunState("run-q")
			node := run.Nodes["define"]
			if run.Status != "running" || node.Status != "pending" || !strings.Contains(node.Feedback, "手机号和邮箱都要支持") || !strings.Contains(node.Feedback, "平台账号：other-user") {
				t.Fatalf("run %s node %+v", run.Status, node)
			}
			if !strings.Contains(run.HumanContext, "手机号和邮箱都要支持") {
				t.Fatalf("human context = %q", run.HumanContext)
			}
			if f.runs() != before {
				t.Fatalf("runs %d -> %d", before, f.runs())
			}
			if f.h.notifications.service.Interactions.PushCardFor(sent, sent.Content, "") != nil {
				t.Fatal("card still offered after the answer")
			}
		})
	}
}

// One pending record: a second answer, or an answer after the platform page
// answered, is refused and leaves the node as it is.
func TestNodeQuestionAnsweredOnlyOnce(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelFeishu)
	f.ask("run-q", nodeQuestionText)
	if reply := f.say("ou-member", "回答：都要"); !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("first = %q", reply)
	}
	feedback := f.node("run-q").Feedback
	if reply := f.say("ou-member", "回答：只要手机号", f.question("run-q")); !strings.Contains(reply, "已经回答过") {
		t.Fatalf("second (quoted) = %q", reply)
	}
	if reply := f.say("ou-member", "回答：只要手机号"); !strings.Contains(reply, "没有待回答的节点问题") {
		t.Fatalf("second = %q", reply)
	}
	if f.node("run-q").Feedback != feedback {
		t.Fatal("second answer reached the node")
	}

	// The platform page answers first ("补充信息，重跑当前节点").
	f.ask("run-p", nodeQuestionText)
	sent := f.question("run-p")
	if err := f.h.engine.Retry("run-p", "define", "", "平台上的答案", engine.NodeExpectation{NodeID: "define", Status: "waiting_approval", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if reply := f.say("ou-member", "回答：群里的答案", sent); !strings.Contains(reply, "已经回答过") {
		t.Fatalf("after platform = %q", reply)
	}
	if fb := f.node("run-p").Feedback; strings.Contains(fb, "群里的答案") || !strings.Contains(fb, "平台上的答案") {
		t.Fatalf("feedback = %q", fb)
	}
}

func TestNodeQuestionOfCancelledRunIsRefused(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelFeishu)
	f.ask("run-q", nodeQuestionText)
	sent := f.question("run-q")
	if err := f.h.engine.Cancel("run-q"); err != nil {
		t.Fatal(err)
	}
	if reply := f.say("ou-member", "回答：都要", sent); !strings.Contains(reply, "运行已取消") {
		t.Fatalf("reply = %q", reply)
	}
	if node := f.node("run-q"); node.Feedback != "" {
		t.Fatalf("node = %+v", node)
	}
}

// Answers run as the sender's platform account: unbound senders and
// project viewers cannot answer.
func TestNodeAnswerChecksProjectPermission(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelFeishu)
	f.ask("run-q", nodeQuestionText)
	if reply := f.say("ou-stranger", "回答：都要"); !strings.Contains(reply, "关联你的平台账号") {
		t.Fatalf("unbound = %q", reply)
	}
	if reply := f.say("ou-viewer", "回答：都要"); !strings.Contains(reply, "没有该项目的执行权限") {
		t.Fatalf("viewer = %q", reply)
	}
	if node := f.node("run-q"); node.Status != "waiting_approval" || node.Feedback != "" {
		t.Fatalf("node = %+v", node)
	}
}

// A Feishu reply to the question message is an answer without the prefix;
// with several open questions "回答 N：" picks one.
func TestNodeAnswerByQuoteAndByNumber(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelFeishu)
	f.ask("run-a", "第一个问题：要不要支持邮箱？")
	f.ask("run-b", "第二个问题：验证码有效期多久？")
	b := f.question("run-b")
	if reply := f.say("ou-member", "回答：随便"); !strings.Contains(reply, "2 个待回答的节点问题") || !strings.Contains(reply, "第一个问题") || !strings.Contains(reply, "第二个问题") {
		t.Fatalf("ambiguous = %q", reply)
	}
	if reply := f.say("ou-member", "五分钟", b); !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("quoted = %q", reply)
	}
	if fb := f.node("run-b").Feedback; !strings.Contains(fb, "五分钟") {
		t.Fatalf("run-b feedback = %q", fb)
	}
	if reply := f.say("ou-member", "回答 1：要"); !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("numbered = %q", reply)
	}
	if fb := f.node("run-a").Feedback; !strings.Contains(fb, "要") {
		t.Fatalf("run-a feedback = %q", fb)
	}
}

// A run started in a private chat sends the question to the starter, who
// answers there; others' private chats see no question.
func TestPrivateChatAnswersNodeQuestion(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelDingTalk)
	if err := f.s.RecordBotChatOrigin(domain.BotChatOrigin{ConfigID: f.bot.ID, ProjectID: "p", ConversationID: "conv-p2p", ChatType: "single", ChatID: "cid_p2p", SenderID: "ou-member", RunID: "run-q"}); err != nil {
		t.Fatal(err)
	}
	f.ask("run-q", nodeQuestionText)
	if sent := f.question("run-q"); sent.TargetType != "single" || sent.TargetID != "ou-member" || !strings.Contains(sent.Content, "发送「回答：你的答案」") {
		t.Fatalf("question = %+v", sent)
	}
	f.conv = domain.BotConversation{ID: "conv-other", BotID: f.bot.ID, ChatID: "cid_other", ChatType: "single"}
	if reply := f.say("ou-viewer", "回答：都要"); !strings.Contains(reply, "没有待回答的节点问题") {
		t.Fatalf("other chat = %q", reply)
	}
	f.conv = domain.BotConversation{ID: "conv-p2p", BotID: f.bot.ID, ChatID: "cid_p2p", ChatType: "single"}
	if reply := f.say("ou-member", "回答：都要"); !strings.Contains(reply, "已把答案提交") || strings.Contains(reply, "回答人") {
		t.Fatalf("private = %q", reply)
	}
	if node := f.node("run-q"); node.Status != "pending" || !strings.Contains(node.Feedback, "都要") {
		t.Fatalf("node = %+v", node)
	}
}

// The answer input of the Feishu card reaches the node like a chat answer;
// a second submit of the same card is refused.
func TestFeishuCardAnswerReachesTheNode(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelFeishu)
	f.ask("run-q", nodeQuestionText)
	sent := f.question("run-q")
	before := f.runs()
	submit := func(answer string) string {
		reply, err := f.h.handleBotCardAction(context.Background(), f.bot, domain.BotInteraction{DeliveryID: sent.ID, Action: "answer", SenderID: "ou-member", Content: answer}, f.conv)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if reply := submit("手机号和邮箱都要"); !strings.Contains(reply, "已把答案提交给「需求定型」") || !strings.Contains(reply, "回答人") {
		t.Fatalf("submit = %q", reply)
	}
	if node := f.node("run-q"); node.Status != "pending" || !strings.Contains(node.Feedback, "手机号和邮箱都要") {
		t.Fatalf("node = %+v", node)
	}
	if reply := submit("只要手机号"); !strings.Contains(reply, "已经回答过") {
		t.Fatalf("second = %q", reply)
	}
	if strings.Contains(f.node("run-q").Feedback, "只要手机号") || f.runs() != before {
		t.Fatal("second submit reached the node")
	}
}

func weComNodeQuestionQuote(delivery domain.NotificationDelivery, question string) string {
	content := strings.ReplaceAll(delivery.Content, domain.NotificationURLMarker, "https://platform.example/#/runs/"+delivery.RunID)
	return botinteraction.WithNodeQuestion(content, question, delivery)
}

// A delivery reference identifies one node even when another run asks the
// same question and WeCom only returns the quoted text.
func TestWeComQuoteAnswersNodeQuestion(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelWeCom)
	question := nodeQuestionText + "\n邮箱需要验证吗？"
	f.ask("run-other", question)
	f.ask("run-q", question)
	sent := f.question("run-q")
	quote := weComNodeQuestionQuote(sent, question)
	item := domain.BotInteraction{ID: "msg-wecom-quote", BotID: f.bot.ID, ConversationID: f.conv.ID, SenderID: "ou-member", Content: "@Bot 都要", Quote: quote}
	reply, err := f.h.generateBotReply(context.Background(), f.bot, f.conv, nil, item)
	if err != nil || !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("reply = %q %v", reply, err)
	}
	if node := f.node("run-q"); !strings.Contains(node.Feedback, "都要") {
		t.Fatalf("node = %+v", node)
	}
	item.Content = "@Bot 补充已回答的问题"
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "答案未提交") {
		t.Fatalf("stale linked question = %q, handled = %v", reply, handled)
	}
	if node := f.node("run-other"); node.Status != "waiting_approval" || node.Feedback != "" {
		t.Fatalf("another run changed: %+v", node)
	}
}

// A partial quote prompts for a number. A referenced delivery can be
// answered once even when another question shares its opening text.
func TestWeComQuoteAnswersTheQuotedOfSimilarQuestions(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelWeCom)
	f.ask("run-q1", nodeQuestionText+"邮箱需要验证吗？")
	f.question("run-q1")
	secondQuestion := nodeQuestionText + "第三方登录要不要？"
	f.ask("run-q2", secondQuestion)
	second := f.question("run-q2")
	secondQuote := weComNodeQuestionQuote(second, secondQuestion)
	if !strings.Contains(second.Content, "第三方登录") {
		t.Fatalf("second question content = %q", second.Content)
	}
	ask := func(id, quote string) string {
		item := domain.BotInteraction{ID: id, BotID: f.bot.ID, ConversationID: f.conv.ID, SenderID: "ou-member", Content: "@Bot 都要", Quote: quote}
		reply, err := f.h.generateBotReply(context.Background(), f.bot, f.conv, nil, item)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if reply := ask("msg-wecom-prefix", "问题："+nodeQuestionText); !strings.Contains(reply, "2 个待回答的节点问题") {
		t.Fatalf("ambiguous quote reply = %q", reply)
	}
	if f.node("run-q1").Feedback != "" || f.node("run-q2").Feedback != "" {
		t.Fatal("ambiguous quote answered a node")
	}
	if reply := ask("msg-wecom-second", secondQuote); !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("reply = %q", reply)
	}
	if f.node("run-q1").Feedback != "" || !strings.Contains(f.node("run-q2").Feedback, "都要") {
		t.Fatalf("first = %+v second = %+v", f.node("run-q1"), f.node("run-q2"))
	}
	// Quoting the answered second question again is refused as stale; it
	// does not fall through to the first question sharing its beginning.
	if reply := ask("msg-wecom-again", secondQuote); !strings.Contains(reply, "答案未提交") || strings.Contains(reply, "已把答案提交") {
		t.Fatalf("quote of answered question = %q", reply)
	}
	if reply := ask("msg-wecom-prefix-again", "问题："+nodeQuestionText); strings.Contains(reply, "已把答案提交") {
		t.Fatalf("prefix quote after an answer = %q", reply)
	}
	if f.node("run-q1").Feedback != "" {
		t.Fatalf("first node got another question's answer: %+v", f.node("run-q1"))
	}
}

func TestWeComQuoteOfAnOldQuestionRequiresANumber(t *testing.T) {
	const oldQuestion = nodeQuestionText + "\n第三方登录要不要？"
	for name, current := range map[string]string{
		"common prefix":          nodeQuestionText + "邮箱需要验证吗？",
		"whole question prefix":  nodeQuestionText,
		"same complete question": oldQuestion,
	} {
		t.Run(name, func(t *testing.T) {
			f := newNodeQuestionFixture(t, domain.BotChannelWeCom)
			f.ask("old", oldQuestion)
			old := f.question("old")
			if reply := f.say("ou-member", "回答：暂时不用"); !strings.Contains(reply, "已把答案提交") {
				t.Fatalf("answer old question = %q", reply)
			}
			olderThanWindow := time.Now().UTC().Add(-botNodeQuestionWindow - time.Hour)
			if err := f.s.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", old.ID).Update("created_at", olderThanWindow).Error; err != nil {
				t.Fatal(err)
			}
			f.ask("current", current)
			f.question("current")

			quote := old.Content
			if current == oldQuestion {
				quote = strings.ReplaceAll(quote, domain.NotificationURLMarker, "https://platform.example/#/runs/old")
			}
			item := domain.BotInteraction{BotID: f.bot.ID, ConversationID: f.conv.ID, SenderID: "ou-member", Quote: quote}
			for _, text := range []string{"@Bot 补充旧问题", "@Bot 回答：补充旧问题"} {
				item.Content = text
				reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item)
				if !handled || !strings.Contains(reply, "回答 序号：你的答案") {
					t.Fatalf("old question reply = %q, handled = %v", reply, handled)
				}
				if node := f.node("current"); node.Status != "waiting_approval" || node.Feedback != "" {
					t.Fatalf("current question changed: %+v", node)
				}
			}
			item.Content = "@Bot 回答 1：确认回答当前问题"
			if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "已把答案提交") {
				t.Fatalf("numbered answer = %q, handled = %v", reply, handled)
			}
			if node := f.node("current"); !strings.Contains(node.Feedback, "确认回答当前问题") {
				t.Fatalf("current question did not receive the numbered answer: %+v", node)
			}
		})
	}
}

func TestWeComQuoteOfAnotherNodeInTheSameRunRequiresANumber(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelWeCom)
	f.ask("run-q", nodeQuestionText)
	old := f.question("run-q")
	if reply := f.say("ou-member", "回答：支持邮箱"); !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("answer old question = %q", reply)
	}
	if err := f.s.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", old.ID).
		Update("created_at", time.Now().UTC().Add(-botNodeQuestionWindow-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	run, _ := f.s.RunState("run-q")
	run.Status = "waiting"
	run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: "check", Name: "验收核对", Type: "agent"})
	run.Nodes["check"] = &domain.NodeRun{NodeID: "check", Status: "waiting_approval", Attempt: 1, Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", ActionRequired: nodeQuestionText}}
	run.Events = append(run.Events, domain.RunEvent{ID: "run-q-check", NodeID: "check", Type: "gate.waiting", Timestamp: time.Now().UTC()})
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	current := f.question(run.ID)
	if current.NodeID != "check" || !strings.Contains(current.Content, "验收核对") {
		t.Fatalf("current question = %+v", current)
	}

	item := domain.BotInteraction{SenderID: "ou-member", Content: "@Bot 补充需求定型的问题",
		Quote: strings.ReplaceAll(old.Content, domain.NotificationURLMarker, "https://platform.example/#/runs/run-q")}
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "回答 序号：你的答案") {
		t.Fatalf("old question quote = %q, handled = %v", reply, handled)
	}
	item.Quote = weComNodeQuestionQuote(old, nodeQuestionText)
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "答案未提交") {
		t.Fatalf("old delivery reference = %q, handled = %v", reply, handled)
	}
	item.Quote = weComNodeQuestionQuote(current, nodeQuestionText)
	otherChat := f.conv
	otherChat.ChatID = "another-group"
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, otherChat, item); !handled || !strings.Contains(reply, "答案未提交") {
		t.Fatalf("delivery from another conversation = %q, handled = %v", reply, handled)
	}
	run, _ = f.s.RunState(run.ID)
	if node := run.Nodes["check"]; node.Status != "waiting_approval" || node.Feedback != "" {
		t.Fatalf("another node changed: %+v", node)
	}
	item.Content = "@Bot 提交验收答案"
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "已把答案提交给「验收核对」") {
		t.Fatalf("current delivery reference = %q, handled = %v", reply, handled)
	}
}

func TestWeComQuoteOfPreviousAttemptCannotAnswerTheCurrentAttempt(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelWeCom)
	f.ask("run-q", nodeQuestionText)
	old := f.question("run-q")
	if reply := f.say("ou-member", "回答：支持邮箱"); !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("first attempt answer = %q", reply)
	}

	run, _ := f.s.RunState("run-q")
	run.Status = "waiting"
	run.Nodes["define"] = &domain.NodeRun{NodeID: "define", Status: "waiting_approval", Attempt: 2, Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", ActionRequired: nodeQuestionText}}
	run.Events = append(run.Events, domain.RunEvent{ID: "run-q-second", NodeID: "define", Type: "gate.waiting", Timestamp: time.Now().UTC()})
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	current := f.question(run.ID)
	if current.ID == old.ID || current.Attempt != 2 {
		t.Fatalf("second delivery = %+v", current)
	}

	item := domain.BotInteraction{SenderID: "ou-member", Content: "@Bot 补充上一次的答案",
		Quote: weComNodeQuestionQuote(old, nodeQuestionText)}
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "答案未提交") {
		t.Fatalf("previous attempt quote = %q, handled = %v", reply, handled)
	}
	if node := f.node(run.ID); node.Status != "waiting_approval" || node.Feedback != "" {
		t.Fatalf("second attempt changed: %+v", node)
	}
	item.Content, item.Quote = "@Bot 只支持手机号", weComNodeQuestionQuote(current, nodeQuestionText)
	if reply, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, item); !handled || !strings.Contains(reply, "已把答案提交") {
		t.Fatalf("current attempt quote = %q, handled = %v", reply, handled)
	}
}

// Ordinary messages are not answers.
func TestPlainMessageIsNotANodeAnswer(t *testing.T) {
	f := newNodeQuestionFixture(t, domain.BotChannelFeishu)
	f.ask("run-q", nodeQuestionText)
	if _, handled := f.h.botNodeAnswer(context.Background(), f.bot, f.conv, domain.BotInteraction{SenderID: "ou-member", Content: "@Bot 现在进度怎么样"}); handled {
		t.Fatal("plain message taken as an answer")
	}
	for text, want := range map[string]string{"回答：都要": "都要", "回答 2： 五分钟": "五分钟", "答复:可以": "可以"} {
		answer, _, ok := botNodeAnswerText(text)
		if !ok || answer != want {
			t.Fatalf("%q -> %q %v", text, answer, ok)
		}
	}
	if _, _, ok := botNodeAnswerText("回答一下进度"); ok {
		t.Fatal("prefix without colon accepted")
	}
}
