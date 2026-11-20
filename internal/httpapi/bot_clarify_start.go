package httpapi

import (
	"encoding/json"
	"strings"

	"agentflow/internal/domain"
)

// Clarification before an SOP start from a chat. When the project or the
// SOP asks for it (domain.ClarifyRequiredBeforeStart) and the requirement
// has no saved conclusion yet, a start requested in a WeCom, Feishu or
// DingTalk chat (confirming "创建后立即启动", an issues_dispatch draft, or a
// dispatch of a full-access Bot) does not start the SOP. The clarification
// opens in that conversation instead, marked StartOnSave, and saving its
// conclusion starts the SOP once. "澄清 LOOP-7" stays a separate entry that
// starts nothing by itself.

// botIssueStartWorkflows lists the SOPs a start of the requirement runs:
// workflowID when the start names one, else the requirement's SOP chain,
// else the SOP a new requirement of the project is bound to.
func (h *assistantHandlers) botIssueStartWorkflows(issue domain.Issue, workflowID string) []domain.WorkflowDefinition {
	ids := []string{}
	if id := strings.TrimSpace(workflowID); id != "" {
		ids = append(ids, id)
	} else {
		for _, b := range issue.EffectiveSOPBindings() {
			ids = append(ids, b.WorkflowID)
		}
		if len(ids) == 0 && !issue.WithoutSOP() {
			if def := h.repo.DefaultIssueWorkflow(issue.ProjectID, issue.Source, issue.Kind); def != "" {
				ids = append(ids, def)
			}
		}
	}
	out := make([]domain.WorkflowDefinition, 0, len(ids))
	for _, id := range ids {
		if wf, ok := h.repo.Workflow(id); ok {
			out = append(out, wf)
		}
	}
	return out
}

// botClarifyBeforeStart reports whether a start of the requirement has to
// wait for a clarification conclusion.
func (h *assistantHandlers) botClarifyBeforeStart(issue domain.Issue, workflowID string) bool {
	if issue.Clarification.Status == domain.ClarificationReady {
		return false
	}
	project, ok := h.repo.Project(issue.ProjectID)
	if !ok {
		return false
	}
	return domain.ClarifyRequiredBeforeStart(project, h.botIssueStartWorkflows(issue, workflowID))
}

// holdBotStart is called instead of starting the requirement's SOP when
// botClarifyBeforeStart holds: it opens the clarification in the chat the
// start came from, to start the SOP on saving, and returns what the chat
// is told. The SOP is never started here.
func (h *assistantHandlers) holdBotStart(bot domain.NotificationBot, origin domain.BotChatOrigin, hasOrigin bool, user domain.User, issue domain.Issue) string {
	title := h.botIssueTitle(issue)
	if !hasOrigin {
		return "需求 " + title + " 需要先澄清再启动 SOP，SOP 没有启动。请到平台的需求详情里澄清，确认结论后再启动。"
	}
	conv, err := h.repo.BotConversation(origin.ConversationID)
	if err != nil || conv.BotID != bot.ID {
		return "需求 " + title + " 需要先澄清再启动 SOP，SOP 没有启动。发起的会话已不可用，请发「澄清 " + h.botIssueLabel(issue) + "」开始澄清。"
	}
	return h.openBotClarify(bot, conv, domain.BotInteraction{SenderID: origin.SenderID}, user, issue, true)
}

// botHoldDispatch is the start gate of one issues_dispatch call: held=true
// means the SOP was not started and reply says why.
func (h *assistantHandlers) botHoldDispatch(bot domain.NotificationBot, origin domain.BotChatOrigin, user domain.User, issueID, workflowID string) (string, bool) {
	issue, ok := h.repo.Issue(strings.TrimSpace(issueID))
	if !ok || issue.ProjectID != bot.ProjectID || !h.botClarifyBeforeStart(issue, workflowID) {
		return "", false
	}
	return h.holdBotStart(bot, origin, true, user, issue), true
}

// botDraftDispatchWorkflow is the SOP an issues_dispatch draft names, or "".
func botDraftDispatchWorkflow(d assistantDraft) string {
	var body struct {
		WorkflowID string `json:"workflowId"`
	}
	if len(d.Body) > 0 && json.Unmarshal(d.Body, &body) == nil {
		return strings.TrimSpace(body.WorkflowID)
	}
	return ""
}

// botDispatchCallArgs reads the requirement and the SOP of an
// issues_dispatch tool call.
func botDispatchCallArgs(args map[string]any) (string, string) {
	path, _ := args["path"].(map[string]any)
	body, _ := args["body"].(map[string]any)
	id, _ := path["id"].(string)
	wf, _ := body["workflowId"].(string)
	return strings.TrimSpace(id), strings.TrimSpace(wf)
}

// remapDraftIndex moves per-draft values to the positions the drafts have
// after some were taken out (remap: old index → new index).
func remapDraftIndex[V any](m map[int]V, remap map[int]int) map[int]V {
	out := make(map[int]V, len(m))
	for i, v := range m {
		if j, ok := remap[i]; ok {
			out[j] = v
		}
	}
	return out
}
