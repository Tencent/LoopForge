package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestAssistantDiagnosticsCaptureNativeFailureAndExit(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
echo 'Authorization: Bearer header-private-value' >&2
echo 'OPENAI_API_KEY=known-private-value password="unknown-password" /Users/example/private/config.json' >&2
echo '{"type":"error","error":{"message":"rate limit reached","code":"rate_limit","statusCode":429,"requestId":"request-example","traceId":"trace-example","retryable":true}}'
exit 7
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	g := domain.AssistantGeneration{ID: "diagnostic-turn", SessionID: "diagnostic-session", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	ctx := context.WithValue(context.Background(), assistantDiagnosticsKey{}, d)
	user := domain.User{ID: 1, CodeBuddyAPIKey: "known-private-value"}
	_, _, err := runAssistantCLI(ctx, assistantCLIRequest{Prompt: "private prompt must never be exported", SessionRoot: dir}, user, nil)
	if err == nil {
		t.Fatal("nonzero CLI was accepted")
	}
	f := d.fail(ctx, err, user)
	g.Failure, g.Status = &f, "failed"
	report := d.snapshot(g)
	if f.Code != "RATE_LIMITED" || len(report.Attempts) != 1 {
		t.Fatalf("wrong failure: %+v", report)
	}
	a := report.Attempts[0]
	if a.ExitCode == nil || *a.ExitCode != 7 || a.FinishedAt == nil || a.Status != "failed" || len(a.Events) != 1 {
		t.Fatalf("lost process outcome: %+v", a)
	}
	e := a.Events[0]
	if e.RequestID != "request-example" || e.TraceID != "trace-example" || e.StatusCode != 429 || e.Retryable == nil || !*e.Retryable {
		t.Fatalf("lost native IDs: %+v", e)
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{"known-private-value", "header-private-value", "unknown-password", "/Users/example", "private prompt must never be exported", "--print"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("diagnostics leaked %q: %s", secret, raw)
		}
	}
	// Readers must receive immutable snapshots, even while another attempt runs.
	d.begin("codebuddy", "second-model", true, user).stderr("new output")
	if len(report.Attempts) != 1 || len(report.Attempts[0].Stderr) != 2 {
		t.Fatal("snapshot changed after another attempt")
	}
}

func TestAssistantDiagnosticSanitizationAndBounds(t *testing.T) {
	user := domain.User{GitToken: "git-secret", GongfengRefreshToken: "refresh-secret", AgentCLICredentials: map[string]domain.AgentCLICredential{"zcode": {APIKey: "agent-secret"}}}
	pemBlock := "-----BEGIN " + "PRIVATE KEY-----\nprivate-key-content\n-----END " + "PRIVATE KEY-----"
	input := `{"api_key":"unrecognized-key","Authorization":"Bearer unknown-bearer","password":"unknown-pass"}
https://username:url-pass@example.com/v1?signature=url-secret#fragment-secret
git-secret refresh-secret agent-secret sk-abcdefghijklmnopqrstuvwxyz
` + pemBlock + `
/home/user/.config/private.json C:\Users\user\private.json`
	out := sanitizeAssistantDiagnostic(input, user, 8192)
	for _, secret := range []string{"unrecognized-key", "unknown-bearer", "unknown-pass", "url-pass", "url-secret", "fragment-secret", "git-secret", "refresh-secret", "agent-secret", "sk-abc", "private-key-content", "/home/user", "C:\\Users"} {
		if strings.Contains(out, secret) {
			t.Fatalf("leaked %q: %s", secret, out)
		}
	}
	g := domain.AssistantGeneration{ID: "bounded", Status: "running"}
	d := newAssistantDiagnosticsCollector(g)
	for i := 0; i < 20; i++ {
		a := d.begin("codebuddy", "model", false, user)
		for j := 0; j < 80; j++ {
			a.stderr(strings.Repeat("x", 2048))
			a.protocol(`{"type":"error","error":{"message":"sample failure","requestId":"req-example"},"input":{"password":"never keep tool input"}}`)
		}
	}
	report := d.snapshot(g)
	if len(report.Attempts) != 16 || !report.Truncated {
		t.Fatal("attempt archive is not bounded")
	}
	for _, a := range report.Attempts {
		if len(a.Stderr) > 20 || len(a.Events) > 40 {
			t.Fatal("attempt is not bounded")
		}
	}
	raw, _ := json.Marshal(report)
	if bytes.Contains(raw, []byte("never keep tool input")) {
		t.Fatal("retained tool input")
	}
}

func TestAssistantDiagnosticDownloadChecksSessionAndOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, owner := workbenchFixture(t)
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "diag-session", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "diag-one", SessionID: "diag-session", UserID: owner.ID, Status: "running", StartedAt: time.Now().UTC()}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	d := newAssistantDiagnosticsCollector(g)
	f := d.fail(context.Background(), errors.New("network interrupted"), owner)
	g.Status, g.Failure = "failed", &f
	if err := h.repo.CheckpointAssistantGeneration(g, d.snapshot(g)); err != nil {
		t.Fatal(err)
	}
	request := func(user domain.User, session string) (int, string, string) {
		c, w := workbenchContext(user, "GET", "/diagnostics?download=1", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: session}, {Key: "generationId", Value: g.ID}}
		h.generationDiagnostics(c)
		return w.Code, w.Body.String(), w.Header().Get("Content-Disposition")
	}
	status, body, disposition := request(owner, g.SessionID)
	if status != 200 || !strings.Contains(disposition, "attachment;") || !strings.Contains(body, "NETWORK_ERROR") {
		t.Fatalf("download failed: %d %s %s", status, disposition, body)
	}
	if err := h.repo.PutUser(domain.User{LoginName: "diag-outsider"}); err != nil {
		t.Fatal(err)
	}
	outsider, _ := h.repo.UserByLoginName("diag-outsider")
	if status, _, _ := request(outsider, g.SessionID); status < 400 {
		t.Fatal("other user downloaded private diagnostics")
	}
	if status, _, _ := request(owner, "different-session"); status < 400 {
		t.Fatal("generation ID bypassed session scope")
	}
}

func TestAssistantDiagnosticClassifiesProviderMetadataBeforeTranslation(t *testing.T) {
	g := domain.AssistantGeneration{ID: "native", Status: "running"}
	d := newAssistantDiagnosticsCollector(g)
	a := d.begin("zcode", "model", false, domain.User{})
	data, err := os.ReadFile("../runtime/testdata/zcode-0.17.1-stream-401-sample.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		a.protocol(line)
	}
	f := d.fail(context.Background(), errors.New("exit status 1"), domain.User{})
	if f.Code != "AUTHENTICATION_FAILED" || strings.Contains(f.Message, "CodeBuddy") {
		t.Fatalf("wrong provider failure: %+v", f)
	}
	g.Status, g.Failure = "failed", &f
	report := d.snapshot(g)
	raw, _ := json.Marshal(report)
	if !bytes.Contains(raw, []byte("ece088ae-92ca-43a2-ae2d-d8ef1ac86680")) || !bytes.Contains(raw, []byte("864e141b-aa26-4c64-9d32-759185219185")) {
		t.Fatal("dropped native request/trace ID")
	}
	if bytes.Contains(raw, []byte("requestHeaders")) || bytes.Contains(raw, []byte("baseURL")) {
		t.Fatal("retained provider configuration or headers")
	}
}

func TestAssistantDiagnosticFirstErrorIgnoresBenignNoiseAndUpgradesPlaceholder(t *testing.T) {
	g := domain.AssistantGeneration{ID: "first-error-turn", SessionID: "s1", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	a := d.begin("codex", "hy4", false, domain.User{CodexAPIKey: "secret-codex-key"})
	a.stderr(`{"timestamp":"2026-11-13T07:14:22Z","level":"ERROR","fields":{"message":"ReasoningSummaryDelta without active item"},"target":"codex_core::util"}`)
	if snap := d.snapshot(g); len(snap.Attempts[0].Events) != 0 || snap.Attempts[0].FirstError != nil {
		t.Fatalf("benign codex delta warning polluted structured error events: %+v", snap.Attempts[0])
	}
	a.protocol(`{"method":"error","params":{"willRetry":true,"error":{"message":"Reconnecting... 1/5"}}}`)
	a.stderr(`{"timestamp":"2026-11-13T07:14:23Z","level":"WARN","fields":{"message":"stream disconnected - retrying","sampling_error":"unexpected status 401 Unauthorized: Invalid token (request id: req-real-401), url: https://api.example.com/v1/responses"},"target":"codex_core::responses_retry"}`)
	for i := 0; i < 45; i++ {
		a.stderr("trailing warning line")
		a.protocol(`{"method":"error","params":{"willRetry":true,"error":{"message":"Reconnecting... later"}}}`)
	}
	f := d.fail(context.Background(), errors.New("exit status 1"), domain.User{})
	g.Status, g.Failure = "failed", &f
	snap := d.snapshot(g)
	first := snap.Attempts[0].FirstError
	if first == nil || first.StatusCode != 401 || first.RequestID != "req-real-401" {
		t.Fatalf("FirstError did not upgrade to substantive root cause: %+v", first)
	}
	if f.Code != "AUTHENTICATION_FAILED" {
		t.Fatalf("expected fallback classification to use FirstError after event rotation, got %+v", f)
	}
	if len(snap.Attempts[0].FirstStderr) != 8 || snap.Dropped["stderrLines"] == 0 || snap.Dropped["protocolEvents"] == 0 {
		t.Fatalf("expected head stderr and drop counters, got firstStderr=%d dropped=%v", len(snap.Attempts[0].FirstStderr), snap.Dropped)
	}
}

func TestAssistantDiagnosticCapturesGatewayToolLifecycleAndRedactsSuccess(t *testing.T) {
	h, u := workbenchFixture(t)
	u.GitToken = "private-git-token"
	g := domain.AssistantGeneration{ID: "gateway-diag", SessionID: "gateway-sess", UserID: u.ID, Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	attempt := d.begin("codex", "gpt-5.6", false, u)
	ctx := context.WithValue(context.Background(), assistantDiagnosticsKey{}, d)
	sess, files, err := startAgentToolSession(ctx, u, "workbench", AgentToolAssistant, assistantExecModeForApproval(AssistantApprovalConfirm), "", nil, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	defer files.Cleanup()

	gw := &agentToolGateway{deps: h.deps}
	_ = gw.handleRPC(ctx, sess, agentToolRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize"})
	_ = gw.handleRPC(ctx, sess, agentToolRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	okParams, _ := json.Marshal(map[string]any{"name": "workbench_tool_schema", "arguments": map[string]any{"name": "projects_get", "secretArg": "never-log-tool-arg"}})
	okResp := gw.handleRPC(ctx, sess, agentToolRPCRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: okParams})
	if okResp.Error != nil {
		t.Fatalf("unexpected gateway error: %+v", okResp.Error)
	}
	failParams, _ := json.Marshal(map[string]any{"name": "agentflow__issues_delete", "arguments": map[string]any{"path": map[string]any{"id": "1"}}})
	_ = gw.handleRPC(ctx, sess, agentToolRPCRequest{JSONRPC: "2.0", ID: 4, Method: "tools/call", Params: failParams})
	attempt.finish(nil)

	g.Status = "completed"
	report := d.snapshot(g)
	if len(report.Tools) != 2 {
		t.Fatalf("expected 2 gateway tools, got %+v", report.Tools)
	}
	if report.Tools[0].Status != "completed" || report.Tools[0].Error != "" || report.Tools[0].AttemptID != attempt.id {
		t.Fatalf("unexpected successful tool record: %+v", report.Tools[0])
	}
	if report.Tools[1].Status != "failed" || !strings.Contains(report.Tools[1].Error, "unknown tool") {
		t.Fatalf("missing failed gateway tool reason: %+v", report.Tools[1])
	}
	raw, _ := json.Marshal(report)
	if bytes.Contains(raw, []byte("never-log-tool-arg")) || bytes.Contains(raw, []byte("inputSchema")) || bytes.Contains(raw, []byte("private-git-token")) {
		t.Fatalf("gateway diagnostics leaked arguments, successful output, or secret: %s", raw)
	}
	if !bytes.Contains(raw, []byte("mcp.gateway_initialized")) || !bytes.Contains(raw, []byte("mcp.tools_listed")) {
		t.Fatalf("missing gateway timeline milestones: %s", raw)
	}
}

func TestAssistantDiagnosticRecordsUserCancellationTermination(t *testing.T) {
	h, u := workbenchFixture(t)
	entered := make(chan struct{}, 1)
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"cancel-diag-chat","messages":[{"role":"user","content":"测试停止归因"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	<-entered
	g, _ := h.repo.AssistantGeneration(u.ID, "cancel-diag-chat")
	if err := h.repo.CancelAssistantGeneration(u.ID, "cancel-diag-chat", g.ID); err != nil {
		t.Fatal(err)
	}
	waitGeneration(t, h, u.ID, "cancel-diag-chat", func(gen domain.AssistantGeneration) bool { return gen.Status == "cancelled" })
	report, ok := h.repo.AssistantDiagnostics(u.ID, "cancel-diag-chat", g.ID)
	if !ok || report.Termination == nil || report.Termination.Code != "USER_CANCELLED" {
		t.Fatalf("expected USER_CANCELLED in persisted diagnostics, got %+v", report)
	}
}
