package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"context"
	"path/filepath"
	"strings"
)

func (h *assistantHandlers) generateSOPReport(ctx context.Context, run domain.Run, model, prompt string) (domain.AINotificationContent, error) {
	return h.generateReport(ctx, run, model, prompt, "你是整体交付报告编写者。只能根据提供的资料总结结果。仅允许调用 submit_notification_report，必须通过该工具提交报告，不能用普通文本代替。")
}
func (h *assistantHandlers) generateReport(ctx context.Context, run domain.Run, model, prompt, system string) (domain.AINotificationContent, error) {
	user, ok := h.repo.User(run.ExecutionContext.ResponsibleUserID)
	if !ok || strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		return domain.AINotificationContent{}, notification.ErrReportCredentials
	}
	catalog := loadAssistantModels(ctx, user.CodeBuddyInternetEnvironment)
	if assistantAllowedModel(model, catalog) != model {
		return domain.AINotificationContent{}, notification.ErrReportModel
	}

	modelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := ensureAssistantSessionDirs(user.ID); err != nil {
		return domain.AINotificationContent{}, notification.ErrReportCLI
	}
	session, files, err := startAgentToolSession(ctx, user, run.ExecutionContext.ProjectID, AgentToolReport, assistantExecMode{}, "", nil, cancel, filepath.Join(assistantSessionRoot(user.ID), "mcp-reports"))
	if err != nil {
		return domain.AINotificationContent{}, notification.ErrReportCLI
	}
	defer session.Close()
	defer files.Cleanup()
	session.RunID = run.ID
	session.SetTerminalOnly()
	generate := h.generate
	if generate == nil {
		generate = func(callCtx context.Context, input string, u domain.User) (string, error) {
			raw, _, err := runAssistantCLI(callCtx, assistantCLIRequest{Prompt: input, Model: model, UserID: u.ID, ReportOnly: true, Native: true, MCPConfig: files.MCPConfig, Settings: files.Settings, SystemPrompt: system}, u, nil)
			return raw, err
		}
	}
	for attempt := 0; attempt <= nativeTerminalMaxRetries; attempt++ {
		input := prompt
		if attempt > 0 {
			input = "上一轮没有有效提交报告。请修正参数并调用 submit_notification_report；普通文本不会被接收。\n" + prompt
		}
		_, cliErr := generate(modelCtx, input, user)
		if result, ok := session.takeFinal(); ok && result.Report != nil {
			report := *result.Report
			report.Title = redactAssistantSecrets(strings.ReplaceAll(report.Title, user.CodeBuddyAPIKey, "[redacted]"), user)
			report.Summary = redactAssistantSecrets(strings.ReplaceAll(report.Summary, user.CodeBuddyAPIKey, "[redacted]"), user)
			report.Body = redactAssistantSecrets(strings.ReplaceAll(report.Body, user.CodeBuddyAPIKey, "[redacted]"), user)
			return report, nil
		}
		if ctx.Err() != nil {
			return domain.AINotificationContent{}, ctx.Err()
		}
		if cliErr != nil && !nativeShouldRetryTerminal(ctx, cliErr) {
			return domain.AINotificationContent{}, notification.ErrReportCLI
		}
	}
	return domain.AINotificationContent{}, notification.ErrReportFormat
}
