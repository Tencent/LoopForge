package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

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

// botDraftSummaryLine describes one draft for the chat and the page.
func botDraftSummaryLine(d assistantDraft) string {
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
	for i, d := range drafts {
		line := fmt.Sprintf("%d. %s", i+1, botDraftSummaryLine(d))
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
	return "\n\n以下操作需要你在平台确认后才会执行（" + fmt.Sprint(int(repo.BotDraftTTL.Minutes())) + " 分钟内有效）：\n" + summary + "\n[在平台确认这些操作](" + link + ")"
}

type botDraftView struct {
	domain.BotPendingDraft
	BotName     string   `json:"botName,omitempty"`
	ProjectName string   `json:"projectName,omitempty"`
	Operations  []string `json:"operations"`
	Results     []string `json:"results,omitempty"`
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
	bot, err := h.repo.NotificationBot(draft.ConfigID)
	if err != nil || !bot.Enabled || !bot.InteractionEnabled || bot.EffectiveWriteApproval() != domain.BotWriteConfirm {
		h.invalidateBotDraft(c, draft, user, "机器人已停用、删除或不再使用网页确认")
		return
	}
	binding, err := h.repo.BotUserBinding(bot.IdentityScope(), draft.SenderID)
	if err != nil || binding.UserID != user.ID {
		h.invalidateBotDraft(c, draft, user, "聊天账号的绑定已解除或已变化")
		return
	}
	if _, err := h.botExecutionUser(bot, draft.SenderID); err != nil {
		h.invalidateBotDraft(c, draft, user, err.Error())
		return
	}
	begun, err := h.repo.BeginBotPendingDraft(id, user.ID, now)
	if errors.Is(err, repo.ErrBotDraftNotPending) {
		c.JSON(200, h.botDraftView(begun))
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	var drafts []assistantDraft
	if err := json.Unmarshal([]byte(begun.Drafts), &drafts); err != nil {
		_ = h.repo.FinishBotPendingDraft(id, repo.BotDraftFailed, "草稿内容无法读取，没有执行", time.Now().UTC())
		draft, _ = h.repo.BotPendingDraft(id, time.Now().UTC())
		c.JSON(200, h.botDraftView(draft))
		return
	}
	for i := range drafts {
		drafts[i].Saved, drafts[i].Auto, drafts[i].SaveError = false, false, ""
	}
	// Same preflight and execution as the web assistant: drafts with
	// warnings are not executed. The request context is detached so a
	// closed tab cannot interrupt the writes halfway.
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Minute)
	defer cancel()
	applied := h.resolveAssistantDrafts(execCtx, user, draft.ProjectID, drafts, AssistantApprovalAuto)
	results := make([]string, 0, len(applied))
	status := repo.BotDraftExecuted
	for _, d := range applied {
		line := botDraftSummaryLine(d)
		switch {
		case d.Saved:
			results = append(results, "已完成："+line)
		case d.SaveError != "":
			status = repo.BotDraftFailed
			results = append(results, "未完成："+line+"（"+d.SaveError+"）")
		default:
			status = repo.BotDraftFailed
			results = append(results, "未执行："+line+"（"+strings.Join(d.Warnings, "；")+"）")
		}
	}
	if err := h.repo.FinishBotPendingDraft(id, status, strings.Join(results, "\n"), time.Now().UTC()); err != nil {
		respond(c, err)
		return
	}
	draft, _ = h.repo.BotPendingDraft(id, time.Now().UTC())
	c.JSON(200, h.botDraftView(draft))
}

func (h *assistantHandlers) invalidateBotDraft(c *gin.Context, draft domain.BotPendingDraft, user domain.User, reason string) {
	now := time.Now().UTC()
	if err := h.repo.CancelBotPendingDraft(draft.ID, user.ID, reason, now); err != nil && !errors.Is(err, repo.ErrBotDraftNotPending) && !errors.Is(err, gorm.ErrRecordNotFound) {
		respond(c, err)
		return
	}
	current, _ := h.repo.BotPendingDraft(draft.ID, now)
	c.JSON(409, gin.H{"error": "操作已失效：" + reason, "code": "draft_invalidated", "draft": h.botDraftView(current)})
}
