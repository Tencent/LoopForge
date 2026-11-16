package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
)

const (
	assistantProposalStart = "start"
	assistantProposalEnd   = "end"

	assistantProposalReasonMax = 500
)

// assistantProposal marks a draft Main Bot submits on its own to hand work to
// a SOP. Roles never need one: Main Bot dispatches them directly.
type assistantProposal struct {
	Reason string `json:"reason"`
	// Timing is start when the whole request belongs to the SOP and Main Bot
	// asks before doing the work, end when Main Bot finished its part and
	// offers follow-up work the turn does not depend on.
	Timing string `json:"timing"`
	// ExpiresAt is when an unanswered timing=start proposal hands the work
	// back to Main Bot. Host-stamped; model output never sets it.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

func assistantProposalSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"reason", "timing"},
		"description":          "走 SOP 的建议：用户没 @ SOP、也没点名要走 SOP 或完整流程，是你自己决定交给 SOP（绑定 SOP 新建需求的 issues_save、issues_dispatch、runs_start）时必填；用户明确要求时不填",
		"properties": map[string]any{
			"reason": map[string]any{"type": "string", "description": "一句话说明为什么这件事走 SOP 更合适"},
			"timing": map[string]any{"type": "string", "enum": []string{assistantProposalStart, assistantProposalEnd}, "description": "start：整件事走 SOP、你还没开始做；end：你已做完自己的部分，这是可选的后续工作"},
		},
	}
}

// assistantSOPDraft reports whether a draft hands work to a SOP: dispatching
// an issue, starting a run, or a new requirement bound to a SOP, which is how
// the SOP route starts when the requirement does not exist yet.
func assistantSOPDraft(d assistantDraft) bool {
	switch d.Tool {
	case "issues_dispatch", "runs_start":
		return true
	case "issues_save":
		var body struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(d.Body, &body)
		return strings.TrimSpace(body.ID) == "" && assistantProposalTarget(d) != ""
	default:
		return false
	}
}

func validateAssistantProposal(d *assistantDraft) error {
	if d.Proposal != nil && !assistantSOPDraft(*d) {
		d.Proposal = nil
	}
	if d.Proposal == nil {
		return nil
	}
	d.Proposal.Reason = truncateRunes(strings.TrimSpace(d.Proposal.Reason), assistantProposalReasonMax)
	d.Proposal.Timing = strings.TrimSpace(d.Proposal.Timing)
	if d.Proposal.Timing != assistantProposalStart && d.Proposal.Timing != assistantProposalEnd {
		return fmt.Errorf("proposal.timing 只能是 start 或 end")
	}
	if d.Proposal.Reason == "" {
		return fmt.Errorf("proposal.reason 不能为空，用一句话说明为什么走 SOP 更合适")
	}
	return nil
}

// assistantProposalTarget names what a proposal hands the work to: the issue
// whose SOP it dispatches, or the SOP it starts or binds.
func assistantProposalTarget(d assistantDraft) string {
	if d.Tool == "issues_dispatch" {
		return assistantPathValue(d.Path, "id")
	}
	var body struct {
		WorkflowID string `json:"workflowId"`
	}
	_ = json.Unmarshal(d.Body, &body)
	return strings.TrimSpace(body.WorkflowID)
}

// assistantProposalHistory is what the transcript says about earlier
// proposals: whether the user turned them off and in which message, which
// ones were declined, whether an optional add-on was ever declined, and
// whether the latest answered one was taken.
type assistantProposalHistory struct {
	Muted       bool
	MutedAt     int
	DeclinedEnd bool
	Accepted    bool
	Declined    map[string]string
}

func proposalHistory(messages []assistantMessage) assistantProposalHistory {
	out := assistantProposalHistory{MutedAt: -1, Declined: map[string]string{}}
	for i, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		var reply struct {
			Drafts []assistantDraft `json:"drafts"`
		}
		if json.Unmarshal([]byte(message.Content), &reply) != nil {
			continue
		}
		for _, d := range reply.Drafts {
			if d.Proposal == nil {
				continue
			}
			switch d.Decision {
			case domain.AssistantProposalAccepted:
				out.Accepted = true
			case "":
				// 帮我批准 runs an approved proposal without anyone answering it.
				if d.Saved {
					out.Accepted = true
				}
			case domain.AssistantProposalMuted:
				out.Muted, out.MutedAt = true, i
				fallthrough
			case domain.AssistantProposalDeclined:
				out.Accepted = false
				out.Declined[assistantProposalTarget(d)] = assistantProposalLabel(d)
				out.DeclinedEnd = out.DeclinedEnd || d.Proposal.Timing == assistantProposalEnd
			case domain.AssistantProposalExpired:
				// Main Bot already took this one over; asking again would
				// block the conversation a second time.
				out.Accepted = false
				out.Declined[assistantProposalTarget(d)] = assistantProposalLabel(d)
			}
		}
	}
	return out
}

func assistantProposalLabel(d assistantDraft) string {
	if d.Tool == "issues_dispatch" {
		return "派发需求 " + assistantProposalTarget(d) + " 的 SOP"
	}
	return "启动 SOP " + assistantProposalTarget(d)
}

// delegationContext tells Main Bot when to hand work to a role or a SOP on
// its own, only for what the user turned on in the AI 助手 settings, and,
// whenever the project has SOPs, that outside full access a SOP it picks
// itself needs the user's or the reviewer's consent. Roles are always
// dispatched directly.
func delegationContext(req *assistantRequest, prefs domain.AssistantPreferences, mode string, hasSOP bool) string {
	if strings.TrimSpace(req.ProjectID) == "" {
		return ""
	}
	var b strings.Builder
	if prefs.ProactiveRoles || prefs.ProactiveSOPs {
		b.WriteString(assistantDelegationHeading)
		b.WriteString(assistantDelegationSelf)
		b.WriteString(assistantDelegationSkill)
		if prefs.ProactiveRoles {
			b.WriteString(assistantDelegationRole)
			b.WriteString(assistantDelegationRoleCheck)
		}
		if prefs.ProactiveSOPs {
			b.WriteString(assistantDelegationSOP)
			if mode == AssistantApprovalAuto {
				b.WriteString(assistantDelegationSOPAuto)
			}
		}
	}
	if mode == AssistantApprovalAuto || !hasSOP {
		return b.String()
	}
	b.WriteString("## 交给 SOP 前先征得同意\n")
	if mode == AssistantApprovalAssisted {
		b.WriteString("当前是帮我批准：你自己决定走 SOP 时也直接调用写入工具，由独立 AI 审查这件事是否该走 SOP，通过才执行；未通过转成草稿时，按下面的规则带上 proposal 交给用户确认。\n")
	}
	b.WriteString(assistantProposalRules)
	history := proposalHistory(req.Messages)
	if history.Muted {
		b.WriteString("用户已在本会话关闭 SOP 建议：不要再提交任何带 proposal 的草稿，自己完成用户的请求。\n")
	} else if len(history.Declined) > 0 {
		labels := make([]string, 0, len(history.Declined))
		for _, label := range history.Declined {
			labels = append(labels, label)
		}
		b.WriteString("用户在本会话拒绝过这些建议，不要再提：" + strings.Join(labels, "、") + "。\n")
		if history.DeclinedEnd {
			b.WriteString("用户在本会话拒绝过 timing=end 的后续建议：不要再提任何 timing=end 建议。\n")
		}
	}
	return b.String()
}

// assistantInstructionsContext carries the user's own rules for Main Bot.
func assistantInstructionsContext(prefs domain.AssistantPreferences) string {
	text := strings.TrimSpace(prefs.Instructions)
	if text == "" {
		return ""
	}
	return "## 用户给你的自定义规则\n以下是用户在「AI 助手」设置里写的规则，在不违反平台规则和审批模式的前提下照做：\n" + text + "\n"
}

const (
	assistantDelegationHeading   = "## 自己做、用技能，还是交给角色或 SOP\n"
	assistantDelegationSelf      = "- 默认自己做：问答、解释、只读查询、小范围改动，以及用户说了“你来做”“直接改”的事。\n"
	assistantDelegationSkill     = "- 技能：任务和某个技能的描述对得上时，先用 Skill 工具加载它，照它的说明做；加载技能不需要用户确认。\n"
	assistantDelegationRole      = "- 派角色：用户要的整件事就是某个角色的本职（如独立评审、整套测试），且工作量不小；你这轮改了代码，改动碰到资金/积分、并发、鉴权权限、数据迁移或对外接口这类出错代价高的地方，需要评审或测试角色独立复核；有几块互不依赖的工作可以并行。符合时直接用 Agent 工具派给对应的子 Agent，不用征求同意；派发前用一句话告诉用户交给了谁、为什么。\n"
	assistantDelegationRoleCheck = "- 收尾检查：回复前看一眼——这轮改了上面说的高风险代码、项目里又有评审或测试角色时，先用 Agent 工具派它独立复核（prompt 写清仓库、改动文件和要重点复核的点），核对它的结论后一起汇报；否则照常回复。\n"
	assistantDelegationSOP       = "- 走 SOP：用户提的是一条要落到仓库的新需求或新功能（不是修 bug、不是小改动），且项目里有用途对得上的 SOP；用户说“推进/交付/做这个需求”就属于这种。动手实现前先交给 SOP，不要自己做完再说。\n"
	assistantDelegationSOPAuto   = "当前是完全权限：走 SOP 时直接调用写入工具（需求还没建时用绑定 SOP 的 issues_save，已存在时用 issues_dispatch），在回复里说明交给了哪个 SOP、为什么。\n"

	assistantProposalRules = `用户没 @ SOP、也没点名要走 SOP 或完整流程时，你自己决定交给 SOP 的事是建议，要在 drafts 里提交并带 proposal={reason, timing}：需求还没建时提交绑定 SOP 的 issues_save（body 带 workflowId），这张卡就是建议；需求已存在时提交 issues_dispatch。用户接受后，后续派发就是用户要的操作，不再带 proposal。
- 只说“推进”“交付”“实现这个需求”不算点名。
- timing=start：整件事就该走 SOP、自己做不划算。只做判断所需的少量只读查询，然后立刻提交，不要先做半天再问。确认时限由卡片展示，message 里不要提。
- timing=end：先把自己能做的做完并在 message 里汇报，再把可选的后续 SOP 作为建议提交；用户不点也不影响本轮交付。
- 不要在做到一半时提交建议；每轮最多一个；问答、只读查询和小改动不建议。
- 用户拒绝 timing=start 的建议时，下一轮由你自己完成。
`
)

// vetProposalDrafts makes Main Bot ask before a SOP the user did not ask
// for, outside full access, and rejects proposals the conversation no longer
// accepts, before submit_assistant_reply ends the turn.
func (h *assistantHandlers) vetProposalDrafts(session *AgentToolSession, reply *assistantReply) error {
	if session.ExecMode.AutoWrite && !session.ExecMode.ReviewWrites {
		return nil
	}
	var messages []assistantMessage
	var history *assistantProposalHistory
	load := func() *assistantProposalHistory {
		if history == nil {
			// Prefer the request. A new personal session is stored after the
			// reply, so an SOP the user just named is only here.
			messages = append([]assistantMessage(nil), session.turnMessages...)
			if len(messages) == 0 {
				if conv, ok := h.repo.AssistantSession(session.ChatOwnerID, session.ChatSessionID); ok {
					messages = conv.Messages
				}
			}
			loaded := proposalHistory(messages)
			history = &loaded
		}
		return history
	}
	for i := range reply.Drafts {
		d := &reply.Drafts[i]
		if !assistantSOPDraft(*d) {
			continue
		}
		if d.Proposal == nil {
			// Only what the user said after turning proposals off counts.
			if h.sopRequested(session.ProjectID, messages[load().MutedAt+1:]) {
				continue
			}
			if history.Muted {
				return errors.New("用户已关闭本会话的 SOP 建议，之后也没点名要走 SOP：自己完成用户的请求")
			}
			if history.Accepted {
				continue
			}
			return fmt.Errorf("用户没有 @ SOP 也没点名要走 SOP，%s 这张卡是你的建议：给它带上 proposal={reason, timing:\"start\"}，用户可以选择让你自己做", d.Tool)
		}
		load()
		if history.Muted {
			return errors.New("用户已关闭本会话的 SOP 建议：自己完成用户的请求；如果用户本轮明确要求走 SOP，请他用 @SOP 发起")
		}
		if _, declined := history.Declined[assistantProposalTarget(*d)]; declined {
			return errors.New("用户在本会话已拒绝过这条建议，不要再提：自己完成，或换一种做法")
		}
		if history.DeclinedEnd && d.Proposal.Timing == assistantProposalEnd {
			return errors.New("用户在本会话拒绝过后续建议，不要再提 timing=end 建议：把草稿去掉，直接回复本轮结果")
		}
	}
	return nil
}

// sopRequested reports whether the user asked for an SOP in this
// conversation: by @-mentioning one, or by naming SOP, the full process, or
// one of the project's SOPs.
func (h *assistantHandlers) sopRequested(projectID string, messages []assistantMessage) bool {
	names := []string{"sop", "完整流程", "走流程"}
	for _, wf := range h.repo.Workflows() {
		if wf.ProjectID == projectID {
			names = append(names, strings.ToLower(wf.ID), strings.ToLower(wf.Name))
		}
	}
	for _, m := range messages {
		if m.Role != "user" || m.Trigger != "" {
			continue
		}
		for _, ref := range m.References {
			if ref.Type == "sop" {
				return true
			}
		}
		text := strings.ToLower(m.Content)
		for _, name := range names {
			if name != "" && assistantMentionsUnnegated(text, name) {
				return true
			}
		}
	}
	return false
}

// assistantNegationWindow is how many runes before a mention are checked
// for a negation, so "不要走 SOP" does not count as asking for one.
const assistantNegationWindow = 6

func assistantMentionsUnnegated(text, name string) bool {
	for from := 0; ; {
		at := strings.Index(text[from:], name)
		if at < 0 {
			return false
		}
		at += from
		before := []rune(text[:at])
		if len(before) > assistantNegationWindow {
			before = before[len(before)-assistantNegationWindow:]
		}
		if !strings.ContainsAny(string(before), "不别勿免") && !strings.Contains(string(before), "无需") && !strings.Contains(string(before), "没必要") {
			return true
		}
		from = at + len(name)
	}
}

type assistantProposalFollowUp struct {
	Text string `json:"text"`
}

// decideProposal records the user's answer to a proposal card. The card
// accepts before it runs its operation, so a timeout that already handed the
// work to Main Bot makes the accept fail instead of doing it twice; declining
// a timing=start proposal returns the turn the client sends next.
func (h *assistantHandlers) decideProposal(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	ownerID, shared, err := h.conversationScope(c, id, "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	var body struct {
		MessageIndex int    `json:"messageIndex"`
		DraftIndex   int    `json:"draftIndex"`
		Decision     string `json:"decision"`
	}
	if !decode(c, &body) {
		return
	}
	switch body.Decision {
	case domain.AssistantProposalAccepted, domain.AssistantProposalDeclined, domain.AssistantProposalMuted:
	default:
		c.JSON(400, gin.H{"error": "未知的处理方式"})
		return
	}
	if job, ok := h.readGeneration(ownerID, id); ok && job.Status == "running" {
		c.JSON(409, gin.H{"error": "请等待回复结束后再处理建议"})
		return
	}
	existing, err := loadAssistantConversation(h.repo, ownerID, id)
	if err != nil {
		c.JSON(404, gin.H{"error": "对话不存在"})
		return
	}
	if body.MessageIndex < 0 || body.MessageIndex >= len(existing.Messages) || existing.Messages[body.MessageIndex].Role != "assistant" {
		c.JSON(400, gin.H{"error": "建议不存在"})
		return
	}
	var content map[string]any
	if json.Unmarshal([]byte(existing.Messages[body.MessageIndex].Content), &content) != nil {
		c.JSON(400, gin.H{"error": "建议不存在"})
		return
	}
	drafts, _ := content["drafts"].([]any)
	if body.DraftIndex < 0 || body.DraftIndex >= len(drafts) {
		c.JSON(400, gin.H{"error": "建议不存在"})
		return
	}
	raw, _ := drafts[body.DraftIndex].(map[string]any)
	encoded, _ := json.Marshal(raw)
	var draft assistantDraft
	if raw == nil || json.Unmarshal(encoded, &draft) != nil || draft.Proposal == nil {
		c.JSON(400, gin.H{"error": "这不是派发建议"})
		return
	}
	if draft.Decision != "" {
		c.JSON(409, gin.H{"error": "这条建议已经处理过"})
		return
	}
	raw["decision"] = body.Decision
	next := existing
	next.Messages = append([]assistantMessage(nil), existing.Messages...)
	next.Messages[body.MessageIndex].Content = jsonObject(content)
	var saveErr error
	if shared {
		saveErr = h.repo.PutSharedAssistantReceipt(ownerID, next, existing)
	} else {
		saveErr = h.repo.PutAssistantSessionIfUnchanged(ownerID, next, existing)
	}
	if saveErr != nil {
		c.JSON(409, gin.H{"error": "会话已更新，请刷新后再处理"})
		return
	}
	stored, err := loadAssistantConversation(h.repo, ownerID, id)
	if err != nil {
		c.JSON(500, gin.H{"error": "保存对话失败"})
		return
	}
	var followUp *assistantProposalFollowUp
	if body.Decision != domain.AssistantProposalAccepted && draft.Proposal.Timing == assistantProposalStart {
		followUp = &assistantProposalFollowUp{Text: assistantProposalDeclinedText}
	}
	c.JSON(200, gin.H{"session": stored, "followUp": followUp})
}

const assistantProposalDeclinedText = "不走 SOP，这件事由你直接完成。"

// assistantProposalTimeout is how long a timing=start proposal blocks the
// conversation before Main Bot does the work itself.
const assistantProposalTimeout = 3 * time.Minute

const (
	assistantProposalExpiredText    = "SOP 建议超时未确认，这件事由你直接完成。"
	assistantProposalTimeoutTrigger = "proposal_timeout"
)

// stampProposalDeadlines gives each pending timing=start proposal the
// deadline its card counts down to; a zero deadline means no timer will run.
func stampProposalDeadlines(drafts []assistantDraft, deadline time.Time) {
	for i, d := range drafts {
		if d.Proposal == nil {
			continue
		}
		p := *d.Proposal
		p.ExpiresAt = nil
		if !deadline.IsZero() && p.Timing == assistantProposalStart && d.Decision == "" && !d.Saved {
			at := deadline.UTC()
			p.ExpiresAt = &at
		}
		drafts[i].Proposal = &p
	}
}

// pendingStartProposal finds the timing=start proposal the latest reply is
// still waiting on.
func pendingStartProposal(messages []assistantMessage) (messageIndex, draftIndex int, deadline time.Time, ok bool) {
	last := len(messages) - 1
	if last < 0 || messages[last].Role != "assistant" {
		return 0, 0, time.Time{}, false
	}
	var reply struct {
		Drafts []assistantDraft `json:"drafts"`
	}
	if json.Unmarshal([]byte(messages[last].Content), &reply) != nil {
		return 0, 0, time.Time{}, false
	}
	for i, d := range reply.Drafts {
		if d.Proposal != nil && d.Proposal.Timing == assistantProposalStart && d.Proposal.ExpiresAt != nil && d.Decision == "" && !d.Saved {
			return last, i, *d.Proposal.ExpiresAt, true
		}
	}
	return 0, 0, time.Time{}, false
}

// expireProposalLater hands an unanswered timing=start proposal back to Main
// Bot at its deadline, so a task left alone does not stall on
// a card. Anything the user does first — answering it, sending a message,
// starting another turn — leaves the conversation as it is.
func (h *assistantHandlers) expireProposalLater(parent context.Context, g domain.AssistantGeneration, req assistantRequest, actor *auth.User) {
	session, ok := h.repo.AssistantSession(g.UserID, g.SessionID)
	if !ok {
		return
	}
	messageIndex, draftIndex, deadline, ok := pendingStartProposal(session.Messages)
	if !ok {
		return
	}
	time.AfterFunc(time.Until(deadline), func() {
		done := inflightTasks.Add()
		defer done()
		steer := h.expireProposal(g.UserID, g.SessionID, messageIndex, draftIndex, deadline, g.ActorID, g.ActorName)
		if steer == nil {
			return
		}
		steer.Model, steer.Effort, steer.ApprovalMode = req.Model, req.Effort, req.ApprovalMode
		h.followSteer(parent, g, steer, actor)
	})
}

// expireProposal marks the proposal expired and appends the turn that tells
// Main Bot to do the work, or returns nil when the conversation moved on —
// including a regenerated reply whose card is not the one timed.
func (h *assistantHandlers) expireProposal(ownerID int64, sessionID string, messageIndex, draftIndex int, deadline time.Time, actorID int64, actorName string) *domain.AssistantSteer {
	session, ok := h.repo.AssistantSession(ownerID, sessionID)
	if !ok || len(session.Messages) != messageIndex+1 {
		return nil
	}
	var content map[string]any
	if json.Unmarshal([]byte(session.Messages[messageIndex].Content), &content) != nil {
		return nil
	}
	drafts, _ := content["drafts"].([]any)
	if draftIndex < 0 || draftIndex >= len(drafts) {
		return nil
	}
	raw, _ := drafts[draftIndex].(map[string]any)
	if decision, _ := raw["decision"].(string); raw == nil || decision != "" || raw["saved"] == true {
		return nil
	}
	proposal, _ := raw["proposal"].(map[string]any)
	timing, _ := proposal["timing"].(string)
	stamped, _ := proposal["expiresAt"].(string)
	if at, err := time.Parse(time.RFC3339Nano, stamped); timing != assistantProposalStart || err != nil || !at.Equal(deadline) {
		return nil
	}
	raw["decision"] = domain.AssistantProposalExpired
	steer := &domain.AssistantSteer{Content: assistantProposalExpiredText, ActorID: actorID, ActorName: actorName, Trigger: assistantProposalTimeoutTrigger}
	next := session
	next.Messages = append([]assistantMessage(nil), session.Messages...)
	next.Messages[messageIndex].Content = jsonObject(content)
	next.Messages = append(next.Messages, assistantMessage{Role: "user", Content: steer.Content, AuthorID: actorID, AuthorName: actorName, CreatedAt: time.Now().UTC(), Trigger: steer.Trigger})
	if err := h.repo.PutAssistantSessionIfUnchanged(ownerID, next, session); err != nil {
		return nil
	}
	return steer
}
