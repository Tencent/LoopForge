package httpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Task 4: executeClarify 的引擎接线。这些测试钉住的是「用户选了哪个框架就真的用哪个跑」：
// 缺凭据要在动手之前失败、没装 CLI 要说清是哪个 CLI、一张单换框架必须换新会话。

// clarifyFakeCLI 写一个假执行器：把 argv 追加到 argsPath，然后回一句可用的澄清结论。
func clarifyFakeCLI(t *testing.T, dir, name, argsPath string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	script := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\" >> '" + argsPath + "'\ndone\nprintf '%s\\n' 'END_CALL' >> '" + argsPath + "'\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"{\\\"summary\\\":\\\"已确认需求\\\",\\\"ready\\\":true,\\\"questions\\\":[]}\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func clarifyWiringStore(t *testing.T, users ...domain.User) *repo.Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, user := range users {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestExecuteClarifyRejectsEngineWithoutCredential(t *testing.T) {
	s := clarifyWiringStore(t, domain.User{ID: 13, CodeBuddyAPIKey: "test-key"})
	issue := domain.Issue{ID: "wire-nokey", ProjectID: "platform", AssigneeUserID: 13, Title: "换框架"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s}}

	_, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{Model: "codex:gpt-5.5", Message: "继续"}, nil)
	if err == nil {
		t.Fatal("责任人没配 Codex Key 时必须失败")
	}
	if !strings.Contains(err.Error(), "个人配置中配置 Codex API Key") {
		t.Fatalf("err = %q, want the missing-key message", err.Error())
	}
}

func TestExecuteClarifyReportsMissingCLI(t *testing.T) {
	root := t.TempDir()
	emptyBin := filepath.Join(root, "emptybin")
	if err := os.MkdirAll(emptyBin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("PATH", emptyBin)
	s := clarifyWiringStore(t, domain.User{ID: 13, AgentCLICredentials: map[string]domain.AgentCLICredential{
		"opencode": {APIKey: "sk-test", BaseURL: "https://gateway.example.com/v1"},
	}})
	issue := domain.Issue{ID: "wire-nobin", ProjectID: "platform", AssigneeUserID: 13, Title: "换框架"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s}}

	_, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{Model: "opencode:glm-4", Message: "继续"}, nil)
	if !errors.Is(err, errAssistantCLIMissing) {
		t.Fatalf("err = %v, want errAssistantCLIMissing", err)
	}
	if !strings.Contains(err.Error(), "OpenCode") {
		t.Fatalf("错误必须点名缺的是哪个 CLI：%q", err.Error())
	}
}

func TestExecuteClarifyRunsOnTheEngineTheIssueRemembers(t *testing.T) {
	root := t.TempDir()
	args := filepath.Join(root, "args")
	bin := clarifyFakeCLI(t, root, "codex", args)
	t.Setenv("AGENTFLOW_CODEX_BIN", bin)
	s := clarifyWiringStore(t, domain.User{ID: 13, CodexAPIKey: "sk-test"})
	issue := domain.Issue{ID: "wire-sticky", ProjectID: "platform", AssigneeUserID: 13, Title: "继续澄清",
		Clarification: domain.ClarificationAssessment{SessionID: "af-old", SessionUserID: 13, ModelID: "codex:gpt-5.5"}}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	var opened map[string]any
	h := &streamHandlers{deps: &deps{repo: s}}

	_, _, _, _ = h.executeClarify(context.Background(), issue, chatClarifyRequest{Message: "继续"},
		func(event string, payload any) {
			if event == "open" {
				opened, _ = payload.(map[string]any)
			}
		})
	// 假 codex 不认识 app-server 的 JSON-RPC，这一轮拿不到结论；本单要钉的是「这张单记住的是
	// Codex 就真的跑 codex」，协议转译由 translator 自己的测试负责。
	if opened["engine"] != "codex" || opened["model"] != "codex:gpt-5.5" {
		t.Fatalf("open payload = %v, want engine=codex model=codex:gpt-5.5", opened)
	}
	log, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "app-server") {
		t.Fatalf("责任人记住的是 Codex，却没真的跑 codex app-server：%s", log)
	}
	stored, _ := s.Issue(issue.ID)
	if stored.Clarification.ModelID != "codex:gpt-5.5" {
		t.Fatalf("ModelID lost: %+v", stored.Clarification)
	}
}

func TestExecuteClarifyEngineSwitchStartsFreshSession(t *testing.T) {
	root := t.TempDir()
	args := filepath.Join(root, "args")
	bin := clarifyFakeCLI(t, root, "codebuddy", args)
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	s := clarifyWiringStore(t, domain.User{ID: 13, CodeBuddyAPIKey: "test-key", CodexAPIKey: "sk-test"})
	issue := domain.Issue{ID: "wire-switch", ProjectID: "platform", AssigneeUserID: 13, Title: "换回 CodeBuddy",
		Clarification: domain.ClarificationAssessment{SessionID: "shared-session", SessionUserID: 13, ModelID: "codex:gpt-5.5"}}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	// CodeBuddy 家下确实有同名 transcript：只有「引擎换了就不续跑」这条规则能挡住它。
	mustWriteClarifyFile(t, filepath.Join(clarifyCLIHome(13), ".codebuddy", "projects", "ws", "shared-session.jsonl"), "conversation\n")
	h := &streamHandlers{deps: &deps{repo: s}}

	if _, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{Model: "default", Message: "继续"}, nil); err != nil {
		t.Fatalf("switched engine: %v", err)
	}
	log, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(string(log), "END_CALL\n")[0]
	if strings.Contains(first, "--resume") {
		t.Fatalf("换了框架还续跑 Codex 的会话：%s", first)
	}
	if !strings.Contains(first, "--session-id") || strings.Contains(first, "shared-session") {
		t.Fatalf("换框架必须开新会话：%s", first)
	}
	stored, _ := s.Issue(issue.ID)
	if stored.Clarification.ModelID != "default" {
		t.Fatalf("切回 CodeBuddy 后应记住新的引擎：%+v", stored.Clarification)
	}
	if stored.Clarification.SessionID == "shared-session" {
		t.Fatalf("旧会话 ID 被复用：%+v", stored.Clarification)
	}
}

func TestClarifyChoiceFallsBackWhenStoredModelIsUnusable(t *testing.T) {
	// 历史数据里记住的框架，责任人现在没有 Key 了：这一轮退回 CodeBuddy 默认，
	// 但必须让调用方看得见（可用性端点用 defaultStale 报告）。
	engine, err := (&streamHandlers{}).clarifyChoice(chatClarifyRequest{}, domain.Issue{
		Clarification: domain.ClarificationAssessment{ModelID: "codex:gpt-5.5"},
	}, domain.User{})
	if err != nil {
		t.Fatalf("stale stored engine must fall back, got %v", err)
	}
	if engine.ID != assistantEngineCodeBuddy || engine.ModelID != clarifyDefaultModel {
		t.Fatalf("engine = %+v, want codebuddy/default", engine)
	}
	// 用户当场选中的框架缺凭据则绝不回退。
	if _, err := (&streamHandlers{}).clarifyChoice(chatClarifyRequest{Model: "codex:gpt-5.5"}, domain.Issue{}, domain.User{}); err == nil {
		t.Fatal("explicit engine without a key must fail, not fall back")
	}
}

func TestClarifyToolVocabularyFollowsTheEngine(t *testing.T) {
	if got := clarifyToolVocabulary(assistantEngineCodeBuddy); got != "Read/Grep/Glob" {
		t.Fatalf("codebuddy vocabulary = %q", got)
	}
	if got := clarifyToolVocabulary(assistantEngineClaude); got != "Read/Grep/Glob" {
		t.Fatalf("claude vocabulary = %q", got)
	}
	const text = "不要使用 Read/Grep/Glob，也不要假设已阅读代码。"
	for _, engine := range []string{assistantEngineCodex, "opencode", "cursor"} {
		got := clarifyApplyToolVocabulary(text, engine)
		if strings.Contains(got, "Read/Grep/Glob") {
			t.Fatalf("%s 的提示词还在指挥不存在的工具：%s", engine, got)
		}
		if !strings.Contains(got, clarifyToolVocabulary(engine)) {
			t.Fatalf("%s 提示词没有换成该引擎的读工具说法：%s", engine, got)
		}
	}
	if got := clarifyApplyToolVocabulary(text, assistantEngineCodeBuddy); got != text {
		t.Fatalf("codebuddy 提示词被改写了：%s", got)
	}
}
