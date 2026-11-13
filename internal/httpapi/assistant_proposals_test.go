package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"agentflow/internal/domain"
)

func proposalReply(t *testing.T, drafts ...assistantDraft) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"message": "建议走 SOP", "drafts": drafts})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func sopDraft(issueID, timing string) assistantDraft {
	return assistantDraft{Tool: "issues_dispatch", Path: map[string]any{"id": issueID}, Body: json.RawMessage(`{}`), Proposal: &assistantProposal{Reason: "整件需求走 SOP", Timing: timing}}
}

func TestSOPProposalValidation(t *testing.T) {
	bad := sopDraft("i1", "later")
	if err := validateAssistantDraft(&bad, "workbench"); err == nil {
		t.Fatal("an unknown timing was accepted")
	}
	role := assistantDraft{Tool: "subagent_dispatch", Body: json.RawMessage(`{"roleId":"dev","task":"做"}`), Proposal: &assistantProposal{Reason: "x", Timing: "start"}}
	if err := validateAssistantDraft(&role, "workbench"); err == nil {
		t.Fatal("a role dispatch draft was accepted; roles are dispatched with the Agent tool")
	}
	save := assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"id":"i1","title":"t","projectId":"workbench"}`), Proposal: &assistantProposal{Reason: "x", Timing: "start"}}
	if err := validateAssistantDraft(&save, "workbench"); err != nil || save.Proposal != nil {
		t.Fatalf("a proposal on an ordinary write must be dropped: %v %+v", err, save.Proposal)
	}
	rebind := assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"id":"i1","title":"t","projectId":"workbench","workflowId":"sop"}`), Proposal: &assistantProposal{Reason: "x", Timing: "start"}}
	if err := validateAssistantDraft(&rebind, "workbench"); err != nil || rebind.Proposal != nil {
		t.Fatalf("binding an existing issue to an SOP is not a proposal: %v %+v", err, rebind.Proposal)
	}
	create := assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"title":"t","projectId":"workbench","workflowId":"sop"}`), Proposal: &assistantProposal{Reason: "整件需求走 SOP", Timing: "start"}}
	if err := validateAssistantDraft(&create, "workbench"); err != nil || create.Proposal == nil {
		t.Fatalf("a new requirement bound to an SOP lost its proposal: %v %+v", err, create.Proposal)
	}
	if target := assistantProposalTarget(create); target != "sop" {
		t.Fatalf("target=%s", target)
	}
}

func TestVetProposalDraftsFollowsHistory(t *testing.T) {
	h, u := workbenchFixture(t)
	session := &AgentToolSession{ProjectID: "workbench", UserID: u.ID, ChatOwnerID: u.ID, ChatSessionID: "vet"}
	vet := func(d assistantDraft) error {
		return h.vetProposalDrafts(session, &assistantReply{Drafts: []assistantDraft{d}})
	}
	if err := vet(sopDraft("i1", "end")); err != nil {
		t.Fatal(err)
	}
	declined := sopDraft("i1", "end")
	declined.Decision = domain.AssistantProposalDeclined
	h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "vet", ProjectID: "workbench", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "改一下"}, {Role: "assistant", Content: proposalReply(t, declined)},
	}})
	if err := vet(sopDraft("i1", "start")); err == nil || !strings.Contains(err.Error(), "拒绝过") {
		t.Fatalf("a declined proposal was offered again: %v", err)
	}
	if err := vet(sopDraft("i2", "end")); err == nil || !strings.Contains(err.Error(), "timing=end") {
		t.Fatalf("another add-on followed a declined add-on: %v", err)
	}
	if err := vet(sopDraft("i2", "start")); err != nil {
		t.Fatal("a whole-task proposal was blocked by a declined add-on", err)
	}
	muted := sopDraft("i1", "end")
	muted.Decision = domain.AssistantProposalMuted
	h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "vet", ProjectID: "workbench", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "改一下"}, {Role: "assistant", Content: proposalReply(t, muted)},
	}})
	if err := vet(sopDraft("i2", "start")); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("a muted conversation accepted a proposal: %v", err)
	}
	plain := sopDraft("i2", "start")
	plain.Proposal = nil
	if err := vet(plain); err == nil || !strings.Contains(err.Error(), "自己完成") {
		t.Fatalf("an unrequested SOP dispatch slipped past the mute: %v", err)
	}
	h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "vet", ProjectID: "workbench", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "改一下"}, {Role: "assistant", Content: proposalReply(t, muted)}, {Role: "user", Content: "这个还是走 SOP 吧"},
	}})
	if err := vet(plain); err != nil {
		t.Fatal("a requested SOP dispatch was blocked by the mute", err)
	}
}

func TestDecideProposalRecordsAnswerAndReturnsFollowUp(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "decide", ProjectID: "workbench", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "改一下"},
		{Role: "assistant", Content: proposalReply(t, sopDraft("i1", "start"))},
		{Role: "user", Content: "做需求"},
		{Role: "assistant", Content: proposalReply(t, sopDraft("i2", "start"))},
	}})
	decide := func(message int, decision string) (int, map[string]json.RawMessage) {
		body, _ := json.Marshal(map[string]any{"messageIndex": message, "draftIndex": 0, "decision": decision})
		c, rec := workbenchContext(u, "POST", "/api/assistant/sessions/decide/proposals", bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: "decide"}}
		h.decideProposal(c)
		var out map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// The card accepts before it runs the operation, so a timeout cannot
	// hand the same work to Main Bot while the operation is running.
	code, out := decide(1, domain.AssistantProposalAccepted)
	if code != 200 || string(out["followUp"]) != "null" {
		t.Fatalf("accept=%d %s", code, out)
	}
	stored, _ := h.repo.AssistantSession(u.ID, "decide")
	var reply struct {
		Drafts []assistantDraft `json:"drafts"`
	}
	json.Unmarshal([]byte(stored.Messages[1].Content), &reply)
	if reply.Drafts[0].Decision != domain.AssistantProposalAccepted || reply.Drafts[0].Saved {
		t.Fatalf("draft not marked: %+v", reply.Drafts[0])
	}
	if steer := h.expireProposal(u.ID, "decide", 1, 0, time.Now(), u.ID, u.LoginName); steer != nil {
		t.Fatal("the timeout took over an accepted proposal")
	}
	if code, _ := decide(1, domain.AssistantProposalDeclined); code != 409 {
		t.Fatalf("an answered proposal was answered again: %d", code)
	}
	var followUp assistantProposalFollowUp
	code, out = decide(3, domain.AssistantProposalMuted)
	if code != 200 || json.Unmarshal(out["followUp"], &followUp) != nil || followUp.Text != assistantProposalDeclinedText {
		t.Fatalf("declining a timing=start proposal must hand the work back: %d %s", code, out["followUp"])
	}
	stored, _ = h.repo.AssistantSession(u.ID, "decide")
	if history := proposalHistory(stored.Messages); !history.Muted {
		t.Fatal("muting is not visible to later turns")
	}
}

func TestUnansweredStartProposalHandsTheTurnBack(t *testing.T) {
	h, u := workbenchFixture(t)
	seed := func(messages ...domain.AssistantMessage) {
		t.Helper()
		if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "expire", ProjectID: "workbench", Messages: messages}); err != nil {
			t.Fatal(err)
		}
	}
	ask := domain.AssistantMessage{Role: "user", Content: "做积分转赠需求"}
	deadline := time.Now().Add(assistantProposalTimeout)
	stamped := func(drafts ...assistantDraft) string {
		stampProposalDeadlines(drafts, deadline)
		return proposalReply(t, drafts...)
	}
	forged := sopDraft("i1", "end")
	forged.Proposal.ExpiresAt = &deadline
	unstamped := []assistantDraft{forged}
	stampProposalDeadlines(unstamped, time.Time{})
	if unstamped[0].Proposal.ExpiresAt != nil {
		t.Fatal("a model-supplied deadline survived")
	}
	proposal := domain.AssistantMessage{Role: "assistant", Content: stamped(sopDraft("i1", "start"))}

	seed(ask, domain.AssistantMessage{Role: "assistant", Content: stamped(sopDraft("i1", "end"))})
	session, _ := h.repo.AssistantSession(u.ID, "expire")
	if _, _, _, ok := pendingStartProposal(session.Messages); ok {
		t.Fatal("an optional add-on was treated as blocking")
	}
	seed(ask, domain.AssistantMessage{Role: "assistant", Content: proposalReply(t, sopDraft("i1", "start"))})
	session, _ = h.repo.AssistantSession(u.ID, "expire")
	if _, _, _, ok := pendingStartProposal(session.Messages); ok {
		t.Fatal("a proposal without a deadline got a timer")
	}

	seed(ask, proposal, domain.AssistantMessage{Role: "user", Content: "先别走 SOP，我自己说"})
	if steer := h.expireProposal(u.ID, "expire", 1, 0, deadline, u.ID, u.LoginName); steer != nil {
		t.Fatal("the timeout overrode a message the user already sent")
	}

	seed(ask, proposal)
	session, _ = h.repo.AssistantSession(u.ID, "expire")
	messageIndex, draftIndex, at, ok := pendingStartProposal(session.Messages)
	if !ok || messageIndex != 1 || draftIndex != 0 || !at.Equal(deadline.UTC()) {
		t.Fatalf("pending=%d/%d at=%v %v", messageIndex, draftIndex, at, ok)
	}
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "g-running", SessionID: "expire", UserID: u.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if steer := h.expireProposal(u.ID, "expire", 1, 0, deadline, u.ID, u.LoginName); steer != nil {
		t.Fatal("the timeout rewrote the conversation during a reply")
	}
	done, _ := h.repo.AssistantGeneration(u.ID, "expire")
	done.Status = "completed"
	if err := h.repo.CheckpointAssistantGeneration(done); err != nil {
		t.Fatal(err)
	}

	if steer := h.expireProposal(u.ID, "expire", 1, 0, deadline.Add(time.Second), u.ID, u.LoginName); steer != nil {
		t.Fatal("a timer expired a card it was not started for")
	}
	optional := sopDraft("i1", "end")
	optional.Proposal.ExpiresAt = &deadline
	seed(ask, domain.AssistantMessage{Role: "assistant", Content: proposalReply(t, optional)})
	if steer := h.expireProposal(u.ID, "expire", 1, 0, deadline, u.ID, u.LoginName); steer != nil {
		t.Fatal("the old timer expired the optional proposal of a regenerated reply")
	}
	seed(ask, proposal)

	steer := h.expireProposal(u.ID, "expire", 1, 0, deadline, u.ID, u.LoginName)
	if steer == nil || steer.Trigger != assistantProposalTimeoutTrigger || steer.Content != assistantProposalExpiredText {
		t.Fatalf("steer=%+v", steer)
	}
	saved, _ := h.repo.AssistantSession(u.ID, "expire")
	if len(saved.Messages) != 3 || saved.Messages[2].Trigger != assistantProposalTimeoutTrigger || saved.Messages[2].Role != "user" {
		t.Fatalf("messages=%+v", saved.Messages)
	}
	if !strings.Contains(saved.Messages[1].Content, `"decision":"expired"`) {
		t.Fatalf("proposal not marked expired: %s", saved.Messages[1].Content)
	}
	if _, declined := proposalHistory(saved.Messages).Declined["i1"]; !declined {
		t.Fatal("an expired proposal may be offered again")
	}
	if again := h.expireProposal(u.ID, "expire", 1, 0, deadline, u.ID, u.LoginName); again != nil {
		t.Fatal("the proposal expired twice")
	}

	body, _ := json.Marshal(map[string]any{"messageIndex": 1, "draftIndex": 0, "decision": domain.AssistantProposalAccepted})
	c, rec := workbenchContext(u, "POST", "/api/assistant/sessions/expire/proposals", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: "expire"}}
	h.decideProposal(c)
	if rec.Code != 409 {
		t.Fatalf("an expired proposal was answered: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSOPDraftsNeedAProposalUnlessTheUserAskedForTheSOP(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: "delivery", Name: "交付流水线", ProjectID: "workbench", Status: "published"}); err != nil {
		t.Fatal(err)
	}
	session := &AgentToolSession{ProjectID: "workbench", UserID: u.ID, ChatOwnerID: u.ID, ChatSessionID: "sop"}
	seed := func(messages ...domain.AssistantMessage) {
		t.Helper()
		if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "sop", ProjectID: "workbench", Messages: messages}); err != nil {
			t.Fatal(err)
		}
	}
	vet := func(d assistantDraft) error {
		return h.vetProposalDrafts(session, &assistantReply{Drafts: []assistantDraft{d}})
	}
	dispatch := assistantDraft{Tool: "issues_dispatch", Path: map[string]any{"id": "i1"}, Body: json.RawMessage(`{}`)}
	create := assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"title":"t","workflowId":"delivery"}`)}
	update := assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"id":"i1","title":"t","workflowId":"delivery"}`)}

	// The AI 助手 switches are off: a SOP Main Bot picks itself still asks.
	seed(domain.AssistantMessage{Role: "user", Content: "积分转赠这个需求帮我推进一下"})
	for _, exec := range []assistantExecMode{{}, {AutoWrite: true, ReviewWrites: true}} {
		session.ExecMode = exec
		for _, d := range []assistantDraft{dispatch, create} {
			if err := vet(d); err == nil || !strings.Contains(err.Error(), "proposal") {
				t.Fatalf("%+v: %s without a proposal passed: %v", exec, d.Tool, err)
			}
		}
	}
	if err := vet(update); err != nil {
		t.Fatalf("editing an issue is not an SOP proposal: %v", err)
	}
	if err := vet(sopDraft("i1", "start")); err != nil {
		t.Fatal(err)
	}
	session.ExecMode = assistantExecMode{AutoWrite: true}
	if err := vet(dispatch); err != nil {
		t.Fatalf("full access starts a SOP without asking: %v", err)
	}
	session.ExecMode = assistantExecMode{}

	for _, refused := range []string{"不要走 SOP，你直接改", "这个不需要走完整流程", "别用交付流水线了"} {
		seed(domain.AssistantMessage{Role: "user", Content: refused})
		if err := vet(dispatch); err == nil || !strings.Contains(err.Error(), "proposal") {
			t.Fatalf("%q counted as asking for the SOP: %v", refused, err)
		}
	}
	for _, asked := range []domain.AssistantMessage{
		{Role: "user", Content: "直接走 SOP 完整交付"},
		{Role: "user", Content: "这个走 SOP，不用你自己写"},
		{Role: "user", Content: "用交付流水线跑一下"},
		{Role: "user", Content: "推进一下", References: []domain.AssistantReference{{Type: "sop", ID: "delivery"}}},
	} {
		seed(asked)
		if err := vet(dispatch); err != nil {
			t.Fatalf("%q asked for the SOP: %v", asked.Content, err)
		}
	}

	accepted := create
	accepted.Proposal, accepted.Decision, accepted.Saved = &assistantProposal{Reason: "走 SOP", Timing: "start"}, "accepted", true
	reviewed := create
	reviewed.Proposal, reviewed.Saved, reviewed.Auto = &assistantProposal{Reason: "走 SOP", Timing: "start"}, true, true
	for _, taken := range []assistantDraft{accepted, reviewed} {
		seed(domain.AssistantMessage{Role: "user", Content: "推进一下"},
			domain.AssistantMessage{Role: "assistant", Content: proposalReply(t, taken)},
			domain.AssistantMessage{Role: "user", Content: "已确认并执行 issues_save。"})
		if err := vet(dispatch); err != nil {
			t.Fatalf("the dispatch after a taken SOP proposal asked again: %v", err)
		}
	}

	muted := sopDraft("i2", "start")
	muted.Decision = domain.AssistantProposalMuted
	thread := []domain.AssistantMessage{
		{Role: "user", Content: "走 SOP 吧"},
		{Role: "assistant", Content: proposalReply(t, accepted)},
		{Role: "user", Content: "再做个需求"},
		{Role: "assistant", Content: proposalReply(t, muted)},
	}
	seed(thread...)
	if err := vet(dispatch); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("an earlier accept or request outlived the mute: %v", err)
	}
	// The stored transcript still ends at the mute. A direct chat carries the
	// new request only on the turn, and that request is what counts.
	session.turnMessages = append(append([]domain.AssistantMessage{}, thread...), domain.AssistantMessage{Role: "user", Content: "还是走 SOP 吧"})
	if err := vet(dispatch); err != nil {
		t.Fatalf("the unsaved request after a mute was blocked: %v", err)
	}
	session.turnMessages = nil
	seed(append(thread, domain.AssistantMessage{Role: "user", Content: "还是走 SOP 吧"})...)
	if err := vet(dispatch); err != nil {
		t.Fatalf("asking after the mute was blocked: %v", err)
	}
}

func TestAssistantPreferencesShapeTheContext(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutAgent(domain.AgentDefinition{ID: "reviewer", Name: "评审", ProjectID: "workbench", Prompt: "评审"})
	if p := h.repo.AssistantPreferences(u.ID); p.ProactiveRoles || p.ProactiveSOPs || p.Instructions != "" {
		t.Fatalf("defaults=%+v", p)
	}
	put := func(body string) (int, domain.AssistantPreferences) {
		c, rec := workbenchContext(u, "PUT", "/api/assistant/preferences", bytes.NewBufferString(body))
		h.preferences(c)
		var out domain.AssistantPreferences
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := put(`{"instructions":"` + strings.Repeat("长", domain.AssistantPreferencesInstructionsMax+1) + `"}`); code != 400 {
		t.Fatalf("oversized rules=%d", code)
	}
	contextFor := func(mode string) assistantRequest {
		t.Helper()
		c, _ := workbenchContext(u, "POST", "/", &bytes.Buffer{})
		req := assistantRequest{ProjectID: "workbench", SessionID: "prefs", ApprovalMode: mode, Messages: []domain.AssistantMessage{{Role: "user", Content: "评审一下"}}}
		if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
			t.Fatal(err)
		}
		return req
	}
	const asBefore = "任务明显属于某个角色的职责、或需要它的规则和技能时，可以自行派给它；简单问题自己处理。"
	const askSOP = "## 交给 SOP 前先征得同意"

	for _, exec := range []assistantExecMode{{}, {AutoWrite: true, ReviewWrites: true}, {AutoWrite: true}} {
		schema, _ := json.Marshal(submitAssistantReplySchema(exec))
		if got, want := strings.Contains(string(schema), `"proposal"`), !exec.AutoWrite || exec.ReviewWrites; got != want {
			t.Fatalf("%+v: schema offers proposal=%v", exec, got)
		}
		if strings.Contains(string(schema), `"subagent_dispatch"`) {
			t.Fatalf("%+v: schema offers a role dispatch draft", exec)
		}
	}

	if req := contextFor(AssistantApprovalConfirm); strings.Contains(req.contextText, askSOP) {
		t.Fatalf("a project without SOPs explains SOP proposals: %s", req.contextText)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: "delivery", Name: "交付流水线", ProjectID: "workbench", Status: "published"}); err != nil {
		t.Fatal(err)
	}

	// Both off: roles as before; a SOP Main Bot picks itself still asks
	// outside full access.
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAssisted, AssistantApprovalAuto} {
		req := contextFor(mode)
		if len(req.roles) != 1 || !strings.Contains(req.contextText, asBefore) {
			t.Fatalf("%s with both off: roles=%d context=%s", mode, len(req.roles), req.contextText)
		}
		for _, unwanted := range []string{"## 自己做", "- 派角色：", "- 走 SOP：", "用户给你的自定义规则"} {
			if strings.Contains(req.contextText, unwanted) {
				t.Fatalf("%s with both off adds %q: %s", mode, unwanted, req.contextText)
			}
		}
		if got := strings.Contains(req.contextText, askSOP); got != (mode != AssistantApprovalAuto) {
			t.Fatalf("%s with both off: asks before a SOP=%v", mode, got)
		}
		if got := strings.Contains(req.contextText, "当前是帮我批准"); got != (mode == AssistantApprovalAssisted) {
			t.Fatalf("%s: assisted note=%v", mode, got)
		}
	}

	code, saved := put(`{"proactiveRoles":true,"proactiveSops":false,"instructions":"  回复先给结论  "}`)
	if code != 200 || !saved.ProactiveRoles || saved.ProactiveSOPs || saved.Instructions != "回复先给结论" {
		t.Fatalf("saved=%d %+v", code, saved)
	}
	c, rec := workbenchContext(u, "GET", "/api/assistant/preferences", &bytes.Buffer{})
	h.preferences(c)
	if !strings.Contains(rec.Body.String(), `"proactiveRoles":true`) {
		t.Fatalf("get=%s", rec.Body.String())
	}
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAuto} {
		req := contextFor(mode)
		if len(req.roles) != 1 || strings.Contains(req.contextText, asBefore) {
			t.Fatalf("%s with roles on: roles=%d context=%s", mode, len(req.roles), req.contextText)
		}
		for _, want := range []string{"- 派角色：", "不用征求同意", "- 收尾检查：", "## 用户给你的自定义规则", "回复先给结论"} {
			if !strings.Contains(req.contextText, want) {
				t.Fatalf("%s roles on: context lacks %q: %s", mode, want, req.contextText)
			}
		}
		if strings.Contains(req.contextText, "- 走 SOP：") {
			t.Fatalf("SOPs off still describes when to take one: %s", req.contextText)
		}
	}

	if code, _ := put(`{"proactiveSops":true}`); code != 200 {
		t.Fatalf("SOPs on=%d", code)
	}
	if req := contextFor(AssistantApprovalAuto); !strings.Contains(req.contextText, "- 走 SOP：") || !strings.Contains(req.contextText, "当前是完全权限：走 SOP 时直接调用写入工具") || strings.Contains(req.contextText, askSOP) {
		t.Fatalf("full access with SOPs on: %s", req.contextText)
	}
	if req := contextFor(AssistantApprovalConfirm); !strings.Contains(req.contextText, "- 走 SOP：") || !strings.Contains(req.contextText, askSOP) || !strings.Contains(req.contextText, asBefore) {
		t.Fatalf("confirm with SOPs on: %s", req.contextText)
	}
}

// A direct chat, including SSE, stores the personal session after the reply.
// The proposal gate has to see the user message on that request, or a named
// SOP is refused as if Main Bot had suggested it.
func TestDirectChatSeesTheSOPTheUserJustAskedFor(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: "delivery", Name: "交付流水线", ProjectID: "workbench", Status: "published"}); err != nil {
		t.Fatal(err)
	}
	const asked = "请走 SOP 交付流水线实现新功能"
	dispatch := json.RawMessage(`{"message":"交给交付流水线","drafts":[{"tool":"issues_dispatch","path":{"id":"i1"},"body":{}}]}`)
	gateway := &agentToolGateway{deps: h.deps}
	chat := func(t *testing.T, sessionID string, messages []domain.AssistantMessage, accept, want string) {
		t.Helper()
		var once sync.Once
		var toolText string
		var toolErr bool
		var called bool
		h.generate = func(context.Context, string, domain.User) (string, error) {
			once.Do(func() {
				called = true
				session := assistantToolSessionForChat(sessionID)
				if session == nil {
					t.Error("chat did not open a tool session")
					return
				}
				result, err := gateway.callTerminal(session, submitAssistantReplyTool, dispatch)
				if err != nil {
					toolText, toolErr = err.Error(), true
					return
				}
				toolErr, _ = result["isError"].(bool)
				toolText = mcpToolResultText(result)
			})
			return `{"message":"好","drafts":[]}`, nil
		}
		body, err := json.Marshal(map[string]any{"sessionId": sessionID, "projectId": "workbench", "messages": messages})
		if err != nil {
			t.Fatal(err)
		}
		c, rec := workbenchContext(u, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
		if accept != "" {
			c.Request.Header.Set("Accept", accept)
		}
		h.chat(c)
		if !called {
			t.Fatalf("chat never reached the model: %d %s", rec.Code, rec.Body.String())
		}
		if want == "" {
			if toolErr || !strings.Contains(toolText, "提交成功") {
				t.Fatalf("a named SOP was refused: err=%v %s http=%d %s", toolErr, toolText, rec.Code, rec.Body.String())
			}
			if rec.Code >= 400 {
				t.Fatalf("chat failed after accepting the SOP: %d %s", rec.Code, rec.Body.String())
			}
			return
		}
		if !toolErr || !strings.Contains(toolText, want) {
			t.Fatalf("an unnamed SOP passed: err=%v %s", toolErr, toolText)
		}
	}
	chat(t, "direct-json", []domain.AssistantMessage{{Role: "user", Content: asked}}, "", "")
	chat(t, "direct-sse", []domain.AssistantMessage{{Role: "user", Content: asked}}, "text/event-stream", "")

	muted := sopDraft("i2", "start")
	muted.Decision = domain.AssistantProposalMuted
	prior := []domain.AssistantMessage{
		{Role: "user", Content: "先看看这个需求"},
		{Role: "assistant", Content: proposalReply(t, muted)},
	}
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "direct-muted", ProjectID: "workbench", Messages: prior}); err != nil {
		t.Fatal(err)
	}
	again := append(append([]domain.AssistantMessage{}, prior...), domain.AssistantMessage{Role: "user", Content: asked})
	chat(t, "direct-muted", again, "", "")
	chat(t, "direct-unnamed", []domain.AssistantMessage{{Role: "user", Content: "积分转赠这个需求帮我推进一下"}}, "", "proposal")
}

func assistantToolSessionForChat(sessionID string) *AgentToolSession {
	agentToolSessions.mu.Lock()
	defer agentToolSessions.mu.Unlock()
	for _, session := range agentToolSessions.sessions {
		if session.ChatSessionID == sessionID && session.Mode == AgentToolAssistant {
			return session
		}
	}
	return nil
}

func mcpToolResultText(result map[string]any) string {
	content, _ := result["content"].([]map[string]any)
	if len(content) == 0 {
		return ""
	}
	text, _ := content[0]["text"].(string)
	return text
}
