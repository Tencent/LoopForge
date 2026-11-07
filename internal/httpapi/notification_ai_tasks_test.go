package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"context"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScheduledAssistantNotificationUsesModelOutputWithoutExtraCalls(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.PutUser(domain.User{LoginName: "ai-owner", CodeBuddyAPIKey: "test-ai-key"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := s.UserByLoginName("ai-owner")
	if err = s.PutProject(domain.Project{ID: "ai-project", Name: "项目", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "ai-bot", ProjectID: "ai-project", Name: "通知", BotID: "wecom", Enabled: true, Target: &domain.BotChat{ID: "chat", Name: "项目群", Type: "group"}}
	if err = s.SaveNotificationBot(bot, "test-ciphertext"); err != nil {
		t.Fatal(err)
	}
	task := domain.AssistantTask{ID: "ai-task", ProjectID: "ai-project", Name: "日报", Prompt: "汇总今天进展", IntervalSec: 3600, Notifications: []domain.NotificationBinding{{BotID: bot.ID, Events: []string{"succeeded", "failed"}, Template: "{{task.name}}\n{{ai.summary}}", AIInstruction: "两句话总结"}}}
	if err = s.PutAssistantTask(owner.ID, task); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := &assistantHandlers{deps: &deps{repo: s}}
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		calls++
		if !strings.Contains(prompt, "两句话总结") || !strings.Contains(prompt, "分享通知内容") {
			t.Error("AI instructions missing")
		}
		return "日报报告\n```notification\n{\"title\":\"日报\",\"summary\":\"今日完成登录优化\",\"body\":\"详细进展\"}\n```", nil
	}
	h.runStoredAssistantTask(owner, task)
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if calls != 1 || len(items) != 1 || items[0].Content != "日报\n今日完成登录优化" || items[0].ContentSource != "ai" {
		t.Fatalf("calls %d delivery %+v", calls, items)
	}
	stored, _ := s.AssistantTask(owner.ID, task.ID)
	if strings.Contains(stored.LastMessage, "```notification") {
		t.Fatal("metadata leaked into UI summary")
	}
}
func TestNotificationPreviewDoesNotSendOrMutate(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	if err = s.PutProject(domain.Project{ID: "preview-project", Name: "预览项目"}); err != nil {
		t.Fatal(err)
	}
	provider := &botAPIProvider{}
	svc := notification.New(s)
	svc.Provider = provider
	h := &notificationHandlers{deps: &deps{repo: s}, service: svc}
	g := gin.New()
	g.POST("/preview", h.preview)
	req := httptest.NewRequest("POST", "/preview", strings.NewReader(`{"projectId":"preview-project","sourceType":"schedule_job","binding":{"events":["succeeded"],"template":"{{task.name}} 完成\n{{ai.summary}}"}}`))
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "每日进度汇总 完成") || provider.sent != 0 {
		t.Fatalf("preview %d %s", rec.Code, rec.Body.String())
	}
	items, _ := s.NotificationDeliveries("preview-project")
	if len(items) != 0 {
		t.Fatal("preview created delivery")
	}
}
func TestScheduleNotificationConfigCannotForgeResult(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	if err = s.PutUser(domain.User{LoginName: "schedule-owner"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := s.UserByLoginName("schedule-owner")
	if err = s.PutProject(domain.Project{ID: "p-notify", Name: "P", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "b-notify", ProjectID: "p-notify", Name: "B", Enabled: true, Target: &domain.BotChat{ID: "chat", Name: "项目群", Type: "group"}}
	if err = s.SaveNotificationBot(bot, "ciphertext"); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: s, authClient: auth.NewClient(auth.Config{SessionSecret: "test-notify-secret"})}
	h := &scheduleHandlers{deps: d, notifications: &notificationHandlers{deps: d, service: notification.New(s)}}
	g := gin.New()
	g.POST("/jobs", h.scheduleJobs)
	body := `{"id":"j-notify","name":"日报","projectId":"p-notify","skipDispatch":true,"assistantPrompt":"汇总进展","intervalSec":3600,"lastStatus":"success","lastRunAt":"2026-10-09T01:00:00Z","notificationResult":"伪造 AI 内容","notifications":[{"botId":"b-notify","events":["succeeded"],"template":"{{task.name}}"}]}`
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader(body))
	token, _ := d.authClient.SignUser(&auth.User{ID: owner.ID, LoginName: owner.LoginName, AuthTime: time.Now().UTC()})
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save %d %s", rec.Code, rec.Body.String())
	}
	job, _ := s.ScheduleJob("j-notify")
	if job.LastStatus != "" || job.NotificationResult != "" || !job.LastRunAt.IsZero() {
		t.Fatal("client forged task result")
	}
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 {
		t.Fatal("config save sent notification")
	}
}
