package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"testing"
	"time"
)

func testScheduleNotifications(t *testing.T, s *Store, suffix string) {
	t.Helper()
	bot, _ := notificationFixture(t, s, "job-"+suffix)
	job := domain.ScheduleJob{ID: "job-" + suffix, ProjectID: bot.ProjectID, Name: "每日汇总", SkipDispatch: true, AssistantPrompt: "汇总进展", IntervalSec: 3600, Notifications: []domain.NotificationBinding{{BotID: bot.ID, Events: []string{"succeeded", "failed"}, Template: "{{task.name}}：{{ai.summary}}", AIInstruction: "总结结果"}}}
	if err := s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	job.LastRunAt = time.Now().UTC()
	job.LastStatus = "running"
	if err := s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	// Editing the task while it runs must not rewrite this occurrence's notification.
	changed, _ := s.ScheduleJob(job.ID)
	changed.Notifications[0].Template = "下次运行的模板"
	if err := s.PutScheduleJob(changed); err != nil {
		t.Fatal(err)
	}
	changed.LastStatus = "success"
	changed.LastAssistantMessage = "报告\n```notification\n{\"summary\":\"今天交付了三项需求\",\"body\":\"日报正文\"}\n```"
	if err := s.PutScheduleJob(changed); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScheduleJob(changed); err != nil {
		t.Fatal(err)
	}
	items, err := s.NotificationDeliveries(bot.ProjectID)
	if err != nil || len(items) != 1 {
		t.Fatalf("duplicate completion %v %+v", err, items)
	}
	item := items[0]
	if item.SourceType != "schedule_job" || item.Content != "每日汇总：今天交付了三项需求" || item.ContentSource != "ai" {
		t.Fatalf("bad task content %+v", item)
	}
	// Deleting a bound Bot mid-run does not prevent recording task completion.
	next, _ := s.ScheduleJob(job.ID)
	next.LastStatus = "running"
	next.LastRunAt = time.Now().Add(time.Minute).UTC()
	if err := s.PutScheduleJob(next); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNotificationBot(bot.ID); err != nil {
		t.Fatal(err)
	}
	next.LastStatus = "failed"
	next.LastError = "测试失败"
	if err := s.PutScheduleJob(next); err != nil {
		t.Fatal("deleted bot blocked task result", err)
	}
}
func TestScheduledAINotificationsFreezeContentAndConfig(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testScheduleNotifications(t, s, "sqlite")
}
func TestMySQLScheduledAINotificationTransaction(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local mysql DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testScheduleNotifications(t, s, fmt.Sprint(time.Now().UnixNano()))
}
func TestPrivateAssistantTaskNotificationResults(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot, _ := notificationFixture(t, s, "private-task")
	task := domain.AssistantTask{ID: "personal-summary", ProjectID: bot.ProjectID, Name: "巡检", IntervalSec: 3600, Notifications: []domain.NotificationBinding{{BotID: bot.ID, Events: []string{"succeeded", "failed"}, Template: "{{task.name}} · {{ai.summary}}"}}}
	if err = s.PutAssistantTask(1, task); err != nil {
		t.Fatal(err)
	}
	task.LastStatus = "running"
	task.LastRunAt = time.Now().UTC()
	if err = s.PutAssistantTask(1, task); err != nil {
		t.Fatal(err)
	}
	task.LastStatus = "ok"
	task.LastMessage = "短摘要"
	task.NotificationResult = "完整的执行结果，比列表摘要更长"
	if err = s.PutAssistantTask(1, task); err != nil {
		t.Fatal(err)
	}
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 || items[0].SourceType != "assistant_task" || items[0].Content != "巡检 · 完整的执行结果，比列表摘要更长" {
		t.Fatalf("wrong personal content %+v", items)
	}
	if err = s.PutAssistantTask(1, task); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 {
		t.Fatal("duplicate private notification")
	}
	if err = s.PutAssistantTask(2, task); err == nil {
		t.Fatal("other owner overwrote task")
	}
}
func TestWorkflowScopeAndIssueTitle(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot, run := notificationFixture(t, s, "scope")
	if err = s.PutIssue(domain.Issue{ID: "demand", ProjectID: bot.ProjectID, Title: "登录优化", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	run.ExecutionContext.IssueID = "demand"
	run.Events[0].Type = "node.succeeded"
	binding := &run.WorkflowSnapshot.Nodes[0].Notifications[0]
	binding.Scope = "run"
	binding.Events = []string{"succeeded"}
	binding.Template = "需求 {{issue.title}} 的流程已完成"
	if err = s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 {
		t.Fatal("node completion triggered workflow message")
	}
	run.Status = "succeeded"
	run.Events = append(run.Events, domain.RunEvent{ID: "finish", Type: "run.succeeded", Timestamp: time.Now().UTC()})
	if err = s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 || items[0].NodeID != "" || items[0].Content != "需求 登录优化 的流程已完成" {
		t.Fatalf("wrong workflow notification %+v", items)
	}
}

func TestDeletedTaskResultsAreNotResurrected(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot, _ := notificationFixture(t, s, "deleted-result")
	job := domain.ScheduleJob{ID: "deleted-job", ProjectID: bot.ProjectID, Name: "任务", LastRunAt: time.Now().UTC(), LastStatus: "running", Notifications: []domain.NotificationBinding{{BotID: bot.ID, Events: []string{"succeeded"}}}}
	if err = s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteScheduleJob(job.ID); err != nil {
		t.Fatal(err)
	}
	job.LastStatus = "success"
	if err = s.SaveScheduleJobResult(job); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ScheduleJob(job.ID); ok {
		t.Fatal("deleted schedule recreated")
	}
	task := domain.AssistantTask{ID: "deleted-personal", ProjectID: bot.ProjectID, Name: "个人任务", LastStatus: "running", LastRunAt: time.Now().UTC(), Notifications: job.Notifications}
	if err = s.PutAssistantTask(1, task); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteAssistantTask(1, task.ID); err != nil {
		t.Fatal(err)
	}
	task.LastStatus = "ok"
	if err = s.SaveAssistantTaskResult(1, task); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AssistantTask(1, task.ID); ok {
		t.Fatal("deleted assistant task recreated")
	}
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 {
		t.Fatal("deleted task sent result")
	}
}
