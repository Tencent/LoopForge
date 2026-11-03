package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

type nativeCLICall struct {
	home, workspace, key, mcp, configDir string
	args                                 []string
}

func (c nativeCLICall) flag(name string) string {
	for i, arg := range c.args {
		if arg == name && i+1 < len(c.args) {
			return c.args[i+1]
		}
	}
	return ""
}

func (c nativeCLICall) prompt() string { return c.args[len(c.args)-1] }

func TestAssistantCLIOptionsPrecedePromptDelimiter(t *testing.T) {
	for _, report := range []bool{false, true} {
		command, err := prepareAssistantCommand(assistantCommandOpts{Bin: "codebuddy", Config: "/tmp/test-mcp.json", Prompt: "only positional argument", SessionID: "af-test", Resume: true, ReportOnly: report})
		if err != nil {
			t.Fatal(err)
		}
		for i, arg := range command.Argv {
			if arg == "--" && (i != len(command.Argv)-2 || command.Argv[i+1] != "only positional argument") {
				t.Fatal("CLI options were appended after the positional prompt delimiter")
			}
		}
	}
}

// Exercise the real command construction, HOME selection and credential binding,
// while recording only test credentials and avoiding a dependency on a model.
func nativeCLIRecorder(t *testing.T) (string, func() []nativeCLICall) {
	t.Helper()
	dir := t.TempDir()
	bin, capture := filepath.Join(dir, "codebuddy"), filepath.Join(dir, "calls")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf(`#!/bin/sh
previous=''
config=''
sid=''
for arg in "$@"; do
  if [ "$previous" = '--mcp-config' ]; then config=$(cat "$arg"); fi
  if [ "$previous" = '--resume' ] || [ "$previous" = '--session-id' ]; then sid="$arg"; fi
  previous="$arg"
done
printf '%%s\000' "$HOME" "$PWD" "$CODEBUDDY_API_KEY" "$config" "$CODEBUDDY_CONFIG_DIR" "$@" >> %s
printf '\036' >> %s
if [ -f %s ]; then
  rm %s
  printf 'network interrupted\n' >&2
  exit 1
fi
if [ -f %s ]; then
  rm %s
  printf '%%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"started"}]}}'
  printf '{"type":"error","error":"No conversation found with session ID: %%s"}\n' "$sid"
  exit 0
fi
if [ -f %s ]; then
  rm %s
  printf '{"type":"error","error":"No conversation found with session ID: %%s"}\n' "$sid"
  exit 0
fi
printf '%%s\n' '{"type":"result","result":"原生回复"}'
`, quote(capture), quote(capture), quote(filepath.Join(dir, "fail")), quote(filepath.Join(dir, "fail")), quote(filepath.Join(dir, "progress-missing")), quote(filepath.Join(dir, "progress-missing")), quote(filepath.Join(dir, "missing")), quote(filepath.Join(dir, "missing")))
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	return dir, func() []nativeCLICall {
		data, err := os.ReadFile(capture)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var calls []nativeCLICall
		for _, record := range bytes.Split(data, []byte{0x1e}) {
			if len(record) == 0 {
				continue
			}
			fields := strings.Split(strings.TrimSuffix(string(record), "\x00"), "\x00")
			if len(fields) < 6 {
				t.Fatal("invalid CLI recording")
			}
			calls = append(calls, nativeCLICall{home: fields[0], workspace: fields[1], key: fields[2], mcp: fields[3], configDir: fields[4], args: fields[5:]})
		}
		return calls
	}
}

func sendNativeTurn(t *testing.T, h *assistantHandlers, actor domain.User, session domain.AssistantSession, message string, refs ...domain.AssistantReference) domain.AssistantSession {
	t.Helper()
	messages := append([]assistantMessage(nil), session.Messages...)
	if message != "" {
		messages = append(messages, assistantMessage{Role: "user", Content: message, References: refs})
	}
	body, _ := json.Marshal(assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Messages: messages})
	c, w := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if w.Code != 200 {
		t.Fatalf("chat status %d: %s", w.Code, w.Body.String())
	}
	saved, ok := h.repo.SharedAssistantSession(session.ID)
	if !ok {
		t.Fatal("shared conversation disappeared")
	}
	return saved
}

func TestSharedNativeSessionResumesAcrossActorsAndHandlerRestart(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	t.Setenv("CODEBUDDY_CONFIG_DIR", filepath.Join(t.TempDir(), "operator-state"))
	_, calls := nativeCLIRecorder(t)
	session = sendNativeTurn(t, h, a, session, "第一轮独有文本")
	first := calls()[0]
	restarted := &assistantHandlers{deps: h.deps}
	session = sendNativeTurn(t, restarted, b, session, "第二轮独有文本")
	got := calls()
	if len(got) != 2 {
		t.Fatalf("CLI calls %d", len(got))
	}
	second := got[1]
	if first.flag("--session-id") == "" || second.flag("--resume") != first.flag("--session-id") || second.flag("--session-id") != "" {
		t.Fatal("second actor did not resume the same native session")
	}
	if first.home != second.home || first.workspace != second.workspace || first.home == assistantCLIHome(a.ID) || second.home == assistantCLIHome(b.ID) {
		t.Fatal("shared CLI state is still scoped to an actor")
	}
	if first.configDir != filepath.Join(first.home, ".codebuddy") || second.configDir != first.configDir {
		t.Fatal("operator config directory escaped the conversation HOME")
	}
	if first.key != a.CodeBuddyAPIKey || second.key != b.CodeBuddyAPIKey || first.mcp == second.mcp {
		t.Fatal("caller credentials or per-turn MCP binding were reused")
	}
	for _, old := range []string{"历史咨询", "第一轮独有文本", "原生回复"} {
		if strings.Contains(second.prompt(), old) {
			t.Fatalf("old transcript was replayed: %s", old)
		}
	}
	if !strings.Contains(second.prompt(), "第二轮独有文本") || !strings.Contains(second.prompt(), fmt.Sprintf(`"authorId":%d`, b.ID)) {
		t.Fatal("incremental prompt lost caller attribution")
	}
	if session.UserID != a.ID || session.Messages[len(session.Messages)-2].AuthorID != b.ID {
		t.Fatal("storage owner or message author changed")
	}
	job, _ := h.repo.AssistantGeneration(a.ID, session.ID)
	if job.Status != "completed" || job.ActorID != b.ID {
		t.Fatal("synchronous shared generation did not release its durable slot")
	}
}

func TestSharedNativeSessionSynchronizesConsultationWithoutReplayingEarlierChat(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	_, calls := nativeCLIRecorder(t)
	session = sendNativeTurn(t, h, a, session, "之前的主会话")
	role := domain.AgentDefinition{ID: "dev", ProjectID: session.ProjectID, Name: "开发工程师", Instructions: "只读分析"}
	if err := h.repo.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	session = sendNativeTurn(t, h, b, session, "角色咨询的新问题", domain.AssistantReference{Type: "agent", ID: role.ID})
	session = sendNativeTurn(t, h, a, session, "主会话继续")
	got := calls()
	if len(got) != 3 {
		t.Fatalf("CLI calls %d", len(got))
	}
	if got[1].flag("--resume") != "" || got[1].home == got[0].home {
		t.Fatal("consultation reused Main Bot state")
	}
	if got[2].flag("--resume") != got[0].flag("--session-id") {
		t.Fatal("consultation reset Main Bot state")
	}
	for _, added := range []string{"角色咨询的新问题", "开发工程师", "原生回复", "主会话继续"} {
		if !strings.Contains(got[2].prompt(), added) {
			t.Fatalf("unseen conversation missing: %s", added)
		}
	}
	if strings.Contains(got[2].prompt(), "之前的主会话") {
		t.Fatal("earlier Main Bot messages were replayed")
	}
}

func TestSharedNativeSessionRebuildsEditedHistoryOnce(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	_, calls := nativeCLIRecorder(t)
	session = sendNativeTurn(t, h, a, session, "即将被编辑的问题")
	edited := session.Messages[1]
	edited.Content = "编辑后的问题"
	edited.AuthorID = b.ID
	if err := h.repo.ReplaceAssistantTurn(session, 1, edited); err != nil {
		t.Fatal(err)
	}
	session, _ = h.repo.SharedAssistantSession(session.ID)
	session = sendNativeTurn(t, h, b, session, "")
	got := calls()
	newID := got[1].flag("--session-id")
	if newID == "" || newID == got[0].flag("--session-id") || got[1].flag("--resume") != "" {
		t.Fatal("edited history reused stale native state")
	}
	if !strings.Contains(got[1].prompt(), "编辑后的问题") || strings.Contains(got[1].prompt(), "即将被编辑的问题") {
		t.Fatal("rebuild used stale history")
	}
	sendNativeTurn(t, h, a, session, "编辑后继续")
	got = calls()
	if got[2].flag("--resume") != newID || strings.Contains(got[2].prompt(), "编辑后的问题") {
		t.Fatal("edited conversation was rebuilt on every turn")
	}
}

func TestSharedNativeBranchHasIndependentContinuableState(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	_, calls := nativeCLIRecorder(t)
	session = sendNativeTurn(t, h, a, session, "主会话问题")
	branch := domain.AssistantSession{ID: "sop-branch-native-test", Shared: true, UserID: a.ID, RunID: session.RunID, ProjectID: session.ProjectID, ParentSessionID: session.ID, RootSessionID: session.ID, Messages: append([]assistantMessage(nil), session.Messages[:2]...)}
	if err := h.repo.CreateAssistantSessionBranch(session, branch); err != nil {
		t.Fatal(err)
	}
	branch, _ = h.repo.SharedAssistantSession(branch.ID)
	branch = sendNativeTurn(t, h, b, branch, "")
	sendNativeTurn(t, h, a, branch, "分支继续")
	sendNativeTurn(t, h, b, session, "主会话继续")
	got := calls()
	if len(got) != 4 || got[1].home == got[0].home || got[2].flag("--resume") != got[1].flag("--session-id") || got[3].flag("--resume") != got[0].flag("--session-id") {
		t.Fatal("branch state was rebuilt repeatedly or overwrote the main session")
	}
}

func TestSharedNativeSessionRecoversMissingTranscriptButDoesNotReplayModelErrors(t *testing.T) {
	for _, mode := range []string{"missing", "fail", "progress-missing"} {
		t.Run(mode, func(t *testing.T) {
			h, a, b, _, session := sharedWorkbenchFixture(t)
			dir, calls := nativeCLIRecorder(t)
			session = sendNativeTurn(t, h, a, session, "恢复时需要的历史")
			if err := os.WriteFile(filepath.Join(dir, mode), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			messages := append(append([]assistantMessage(nil), session.Messages...), assistantMessage{Role: "user", Content: "本轮新问题"})
			body, _ := json.Marshal(assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Messages: messages})
			c, w := workbenchContext(b, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
			h.chat(c)
			got := calls()
			if mode == "missing" {
				if w.Code != 200 || len(got) != 3 || got[2].flag("--session-id") == "" || got[2].flag("--session-id") == got[0].flag("--session-id") {
					t.Fatalf("missing transcript recovery: status=%d calls=%d", w.Code, len(got))
				}
				if !strings.Contains(got[2].prompt(), "恢复时需要的历史") || !strings.Contains(got[2].prompt(), "本轮新问题") {
					t.Fatal("recovery omitted authoritative chat history")
				}
				session, _ = h.repo.SharedAssistantSession(session.ID)
				sendNativeTurn(t, h, a, session, "恢复后继续")
				if calls()[3].flag("--resume") != got[2].flag("--session-id") {
					t.Fatal("recovered session was not retained")
				}
			} else {
				if w.Code != 502 || len(got) != 2 {
					t.Fatalf("model error replayed: status=%d calls=%d", w.Code, len(got))
				}
				job, _ := h.repo.AssistantGeneration(session.UserID, session.ID)
				if job.Status != "failed" {
					t.Fatalf("model failure was classified as %s", job.Status)
				}
				session, _ = h.repo.SharedAssistantSession(session.ID)
				sendNativeTurn(t, h, b, session, "")
				if calls()[2].flag("--resume") != "" {
					t.Fatal("failed native turn was treated as a committed checkpoint")
				}
			}
		})
	}
}

func TestSharedNativeSessionFencesAStaleWriter(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	_, calls := nativeCLIRecorder(t)
	session = sendNativeTurn(t, h, a, session, "第一轮")
	req := assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Messages: session.Messages}
	writer, err := openAssistantNativeSession(req, session, a, "")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	messages := append(append([]assistantMessage(nil), session.Messages...), assistantMessage{Role: "user", Content: "不能抢写"})
	body, _ := json.Marshal(assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Messages: messages})
	c, w := workbenchContext(b, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	(&assistantHandlers{deps: h.deps}).chat(c)
	if w.Code != 409 || len(calls()) != 1 {
		t.Fatalf("stale writer was not fenced: %d", w.Code)
	}
}

func TestSharedDirectChatExcludesOtherHandlersAndBackgroundGenerations(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		select {
		case <-release:
			return "完成", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	go func() { defer close(done); sendNativeTurn(t, h, a, session, "运行中的消息") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not start")
	}
	t.Cleanup(func() { close(release); <-done })
	pending, _ := h.repo.SharedAssistantSession(session.ID)
	messages := append(append([]assistantMessage(nil), pending.Messages...), assistantMessage{Role: "user", Content: "不应覆盖的第二轮"})
	body, _ := json.Marshal(assistantRequest{ProjectID: session.ProjectID, RunID: session.RunID, SessionID: session.ID, Messages: messages})
	other := &assistantHandlers{deps: h.deps, generate: h.generate}
	for _, background := range []bool{false, true} {
		c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(body))
		if background {
			other.startGeneration(c)
		} else {
			other.chat(c)
		}
		if w.Code != 409 {
			t.Fatalf("concurrent generation accepted: %d", w.Code)
		}
	}
	saved, _ := h.repo.SharedAssistantSession(session.ID)
	if !sharedTranscriptPrefix(pending.Messages, saved.Messages) || len(saved.Messages) != len(pending.Messages) {
		t.Fatal("rejected generation changed pending history")
	}
}

func sharedNativeMySQLFixture(t *testing.T, keys []string) (*assistantHandlers, domain.User, domain.User, domain.AssistantSession) {
	t.Helper()
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated local Docker MySQL required")
	}
	if !loopbackMySQLDSN(dsn) {
		t.Fatal("native session tests require an isolated loopback MySQL database")
	}
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	prefix := fmt.Sprintf("native-%d", time.Now().UnixNano())
	for i, name := range []string{"a", "b"} {
		if err := st.PutUser(domain.User{LoginName: prefix + name, CodeBuddyAPIKey: keys[i], CodeBuddyInternetEnvironment: "ioa"}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := st.UserByLoginName(prefix + "a")
	b, _ := st.UserByLoginName(prefix + "b")
	if err := st.PutProject(domain.Project{ID: prefix, Name: prefix, OwnerID: a.ID, Members: []domain.ProjectMember{{UserID: b.ID, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(domain.Run{ID: prefix, ExecutionContext: domain.TelemetryContext{ProjectID: prefix}, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	session, err := st.EnsureRunAssistantSession(prefix, prefix, "共享", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	return h, a, b, session
}

func TestMySQLSharedNativeSessionResumesAcrossActors(t *testing.T) {
	h, a, b, session := sharedNativeMySQLFixture(t, []string{"test-only-a", "test-only-b"})
	_, calls := nativeCLIRecorder(t)
	session = sendNativeTurn(t, h, a, session, "MySQL 第一轮")
	sendNativeTurn(t, &assistantHandlers{deps: h.deps}, b, session, "MySQL 第二轮")
	got := calls()
	if len(got) != 2 || got[1].flag("--resume") != got[0].flag("--session-id") || got[1].key != b.CodeBuddyAPIKey {
		t.Fatal("MySQL-backed shared session did not resume across actors")
	}
}

// Opt-in live acceptance: keys are supplied in a private JSON file, never in
// source or logs. Only the disposable loopback MySQL fixture receives them.
func TestSharedNativeSessionLive(t *testing.T) {
	path := os.Getenv("AGENTFLOW_NATIVE_SESSION_TEST_KEYS_FILE")
	if path == "" {
		t.Skip("two explicitly supplied CodeBuddy keys required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private test credential file")
	}
	var keys []string
	if json.Unmarshal(data, &keys) != nil || len(keys) != 2 || keys[0] == "" || keys[1] == "" {
		t.Fatal("two test keys required")
	}
	h, a, b, session := sharedNativeMySQLFixture(t, keys)
	api := New(h.repo, engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), nil, "", "")
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	t.Cleanup(func() { config.SetCurrent(previous) })
	h.api = api
	session = sendNativeTurn(t, h, a, session, "请记住一次性测试口令 AZURE-RIVER-7359。只用自然语言回复已记住，不要调用任何工具。")
	var first, second assistantNativeCheckpoint
	readCheckpoint := func(out *assistantNativeCheckpoint) {
		data, err := os.ReadFile(filepath.Join(assistantSharedSessionRoot(session.ID), "session.json"))
		if err != nil || json.Unmarshal(data, out) != nil {
			t.Fatal("native checkpoint unavailable")
		}
	}
	readCheckpoint(&first)
	session = sendNativeTurn(t, h, b, session, "上一轮要求记住的一次性测试口令是什么？只回答口令，不调用工具。")
	readCheckpoint(&second)
	if !first.Ready || !second.Ready || first.SessionID != second.SessionID {
		t.Fatal("live conversation rebuilt its native session")
	}
	if !strings.Contains(session.Messages[len(session.Messages)-1].Content, "AZURE-RIVER-7359") {
		t.Fatal("second key failed to recall the first key's native context")
	}
	t.Log("Two live keys continued the same native session through the application handlers and isolated MySQL.")
}
