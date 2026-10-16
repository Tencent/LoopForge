package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func activeReportSession(t *testing.T) *AgentToolSession {
	t.Helper()
	agentToolSessions.mu.Lock()
	defer agentToolSessions.mu.Unlock()
	for _, s := range agentToolSessions.sessions {
		if s.Mode == AgentToolReport {
			return s
		}
	}
	t.Fatal("missing report session")
	return nil
}

func TestSOPReportUsesResponsibleUserAndMCPSubmission(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.PutUser(domain.User{ID: 42, LoginName: "executor", CodeBuddyAPIKey: "executor-key", CodeBuddyInternetEnvironment: "ioa"})
	store.PutUser(domain.User{ID: 43, LoginName: "owner", CodeBuddyAPIKey: "owner-key"})
	store.PutProject(domain.Project{ID: "report-project", OwnerID: 43})
	h := &assistantHandlers{deps: &deps{repo: store}}
	calls := 0
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		calls++
		if user.ID != 42 || user.CodeBuddyAPIKey != "executor-key" || user.CodeBuddyInternetEnvironment != "ioa" {
			t.Fatal("wrong execution credentials")
		}
		session := activeReportSession(t)
		if session.RunID != "run-report" || session.ProjectID != "report-project" {
			t.Fatal("wrong run binding")
		}
		gateway := &agentToolGateway{deps: h.deps}
		list := gateway.toolList(ctx, session)
		if len(list) != 1 || list[0]["name"] != submitNotificationReportTool {
			t.Fatalf("tool list: %+v", list)
		}
		if _, err := gateway.callTool(ctx, session, json.RawMessage(`{"name":"agentflow__issues_list","arguments":{}}`)); err == nil {
			t.Fatal("report queried unrelated data")
		}
		g := gin.New()
		g.POST("/internal/agent-tools/:token/mcp", gateway.ServeHTTP)
		payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_notification_report","arguments":{"body":"最终验证通过 executor-key"}}}`
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest("POST", "/internal/agent-tools/"+session.Token+"/mcp", strings.NewReader(payload)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "提交成功") {
			t.Fatal(rec.Body.String())
		}
		if _, err := gateway.callTool(ctx, session, json.RawMessage(`{"name":"submit_notification_report","arguments":{"body":"重复"}}`)); err == nil {
			t.Fatal("duplicate accepted")
		}
		return "not a JSON result", context.Canceled // The committed tool result survives CLI cancellation.
	}
	run := domain.Run{ID: "run-report", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 42, ProjectID: "report-project"}}
	report, err := h.generateSOPReport(context.Background(), run, domain.DefaultNotificationReportModel, "资料")
	if err != nil || report.Body != "最终验证通过 [redacted]" || calls != 1 {
		t.Fatalf("report=%+v calls=%d err=%v", report, calls, err)
	}
	run.ExecutionContext.ResponsibleUserID = 99
	if _, err = h.generateSOPReport(context.Background(), run, domain.DefaultNotificationReportModel, "资料"); !errors.Is(err, notification.ErrReportCredentials) {
		t.Fatal("borrowed owner key")
	}
	command, err := prepareAssistantCommand(assistantCommandOpts{Bin: "codebuddy", Config: "{}", Prompt: "资料", ReportOnly: true, Native: true})
	if err != nil {
		t.Fatal(err)
	}
	if argvHasFlag(command.Argv, "--tools") || !argvHasFlag(command.Argv, "--strict-mcp-config") {
		t.Fatalf("MCP hidden: %v", command.Argv)
	}
	if !strings.Contains(strings.Join(command.Argv, " "), "Read,Grep,Glob") {
		t.Fatal("report allows file reads")
	}
}

func TestSOPReportMissingSubmissionRetriesWithoutParsingText(t *testing.T) {
	store, _ := repo.OpenTest()
	defer store.Close()
	store.PutUser(domain.User{ID: 42, LoginName: "executor", CodeBuddyAPIKey: "key"})
	h := &assistantHandlers{deps: &deps{repo: store}}
	calls := 0
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		calls++
		return "```notification\n{\"body\":\"looks valid\"}\n```", nil
	}
	_, err := h.generateSOPReport(context.Background(), domain.Run{ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 42}}, domain.DefaultNotificationReportModel, "资料")
	if !errors.Is(err, notification.ErrReportFormat) || calls != nativeTerminalMaxRetries+1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	h.generate = func(ctx context.Context, prompt string, user domain.User) (string, error) {
		calls++
		session := activeReportSession(t)
		gateway := &agentToolGateway{deps: h.deps}
		args := json.RawMessage(`{"name":"submit_notification_report","arguments":{"body":" "}}`)
		if calls > 1 {
			args = json.RawMessage(`{"name":"submit_notification_report","arguments":{"body":"补交成功"}}`)
		}
		_, err := gateway.callTool(ctx, session, args)
		return "", err
	}
	report, err := h.generateSOPReport(context.Background(), domain.Run{ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 42}}, domain.DefaultNotificationReportModel, "资料")
	if err != nil || calls != 2 || report.Body != "补交成功" {
		t.Fatalf("repair report=%+v calls=%d err=%v", report, calls, err)
	}

}

func TestSubmitNotificationReportValidationAndModeIsolation(t *testing.T) {
	gateway := &agentToolGateway{}
	session := &AgentToolSession{Mode: AgentToolReport, Final: make(chan AgentTerminalResult, 1)}
	for _, body := range []string{`{"body":" "}`, `{"body":"报告","runId":"other-run"}`, `{"body":2}`, `{"body":"报告"} {}`} {
		args := json.RawMessage(`{"name":"submit_notification_report","arguments":` + body + `}`)
		// The trailing object is directly tested by the argument decoder below.
		if json.Valid(args) {
			result, err := gateway.callTool(context.Background(), session, args)
			if err == nil && result["isError"] != true {
				t.Fatalf("accepted %s", body)
			}
		}
		if _, err := parseSubmitNotificationReportArgs(json.RawMessage(body)); err == nil {
			t.Fatalf("accepted invalid args %s", body)
		}
		if session.finished() {
			t.Fatal("invalid submission ended session")
		}
	}
	for _, mode := range []AgentToolMode{AgentToolAssistant, AgentToolClarify, AgentToolScheduled} {
		other := &AgentToolSession{Mode: mode, Final: make(chan AgentTerminalResult, 1)}
		if _, err := gateway.callTool(context.Background(), other, json.RawMessage(`{"name":"submit_notification_report","arguments":{"body":"报告"}}`)); err == nil {
			t.Fatalf("mode %s accepted report", mode)
		}
	}
	valid := json.RawMessage(`{"name":"submit_notification_report","arguments":{"body":"报告"}}`)
	if result, err := gateway.callTool(context.Background(), session, valid); err != nil || result["isError"] == true {
		t.Fatalf("valid failed %v", err)
	}
}
