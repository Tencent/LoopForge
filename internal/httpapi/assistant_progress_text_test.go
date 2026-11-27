package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Cursor reports every paragraph of the turn joined as its result. A reply
// that opened with a plan before calling tools must be judged by the text
// written after the tools, as the other CLIs report it.
func TestAssistantProgressTextJudgesTextAfterLastToolCall(t *testing.T) {
	answer := "仓库：登记 1 个 GitHub 仓库，默认分支 main。\n\n成员：只有负责人 alice。"
	plan := "我先查本项目的代码仓库和成员，再分段总结。"
	cases := []struct {
		name     string
		message  string
		events   []domain.AssistantActivityEvent
		progress bool
	}{
		{
			name:    "plan, tools, answer joined by Cursor",
			message: plan + answer,
			events: []domain.AssistantActivityEvent{
				{Kind: "text", Text: plan},
				{Kind: "tool", Tool: "agentflow__repositories_list"},
				{Kind: "toolDone"},
				{Kind: "thinking", Text: "整理结果"},
				{Kind: "text", Text: answer},
			},
		},
		{
			name:     "plan then tools without any answer",
			message:  plan,
			events:   []domain.AssistantActivityEvent{{Kind: "text", Text: plan}, {Kind: "tool", Tool: "Read"}, {Kind: "toolDone"}},
			progress: true,
		},
		{
			name:    "text after the last tool is itself a progress note",
			message: plan + "接下来我会修改 store.py 并补测试。",
			events: []domain.AssistantActivityEvent{
				{Kind: "text", Text: plan},
				{Kind: "tool", Tool: "Read"},
				{Kind: "text", Text: "接下来我会修改 store.py 并补测试。"},
			},
			progress: true,
		},
		{
			name:     "no tool call judges the whole reply",
			message:  "稍等，我先检查一下。",
			events:   []domain.AssistantActivityEvent{{Kind: "text", Text: "稍等，我先检查一下。"}},
			progress: true,
		},
		{
			name:    "subagent tools do not hide Main Bot's answer",
			message: plan + answer,
			events: []domain.AssistantActivityEvent{
				{Kind: "text", Text: plan},
				{Kind: "tool", Tool: "Agent", ID: "dispatch-1"},
				{Kind: "tool", Tool: "Read", Parent: "dispatch-1"},
				{Kind: "agentText", Text: "我先读取文件", Parent: "dispatch-1"},
				{Kind: "toolDone", ID: "dispatch-1"},
				{Kind: "text", Text: answer},
				{Kind: "tool", Tool: "Grep", Parent: "dispatch-1"},
			},
		},
		{
			name:     "a CLI notice after the tools is not an answer",
			message:  plan,
			events:   []domain.AssistantActivityEvent{{Kind: "text", Text: plan}, {Kind: "tool", Tool: "Read"}, {Kind: "text", Text: cliBashNotice}},
			progress: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assistantProgressOnly(assistantProgressText(tc.message, tc.events), nil); got != tc.progress {
				t.Fatalf("progress-only = %v, want %v (judged %q)", got, tc.progress, assistantProgressText(tc.message, tc.events))
			}
		})
	}
}

// End to end through the chat handler: a CLI that streams a plan, calls a
// tool, streams the answer and then reports all of it joined (as Cursor
// does) must finish in one run. Before the fix the joined result was taken
// for a progress note, the CLI ran again and the answer was stored twice.
func TestAssistantJoinedResultAfterToolsIsNotRetriedAsProgress(t *testing.T) {
	h, user := workbenchFixture(t)
	bin := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/sh
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查看项目里的 LOOP。"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"list-1","name":"mcp__agentflow__workflows_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list-1","content":"[]"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"项目中有后端研发 LOOP，共四个节点。\n\n建议直接用它派发需求。"}]}}
{"type":"result","result":"我先查看项目里的 LOOP。项目中有后端研发 LOOP，共四个节点。\n\n建议直接用它派发需求。"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	sid := "joined-progress"
	body, _ := json.Marshal(assistantRequest{SessionID: sid, ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "先说一句你要做什么，然后查看已有 LOOP，分 2 段说明"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(user.ID, sid)
		if g.Status == "completed" {
			break
		}
		if g.Status != "running" || time.Now().After(deadline) {
			// Before the fix: retried as progress until 模型连续只返回进度说明.
			t.Fatalf("generation %s: %s", g.Status, g.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, ok := h.repo.AssistantSession(user.ID, sid)
	if !ok || len(saved.Messages) != 2 {
		t.Fatalf("saved session: %+v", saved)
	}
	var stored struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(saved.Messages[1].Content), &stored); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stored.Message, "共四个节点") != 1 || !strings.Contains(stored.Message, "我先查看项目里的 LOOP。") {
		t.Fatalf("stored reply = %q", stored.Message)
	}
	for _, event := range saved.Messages[1].Activity {
		if event.Kind == "status" && strings.Contains(event.Text, "继续执行尚未完成的步骤") {
			t.Fatalf("progress retry status recorded: %+v", saved.Messages[1].Activity)
		}
	}
}

// The plan-only case still continues: text before the tools and nothing
// after them is a progress note even when the CLI streamed it.
func TestAssistantPlanBeforeToolsWithoutAnswerStillContinues(t *testing.T) {
	h, user := workbenchFixture(t)
	// Count only reply runs: the background title summary uses the CLI too.
	h.titleSummary = func(context.Context, domain.User, string) (string, error) { return "", nil }
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
n=$((n + 1))
echo "$n" > '` + counter + `'
if [ "$n" -gt 1 ]; then
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"项目中有后端研发 LOOP，共四个节点。"}]}}
{"type":"result","result":"项目中有后端研发 LOOP，共四个节点。"}
JSON
exit 0
fi
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查看项目里的 LOOP。"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"list-1","name":"mcp__agentflow__workflows_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list-1","content":"[]"}]}}
{"type":"result","result":"我先查看项目里的 LOOP。"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	h.generate = nil
	body, _ := json.Marshal(assistantRequest{SessionID: "plan-only", ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "查看已有 LOOP 并给出结果"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	calls, _ := os.ReadFile(counter)
	if rec.Code != 200 || strings.TrimSpace(string(calls)) != "2" || !strings.Contains(rec.Body.String(), "共四个节点") {
		t.Fatalf("calls=%s code=%d body=%s", strings.TrimSpace(string(calls)), rec.Code, rec.Body.String())
	}
}

// A plan written before the tools, with nothing after them, is still a
// progress note: the turn continues instead of delivering the plan as the
// answer. The plan may be a numbered list or plain prose
// ("先确认分支，再读取提交记录。").
func TestAssistantMultiLinePlanBeforeToolsStillContinues(t *testing.T) {
	for _, tc := range []struct {
		name, session, plan string
	}{
		{"numbered list", "multi-line-plan", `我先查看项目里的 LOOP：\n1. 读取工作流配置\n2. 汇总节点状态`},
		{"prose steps", "prose-plan", `我先看一下仓库：\n先确认分支，再读取提交记录。`},
		{"step with a count", "count-step", `我先看一下仓库。\n重点关注最近 3 次提交。`},
		{"goal clause", "goal-step", `我先看一下报错的用例。\n然后修改代码，让测试通过。`},
		{"check via", "check-via", `我先看一下数据。\n先检查通过 API 拿到的数据，再对比数据库。`},
		{"embedded question", "question-step", `我先看一下测试。\n先看看为什么测试挂了，再修复。`},
		{"precondition", "precondition-step", `我先看一下。\n拿到 token 后调用接口。`},
		{"status as object", "status-object-step", `我先看一下。\n先看日志，再定位报错。`},
		{"clean complement", "clean-complement-step", `我先看一下。\n先把环境清干净，再跑测试。`},
		{"run until", "run-until-step", `我先看一下。\n修到测试全部通过为止。`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runMultiLinePlanCase(t, tc.session, tc.plan, "2", "均在正常运行")
		})
	}
}

// A reply that opens like a plan but states a verdict ("测试全过。") is the
// answer: it is delivered as is, without running the CLI again.
func TestAssistantPlanOpeningWithVerdictIsTheAnswer(t *testing.T) {
	runMultiLinePlanCase(t, "verdict-answer", `我先跑测试。\n测试全过。`, "1", "测试全过")
	runMultiLinePlanCase(t, "check-verdict-answer", `我先跑一下检查。\n检查通过，工作区干净。`, "1", "工作区干净")
	runMultiLinePlanCase(t, "report-header-answer", `我先跑一下检查。\n检查结果：共 3 处告警，都在 store 包。`, "1", "都在 store 包")
	runMultiLinePlanCase(t, "found-answer", `我先查一下。\n查到 3 个失败用例。`, "1", "查到 3 个失败用例")
	runMultiLinePlanCase(t, "status-answer", `我先看一下。\n接口超时。`, "1", "接口超时")
	runMultiLinePlanCase(t, "clean-state-answer", `我先看一下。\n工作区已经干净。`, "1", "工作区已经干净")
}

func runMultiLinePlanCase(t *testing.T, session, plan, wantCalls, wantBody string) {
	t.Helper()
	h, user := workbenchFixture(t)
	h.titleSummary = func(context.Context, domain.User, string) (string, error) { return "", nil }
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
n=$((n + 1))
echo "$n" > '` + counter + `'
if [ "$n" -gt 1 ]; then
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"项目里有四个 LOOP，均在正常运行。"}]}}
{"type":"result","result":"项目里有四个 LOOP，均在正常运行。"}
JSON
exit 0
fi
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"%[1]s"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"list-1","name":"mcp__agentflow__workflows_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list-1","content":"[]"}]}}
{"type":"result","result":"%[1]s"}
JSON
`
	if err := os.WriteFile(bin, []byte(fmt.Sprintf(script, plan)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	h.generate = nil
	body, _ := json.Marshal(assistantRequest{SessionID: session, ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "查看已有 LOOP 并给出结果"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	calls, _ := os.ReadFile(counter)
	if rec.Code != 200 || strings.TrimSpace(string(calls)) != wantCalls || !strings.Contains(rec.Body.String(), wantBody) {
		t.Fatalf("calls=%s code=%d body=%s", strings.TrimSpace(string(calls)), rec.Code, rec.Body.String())
	}
}

// A Bot reply is judged the same way: the joined plan and answer is the
// reply, and the CLI is not run again in terminal-only mode for it.
func TestBotJoinedResultAfterToolsIsNotRetriedAsProgress(t *testing.T) {
	stubCLIModelDiscovery(t)
	_, h, bot := botFixture(t)
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
echo "$((n + 1))" > '` + counter + `'
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查询一下项目状态。"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"runs-1","name":"mcp__agentflow__runs_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"runs-1","content":"[]"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"项目当前有 2 个运行中的 LOOP，均在实现节点。"}]}}
{"type":"result","result":"我先查询一下项目状态。项目当前有 2 个运行中的 LOOP，均在实现节点。"}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-joined", ChatType: "single"}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "项目状态"})
	calls, _ := os.ReadFile(counter)
	if err != nil || strings.TrimSpace(string(calls)) != "1" || !strings.Contains(reply, "均在实现节点") {
		t.Fatalf("calls=%s err=%v reply=%q", strings.TrimSpace(string(calls)), err, reply)
	}
}

// A reply is a progress note when it opens with a plan and every further line
// only continues that plan, in a list or in prose; a line that carries a
// result of work already done makes the reply an answer. The first case is
// the text a real Cursor (grok-4.7) turn wrote after its last tool call, with
// the repository and login renamed.
func TestAssistantProgressOnlyRequiresPlanLines(t *testing.T) {
	cases := []struct {
		text     string
		progress bool
	}{
		{"我先查了本项目的代码仓库和成员，结论如下。\n\n仅 1 个 GitHub 仓：demo-repo，分支 main。\n\n所有者编号 2，与当前用户 alice 一致。\n\n加入申请 0 条，接口未返回其他成员。", false},
		{"我先查了仓库：\n- 1 个 GitHub 仓库\n- 默认分支 main", false},
		{"我先查了本项目的代码仓库和成员，仓库只有 demo-repo。", false},
		{"稍等，我先查了一下：项目有 4 个 LOOP。", false},
		{"我先查看项目里的 LOOP。\n项目里有 4 个 LOOP，都在运行。", false},
		{"我先查看项目里的 LOOP。", true},
		{"稍等，我先检查", true},
		{"我先看看 store.py。\n接下来我会补测试。", true},
		{"我先读代码。\n\n接下来我将运行测试。", true},
		{"我先了解一下需求。", true},
		{"我先看看为什么测试挂了。", true},
		{"我先检查了配置，项目用 MariaDB。", false},
		// Multi-line plans: numbered or bulleted steps and "然后…" lines
		// continue the plan.
		{"我先查看项目里的 LOOP：\n1. 读取工作流配置\n2. 汇总节点状态", true},
		{"我先查看项目里的 LOOP。\n1. 读取工作流配置\n2. 汇总节点", true},
		{"我先检查一下当前仓库。\n- 读取配置文件\n- 运行测试", true},
		{"稍等，我先确认环境。\n然后读取 store.py。", true},
		{"Let me first check the repository:\n1) read the config\n2) run the tests", true},
		{"我先按下面的步骤处理：\n① 读取 README\n② 汇总结论", true},
		{"我先查看仓库：\n- 读取配置\n- 查了 README，项目用 Go 写成", false},
		// Prose steps that neither open with a whitelisted prefix nor use a
		// list: "先确认分支，再读取提交记录。".
		{"我先看一下仓库：\n先确认分支，再读取提交记录。", true},
		{"我先看一下仓库。\n首先读取配置文件，然后运行测试。", true},
		{"我先看一下仓库。\n先读取配置文件，再运行测试。", true},
		{"我先看一下仓库。\n具体步骤：读取配置、运行测试。", true},
		{"我先看一下仓库。\n第一步：读取配置文件。", true},
		{"我先看一下仓库。\n检查范围：配置文件和测试。", true},
		{"我先看一下仓库。\n检查范围：配置文件和测试。\n先运行测试，再读取配置。", true},
		// A prose line that reports a result is an answer, not a step.
		{"我先看一下仓库。\n仓库默认分支是 main，最近提交在 11 月 26 日。", false},
		{"我先看一下仓库。\n项目里有 4 个 LOOP，均正常运行。", false},
		{"我先看一下仓库。\n共有 2 个成员：alice、bob。", false},
		{"我先看一下仓库。\n先确认分支，再读取提交记录。\n最近提交是 abc1234。", false},
		{"我先看一下仓库。\n先看看仓库，查了 README。", false},
		// A line stating a verdict is an answer unless it is still to be
		// checked; English verbs count only as whole words.
		{"我先跑测试。\n测试全过。", false},
		{"我先跑测试。\n单元测试都通过，lint 也干净。", false},
		{"我先看下日志。\n接口没问题。", false},
		{"我先跑测试。\n- 单元测试全过\n- lint 干净", false},
		{"我先跑测试。\n然后测试全过。", false},
		{"Let me first check the file.\nIt reads the config on start.", false},
		{"我先检查配置。\n跑一遍测试看看有没有异常。", true},
		{"我先跑测试。\n确认单元测试都通过。", true},
		{"我先跑测试。\n先运行测试，再检查 lint 是否干净。", true},
		{"我先跑测试。\n- 确认测试全部通过\n- 看看日志有没有问题", true},
		{"Let me first check the file.\nThen read the config and run the tests.", true},
		// 通过 as "via" and 干净 as a complement are steps, not verdicts;
		// plan lead-ins still open a step that names a state or a count.
		{"我先看一下仓库：\n先读取配置，再通过 API 拉取提交记录。", true},
		{"稍等，我先确认环境。\n然后通过数据库查看运行记录。", true},
		{"我先整理一下工作区。\n先删掉临时文件，再清理干净，最后运行测试。", true},
		{"我先看一下仓库。\n先读取 README，再把临时文件清理干净。", true},
		{"让我先看一下仓库：\n先把测试目录清理干净，再运行测试。", true},
		{"我先看一下环境。\n先确认服务是否正常，再读取日志。", true},
		{"我先看一下仓库：\n先查看最近的 3 次提交，再汇总变更。", true},
		{"我先看一下仓库。\n重点关注 store.py 和 handler 的调用关系。", true},
		// A lead-in followed by a result before any action reports findings.
		{"我先看下 PR 的改动。\n检查结果：共 3 处问题，都在 store.py。", false},
		{"我先看一下。\n目标分支是 main，共 3 个提交，最近一次是修复登录。", false},
		{"我先汇总一下。\n最后结论：可以合并。", false},
		{"我先说下范围。\n范围只有 internal/httpapi 一个包。", false},
		{"我先看下日志。\n计划任务每天 9 点跑，最近一次成功。", false},
		{"我先看一下仓库。\n最后汇总结果。", true},
		{"我先看一下仓库。\n目标：把登录修好。", true},
		{"我先跑测试。\n测试都通过，可以合并。", false},
		{"我先跑测试。\n单测都通过。", false},
		// The verdict's subject is not a check intent of its own.
		{"我先看一下仓库。\n检查通过，工作区干净。", false},
		{"我先看一下仓库。\n校验通过。", false},
		{"我先看一下仓库。\n确认检查通过。", true},
		{"我先看一下仓库。\n检查测试是否通过。", true},
		{"Let me first look around.\nCheck the config, then run the tests.", true},
		// Steps written without a whitelisted lead-in still plan when an
		// action comes before any count or result; purpose clauses (让/确保/
		// 保证/直到 … 通过|干净|没问题) are goals, not verdicts; 检查 before a
		// "via" 通过 is the step's verb; 了 inside an embedded question is
		// what the step looks into.
		{"我先看一下仓库。\n重点关注最近 3 次提交。", true},
		{"我先看一下团队。\n查看两个小组的成员。", true},
		{"我先看一下测试。\n先看看为什么测试挂了，再修复。", true},
		{"我先看一下改动。\n然后看看改了哪些文件。", true},
		{"我先看一下仓库。\n目前只需要读取配置和日志。", true},
		{"我先看一下接口。\n把返回值和文档逐项核对。", true},
		{"我先确认一下。\n读一下 v2 的配置。", true},
		{"我先看一下报错的用例。\n然后修改代码，让测试通过。", true},
		{"我先整理一下。\n先改代码，确保 lint 干净。", true},
		{"我先看一下接口。\n修改后确保接口没问题。", true},
		{"我先看一下报错。\n先修复报错，保证构建通过。", true},
		{"我先看一下。\n确保 lint 干净。", true},
		{"我先看一下。\n确保接口没问题。", true},
		{"我先看一下。\n保证构建通过。", true},
		{"Let me first check the build.\nMake sure the tests pass.", true},
		{"我先修一下。\n- 改配置\n- 跑测试直到全部通过", true},
		{"我先看一下数据。\n先检查通过 API 拿到的数据，再对比数据库。", true},
		{"我先看一下流程。\n检查通过 MCP 调用工具的流程。", true},
		{"我先看一下提交。\n然后检查通过 webhook 推送的事件。", true},
		{"我先看一下。\n看看日志里有没有报错。", true},
		{"我先查一下。\n对比 main 和 dev 两个分支的差异。", true},
		{"我先看一下。\n- 看看改了哪些文件\n- 运行测试", true},
		// Lines reporting what was found stay answers.
		{"我先跑一下检查。\n测试都通过，可以合并。", false},
		{"我先跑一下检查。\n检查通过，工作区干净。", false},
		{"我先跑一下检查。\n检查结果：共 3 处告警，都在 store 包。", false},
		{"我先查一下仓库。\n首先，仓库有两个分支：main 和 dev。", false},
		{"我先查一下。\n最后结论：可以合并。", false},
		{"我先查一下。\n查到 3 个失败用例，都在 store 包。", false},
		{"我先查一下。\n找到两个仓库：demo 和 web。", false},
		{"我先看一下。\n查看了日志，没有异常。", false},
		{"我先看一下。\n看了下日志，接口返回 200。", false},
		{"我先查一下。\n读取到的配置是 v2。", false},
		{"我先看一下。\n日志显示接口超时。", false},
		{"我先看一下仓库。\n仓库里没有 README。", false},
		{"我先查一下。\n确认过了，配置没问题。", false},
		{"我先跑一下测试。\n运行结果：2 个失败。", false},
		{"我先跑一下测试。\n测试结果如下：\n- 2 个失败", false},
		{"我先查一下。\n先检查完了再说。", false},
		{"我先看一下测试。\n测试失败 3 个，都在 store 包。", false},
		{"我先看一下测试。\n测试挂在网络超时。", false},
		{"我先看一下日志。\n11 月 26 日的任务失败了两次。", false},
		{"我先看一下仓库。\n仓库有 2 个分支，查看 main 即可。", false},
		{"我先看一下。\n日志里先看报错，再读配置。", true},
		// A lead-in opens a step even when its action comes after a count or
		// a result word; only the forms that report what was found end the
		// plan (a header, a stated total, a closing status).
		{"我先看一下。\n最后给出结论。", true},
		{"我先确认一下。\n步骤：1. 读取配置；2. 运行测试。", true},
		{"我先整理一下。\n先把结果整理成表格。", true},
		{"我先看一下。\n先拉取最新的 5 条记录。", true},
		{"Let me first check the repo.\nStep 1: read the config.", true},
		{"我先看一下。\n先跟目前线上的配置做对比。", true},
		{"我先看一下。\n先打印返回值，再核对文档。", true},
		{"我先看一下。\n首先在 11 月 30 日之前确认窗口。", true},
		{"我先看一下。\n先在两个分支上分别跑测试。", true},
		{"我先看一下。\n最后把结论写进文档。", true},
		{"我先看一下。\n第一步：把 2 个服务的日志拉下来。", true},
		{"我先看一下。\n最后输出结果。", true},
		{"Let me first check the repo.\nFirst, pull the 3 latest runs.", true},
		{"我先看一下。\n先看看有没有 3 个以上的报错。", true},
		{"我先看一下。\n最后，测试全部成功。", false},
		{"我先看一下。\n首先，接口返回正常。", false},
		// A 到 the next step waits for is a precondition, not something
		// found; 报错/异常/超时 right after an action are its object; 干净
		// right after its verb or closing a 把 clause, and 通过 before 为止,
		// are the state a step is to reach.
		{"我先看一下。\n读到文件末尾后统计行数。", true},
		{"我先看一下。\n查到对应提交后回滚。", true},
		{"我先看一下。\n拿到 token 后调用接口。", true},
		{"我先看一下。\n等拿到权限再部署。", true},
		{"我先看一下。\n拿到结果后整理成表格。", true},
		{"我先看一下。\n找到入口函数后再加日志。", true},
		{"我先看一下。\n收到回调之后更新状态。", true},
		{"我先看一下。\n先修复报错。", true},
		{"我先看一下。\n最后排查异常。", true},
		{"我先看一下。\n先看日志，再定位报错。", true},
		{"我先看一下。\n首先复现超时，然后修复。", true},
		{"我先看一下。\n先下载日志，再分析报错。", true},
		{"我先看一下。\n先处理超时。", true},
		{"我先看一下。\n先定位这个报错。", true},
		{"我先看一下。\n先把环境清干净，再跑测试。", true},
		{"我先看一下。\n然后把分支整理干净。", true},
		{"我先看一下。\n先把工作区改干净再提交。", true},
		{"我先看一下。\n修到测试全部通过为止。", true},
		{"我先看一下。\n- 把临时文件删干净", true},
		// The same words still report what was found.
		{"我先看一下。\n查到 3 个失败用例。", false},
		{"我先看一下。\n找到两个仓库：api 和 web。", false},
		{"我先看一下。\n读取到的配置是 v2。", false},
		{"我先看一下。\n拿到了 token，接口返回 200。", false},
		{"我先看一下。\n查到原因后发现是配置错误。", false},
		{"我先看一下。\n查到后端日志里有 3 处报错。", false},
		{"我先看一下。\n最近一次成功。", false},
		{"我先看一下。\n接口超时。", false},
		{"我先看一下。\n测试全部通过。", false},
		{"我先看一下。\n构建报错。", false},
		{"我先看一下。\n日志里有 2 处报错。", false},
		{"我先看一下。\n工作区已经干净。", false},
		{"我先看一下。\n改动干净。", false},
		{"我先看一下。\n首先，部署报错。", false},
		{"我先看一下。\n最后，修复失败。", false},
		{"我先看一下。\n到目前为止测试全部通过。", false},
		{"", false},
		{"   \n\n  ", false},
	}
	for _, tc := range cases {
		if got := assistantProgressOnly(tc.text, nil); got != tc.progress {
			t.Errorf("assistantProgressOnly(%q) = %v, want %v", tc.text, got, tc.progress)
		}
	}
}

// A continuation is judged by its own output. The first run ended with only
// a plan after its tools; the continuation answers without tools. Joined with
// that plan, the answer read as more of it and was retried until 502 (Studio)
// or rejected (Bot).
func TestAssistantContinuationAnswerIsJudgedOnItsOwn(t *testing.T) {
	const answer = `- LOOP：后端研发\n- 状态：运行中`
	script := func(counter string) string {
		return `#!/bin/sh
if [ "$1" = "--version" ]; then echo "1.0.0"; exit 0; fi
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
n=$((n + 1))
echo "$n" > '` + counter + `'
if [ "$n" -gt 1 ]; then
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"text","text":"` + answer + `"}]}}
{"type":"result","result":"` + answer + `"}
JSON
exit 0
fi
cat <<'JSON'
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"list-1","name":"mcp__agentflow__workflows_list","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"list-1","content":"[]"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"我先查看项目里的 LOOP。"}]}}
{"type":"result","result":"我先查看项目里的 LOOP。"}
JSON
`
	}
	t.Run("studio generation", func(t *testing.T) {
		h, user := workbenchFixture(t)
		h.titleSummary = func(context.Context, domain.User, string) (string, error) { return "", nil }
		dir := t.TempDir()
		counter, bin := filepath.Join(dir, "calls"), filepath.Join(dir, "codebuddy")
		if err := os.WriteFile(bin, []byte(script(counter)), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
		h.generate = nil
		c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"continued","clientRequestId":"continued-1","messages":[{"role":"user","content":"查看已有 LOOP 并给出结果"}]}`))
		h.startGeneration(c)
		if rec.Code != 202 {
			t.Fatalf("start: %s", rec.Body.String())
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			g, _ := h.repo.AssistantGeneration(user.ID, "continued")
			if g.Status != "running" {
				calls, _ := os.ReadFile(counter)
				if g.Status != "completed" || strings.TrimSpace(string(calls)) != "2" || !strings.Contains(string(g.Reply), "后端研发") {
					t.Fatalf("calls=%s status=%s error=%s reply=%s", strings.TrimSpace(string(calls)), g.Status, g.Error, g.Reply)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("generation timed out")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	t.Run("bot", func(t *testing.T) {
		stubCLIModelDiscovery(t)
		_, h, bot := botFixture(t)
		dir := t.TempDir()
		counter, bin := filepath.Join(dir, "calls"), filepath.Join(dir, "codebuddy")
		if err := os.WriteFile(bin, []byte(script(counter)), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
		reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{SessionID: "bot-continued", ChatType: "single"}, nil, domain.BotInteraction{SenderID: "new-wecom-user", Content: "项目里的 LOOP"})
		calls, _ := os.ReadFile(counter)
		if err != nil || strings.TrimSpace(string(calls)) != "2" || !strings.Contains(reply, "后端研发") {
			t.Fatalf("calls=%s err=%v reply=%q", strings.TrimSpace(string(calls)), err, reply)
		}
	})
}
