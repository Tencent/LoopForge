package notification

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRenderAIReportErrors(t *testing.T) {
	request := domain.NotificationReportRequest{Binding: domain.NotificationBinding{Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai"}}
	if _, _, err := (&Service{}).renderAIReport(context.Background(), domain.Run{}, "m", request); err == nil || err.Error() != "独立汇报服务不可用" {
		t.Fatalf("missing generator err = %v", err)
	}
	failed := errors.New("generator failed")
	svc := &Service{GenerateReport: func(context.Context, domain.Run, string, string) (domain.AINotificationContent, error) {
		return domain.AINotificationContent{}, failed
	}}
	if _, _, err := svc.renderAIReport(context.Background(), domain.Run{}, "m", request); !errors.Is(err, failed) {
		t.Fatalf("generator err = %v", err)
	}
	svc.GenerateReport = func(context.Context, domain.Run, string, string) (domain.AINotificationContent, error) {
		return domain.AINotificationContent{Title: "t", Body: "  "}, nil
	}
	if _, _, err := svc.renderAIReport(context.Background(), domain.Run{}, "m", request); err == nil || err.Error() != "AI 未返回有效整体报告" {
		t.Fatalf("blank body err = %v", err)
	}
}

func TestRenderAIReportRendersBindingTemplate(t *testing.T) {
	request := domain.NotificationReportRequest{
		Binding: domain.NotificationBinding{Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai", Template: "汇报\n{{ai.body}}"},
		Context: domain.NotificationContext{Result: "节点原始输出"},
	}
	var gotModel string
	var hasDeadline bool
	svc := &Service{GenerateReport: func(ctx context.Context, _ domain.Run, model, _ string) (domain.AINotificationContent, error) {
		gotModel = model
		_, hasDeadline = ctx.Deadline()
		return domain.AINotificationContent{Title: "整体成果", Body: "需求已交付"}, nil
	}}
	content, source, err := svc.renderAIReport(context.Background(), domain.Run{}, "model-x", request)
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "model-x" || !hasDeadline {
		t.Fatalf("model %q deadline %v", gotModel, hasDeadline)
	}
	if source != "ai" || !strings.Contains(content, "需求已交付") || strings.Contains(content, "节点原始输出") {
		t.Fatalf("source %q content %q", source, content)
	}
}
