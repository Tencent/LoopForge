package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

func clarifyProcessLog(event *domain.TelemetryEvent) {
	if event == nil {
		return
	}
	if event.Category == "verification" {
		return
	}
	if event.Category != "process" || event.Name != "process.finished" {
		return
	}
	if event.Level == "error" {
		event.Message = "执行器失败：" + event.Message
		return
	}
	if event.Message == "process exited successfully" {
		event.Message = "执行器退出 0"
	}
}

func applyUsage(run *domain.Run, nr *domain.NodeRun, sessionID string, snap domain.TokenUsage) {
	if run == nil || nr == nil {
		return
	}
	if sessionID == "" {
		sessionID = nr.SessionID
	}
	if sessionID != "" && nr.UsageSessionID != sessionID {
		nr.UsageSessionID = sessionID
		nr.UsageCursor = domain.TokenUsage{}
	}
	delta := subUsage(snap, nr.UsageCursor)
	// 成本识别状态与 token 数值变化解耦：跟随最近一次带状态的快照，
	// 让"真实零成本"与"成本未识别"在 run 级汇总上也可区分。
	if snap.CostStatus != "" {
		run.Usage.CostStatus = snap.CostStatus
		nr.Usage.CostStatus = snap.CostStatus
	}
	if !usageHasValues(delta) {
		nr.UsageCursor = snap
		return
	}
	addUsage(&run.Usage, delta)
	addUsage(&nr.Usage, delta)
	nr.UsageCursor = snap
	run.Usage.TotalTokens = billedTokens(run.Usage)
	nr.Usage.TotalTokens = billedTokens(nr.Usage)
}

func billedTokens(usage domain.TokenUsage) int64 {
	return usage.InputTokens + usage.OutputTokens
}

func usageHasValues(usage domain.TokenUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 || usage.ReasoningTokens > 0 || usage.CostUSD != 0
}

func addUsage(dst *domain.TokenUsage, src domain.TokenUsage) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CostUSD += src.CostUSD
}

func subUsage(cur, prev domain.TokenUsage) domain.TokenUsage {
	return domain.TokenUsage{
		InputTokens:      clampNonNeg(cur.InputTokens - prev.InputTokens),
		OutputTokens:     clampNonNeg(cur.OutputTokens - prev.OutputTokens),
		CacheReadTokens:  clampNonNeg(cur.CacheReadTokens - prev.CacheReadTokens),
		CacheWriteTokens: clampNonNeg(cur.CacheWriteTokens - prev.CacheWriteTokens),
		ReasoningTokens:  clampNonNeg(cur.ReasoningTokens - prev.ReasoningTokens),
		CostUSD:          cur.CostUSD - prev.CostUSD,
	}
}

func clampNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func shouldSelfCorrect(onFailure string) bool {
	return resolveGateOnFailure(onFailure, "") == "loop"
}

func buildLoopRetro(run domain.Run, nodeID string, iterations int, lastErr error, onFailure string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Loop 复盘 — `%s`\n\n", nodeID)
	fmt.Fprintf(&b, "- 自修正轮次：%d\n", iterations)
	if lastErr != nil {
		fmt.Fprintf(&b, "- 最终结果：未通过\n- 最后错误：`%s`\n\n", strings.ReplaceAll(lastErr.Error(), "\n", " "))
	} else {
		b.WriteString("- 最终结果：通过\n\n")
	}
	var completed, missingResult, gatePass, gateFail, loopStarts, toolFinishes int
	var issues []string
	for _, event := range run.Telemetry {
		if event.NodeID != nodeID {
			continue
		}
		switch {
		case event.Name == "agent.completed":
			completed++
		case event.Category == "process" && event.Name == "process.finished" && event.Source.Layer == "executor" && !strings.Contains(event.Message, "失败"):
			if completed == 0 {
				missingResult++
			}
		case event.Category == "verification" && event.Name == "process.finished" && event.Level != "error":
			gatePass++
		case event.Category == "verification" && event.Name == "process.finished" && event.Level == "error":
			gateFail++
		case event.Name == "agent.loop":
			loopStarts++
			issues = append(issues, "- Loop："+event.Message)
		case event.Category == "tool" && event.Name == "tool.call" && event.Phase == "finish":
			toolFinishes++
		}
	}
	if missingResult > 0 {
		if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
			issues = append(issues, "- 执行器退出 0，但 stream-json 没有 `type:result` / `agent.completed`（协议中断，不是门禁失败，也不要先当成 max-turns）")
		} else {
			issues = append(issues, "- 执行器有退出 0 但没有 `agent.completed`（可能撞 max-turns / 协议中断）")
		}
	}
	if lastErr != nil && onFailure == "reject" {
		issues = append(issues, "- 按门禁 onFailure=reject：打回上游，不在本节点自修正")
	} else if lastErr != nil && onFailure == "fail" {
		issues = append(issues, "- 按门禁 onFailure=fail：本节点失败")
	} else if lastErr != nil && onFailure == "human" {
		issues = append(issues, "- 按门禁 onFailure=human：转人工确认")
	}
	if lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result") {
		issues = append(issues, "- 沙箱里可能已经写了报告，但节点在登记交付物前就被判失败，所以页面上往往只剩 loop-retro")
	} else if lastErr != nil && strings.Contains(lastErr.Error(), "No such file") {
		issues = append(issues, "- 产物未落地或会话提前结束")
	}
	fmt.Fprintf(&b, "## 信号\n\n- agent.completed：%d\n- 门禁通过 / 失败：%d / %d\n- 工具调用（finish）：%d\n- 进入自修正：%d\n\n", completed, gatePass, gateFail, toolFinishes, loopStarts)
	b.WriteString("## 问题\n\n")
	if len(issues) == 0 {
		b.WriteString("- 本节点未发现需要改 Loop 的异常。\n")
	} else {
		b.WriteString(strings.Join(issues, "\n") + "\n")
	}
	b.WriteString("\n## 建议\n\n")
	switch {
	case lastErr != nil && onFailure == "reject":
		b.WriteString("- 按页面配置打回上游，不要再开自修正。\n")
	case lastErr != nil && onFailure == "fail":
		b.WriteString("- 按页面配置结束本节点为失败。\n")
	case lastErr != nil && onFailure == "human":
		b.WriteString("- 按页面配置转人工确认。\n")
	case lastErr != nil && onFailure == "loop":
		b.WriteString("- 自修正轮次已用尽。若应直接打回或失败，到页面改这条门禁的失败策略。\n")
	case lastErr != nil && strings.Contains(lastErr.Error(), "without a terminal result"):
		b.WriteString("- 缺 `type:result` 时应交给门禁核对产物，而不是按节点失败整链打回；工具次数远低于 `--max-turns` 时不要写成撞轮次。\n")
	default:
		b.WriteString("- 失败策略只认门禁 onFailure（loop / reject / fail / human），在 Studio 里改，保存即写库。\n")
	}
	return b.String()
}

func (e *Engine) attachLoopRetro(runID, nodeID, workspace string, iterations int, lastErr error, onFailure string) {
	if lastErr == nil && iterations <= 1 {
		return
	}
	live, ok := e.repo.Run(runID)
	if !ok {
		return
	}
	attempt := 0
	if nr := live.Nodes[nodeID]; nr != nil {
		attempt = nr.Attempt
	}
	body := []byte(buildLoopRetro(live, nodeID, iterations, lastErr, onFailure))
	rel := filepath.ToSlash(filepath.Join(".agentflow", "loop-retro-"+nodeID+".md"))
	if workspace != "" {
		_ = os.MkdirAll(filepath.Join(workspace, ".agentflow"), 0o755)
		_ = os.WriteFile(filepath.Join(workspace, rel), body, 0o644)
	}
	e.updateRun(runID, func(r *domain.Run) {
		appendArtifact(r, nodeID, attempt, "loop-retro", rel, "text/markdown", body)
	})
	e.observe(runID, nodeID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.published", Phase: "finish", Message: "loop-retro", Attributes: map[string]any{"path": rel, "size": len(body)}})
}
