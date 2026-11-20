package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// WeCom "confirm" write mode (stage G). The assistant runs with platform
// writes blocked and submits drafts; the drafts are frozen in
// bot_pending_drafts and the chat reply carries a summary and a link to
// /bot-approve.html#draft=<id>. Only the bound platform user can open and
// confirm it, after the server re-checks the binding, the Bot configuration,
// project permission, expiry and the frozen parameters. The confirmation
// moves the draft from pending to executing in one statement, then runs the
// drafts through the same preflight and execution as the web assistant.

var (
	botDraftResources = map[string]string{"issues": "需求", "workflows": "SOP", "agents": "角色", "projects": "项目", "rules": "规则", "skills": "技能", "schedule_jobs": "定时任务", "runs": "运行", "assistant_tasks": "AI 助手任务", "sandboxes": "沙箱", "dockerfiles": "Dockerfile", "environments": "环境安装脚本", "business_domains": "业务领域", "repositories": "仓库"}
	botDraftVerbs     = map[string]string{"save": "保存", "delete": "删除", "dispatch": "派发", "start": "启动", "cancel": "取消", "reject": "退回", "run": "运行", "restore": "恢复", "pass": "通过", "retry": "重试", "pause": "暂停", "resume": "继续", "reopen": "重新打开", "publish": "发布", "clone": "复制"}
)

// botDraftLabel names a platform write in Chinese ("保存需求"), falling back
// to the operation description and then the tool name.
func botDraftLabel(tool string) string {
	tool = strings.TrimSpace(tool)
	if i := strings.LastIndex(tool, "_"); i > 0 {
		if verb, ok := botDraftVerbs[tool[i+1:]]; ok {
			if resource, ok := botDraftResources[tool[:i]]; ok {
				return verb + resource
			}
		}
	}
	if op, ok := assistantPublicOperations[tool]; ok && strings.TrimSpace(op.Description) != "" {
		return truncateRunes(strings.TrimSpace(strings.SplitN(op.Description, ".", 2)[0]), 40)
	}
	return tool
}

// setDraftBodyField returns body with key set to value.
func setDraftBodyField(body json.RawMessage, key string, value any) json.RawMessage {
	obj := map[string]any{}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &obj)
	}
	if value == nil {
		delete(obj, key)
	} else {
		obj[key] = value
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// startBotDraftIssue dispatches the SOP of a requirement the draft created.
func (h *assistantHandlers) startBotDraftIssue(ctx context.Context, user domain.User, projectID string, d assistantDraft) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(d.Result), &created) != nil || strings.TrimSpace(created.ID) == "" {
		return "", errors.New("没有拿到新需求的编号，请在平台手动启动")
	}
	result, err := h.dispatchAssistantMCPCall(withAssistantExecMode(ctx, assistantExecMode{AutoWrite: true}), user, projectID, assistantMCPCall{Server: "agentflow", Tool: "issues_dispatch", Arguments: map[string]any{"path": map[string]any{"id": created.ID}, "body": map[string]any{}}})
	if err != nil {
		return "", err
	}
	var task struct {
		RunID string `json:"runId"`
	}
	if json.Unmarshal([]byte(result), &task) == nil && task.RunID != "" {
		return "（运行 " + task.RunID + "）", nil
	}
	return "", nil
}

// botDraftSummaryLine describes one draft for the chat and the page.
func botDraftSummaryLine(d assistantDraft) string {
	return botDraftSummaryLineSOP(d, "")
}

// botDraftSummaryLineSOP adds the SOP a new requirement will be bound to.
func botDraftSummaryLineSOP(d assistantDraft, sop string) string {
	line := botDraftSummaryText(d)
	if sop != "" {
		line += "（SOP：" + sop + "）"
	}
	return line
}

func botDraftSummaryText(d assistantDraft) string {
	label := botDraftLabel(d.Tool)
	target := ""
	var body map[string]any
	if len(d.Body) > 0 && json.Unmarshal(d.Body, &body) == nil {
		for _, key := range []string{"title", "name", "id"} {
			if v, ok := body[key].(string); ok && strings.TrimSpace(v) != "" {
				target = truncateRunes(strings.TrimSpace(v), 60)
				break
			}
		}
	}
	if target == "" {
		target = assistantDraftRefID(d)
	}
	if target != "" {
		return label + "：" + target
	}
	return label
}

// confirmBotDrafts freezes write drafts for page confirmation and returns
// the text appended to the chat reply.
func (h *assistantHandlers) confirmBotDrafts(ctx context.Context, bot domain.NotificationBot, user domain.User, item domain.BotInteraction, drafts []assistantDraft) string {
	base := ""
	if h.notifications != nil && h.notifications.service != nil {
		base = strings.TrimRight(h.notifications.service.PublicURL, "/")
	}
	if u, err := url.Parse(base); base == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "\n\n这次需要修改平台数据，但平台外部访问地址尚未配置，无法生成确认链接，操作没有执行。请联系管理员，或到平台页面完成。"
	}
	drafts = h.resolveAssistantDrafts(ctx, user, bot.ProjectID, drafts, AssistantApprovalConfirm)
	raw, err := json.Marshal(drafts)
	if err != nil {
		return "\n\n操作草稿无法保存，没有执行。请到平台页面完成。"
	}
	lines := make([]string, 0, len(drafts))
	newIssues := 0
	for i, d := range drafts {
		sop := ""
		if explicit, def, ok := h.botDraftIssueSOP(bot.ProjectID, d); ok {
			sop = h.botWorkflowName(explicit)
			if explicit == "" {
				sop = "项目默认 · " + h.botWorkflowName(def)
			}
			newIssues++
		}
		line := fmt.Sprintf("%d. %s", i+1, botDraftSummaryLineSOP(d, sop))
		if len(d.Warnings) > 0 {
			line += "（注意：" + truncateRunes(strings.Join(d.Warnings, "；"), 120) + "）"
		}
		lines = append(lines, line)
	}
	summary := strings.Join(lines, "\n")
	saved, err := h.repo.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, IdentityScope: bot.IdentityScope(), SenderID: item.SenderID, ProjectID: bot.ProjectID, UserID: user.ID, InteractionID: item.ID, Summary: summary, Drafts: string(raw)}, time.Now().UTC())
	if err != nil {
		return "\n\n操作草稿无法保存，没有执行。请稍后重试或到平台页面完成。"
	}
	link := base + "/bot-approve.html#draft=" + saved.ID
	head := "\n\n以下操作需要你在平台确认后才会执行（" + fmt.Sprint(int(repo.BotDraftTTL.Minutes())) + " 分钟内有效）：\n" + summary
	if newIssues > 0 {
		head += "\n确认页可以更换 SOP，并选择创建后立即启动。"
	}
	if bot.ChannelKind() != domain.BotChannelWeCom {
		// Feishu and DingTalk replies are plain text: the URL stands on
		// its own line so auto-linking does not swallow other characters.
		return head + "\n在平台确认这些操作：\n" + link
	}
	return head + "\n[在平台确认这些操作](" + link + ")"
}

type botDraftView struct {
	domain.BotPendingDraft
	BotName     string   `json:"botName,omitempty"`
	ProjectName string   `json:"projectName,omitempty"`
	Operations  []string `json:"operations"`
	Results     []string `json:"results,omitempty"`
	// NewIssues are the requirements this draft creates; the user picks
	// their SOP and whether to start it right after creation.
	NewIssues []botDraftIssue    `json:"newIssues,omitempty"`
	Workflows []botDraftWorkflow `json:"workflows,omitempty"`
}

type botDraftIssue struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	// WorkflowID is the SOP the model named; empty means the project
	// default, which is DefaultWorkflowID / DefaultWorkflowName.
	WorkflowID          string `json:"workflowId"`
	DefaultWorkflowID   string `json:"defaultWorkflowId"`
	DefaultWorkflowName string `json:"defaultWorkflowName"`
}

type botDraftWorkflow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// botDraftChoice is the user's SOP choice for one new requirement; an empty
// workflowId selects the project default SOP.
type botDraftChoice struct {
	Index      int    `json:"index"`
	WorkflowID string `json:"workflowId"`
	Start      bool   `json:"start"`
}

// botDraftIssueWorkflow reports the SOP a draft creating a new requirement
// binds to: the workflowId the model set, else the project default. Drafts
// that update an existing issue are not new requirements.
func (h *assistantHandlers) botDraftIssueWorkflow(projectID string, d assistantDraft) (string, bool) {
	explicit, def, ok := h.botDraftIssueSOP(projectID, d)
	if explicit != "" {
		return explicit, ok
	}
	return def, ok
}

// botDraftIssueSOP returns the SOP the model named for a new requirement
// (empty when none) and the project default it would otherwise get.
func (h *assistantHandlers) botDraftIssueSOP(projectID string, d assistantDraft) (string, string, bool) {
	if strings.TrimSpace(d.Tool) != "issues_save" {
		return "", "", false
	}
	var body map[string]any
	if len(d.Body) > 0 && json.Unmarshal(d.Body, &body) != nil {
		return "", "", false
	}
	if id, _ := body["id"].(string); strings.TrimSpace(id) != "" {
		if _, exists := h.repo.Issue(strings.TrimSpace(id)); exists {
			return "", "", false
		}
	}
	source, _ := body["source"].(string)
	kind, _ := body["kind"].(string)
	explicit, _ := body["workflowId"].(string)
	return strings.TrimSpace(explicit), h.repo.DefaultIssueWorkflow(projectID, source, kind), true
}

func (h *assistantHandlers) botWorkflowName(id string) string {
	if wf, ok := h.repo.Workflow(id); ok && strings.TrimSpace(wf.Name) != "" {
		return wf.Name
	}
	if id == "" {
		return "未绑定"
	}
	return id
}

// botDraftWorkflows lists the SOPs a requirement of the project may be bound
// to, like the requirement form on the platform: the project's own SOPs,
// shared SOPs and the platform generic templates (archived ones excluded),
// plus the current choices. The project default is offered separately.
func (h *assistantHandlers) botDraftWorkflows(projectID string, include ...string) []botDraftWorkflow {
	seen := map[string]bool{}
	out := []botDraftWorkflow{}
	add := func(id, name string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, botDraftWorkflow{ID: id, Name: name})
	}
	generic := []domain.WorkflowDefinition{}
	for _, wf := range h.repo.Workflows() {
		if wf.Status == "archived" {
			continue
		}
		switch {
		case wf.Generic:
			generic = append(generic, wf)
		case wf.ProjectID == projectID || wf.ProjectID == "" || wf.Shared:
			add(wf.ID, wf.Name)
		}
	}
	for _, id := range include {
		add(id, h.botWorkflowName(id))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for _, wf := range generic {
		add(wf.ID, "通用 · "+wf.Name)
	}
	return out
}

func (h *assistantHandlers) botDraftNewIssues(projectID string, drafts []assistantDraft) []botDraftIssue {
	out := []botDraftIssue{}
	for i, d := range drafts {
		explicit, def, ok := h.botDraftIssueSOP(projectID, d)
		if !ok {
			continue
		}
		out = append(out, botDraftIssue{Index: i, Title: botDraftSummaryText(d), WorkflowID: explicit, DefaultWorkflowID: def, DefaultWorkflowName: h.botWorkflowName(def)})
	}
	return out
}

func (h *assistantHandlers) botDraftUser(c *gin.Context) (domain.User, bool) {
	if _, ok := bearerToken(c.Request); ok {
		c.JSON(401, gin.H{"error": "请使用平台网页登录确认"})
		return domain.User{}, false
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current == nil || current.IsGuest || current.ID <= 0 {
		c.JSON(401, gin.H{"error": "请先登录平台"})
		return domain.User{}, false
	}
	user, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录平台"})
		return domain.User{}, false
	}
	return user, true
}

func (h *assistantHandlers) botDraftView(d domain.BotPendingDraft) botDraftView {
	view := botDraftView{BotPendingDraft: d, Operations: strings.Split(d.Summary, "\n")}
	if bot, err := h.repo.NotificationBot(d.ConfigID); err == nil {
		view.BotName = bot.Name
	}
	if project, ok := h.repo.Project(d.ProjectID); ok {
		view.ProjectName = project.Name
	}
	if d.Result != "" {
		view.Results = strings.Split(d.Result, "\n")
	}
	if d.Status == repo.BotDraftPending {
		var drafts []assistantDraft
		if json.Unmarshal([]byte(d.Drafts), &drafts) == nil {
			view.NewIssues = h.botDraftNewIssues(d.ProjectID, drafts)
			if len(view.NewIssues) > 0 {
				ids := make([]string, 0, len(view.NewIssues))
				for _, issue := range view.NewIssues {
					ids = append(ids, issue.WorkflowID)
				}
				view.Workflows = h.botDraftWorkflows(d.ProjectID, ids...)
			}
		}
	}
	return view
}

// botDraft serves GET /api/bot-drafts/:id and the confirm/cancel actions.
// Drafts of other users answer 404, like unknown IDs.
func (h *assistantHandlers) botDraft(c *gin.Context) {
	user, ok := h.botDraftUser(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	now := time.Now().UTC()
	draft, err := h.repo.BotPendingDraft(id, now)
	if err != nil || draft.UserID != user.ID {
		c.JSON(404, gin.H{"error": "确认链接无效或不属于当前账号"})
		return
	}
	if c.Request.Method == "GET" {
		c.JSON(200, h.botDraftView(draft))
		return
	}
	if !botBindingSameOrigin(c) {
		c.JSON(403, gin.H{"error": "请从平台页面操作"})
		return
	}
	if strings.HasSuffix(c.Request.URL.Path, "/cancel") {
		if err := h.repo.CancelBotPendingDraft(id, user.ID, "用户在平台页面取消", now); err != nil && !errors.Is(err, repo.ErrBotDraftNotPending) {
			respond(c, err)
			return
		}
		draft, _ = h.repo.BotPendingDraft(id, now)
		c.JSON(200, h.botDraftView(draft))
		return
	}
	if draft.Status != repo.BotDraftPending {
		// A repeated click returns the current state.
		c.JSON(200, h.botDraftView(draft))
		return
	}
	var req struct {
		Choices []botDraftChoice `json:"choices"`
	}
	if c.Request.ContentLength != 0 && !decodeLimit(c, &req, 64<<10) {
		return
	}
	current, invalid, err := h.executeBotDraft(c.Request.Context(), user, draft, req.Choices, nil)
	switch {
	case err != nil:
		respond(c, err)
	case invalid != "":
		c.JSON(409, gin.H{"error": "操作已失效：" + invalid, "code": "draft_invalidated", "draft": h.botDraftView(current)})
	default:
		c.JSON(200, h.botDraftView(current))
	}
}

// executeBotDraft re-checks and runs a pending draft for user (the platform
// page and the Feishu confirmation card share it). choices set the SOP of
// new requirements; startAll, when set, starts every new requirement's SOP.
// A failed re-check cancels the draft and returns the reason in invalid.
func (h *assistantHandlers) executeBotDraft(ctx context.Context, user domain.User, draft domain.BotPendingDraft, choices []botDraftChoice, startAll *bool) (domain.BotPendingDraft, string, error) {
	id := draft.ID
	now := time.Now().UTC()
	invalidate := func(reason string) (domain.BotPendingDraft, string, error) {
		if err := h.repo.CancelBotPendingDraft(id, user.ID, reason, now); err != nil && !errors.Is(err, repo.ErrBotDraftNotPending) && !errors.Is(err, gorm.ErrRecordNotFound) {
			return draft, "", err
		}
		current, _ := h.repo.BotPendingDraft(id, now)
		return current, reason, nil
	}
	bot, err := h.repo.NotificationBot(draft.ConfigID)
	if err != nil || !bot.Enabled || !bot.InteractionEnabled || bot.EffectiveWriteApproval() != domain.BotWriteConfirm {
		return invalidate("机器人已停用、删除或不再使用网页确认")
	}
	binding, err := h.repo.BotUserBinding(bot.IdentityScope(), draft.SenderID)
	if err != nil || binding.UserID != user.ID {
		return invalidate("聊天账号的绑定已解除或已变化")
	}
	if _, err := h.botExecutionUser(bot, draft.SenderID); err != nil {
		return invalidate(err.Error())
	}
	begun, err := h.repo.BeginBotPendingDraft(id, user.ID, now)
	if errors.Is(err, repo.ErrBotDraftNotPending) {
		return begun, "", nil
	}
	if err != nil {
		return draft, "", err
	}
	finish := func(status, result string) (domain.BotPendingDraft, string, error) {
		if err := h.repo.FinishBotPendingDraft(id, status, result, time.Now().UTC()); err != nil {
			return begun, "", err
		}
		current, _ := h.repo.BotPendingDraft(id, time.Now().UTC())
		return current, "", nil
	}
	var drafts []assistantDraft
	if err := json.Unmarshal([]byte(begun.Drafts), &drafts); err != nil {
		return finish(repo.BotDraftFailed, "草稿内容无法读取，没有执行")
	}
	for i := range drafts {
		drafts[i].Saved, drafts[i].Auto, drafts[i].SaveError = false, false, ""
	}
	// Apply the SOP choices of new requirements. Only SOPs offered on the
	// page are accepted; the frozen draft itself is unchanged.
	start := map[int]bool{}
	sops := map[int]string{}
	newIssues := h.botDraftNewIssues(draft.ProjectID, drafts)
	defaults := map[int]string{}
	for _, issue := range newIssues {
		sops[issue.Index] = issue.WorkflowID
		if issue.WorkflowID == "" {
			sops[issue.Index] = issue.DefaultWorkflowID
		}
		defaults[issue.Index] = issue.DefaultWorkflowID
		if startAll != nil {
			start[issue.Index] = *startAll
		}
	}
	if len(choices) > 0 {
		ids := []string{}
		for _, issue := range newIssues {
			ids = append(ids, issue.WorkflowID)
		}
		allowed := map[string]bool{}
		for _, wf := range h.botDraftWorkflows(draft.ProjectID, ids...) {
			allowed[wf.ID] = true
		}
		for _, choice := range choices {
			current, ok := sops[choice.Index]
			if !ok {
				continue
			}
			wf := strings.TrimSpace(choice.WorkflowID)
			if wf == "" {
				// The project default: drop any SOP the model named so the
				// platform binds the requirement the usual way.
				wf = defaults[choice.Index]
				if wf != current {
					drafts[choice.Index].Body = setDraftBodyField(drafts[choice.Index].Body, "workflowId", nil)
				}
			} else if !allowed[wf] {
				return finish(repo.BotDraftFailed, "所选 SOP 不可用于当前项目，没有执行")
			} else if wf != current {
				drafts[choice.Index].Body = setDraftBodyField(drafts[choice.Index].Body, "workflowId", wf)
			}
			sops[choice.Index] = wf
			start[choice.Index] = choice.Start
		}
	}
	// Same preflight and execution as the web assistant: drafts with
	// warnings are not executed. The context is detached so a closed tab or
	// a dropped connection cannot interrupt the writes halfway.
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	applied := h.resolveAssistantDrafts(execCtx, user, draft.ProjectID, drafts, AssistantApprovalAuto)
	results := make([]string, 0, len(applied))
	status := repo.BotDraftExecuted
	for i, d := range applied {
		sop := ""
		if wf, ok := sops[i]; ok {
			sop = h.botWorkflowName(wf)
		}
		line := botDraftSummaryLineSOP(d, sop)
		switch {
		case d.Saved:
			results = append(results, "已完成："+line)
			if start[i] {
				if started, err := h.startBotDraftIssue(execCtx, user, draft.ProjectID, d); err != nil {
					status = repo.BotDraftFailed
					results = append(results, "未启动："+sop+"（"+truncateRunes(err.Error(), 200)+"）")
				} else {
					results = append(results, "已启动："+sop+started)
				}
			}
		case d.SaveError != "":
			status = repo.BotDraftFailed
			results = append(results, "未完成："+line+"（"+d.SaveError+"）")
		default:
			status = repo.BotDraftFailed
			results = append(results, "未执行："+line+"（"+strings.Join(d.Warnings, "；")+"）")
		}
	}
	return finish(status, strings.Join(results, "\n"))
}

// botDraftCardAction handles the buttons of a Feishu confirmation card. Only
// the platform user bound to the sender who asked can use them; the result
// is the chat reply.
func (h *assistantHandlers) botDraftCardAction(ctx context.Context, bot domain.NotificationBot, item domain.BotInteraction) (string, error) {
	user, err := h.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return err.Error(), botinteraction.ErrCardActionDeferred
	}
	now := time.Now().UTC()
	draft, err := h.repo.BotPendingDraft(strings.TrimSpace(item.DeliveryID), now)
	if err != nil || draft.UserID != user.ID || draft.ConfigID != bot.ID || draft.SenderID != item.SenderID {
		return "这份操作不存在、已过期，或不是你发起的，没有执行。", nil
	}
	if draft.Status != repo.BotDraftPending {
		return botDraftStateReply(draft), nil
	}
	if item.Action == botinteraction.DraftActionCancel {
		if err := h.repo.CancelBotPendingDraft(draft.ID, user.ID, "用户在聊天卡片中取消", now); err != nil && !errors.Is(err, repo.ErrBotDraftNotPending) {
			return "", err
		}
		return "已取消，这些操作不会执行。", nil
	}
	startAll := item.Action == botinteraction.DraftActionConfirmStart
	current, invalid, err := h.executeBotDraft(ctx, user, draft, nil, &startAll)
	if err != nil {
		return "", err
	}
	if invalid != "" {
		return "操作已失效：" + invalid + "。没有执行。", nil
	}
	return botDraftStateReply(current), nil
}

// botDraftStateReply describes a draft that is no longer pending.
func botDraftStateReply(d domain.BotPendingDraft) string {
	head := map[string]string{repo.BotDraftExecuted: "操作已执行：", repo.BotDraftFailed: "操作部分或全部未完成：", repo.BotDraftExecuting: "这份操作正在执行，请稍后在平台查看结果。", repo.BotDraftNeedsReview: "这份操作执行中断，结果待核对，平台不会自动重新执行。", repo.BotDraftExpired: "这份操作已过期，没有执行。请重新发起。", repo.BotDraftCancelled: "这份操作已取消，没有执行。"}[d.Status]
	if head == "" {
		head = "这份操作的状态：" + d.Status
	}
	if d.Result != "" && (d.Status == repo.BotDraftExecuted || d.Status == repo.BotDraftFailed) {
		return head + "\n" + d.Result
	}
	if d.Reason != "" && d.Status == repo.BotDraftCancelled {
		return head + "（" + d.Reason + "）"
	}
	return head
}
