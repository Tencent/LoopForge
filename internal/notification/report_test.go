package notification

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIndependentReportFrozenAcrossSendRetries(t *testing.T) {
	svc, provider, bot, run := serviceFixture(t)
	svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{})
	bot.Model = "gpt-6.1-sol"
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	run.WorkflowSnapshot.Notifications = []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai", Template: "需求完成\n{{ai.body}}"}}
	run.Status = "succeeded"
	run.Nodes["n"].Status = "succeeded"
	run.Nodes["n"].Output = "最终验证通过"
	run.Artifacts = []domain.Artifact{{ID: "old", Name: "验证报告", Attempt: 1, Content: "旧尝试发现错误"}, {ID: "final", Name: "验证报告", Attempt: 2, Content: "最新验证无错误"}}
	run.Events = []domain.RunEvent{{ID: "complete", Type: "run.succeeded", Timestamp: time.Now().UTC()}}
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	bot.Model = domain.DefaultNotificationReportModel
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.GenerateReport = func(_ context.Context, actual domain.Run, model, prompt string) (domain.AINotificationContent, error) {
		calls++
		if model != "gpt-6.1-sol" || actual.ID != run.ID || !strings.Contains(prompt, "旧尝试发现错误") || !strings.Contains(prompt, "最新验证无错误") || !strings.Contains(prompt, "最终验证通过") {
			t.Fatal("missing run artifacts or selected model")
		}
		return domain.AINotificationContent{Title: "整体成果", Body: "需求已实现，最终验证通过。"}, nil
	}
	provider.err = errors.New("send unavailable")
	if _, err := svc.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 || items[0].ReportState != "generated" || items[0].ReportRequest != "" || items[0].Status != "pending" {
		t.Fatalf("%+v", items)
	}
	stale := items[0]
	stale.LeaseToken = "stale"
	stale.Content = "wrong"
	if ok, err := svc.Store.FreezeNotificationReport(stale); err != nil || ok {
		t.Fatal("stale worker changed report")
	}
	svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", items[0].ID).Update("next_attempt_at", time.Now().UTC().Add(-time.Minute))
	provider.err = nil
	if _, err := svc.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || provider.sent != 1 || !strings.Contains(provider.content, "需求已实现，最终验证通过。") || strings.Contains(provider.content, "旧尝试") {
		t.Fatalf("calls %d sent %d %s", calls, provider.sent, provider.content)
	}
}

func TestIndependentReportFailureUsesStandardMessage(t *testing.T) {
	svc, provider, bot, run := serviceFixture(t)
	svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{})
	run.WorkflowSnapshot.Notifications = []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai"}}
	run.Status = "succeeded"
	run.Nodes["n"].Status = "succeeded"
	run.Nodes["n"].Output = "不应当作整体报告发送的节点输出"
	run.Events = []domain.RunEvent{{ID: "complete", Type: "run.succeeded", Timestamp: time.Now().UTC()}}
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	svc.GenerateReport = func(context.Context, domain.Run, string, string) (domain.AINotificationContent, error) {
		return domain.AINotificationContent{}, errors.New("missing key")
	}
	if _, err := svc.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 || items[0].ReportState != "fallback" || items[0].ReportError == "" || provider.sent != 1 || !strings.Contains(provider.content, "SOP") || strings.Contains(provider.content, "节点输出") {
		t.Fatalf("%+v %s", items, provider.content)
	}
}

func TestReportFailureReasonDoesNotExposeRawCLIOutput(t *testing.T) {
	if got := reportFailureReason(ErrReportCredentials); got != ErrReportCredentials.Error() {
		t.Fatal(got)
	}
	if got := reportFailureReason(errors.New("raw key=private-secret")); strings.Contains(got, "private-secret") {
		t.Fatal("leaked raw error")
	}
	if got := reportFailureReason(context.DeadlineExceeded); got != "整体汇报生成超时" {
		t.Fatal(got)
	}
}
